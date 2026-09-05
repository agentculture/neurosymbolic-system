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


@pytest.fixture()
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


def stream_senses(bot: ToyRobot, count: int, fields: dict[str, Any]) -> None:
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
        assert "2" in refusal["message"] and "1" in refusal["message"], refusal
        assert refusal["remediation"], "the refusal named no way to fix it"
    finally:
        bot.stop()


# --- the fixture consumer must stay a consumer ------------------------------

#: Identifiers a consumer must never need. Each names a decision the engine
#: makes: rule timing, contention, drop accounting, and refusing an
#: out-of-domain command. A client that reimplemented one of them would be
#: proving the opposite of what this fixture exists to prove.
FORBIDDEN = ("cooldown", "hysteresis", "arbitrat", "senselog", "validate")

#: The ceiling on the fixture client. It is an ACCEPTANCE CRITERION, not a
#: style rule: "a consumer writes almost nothing" is the claim, and a number is
#: the only way to keep it true as the engine grows.
MAX_CLIENT_LINES = 200


def test_toy_client_stays_a_client() -> None:
    source = (FIXTURES / "client.py").read_text()

    lines = len(source.splitlines())
    assert lines <= MAX_CLIENT_LINES, f"client.py is {lines} lines, ceiling is {MAX_CLIENT_LINES}"

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
        assert not hits, f"client.py implements {identifier!r}: {hits}"
