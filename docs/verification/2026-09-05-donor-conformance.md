# Donor conformance — what was replayed, and against what

Date: 2026-09-05. Task t14 of the `go-rule-engine-core` plan.

This document is the evidence half of `internal/conformance`. The fixtures
there replay rule sets and sense traces derived from the two donor robots'
own test suites, and this file records **where each case came from**, **how its
expected trace was checked**, and **the exact counts and commits** behind spec
claim `c24`.

It also states plainly what is **not** reproduced, and why. A conformance suite
that quietly skipped the parts that do not port would be worse than no
conformance suite: it would read as coverage.

## The donor commits these numbers were taken at

Every command below was re-run on 2026-09-05 at these commits. Re-running them
at the same shas must produce the same numbers; that is what makes the claim
falsifiable rather than remembered.

| Repository | Commit | Working tree at the time |
|---|---|---|
| `reachy-mini-cli` | `e9a8f54ff0aa04a93c2b13ed44aa5db606bd756f` (branch `wireless-motor-enable`) | clean |
| `microduck-cli` | `ea21bdfa6c87557072f73b0a8a2e66fc1696a301` | clean |
| `arm101-cli` | `97f0f491b58a5158f5052a5f1e80c606f431df2c` | one modified untracked-content file, `.eidetic/memory/arm101-cli__public.jsonl` — a memory store, not source |

## Claim c24, re-verified

> Today each robot CLI carries its own copy of the rules layer:
> reachy-mini-cli (~20k lines across `behavior/` + `motion/`, 18 files
> importing CLI or daemon internals), microduck-cli (a second,
> extraction-first engine with `schema_version = 1`), and arm101-cli has none
> and would be the third copy.

### reachy-mini-cli — the line total

```sh
find reachy/behavior reachy/motion -name '*.py' -not -path '*__pycache__*' \
  | xargs wc -l | tail -1
```

**20029 lines** across **49** Python files (`behavior/` 17407, `motion/` 2622).
"~20k lines" holds exactly.

### reachy-mini-cli — the files importing CLI or daemon internals

```sh
grep -rlE 'reachy\.cli\._errors|reachy\.daemon' \
  reachy/behavior reachy/motion --include='*.py' | wc -l
```

**18 files** — 12 matching `reachy.cli._errors`, 8 matching `reachy.daemon`
(two files match both). Named, in order:

```text
reachy/behavior/audio_tee.py      reachy/behavior/mute_intent.py
reachy/behavior/clip_rider.py     reachy/behavior/reload_driver.py
reachy/behavior/control.py        reachy/behavior/rule_engine.py
reachy/behavior/engine.py         reachy/behavior/rules.py
reachy/behavior/face_lock.py      reachy/behavior/speech_act.py
reachy/behavior/goto_intent.py    reachy/behavior/supervisor.py
reachy/behavior/intents.py        reachy/motion/pat_signal.py
reachy/behavior/library.py        reachy/motion/server.py
reachy/behavior/liveness.py       reachy/motion/sleep_signal.py
```

Those 18 imports are the seam work this repository exists to do. **Removing
them is the consumers' work, not this repository's** — see "Whose work the swap
is" below.

### microduck-cli — `schema_version = 1`

```sh
grep -n 'SCHEMA_VERSION = ' microduck_cli/behavior/rules.py
```

`microduck_cli/behavior/rules.py:111: SCHEMA_VERSION = 1`, and it is
**required**: `_validate_schema_version` refuses a file that is missing it or
that carries any other value, naming the expected version in both refusals.
The shipped `microduck_cli/behavior/default_rules.toml` carries
`schema_version = 1` on its first non-comment line. The package is a second,
independent engine of **6418 lines** across **17** files under
`microduck_cli/behavior/`.

### arm101-cli — no behavior package

```sh
find . -type d -name behavior      # no output
find . -name 'default_rules.toml'  # no output
git ls-files | grep -c behavior    # 0
```

There is no behavior package, no rules file, and no tracked path naming either.
`arm101-cli` would be the **third** copy of this layer if one were written
there; it is the reason the seam is being drawn once here rather than a third
time by hand.

## Whose work the consumer-side swap is

This repository's job is to land the seam. **Deleting a donor's copy and making
it depend on this package is that donor's own change, in that donor's own
repository, on that donor's own schedule.** Nothing in this task touched
`reachy-mini-cli`, `microduck-cli` or `arm101-cli`; the fixtures here were
derived by reading them.

Concretely, the consumer-side work still outstanding is: the 18 `behavior/` +
`motion/` files above losing their `reachy.cli._errors` / `reachy.daemon`
imports as their logic moves behind this library's seams, and `microduck-cli`
doing the same for its 17-file `behavior/` package. Neither is a prerequisite
for this suite, and neither is claimed as done.

## The cases, and how each expected trace was checked

Every fixture lives in `internal/conformance/testdata/<donor>/<case>/`. Each
carries `case.toml` (the tick period, tick count, base layer and rule layer
stack), `senses.jsonl` (one line per tick that changes a reading), `rules.toml`
or a `rule_layers` stack, and `expected.jsonl` — one line per tick, recording
that tick's events, channel ownership, and the channels its composed pose
carried.

`go test ./internal/conformance/ -update` regenerates every `expected.jsonl`.
**A regenerated trace is not evidence.** It records what the engine did, which
is the thing under test. It becomes evidence only when it has been re-read
against the donor assertion in the table below, which is what was done for
every line committed here.

### reachy-mini-cli

| Case | Donor source | What the recorded trace shows, and the donor assertion it answers |
|---|---|---|
| `pat-acknowledge` | shipped `default_rules.toml` (verbatim) + `tests/test_behavior_rule_engine.py::test_cooldown_skip_is_logged_with_reason` and `::test_every_tick_refire_settles_under_cooldown`; `tests/test_behavior_default_rules.py` pins `cooldown_s = 5.0` | Pat held for the whole run at 250 ms steps. Fires at tick 1 and tick 21 — 5.0 s apart, so the second fire is the first tick the cooldown allows. Between them ONE `rule.suppress reason=cooldown` frame at tick 2 and ONE summary frame (`cooldown suppressed 19 ticks`) at tick 21, immediately before the refire. That is the donor's #99 episode cadence — entry, then summary when the streak ends — not 19 per-tick drops. |
| `greet-when-addressed` | shipped `default_rules.toml` (verbatim); `tests/test_behavior_default_rules.py` pins `run = "speak"`, `duration_s = 1.6`, `cooldown_s = 12.0`, `say = "I'm here."` | One addressed utterance at tick 1, cleared at tick 2. One `rule.fire` carrying `"say": "I'm here."` **forwarded, not rendered** — this runtime owns no actuator. `speak` claims only `head`, so ownership shows `head` held by the fired behavior for ticks 2–7 while `antennas` and `body_yaw` stay with `feel-alive`; at tick 8 the 1.6 s lifetime has elapsed and the head returns to the base layer. |
| `hysteresis-rearm` | `tests/test_behavior_rule_engine.py::test_hysteresis_requires_continuous_false_before_refire` | The donor's exact T,T,T,F,F,F,F,F,T sequence at dt = 0.25 with `cooldown_s = 0`, `hysteresis = 1.0`. **2 fires** (ticks 1 and 9) and **2 `reason=rearming` frames** (the entry at tick 2, the summary at tick 4) and **no `reason=cooldown` anywhere** — the three things the donor test asserts, in that order. The re-arm lands at tick 9 because the predicate first read false at tick 4 (t = 1.0 s) and 1.0 s of continuous false completes at tick 8. |
| `inhibit-blocks-react` | `::test_inhibit_blocks_react_and_logs_inhibited` and `::test_inhibit_lifts_when_predicate_clears` | Tick 1: the inhibit fires carrying `disable: ["nod"]` and the react is suppressed `reason=inhibited` — nothing is admitted. Tick 2: the inhibit's predicate clears, the suppression episode closes with its summary, and the react fires in the same tick. Tick 3: the react's own 5 s cooldown takes over. |
| `abstain-yields-base` | `reachy/behavior/library.py`'s `orient-to-sound` (a `wants_sense=True` entry) and the shipped `default_rules.toml`'s own note: "with no credible sound `orient-to-sound` ABSTAINS rather than freezing, so `feel-alive` keeps breathing through it" | A bearing at ticks 1–2, none at ticks 3–4, a bearing again at ticks 5–6. Ownership of all three channels moves claimant → base layer → claimant, with the claimant never evicted. A frozen channel would show the claimant holding ownership through ticks 3–4; it does not. |

### microduck-cli

| Case | Donor source | What the recorded trace shows, and the donor assertion it answers |
|---|---|---|
| `fallen-inhibit` | shipped `default_rules.toml` (verbatim) + `tests/test_rule_engine.py::test_an_inhibit_rule_suppresses_the_named_action_as_a_named_drop`; `tests/test_default_rules.py` pins the disable list | A `move` is fired by an overlay rule at tick 1 and owns `twist` from tick 2. At tick 6 `fallen` reads true: `fallen-inhibit` fires carrying `disable: ["do","idle","look","mode","move","sound"]` — the donor's list exactly — and the running `move` is **evicted**, so from tick 7 nothing owns anything. The already-active suppression episode on the overlay rule transitions to `reason=inhibited` in the same tick, one frame, not two. **`idle` is in the donor's disable list, so the base layer is evicted too.** That is the donor's rule doing what it says. |
| `low-battery-inhibit` | shipped `default_rules.toml` (verbatim); the 0.15 threshold is upstream `robotctl monitor`'s own `BATTERY_CRITICAL_PCT = 15.0` | Identical setup, `battery_frac` 0.5 → 0.1 at tick 6. The distinguishing evidence is the fire event's disable list: `["do","idle","mode","move"]` — `look` and `sound` stay enabled, exactly as the donor argues ("refusing them buys nothing"). |
| `stop-when-limp` | `tests/test_rule_engine.py::test_a_cooldown_rule_fires_at_most_once_in_250_ticks_at_50hz` and `::test_the_first_firing_is_never_cooldown_gated` | The donor's cadence and tick count, verbatim: 50 Hz, 250 ticks, `limp` true throughout, over the shipped rule whose `cooldown_s = 5.0` is the schema default. **Exactly one `rule.fire`, at tick 1**, and one cooldown episode entry at tick 2 that never closes inside the run. |
| `overlay-tombstone` | `tests/test_rules.py::test_merge_enabled_false_tombstones_base_rule` and `::test_tombstone_react_rule` | The `stop-when-limp` senses, unchanged, with a second layer whose whole content is `id = "stop-when-limp"` / `enabled = false`. **No event of any kind in 20 ticks.** One line of overlay, one shipped rule gone, no fork of its body. |

### What the harness compares

- **Events** — every `rule.fire`, `rule.suppress`, `intent.applied` and
  `intent.blocked` frame, with the donor's own `rule` / `kind` / `field` / `op`
  / `reason` / `say` / `disable` keys. A rule-fired admission is ONE
  `rule.fire`; `intent.applied` is the injected-intent path and does not double
  up on it.
- **Ownership** — `{channel: owner id}` as arbitration resolved it that tick.
- **Pose completeness** — the channel names the composed pose carried. Every
  tick must carry every declared channel, which is the property the engine
  promises (an unclaimed channel falls to neutral, so a target is never
  partial).

**Pose values are deliberately not compared.** The donors' trajectories live in
Python behaviour functions this runtime does not port; the shapes in each
fixture's `adaptor.toml` are legible stand-ins, so comparing their numbers
would be comparing the fixture to itself.

`TestEveryTransitionIsExactlyOneFrame` is the separate structural check: no
event appears twice in one tick, and a suppression episode never emits a second
frame under a reason it is already suppressed under. The flood it guards
against is measured, not hypothetical — the donor's issue #99 wrote 6722 drop
lines into a 3 h journal against 42 genuine fires.

`TestBuiltEngineKeepsStdoutPureProtocol` builds `cmd/neurosymbolic-engine`,
runs it over stdio against the reachy fixture for at least 50 ticks, and
asserts that every byte of **stdout** is accounted for by length-prefixed
protocol frames — each a JSON object carrying `v` and `kind`, nothing else —
while **stderr** carries the `[SENSE stage=…]` lines and only those.

## What is a fixture choice, not donor fact

Stated here so nobody reads a fixture number as a measurement:

- **Every channel arity, neutral and trajectory.** The donor's Reachy head is a
  4×4 pose matrix; the fixture calls it width 6. Nothing in this runtime
  interprets a channel's width, so the widths are picked to be legible and all
  different.
- **Every trajectory duration.** These bound a one-shot the rules file gives no
  `duration_s`. `pet-reaction`'s 1.0 s stands in for the donor's
  `_PET_REACTION_BACKSTOP_S`; it is shorter so a bounded replay can watch the
  behavior expire.
- **Every param domain.** The conformance rules files set no params, so only
  the names matter.
- **The `hysteresis-rearm` rule's two edits**, both stated in the fixture
  itself: `hysteresis = 1.0` (the shipped rule carries none — the donor asserts
  the semantics in its test, not in `default_rules.toml`) and
  `duration_s = 0.5` instead of the shipped 12.0 (the donor test drives its
  engine with `keep_active=False`, so the admitted behavior never lingers; at
  12.0 the second fire would be suppressed `already-active` instead, which is
  correct behaviour but not the assertion being mirrored).
- **`microduck/overlay.toml`'s `drive-on-pad` rule.** The shipped microduck
  defaults never admit a `move`, and an inhibit that evicts a running action
  needs a running action. The overlay supplies one the way an operator's own
  overlay would — a second layer, merged per rule id.
- **`case.toml` itself**, which is a harness file rather than anything either
  donor ships. It carries the tick period (the two donors' test suites run at
  two different cadences — 250 ms steps for reachy's rule-engine tests, 50 Hz
  for microduck's — and a runtime whose tunings only worked at one rate would be
  a bug class of its own), the tick count, the base action, the rule layer
  stack, and the preadmit list.
- **`adaptor.toml` and the shipped rules files live one level up**, at
  `testdata/<donor>/`, and a case directory that does not carry its own gets
  the donor's. Five copies of one vocabulary would drift.
  `TestFixturesUseDonorRuleFilesVerbatim` pins the two shipped rules copies to
  the copies `internal/rules/testdata` already holds.

## Not reproduced

### The donors' contention classes beyond `stoppable` and `passive`

A rules file has no way to name a contention class, so every rule-fired action
in this runtime is `stoppable` and only the base layer is `passive`. MicroDuck's
`do` is `UNSTOPPABLE` and its `stop` and `mode` are `STOPPING`; reachy's
`pet-reaction` and `orient-to-sound` are `STOPPABLE` and its `feel-alive` is
`PASSIVE`. So the microduck cases here exercise the **inhibit** path to eviction
(`fallen-inhibit` evicting a running `move`) and **not** the STOPPING-class
admission path (`stop` evicting a shared `stoppable` on admit), which the donor
covers in `tests/test_intents.py`. The engine implements all four classes
(`internal/tick/arbitration.go`); what is missing is a way for a **fixture** to
ask for one. No fixture pretends otherwise.

### Abstention as a data-only fixture

A vocabulary must give every claimed channel a trajectory
(`internal/adaptor/vocabulary.go`, `validateTrajectories`), so an action
described purely as data can never decline a channel. The donor's abstaining
behaviours are its `wants_sense=True` library entries, which are code. The
`abstain-yields-base` case therefore **preadmits** its claimant through the
harness with its own contribution function — the shape a consumer CLI uses —
rather than firing it from a rule. This is disclosed in the case's own
`case.toml` and is the only case that is not rules-driven.

### The donors' behaviour functions

No trajectory, easing or motion primitive was ported. `feel-alive`'s breathing,
`pet-reaction`'s settle, `orient-to-sound`'s graded ladder
(`CorroboratedGate` + `LatchedDoaGuard`) and MicroDuck's `_contribute_*`
functions all stay in the donors. That is why pose values are not compared.

### Speech, export feeds, the goto lane, tick metrics

Out of scope for the rule-engine core (issues #8 and #11). No fixture asserts
anything about them, and none pretends to.

### One engine behaviour this task observed but did not fix

Running `neurosymbolic-engine run --stdio` and then **closing stdin** ends the
engine's tick loop — the `[SENSE stage=compose source=run event=stopped]` line
is written — but **the process does not exit**. `TestBuiltEngineKeepsStdoutPureProtocol`
therefore shuts the engine down with SIGTERM, which is the path a supervising
consumer uses and which does exit cleanly. This is recorded rather than fixed:
t14 does not modify `internal/compose` or `internal/stream`.

## Re-running everything in this document

```sh
# the conformance suite
CGO_ENABLED=0 go test ./internal/conformance/

# regenerate the traces (then re-check them against the table above)
CGO_ENABLED=0 go test ./internal/conformance/ -update

# the c24 counts, at the commits in the first table
cd ../reachy-mini-cli && git rev-parse HEAD
find reachy/behavior reachy/motion -name '*.py' -not -path '*__pycache__*' \
  | xargs wc -l | tail -1
grep -rlE 'reachy\.cli\._errors|reachy\.daemon' \
  reachy/behavior reachy/motion --include='*.py' | wc -l

cd ../microduck-cli && git rev-parse HEAD
grep -n 'SCHEMA_VERSION = ' microduck_cli/behavior/rules.py

cd ../arm101-cli && git rev-parse HEAD
find . -type d -name behavior
```
