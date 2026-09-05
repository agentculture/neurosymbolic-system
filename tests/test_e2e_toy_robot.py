"""End-to-end: a third robot, driven zero-to-pose-stream by the built binary.

This is the acceptance test for the claim the whole repository rests on — that
the engine holds no robot. Reachy Mini and MicroDuck are both donors, so a
runtime that quietly special-cased either would still pass every test written
against them. The toy robot in ``tests/toy_robot/`` is neither: three channels
of arities nobody has hardware for, four senses, three actions, and not one
line of Go or Python written for it beyond a TOML adaptor config and a TOML
rules file.

What a fresh consumer supplies, and the whole of it (spec h16/h4):

* the built ``neurosymbolic-engine`` binary,
* ``tests/toy_robot/adaptor.toml`` — the plant,
* ``tests/toy_robot/rules.toml`` — the behavior,
* ``tests/toy_robot/client.py`` — a stdlib fixture consumer that speaks the
  wire and settles its robot when the stream ends.

No engine code. That is the test.

Every test here needs a build::

    CGO_ENABLED=0 go build -o <path> ./cmd/neurosymbolic-engine

named by ``NEUROSYMBOLIC_ENGINE``, and is skipped otherwise.
"""

from __future__ import annotations

import io
import os
import signal
import sys
import time
import tokenize
from pathlib import Path
from typing import Any

import pytest

FIXTURES = Path(__file__).parent / "toy_robot"
sys.path.insert(0, str(FIXTURES.parent))

from toy_robot.client import ToyRobot  # noqa: E402
from toy_robot.stdio_client import StdioToyRobot  # noqa: E402

ENGINE_PATH = os.environ.get("NEUROSYMBOLIC_ENGINE")

requires_built_engine = pytest.mark.skipif(
    not ENGINE_PATH or not Path(ENGINE_PATH).is_file(),
    reason="NEUROSYMBOLIC_ENGINE must point at a built neurosymbolic-engine binary",
)

#: The tick period and heartbeat interval every test drives the engine at. The
#: heartbeat is deliberately far shorter than the 1s default: criterion 3 is
#: "within two heartbeat intervals", and a test that waited two real seconds to
#: assert that would be a test nobody runs.
PERIOD_S = 0.02
HEARTBEAT_S = 0.1

#: The toy robot's channels and their arities, from adaptor.toml. A pose is
#: COMPLETE or it is a bug: an unclaimed channel falls to its declared neutral
#: rather than being left out of the target.
CHANNELS = {"wheels": 2, "beacon": 1, "arm": 3}

#: The toy robot's neutral pose, from adaptor.toml — where an unclaimed channel
#: falls, and what the engine settles to on the way out.
NEUTRAL = {"wheels": [0.0, 0.0], "beacon": [0.0], "arm": [0.0, 0.0, 0.0]}


def engine_args(*extra: str) -> list[str]:
    return [
        "--adaptor",
        str(FIXTURES / "adaptor.toml"),
        "--rules",
        str(FIXTURES / "rules.toml"),
        "--period",
        f"{int(PERIOD_S * 1000)}ms",
        "--heartbeat",
        f"{int(HEARTBEAT_S * 1000)}ms",
        "--base-action",
        "hum",
        *extra,
    ]


@pytest.fixture
def robot(tmp_path: Path) -> Any:
    """A started, greeted toy robot, torn down however the test left it."""
    settled: list[str] = []
    bot = ToyRobot(
        engine=str(ENGINE_PATH),
        socket_dir=str(tmp_path),
        args=engine_args(),
        settle=settled.append,
        heartbeat_s=HEARTBEAT_S,
    )
    bot.start()
    bot.hello()
    assert bot.wait_for("hello"), "the engine never greeted back"
    try:
        yield bot
    finally:
        bot.stop()


def stream_senses(bot: Any, count: int, fields: dict[str, Any]) -> None:
    """Publish ``count`` sense frames at the engine's own period."""
    for _ in range(count):
        bot.sense(dict(fields))
        time.sleep(PERIOD_S)


def fired(bot: ToyRobot, rule: str) -> list[dict[str, Any]]:
    return [
        f
        for f in bot.of_kind("event")
        if f.get("name") == "rule.fire" and f.get("data", {}).get("rule") == rule
    ]


def suppressed(bot: ToyRobot, rule: str) -> list[dict[str, Any]]:
    return [
        f
        for f in bot.of_kind("event")
        if f.get("name") == "rule.suppress" and f.get("data", {}).get("rule") == rule
    ]


# --- (1) zero to a rules-driven pose stream ---------------------------------


@requires_built_engine
def test_zero_to_pose_stream_with_rules_firing(robot: ToyRobot) -> None:
    # A bumper pressed in a dim room: bump-retreat's `all` conjunction holds.
    stream_senses(robot, 50, {"bumper": True, "light_level": 0.2, "tag": "a"})

    poses = robot.of_kind("pose")
    assert len(poses) >= 40, f"only {len(poses)} pose frames arrived in ~1s at 50 Hz"
    for frame in poses:
        channels = frame["channels"]
        assert set(channels) == set(CHANNELS), f"incomplete pose: {sorted(channels)}"
        for name, arity in CHANNELS.items():
            assert len(channels[name]) == arity, f"{name} carried {len(channels[name])} values"

    # The rule fired, and it is visible in BOTH places a fire must be visible:
    # as an event frame a consumer can act on, and as a SENSE line an operator
    # can grep out of the journal.
    fires = fired(robot, "bump-retreat")
    assert fires, "bump-retreat never fired"
    assert fires[0]["data"]["behavior"] == "retreat"

    robot.stop()
    stderr = robot.stderr.decode(errors="replace")
    assert "[SENSE stage=rule" in stderr, stderr[-2000:]
    assert "event=bump-retreat" in stderr, stderr[-2000:]


@requires_built_engine
def test_an_inhibit_suppresses_a_react_rule(robot: ToyRobot) -> None:
    # Phase 1: the tag is present, so lost-tag-wave's absence clock is held at
    # zero and nothing fires.
    stream_senses(robot, 10, {"bumper": False, "light_level": 0.02, "tag": "a"})
    # Phase 2: the tag is GONE — stated explicitly as null, never inferred from
    # a frame's silence — in a room dark enough for dark-inhibit to hold.
    stream_senses(robot, 45, {"bumper": False, "light_level": 0.02, "tag": None})

    assert fired(robot, "dark-inhibit"), "dark-inhibit never fired"
    blocked = suppressed(robot, "lost-tag-wave")
    assert blocked, "lost-tag-wave was never even evaluated to a suppression"
    assert any(f["data"]["reason"] == "inhibited" for f in blocked), [
        f["data"]["reason"] for f in blocked
    ]
    assert not fired(robot, "lost-tag-wave"), "the inhibit did not actually block the wave"


# --- (2) management over the stream, while the tick keeps running -----------


@requires_built_engine
def test_status_is_answered_without_interrupting_the_stream(robot: ToyRobot) -> None:
    stream_senses(robot, 10, {"bumper": False, "light_level": 0.8, "tag": "a"})
    before = len(robot.of_kind("pose"))

    robot.mgmt("status", call_id="s1")
    assert robot.wait_for("mgmt_result"), "status was never answered"
    stream_senses(robot, 15, {"bumper": False, "light_level": 0.8, "tag": "a"})

    reply = robot.of_kind("mgmt_result")[0]["result"]
    assert reply["code"] == 0, reply
    status = reply["result"]
    assert status["ticks"] > 0
    assert status["rule_layers"] == 1
    assert status["active_mode"] == "gentle"
    # The base layer is passive, so it owns the beacon and nothing else.
    assert "hum" in status["active"]
    assert set(status["ownership"]) == set(CHANNELS)

    # The robot kept moving through the management call: poses arrived after
    # it, and the tick numbers around it are contiguous. An operator's question
    # must not spend the robot's 20 ms budget.
    poses = robot.of_kind("pose")
    assert len(poses) > before, "the stream stopped while status was answered"
    ticks = [f["tick"] for f in poses]
    gaps = [b - a for a, b in zip(ticks, ticks[1:])]
    assert max(gaps) <= 2, f"the tick stream lost more than one tick: {gaps}"


# --- (3)/(4) the two ways an engine goes away -------------------------------


@requires_built_engine
def test_sigterm_sends_an_end_frame_and_settles(robot: ToyRobot) -> None:
    stream_senses(robot, 10, {"bumper": False, "light_level": 0.8, "tag": "a"})

    robot.signal(signal.SIGTERM)
    assert robot.wait_for("end", timeout=5.0), "no end-of-stream frame after SIGTERM"

    end = robot.of_kind("end")[0]
    assert end["reason"], "the end frame named no reason"
    assert robot.settled, "the consumer's settle hook never ran"
    assert robot.settled[0].startswith("end:"), robot.settled


@requires_built_engine
def test_sigkill_is_caught_by_the_heartbeat_lapse(robot: ToyRobot) -> None:
    stream_senses(robot, 10, {"bumper": False, "light_level": 0.8, "tag": "a"})
    assert robot.of_kind("heartbeat"), "no heartbeat arrived before the kill"

    # SIGKILL: no end frame, no settle pose, no cooperation of any kind. The
    # heartbeat exists precisely so this case still reaches the consumer.
    killed_at = time.monotonic()
    robot.signal(signal.SIGKILL)

    deadline = killed_at + 2 * HEARTBEAT_S + 1.0
    while not robot.settled and time.monotonic() < deadline:
        time.sleep(0.01)
    assert robot.settled, "the consumer never noticed the engine was gone"
    assert robot.settled[0] == "heartbeat-lapse", robot.settled


# --- (5) a client that speaks a different protocol is refused by name -------


@requires_built_engine
def test_a_protocol_mismatch_is_refused_naming_both_versions(tmp_path: Path) -> None:
    bot = ToyRobot(
        engine=str(ENGINE_PATH),
        socket_dir=str(tmp_path),
        args=engine_args(),
        heartbeat_s=HEARTBEAT_S,
    )
    bot.start()
    try:
        bot.hello(version=2)
        assert bot.wait_for("error"), "a version-2 hello was not refused"
        refusal = bot.of_kind("error")[0]
        assert "2" in refusal["message"], refusal
        assert "1" in refusal["message"], refusal
        assert refusal["remediation"], "the refusal named no way to fix it"
    finally:
        bot.stop()


# --- the same engine, over a child's pipes ---------------------------------


@requires_built_engine
def test_stdio_transport_streams_poses_and_ends_on_sigterm() -> None:
    """The identical wire, with no socket anywhere.

    A consumer that spawns the engine gets lifecycle ownership for free — the
    parent's exit closes the pipes — and there is no socket file, directory or
    stale-socket cleanup to get wrong. This asserts the transport carries the
    same guarantees the socket one does: a complete pose every tick, and an
    end-of-stream frame naming its reason when the engine is asked to stop.

    It also pins stdout's purity. stdout IS the wire here, so a single stray
    diagnostic printed to it would desynchronise the framing permanently; every
    frame decoding cleanly is that assertion.
    """
    settled: list[str] = []
    bot = StdioToyRobot(
        engine=str(ENGINE_PATH),
        args=engine_args(),
        settle=settled.append,
        heartbeat_s=HEARTBEAT_S,
    )
    bot.start()
    try:
        bot.hello()
        assert bot.wait_for("hello"), "the engine never greeted back over stdio"

        stream_senses(bot, 50, {"bumper": True, "light_level": 0.2, "tag": "a"})

        poses = bot.of_kind("pose")
        assert len(poses) >= 40, f"only {len(poses)} pose frames arrived in ~1s at 50 Hz"
        for frame in poses:
            channels = frame["channels"]
            assert set(channels) == set(CHANNELS), f"incomplete pose: {sorted(channels)}"
            for name, arity in CHANNELS.items():
                assert len(channels[name]) == arity, f"{name} carried {len(channels[name])} values"

        bot.signal(signal.SIGTERM)
        assert bot.wait_for("end", timeout=5.0), "no end-of-stream frame after SIGTERM"
        assert bot.of_kind("end")[0]["reason"], "the end frame named no reason"
        assert bot.settled, "settle() never ran"
        assert bot.settled[0].startswith("end:"), bot.settled
    finally:
        bot.stop()

    # Every senselog line went to stderr, where an operator greps for it, and
    # none of it reached the wire.
    assert "[SENSE stage=compose" in bot.stderr.decode(errors="replace")


@requires_built_engine
def test_stdio_settle_pose_is_the_last_pose_before_the_end_frame() -> None:
    """The shutdown's ordering contract, seen from a real consumer.

    The engine writes one settling neutral pose as its loop exits so a robot is
    not left holding whatever the last tick happened to compose. It rides the
    ordinary bounded queue; the end frame does not. The endpoint drains the
    queue before announcing the end precisely so a consumer's last word from
    the robot is the settle — not a pose from before the stop.
    """
    bot = StdioToyRobot(engine=str(ENGINE_PATH), args=engine_args(), heartbeat_s=HEARTBEAT_S)
    bot.start()
    try:
        bot.hello()
        assert bot.wait_for("hello")
        # Drive bump-retreat so the poses before the stop are NOT neutral, and
        # "the last one is neutral" is a real assertion.
        stream_senses(bot, 25, {"bumper": True, "light_level": 0.2, "tag": "a"})

        bot.signal(signal.SIGTERM)
        assert bot.wait_for("end", timeout=5.0), "no end-of-stream frame after SIGTERM"
        time.sleep(0.1)  # let any trailing frame land before reading the log

        frames = [f for f in bot.frames if f.get("kind") in ("pose", "end")]
        end_at = next(i for i, f in enumerate(frames) if f["kind"] == "end")
        assert end_at > 0, "the end frame arrived before any pose"
        last_pose = frames[end_at - 1]
        assert last_pose["kind"] == "pose", frames[end_at - 1]
        assert last_pose["channels"] == NEUTRAL, last_pose["channels"]
    finally:
        bot.stop()


@requires_built_engine
def test_closing_stdin_exits_the_engine_without_a_signal() -> None:
    """A parent that hangs up must not leave an orphan (deviation d3).

    In --stdio mode the parent owns the engine's lifetime: it spawned it and it
    holds both pipes. Closing them is the ONLY notice a parent that is going
    away gets to give — one that has already exited cannot send a signal — so
    an engine that needed a signal to stop would be an orphan nobody is left to
    clean up. That was the bug: the tick loop went on composing poses into a
    closed pipe forever, every one a silent drop.

    The stop must be an ordinary graceful one, with every guarantee intact: the
    settling neutral pose reaches a parent still reading stdout, the end frame
    names `stdin-closed` (the CAUSE, not the consequence), and the process
    exits 0 with no signal sent at any point in this test.
    """
    settled: list[str] = []
    bot = StdioToyRobot(
        engine=str(ENGINE_PATH),
        args=engine_args(),
        settle=settled.append,
        heartbeat_s=HEARTBEAT_S,
    )
    bot.start()
    try:
        bot.hello()
        assert bot.wait_for("hello"), "the engine never greeted back over stdio"
        # Move the robot off neutral, so "the last pose is neutral" means something.
        stream_senses(bot, 15, {"bumper": True, "light_level": 0.2, "tag": "a"})

        closed_at = time.monotonic()
        bot.close_stdin()

        code = bot.wait(timeout=1.0)
        elapsed = time.monotonic() - closed_at
        assert code is not None, "the engine outlived its parent's pipe: an orphan"
        assert code == 0, f"the engine exited {code}, want 0 — a closed pipe is a clean stop"
        assert elapsed < 1.0, f"the engine took {elapsed:.3f}s to notice the closed pipe"

        assert bot.wait_for("end", timeout=2.0), "no end-of-stream frame after the pipe closed"
        end = bot.of_kind("end")[0]
        assert end["reason"] == "stdin-closed", end

        frames = [f for f in bot.frames if f.get("kind") in ("pose", "end")]
        end_at = next(i for i, f in enumerate(frames) if f["kind"] == "end")
        assert end_at > 0, "the end frame arrived before any pose"
        assert frames[end_at - 1]["channels"] == NEUTRAL, frames[end_at - 1]["channels"]

        assert bot.settled, "settle() never ran"
        assert bot.settled[0] == "end:stdin-closed", bot.settled
    finally:
        bot.stop()

    log = bot.stderr.decode(errors="replace")
    assert "the stdio peer closed the pipe" in log, log[-2000:]


# --- the fixture consumers must stay consumers ------------------------------

#: Identifiers a consumer must never need. Each names a decision the engine
#: makes: rule timing, contention, drop accounting, and refusing an
#: out-of-domain command. A client that reimplemented one of them would be
#: proving the opposite of what this fixture exists to prove.
FORBIDDEN = ("cooldown", "hysteresis", "arbitrat", "senselog", "validate")

#: The ceiling on the fixture client. It is an ACCEPTANCE CRITERION, not a
#: style rule: "a consumer writes almost nothing" is the claim, and a number is
#: the only way to keep it true as the engine grows.
MAX_CLIENT_LINES = 200

#: Every fixture consumer, each held to the same ceiling. A second transport
#: got its own file rather than a mode on the first precisely because raising
#: the ceiling to fit it in would have been quietly deleting the assertion.
CLIENT_FILES = ("client.py", "stdio_client.py")


@pytest.mark.parametrize("filename", CLIENT_FILES)
def test_toy_client_stays_a_client(filename: str) -> None:
    source = (FIXTURES / filename).read_text()

    lines = len(source.splitlines())
    assert lines <= MAX_CLIENT_LINES, f"{filename} is {lines} lines, ceiling is {MAX_CLIENT_LINES}"

    # Grep the CODE, not the prose. Comments and docstrings explaining what a
    # consumer must not do are exactly what should be allowed to say these
    # words; a `cooldown` in an expression is the leak this guards against.
    code = [
        token.string
        for token in tokenize.generate_tokens(io.StringIO(source).readline)
        if token.type not in (tokenize.COMMENT, tokenize.STRING)
    ]
    for identifier in FORBIDDEN:
        hits = [token for token in code if identifier in token.lower()]
        assert not hits, f"{filename} implements {identifier!r}: {hits}"
