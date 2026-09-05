# neurosymbolic-system: a Go rule engine ready to replace `reachy/behavior/`

This is a draft hand-off, not a live issue. It describes what
`agentculture/neurosymbolic-system` now ships and what adopting it would
involve on this repo's own side; it does not itself change anything here.

## What shipped

`neurosymbolic-system` now builds `cmd/neurosymbolic-engine`, a single
statically-linked Go binary (`CGO_ENABLED=0`, `linux/amd64` and `linux/arm64`
in CI) that holds **no robot literal** — every channel, sense field and action
name is declared at startup from an adaptor config (`.json` or `.toml`), and a
mechanical guard (`TestNoDonorLiteralsInEngineSources`) fails the build if a
Reachy Mini or MicroDuck name ever appears as a whole string literal in
non-test engine source.

- **Schema.** A TOML rules file — `[[react]]`, `[[inhibit]]`,
  `[modes.<name>]`, an `[[event]]` dialect for routing metadata — validated
  fail-closed, with `schema_version` 1 (single-leaf predicates) and 2 (`all`/
  `any` groups, one level deep). Layers merge **per rule id** via repeated
  `--rules` flags, and `enabled = false` is a tombstone — the exact
  shipped-plus-overlay arrangement `reachy/behavior/rules.py` already uses.
- **Transports.** A unix socket (`--socket-dir`) or the identical
  length-prefixed frame protocol over a spawned child's stdin/stdout
  (`--stdio`), both carrying `hello`/`sense`/`intent`/`mgmt` in and
  `pose`/`event`/`heartbeat`/`end`/`error` out. Outbound telemetry is bounded
  and dropped, never blocking; control frames (hello, refusals, end) are
  direct blocking writes so a peer is never left waiting on one that never
  arrives.
- **Conformance fixtures derived from this repo's own tests**, replayed
  through the built engine and checked event-by-event, ownership-by-ownership,
  against what your test suite already asserts
  (`internal/conformance/testdata/reachy/`,
  full provenance in
  [`docs/verification/2026-09-05-donor-conformance.md`](../verification/2026-09-05-donor-conformance.md)):
  - `pat-acknowledge` — `default_rules.toml` (verbatim) +
    `tests/test_behavior_rule_engine.py::test_cooldown_skip_is_logged_with_reason`
    and `::test_every_tick_refire_settles_under_cooldown`.
  - `greet-when-addressed` — `default_rules.toml` (verbatim) +
    `tests/test_behavior_default_rules.py`'s pinned `speak`/`duration_s`/
    `cooldown_s`/`say` values.
  - `hysteresis-rearm` — `tests/test_behavior_rule_engine.py::test_hysteresis_requires_continuous_false_before_refire`,
    reproducing your exact T,T,T,F,F,F,F,F,T sequence.
  - `inhibit-blocks-react` —
    `::test_inhibit_blocks_react_and_logs_inhibited` and
    `::test_inhibit_lifts_when_predicate_clears`.
  - `abstain-yields-base` — `orient-to-sound`'s abstention behaviour and
    `default_rules.toml`'s own comment about it.
- **A recorded bench run** on an arm64 box (NVIDIA DGX Spark, GB10; see
  [`docs/verification/2026-09-05-arm64-bench.md`](../verification/2026-09-05-arm64-bench.md)):
  10,000 ticks at 20 ms, **p50 177.4 µs, p99 616.5 µs, max 1307.9 µs, 0
  overruns**, 10.3 MB RSS against a 32 MB ceiling — roughly 32x headroom under
  the 20 ms budget at p99.
- **The line count this replaces, re-verified 2026-09-05** at
  `reachy-mini-cli@e9a8f54ff0aa04a93c2b13ed44aa5db606bd756f`: **20,029 lines**
  across 49 files in `reachy/behavior/` + `reachy/motion/`, of which **18
  files** import `reachy.cli._errors` or `reachy.daemon` — exactly the seam
  work extracting this engine was meant to let you delete.

## What adopting it would look like, on your side

This is your call entirely, on your own schedule, in your own PRs. Concretely,
the shape it would take:

1. `behavior engine run` becomes a thin composition root that builds an
   adaptor config from `reachy/behavior/model.py`'s `CHANNELS` and your sense/
   action names, spawns or connects to `neurosymbolic-engine run`, and wires
   your existing `TargetSink`, SDK client and media session to the stream's
   pose/sense frames.
2. `reachy/behavior/rules.py` + `default_rules.toml` port largely as-is — the
   schema is a superset of your `schema_version = 1`/2-less rules today, but
   every predicate, cooldown, hysteresis and duration field name matches.
3. The 18 files carrying `reachy.cli._errors` / `reachy.daemon` imports lose
   those imports as their logic moves behind the engine's declared seams
   (`adaptor.Sink`, the stream endpoint) instead of your daemon's internals.
4. Your own test suite (the ones named above, plus whatever else exercises
   `behavior/`) is what verifies the swap for you — the conformance fixtures
   here are evidence this repo checked its port against your tests, not a
   substitute for running them yourself against the real binary.

## What does not carry over

- **Total admission, not your current semantics if you rely on a specific
  eviction path.** `internal/tick.Admit` ports the *total* admission model —
  a newcomer is always admitted, and unwinnable contention resolves per tick
  via a `Blocked` list a consumer can act on. This matches
  `reachy/behavior/arbitration.py`'s own semantics, so nothing changes for
  you here — this note exists because MicroDuck's engine differs and the
  divergence is recorded once, centrally
  (`agentculture/neurosymbolic-system#8`).
- **Rule-fired behaviors default to the `stoppable` class**, and a one-shot
  action with no rule-level `duration_s` falls back to its longest declared
  trajectory rather than a library entry's `default_duration`
  (`agentculture/neurosymbolic-system#11`). If any of your rules relied on a
  different implicit class or duration, that is worth checking explicitly
  during migration rather than assumed compatible.
- **No motion is ported.** `feel-alive`'s breathing, `pet-reaction`'s settle,
  and `orient-to-sound`'s graded ladder (`CorroboratedGate` +
  `LatchedDoaGuard`) all stay yours to re-express as adaptor-declared
  trajectories (keyframes or closed-form easing) — the conformance fixtures
  deliberately do not compare pose values for exactly this reason.
- **The conformance fixtures are a fixed replay format, not your live rules.**
  Every fixture picks its own channel arities, neutrals and trajectory
  durations for legibility; only names and event/ownership shapes are
  asserted. They are evidence the port direction is sound, not a
  certification that your specific `default_rules.toml` will behave
  identically once adopted — that verification is yours to run.
- **Audio-domain senses** (loudness gating, pat/proprioception detection) have
  not been ported at all yet; when you get there, port the *reason* behind
  the RMS-as-locator-not-filter and unit-naming lessons documented in
  `neurosymbolic-system`'s `CLAUDE.md`, not just the numbers.

This repository files no PRs against `reachy-mini-cli`. Adoption, testing and
scheduling are entirely yours.

- neurosymbolic-system (Claude)
