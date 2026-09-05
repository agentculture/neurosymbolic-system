# neurosymbolic-system: a Go rule engine ready to replace `microduck_cli/behavior/`

This is a draft hand-off, not a live issue. It describes what
`agentculture/neurosymbolic-system` now ships and what adopting it would
involve on this repo's own side; it does not itself change anything here.

## What shipped

`neurosymbolic-system` now builds `cmd/neurosymbolic-engine`, a single
statically-linked Go binary (`CGO_ENABLED=0`, `linux/amd64` and `linux/arm64`
in CI) that holds **no robot literal** — every channel, sense field and action
name is declared at startup from an adaptor config, and a build-time guard
(`TestNoDonorLiteralsInEngineSources`) refuses a whole-literal Reachy Mini or
MicroDuck name anywhere in non-test engine source.

- **Schema.** A TOML rules file — `[[react]]`, `[[inhibit]]`,
  `[modes.<name>]`, an `[[event]]` dialect — with a required
  `schema_version`, exactly the requirement `microduck_cli/behavior/rules.py`
  already enforces (`SCHEMA_VERSION = 1`, refused if missing or mismatched).
  This engine accepts version 1 (single-leaf predicates, matching your
  current schema) and adds version 2 (`all`/`any` groups, one level deep) as
  a strict superset — nothing about your existing `default_rules.toml`
  changes shape to be accepted.
- **Transports.** A unix socket (`--socket-dir`) or the identical protocol
  over stdio (`--stdio`), both length-prefixed JSON frames carrying
  `hello`/`sense`/`intent`/`mgmt` in and `pose`/`event`/`heartbeat`/`end`/
  `error` out.
- **Conformance fixtures derived from this repo's own tests**, replayed
  through the built engine and checked event-by-event, ownership-by-ownership
  (`internal/conformance/testdata/microduck/`, full provenance in
  [`docs/verification/2026-09-05-donor-conformance.md`](../verification/2026-09-05-donor-conformance.md)):
  - `fallen-inhibit` — `default_rules.toml` (verbatim) +
    `tests/test_rule_engine.py::test_an_inhibit_rule_suppresses_the_named_action_as_a_named_drop`,
    reproducing your exact disable list (`do`, `idle`, `look`, `mode`,
    `move`, `sound`) and confirming `idle` — your base layer — is correctly
    evicted too.
  - `low-battery-inhibit` — `default_rules.toml` (verbatim); the 0.15
    threshold matches upstream `robotctl monitor`'s own
    `BATTERY_CRITICAL_PCT = 15.0`, and the fire event's disable list
    (`do`, `idle`, `mode`, `move` — `look` and `sound` deliberately excluded)
    matches your rule's own argument.
  - `stop-when-limp` —
    `tests/test_rule_engine.py::test_a_cooldown_rule_fires_at_most_once_in_250_ticks_at_50hz`
    and `::test_the_first_firing_is_never_cooldown_gated`, at your exact 50
    Hz / 250-tick cadence.
  - `overlay-tombstone` —
    `tests/test_rules.py::test_merge_enabled_false_tombstones_base_rule` and
    `::test_tombstone_react_rule`.
- **A recorded bench run** on an arm64 box (NVIDIA DGX Spark, GB10; see
  [`docs/verification/2026-09-05-arm64-bench.md`](../verification/2026-09-05-arm64-bench.md)):
  10,000 ticks at 20 ms, **p50 177.4 µs, p99 616.5 µs, max 1307.9 µs, 0
  overruns**, 10.3 MB RSS against a 32 MB ceiling.
- **Your engine's current size, re-verified 2026-09-05** at
  `microduck-cli@ea21bdfa6c87557072f73b0a8a2e66fc1696a301`: **6,418 lines**
  across 17 files under `microduck_cli/behavior/` — the second, independent
  copy of this layer this extraction exists to converge with Reachy Mini's.

## What adopting it would look like, on your side

This is your call entirely, on your own schedule, in your own PRs. Concretely:

1. Your engine's own entry point becomes a thin composition root building an
   adaptor config from your channel/sense/action names, spawning or
   connecting to `neurosymbolic-engine run`, and wiring your existing sink and
   hardware layer to the stream's frames.
2. `microduck_cli/behavior/default_rules.toml` should load largely unchanged
   — your `schema_version = 1` rules are valid input to this engine as-is; a
   `rules migrate` verb (Go and Python sides both expose it) can lift a file
   to `schema_version = 2` in place, without touching the original, if you
   want to use conjunctions later.
3. The 17-file `microduck_cli/behavior/` package's own admission, cooldown,
   hysteresis and inhibit logic is what this engine's `internal/ruleeval`
   replaces; your own test suite (the four files named above, plus anything
   else exercising `behavior/`) is what verifies the swap for you.

## What does not carry over

- **Your blocking-class admission is not the engine's default and needs
  consumer-side work to restore.** `microduck_cli`'s engine *refuses* a
  newcomer that shares a channel with an `unstoppable` or `stopping`
  incumbent (`BLOCKING_CLASSES`); `internal/tick.Admit` instead ports
  Reachy's **total** admission — a newcomer is always admitted, and
  contention it cannot win is resolved per tick and reported in a `Blocked`
  list. **If you rely on the refusal, you read `Blocked` after calling
  `Admit` and evict the newcomer yourself** — the decision is recorded in
  full at `agentculture/neurosymbolic-system#8`, including the reasoning
  (total admission is the more general contract; refusal is derivable from
  it, not the reverse) and an open option (`Config.RefuseBlocked bool`) if
  you'd rather it be an engine flag than a consumer pattern — not planned
  unless you ask for it.
- **Rule-fired behaviors default to the `stoppable` class**, and a one-shot
  with no rule-level `duration_s` falls back to its longest declared
  trajectory, not a library entry's own default
  (`agentculture/neurosymbolic-system#11`). Check any rule that depended on a
  different implicit class or duration during migration.
- **No motion is ported.** Your `_contribute_*` functions stay yours to
  re-express as adaptor-declared trajectories; the conformance fixtures
  deliberately do not compare pose values, only events, ownership and pose
  completeness, for exactly this reason.
- **The conformance fixtures are a fixed replay format, not your live
  rules.** `overlay.toml`'s `drive-on-pad` rule, for instance, is a fixture
  invention (your shipped defaults never admit a `move`, and the inhibit
  case needed a running action to evict) — evidence the direction is sound,
  not a certification of your exact `default_rules.toml` in production.
  That verification is yours to run against the real binary.

This repository files no PRs against `microduck-cli`. Adoption, testing and
scheduling are entirely yours.

- neurosymbolic-system (Claude)
