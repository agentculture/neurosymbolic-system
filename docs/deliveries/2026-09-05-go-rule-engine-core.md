# Delivery Summary — Go rule engine core

plan: `go-rule-engine-core` · run: `complete` · date: `2026-09-05`
baseline: `devague summary skeleton`

## Intent

Ship neurosymbolic-system as one static Go binary — the rule engine that
powers rule-based CLI actions for any robot: event-based and tick-based rules
from one or many TOML files, an optional embedding/small-model fast path,
driven from Python by reachy-mini-cli, microduck-cli and arm101-cli through a
thin stdlib client, each robot adding only adaptors and configuration. The run
executed the 16-task, 7-wave plan exported from the converged and challenged
frame (`docs/specs/2026-09-05-go-rule-engine-core.md`,
`docs/plans/2026-09-05-go-rule-engine-core.md`), fanned out one agent per task
per wave in isolated worktrees under `../.worktrees.neurosymbolic-system/`,
each merge gated by tests before and after. Only this repository was touched;
sibling repositories were read, never written (frame decision c31).

## Planned Work

Quoted verbatim from the `devague summary` skeleton:

- `t1` — Go module scaffold and CI: go.mod at repo root, cmd/neurosymbolic-engine, internal/ layout, go.yml job (vet, test, build) on a linux/amd64 + linux/arm64 matrix with `CGO_ENABLED`=0, and a static-binary assertion
- `t2` — Rules schema and loader (internal/rules): TOML, `schema_version` 1 and 2, data predicates with all/any conjunction in v2, per-id merge of shipped + overlay + N files in listed order, enabled=false tombstones, fail-closed validation naming the rule id
- `t3` — Adaptor vocabulary (internal/adaptor): channels, sense fields with types, action names with param domains declared at startup from a TOML config; the engine holds no robot literal; a rules file naming an undeclared field or action is refused at load
- `t4` — Tick core (internal/tick): parameterized period, injected clock, per-channel arbitration by class and recency with abstention, complete-pose composition with neutral fill, TargetSink interface
- `t5` — Rule evaluation on the tick (internal/ruleeval): `cooldown_s`, hysteresis, `duration_s`, `absent_for`, per-episode suppression state; one admission registry for rule-fired actions and agent intents (run once, standing goal, set mode, inhibit)
- `t6` — Event-keyed rule dialect (internal/events): source/type entries with priority, urgency, dedupe window, template and a default: entry in the same TOML loader and validator; events surface on the tick as one-tick sense fields and as a routed event record
- `t7` — senselog (internal/senselog): the \[SENSE stage=.. source=.. event=..\] stderr grammar, per-episode suppression logging (entry, reason change, end with tick count), stdout never written
- `t8` — Stream endpoint (internal/stream): unix socket with length-prefixed JSON frames, sense frames in and pose frames out at tick rate, backpressure as a named drop, one owner per socket
- `t9` — Management endpoint and Go CLI verbs (internal/mgmt, internal/clifmt): request/response over the same socket or a one-off exec, verbs whoami / version / doctor / rules check / rules list / status, the {code, message, remediation} error body, exit 0/1/2, --json, error:/hint: on stderr
- `t10` — Decision provider (internal/provider): OpenAI-compatible embeddings and small-model client, warmed at startup, called from a worker behind a bounded queue with a timeout; a rule can name it as a predicate source; absence, timeout or error is an abstention with a named drop
- `t11` — Import allowlist guard: a test that walks go list -deps of the engine command and fails on any package outside the stdlib plus this module; documents the allowlist
- `t12` — Python management surface: `neurosymbolic_system`/`engine_client.py` (stdlib socket/exec client relaying the error body verbatim as CliError), engine locator (PATH then `NEUROSYMBOLIC_ENGINE`), a doctor check for a missing or version-mismatched engine, and engine / rules noun groups in the Python CLI that delegate to the binary
- `t13` — Toy-robot fixture adaptor and end-to-end test: a third robot (neither Reachy nor MicroDuck) with its own channels, senses and actions, a TOML rules file, a fixture sink, driven zero-to-pose-stream through the stream endpoint
- `t14` — Conformance fixtures from both donors: rules files plus sense traces in, expected fire / suppress / pose traces out, derived from reachy-mini-cli and microduck-cli rule tests (read-only in those repos), replayed through the engine; records the donor commit shas and line counts cited in the spec
- `t15` — Bench verb and arm64 verification record: engine bench prints tick p50 / p99, overrun count and steady-state RSS for a 200-rule / 20-field load over 10,000 ticks; one run on an arm64 box is committed under docs/verification with the commit sha
- `t16` — Docs and hand-off: README adaptor guide (channels, senses, actions, sink, rules, the two transports, motor safety stays on the motor-control surface), CLAUDE.md moved from design brief to as-built, CHANGELOG, and one communicate issue per consumer repo announcing the engine — with a test asserting no commit in the branch touches a path outside this repo

## Actual Delivery

Run range: `48e6e9e..743ace9` on `spec/go-rule-engine` (97 commits, 212 files,
+38171 / −261). Every task merged through the TDD gate; two merges were
reverted and re-landed after a fix (t3, t15 — see Drift).

| Plan task | Status | What actually landed |
|-----------|--------|----------------------|
| `t1` | delivered | `go.mod`, `cmd/neurosymbolic-engine`, `version` verb from ldflags, `.github/workflows/go.yml` (amd64 + arm64, `CGO_ENABLED=0`, static-link assertion), Makefile; merge `b630f09` |
| `t2` | delivered | `internal/rules`: schema v1/v2, `all`/`any`, layered per-id merge, tombstones, modes/`active_mode`, `params`, `say`, fail-closed refusals naming rule id and path; both donors' `default_rules.toml` verbatim in testdata; `BurntSushi/toml` v1.6.0 pinned with `// allow:` and vendored; merge `db8f658` |
| `t3` | delivered | `internal/adaptor`: channels with arity/neutral, typed senses with `age_field`, actions with param domains and per-channel trajectories (keyframes, easing, `loops`), `CheckReferences`, `ValidateParams`, `Sink`/`RecordingSink`, whole-literal donor-name guard; JSON loader (TOML loader landed in t9); merge `330c702` (re-land after the guard fix) |
| `t4` | delivered | `internal/tick`: injected `Clock`/`FakeClock`, four contention classes ported from `arbitration.py`, abstention-aware arbitration, complete-pose composition, trajectory sampling, single state-owning goroutine with bounded inbox, seam and event-consumer panic isolation, settle-on-exit, `Stats`; merge `d97b71c` |
| `t5` | delivered | `internal/sense` (engine-owned snapshot with per-field last-seen) and `internal/ruleeval` (predicate eval, cooldown/hysteresis/`duration_s`/`absent_for`, one admission `Registry` for rule fires and intents, fault-isolating `Bus`); merge `fd72d6b` |
| `t6` | delivered | `[[event]]` / `[event_default]` in the shared loader; `internal/events.Router` (priority, urgency, dedupe window, template, `TickFields`); Nova `rules.yaml` transcribed to testdata; merge `44f10ca` |
| `t7` | delivered | `internal/senselog`: grammar, `Streak` per-episode logging, `Parse`; stdout-clean test; writes made goroutine-safe in t15; merge `9f1a702` |
| `t8` | delivered | `internal/stream`: length-prefixed JSON framing, `hello`/`sense`/`intent`/`mgmt` in, `pose`/`event`/`heartbeat`/`end`/`error` out, protocol version 1, backpressure drop, one owner per socket, 0600 socket in a consumer dir, no TCP without `--insecure-tcp` (proved by a wrapped listener factory), stdio variant; merge `7c746ca` |
| `t9` | delivered | `internal/clifmt` (error contract), `internal/mgmt` (`whoami`, `version`, `doctor`, `status`, `rules check/list/migrate/reload`, transport-agnostic `Handler`, `Reloader`/`StatusSource`), `adaptor.LoadTOML`; merge `d65c5fd` |
| `t10` | delivered | `internal/provider`: embedding and completion kinds over OpenAI-compatible HTTP, warmed at startup, depth-2 queue, off-thread worker writing declared sense fields, named per-episode drops; merge `307babc` |
| `t11` | delivered | `internal/allowlist` test over `go list -deps` with the `// allow:` go.mod convention and a stdlib denylist; `docs/go-dependencies.md`; merge `7485f65` |
| `t12` | delivered | `neurosymbolic_system/engine_client.py`, `engine` and `rules` noun groups, `engine_present`/`engine_protocol` doctor checks (warning severity), catalog/overview/learn in lockstep; merge `ed49503` |
| `t13` | delivered | `internal/compose` (`neurosymbolic-engine run`), `version --json` protocol field, guard `protocol_tokens.txt`, `tests/toy_robot/{adaptor.toml,rules.toml,client.py}` (189 lines) and `tests/test_e2e_toy_robot.py`, CI builds the engine before pytest; merge `b341319`; follow-ups `f5dcd3a` (t13b) and `af164ba` (t13c) |
| `t14` | delivered | `internal/conformance` replay with 9 cases (5 reachy, 4 microduck) and `docs/verification/2026-09-05-donor-conformance.md` with the re-measured c24 counts at donor commits `e9a8f54` / `ea21bdf` / `97f0f49`; merge `0df3be2` |
| `t15` | delivered | `internal/bench` and the `bench` verb; `docs/verification/2026-09-05-arm64-bench.md` (DGX Spark GB10, commit `3d8292d`: p50 177 µs, p99 616 µs, 0 overruns / 10 000 ticks, RSS 10.3 MB); senselog concurrency fix; merge `871b6e1` (re-land after a flaky-test fix) |
| `t16` | partial | `README.md` rewritten with the adaptor guide, `CLAUDE.md` as-built, `tests/test_repo_boundary.py`, three hand-off drafts under `docs/handoff/`; merge `743ace9`. Missing by design: the CHANGELOG entry (written by the version bump at PR time) and the three consumer issues (drafted, not posted — outward-facing, awaiting operator review) |

## Mid-work Decisions

Every decision below is filed as a labelled issue in this repository so the
operator can see it without reading the run. Deviation records `d1`–`d3` are
recorded via `devague deviate` and are **proposed**, awaiting the operator's
confirm; they are quoted here as the record, not re-litigated.

- `d1` (proposed) — t3's adaptor config loads from JSON instead of TOML; the TOML decoder landed in t2 in the same wave and t3 had to stay stdlib-only and file-disjoint from `go.mod`. The TOML front end landed in t9. Issue #4.
- `d2` (proposed) — three wiring gaps found at integration and fixed as t13b: a `--provider` flag in the composition root, the settling neutral pose flushed before the `end` frame, an automated stdio end-to-end test. Issue #14.
- `d3` (proposed) — `run --stdio` did not exit on stdin EOF (found by t14's conformance stdio test); fixed as t13c: EOF cancels the run, the `end` frame names `stdin-closed`, exit 0; a socket peer leaving keeps the engine. Issue #15.
- `BurntSushi/toml` pinned with an `// allow:` argument and vendored for offline `go build` on a robot box (plan risks r2, r4 resolved). Issue #5.
- The donor-literal guard matches whole literals and subtracts two justified exemption lists (three rules-schema keywords; the `pose` wire token); channel and action names cannot be exempted. Issues #6, #13.
- Event identity is `source/type`; priority and urgency are routing metadata plus a one-tick sense field, never arbitration inputs (risk r1 resolved); Nova's three payload-conditional `rule/fire:<id>` overrides are not representable and were omitted, not approximated. Issue #7.
- Admission is total (Reachy semantics); MicroDuck's blocking-class refusal is left to the consumer via `AdmitResult.Blocked`. The donor's mid-tick `done` signal was not ported. Issue #8.
- A panicking tick-seam rider or event consumer is recovered as a named drop and counted in `Stats.SeamPanics`; the engine keeps ticking. Issue #9.
- The Python CI job builds the Go engine and exports `NEUROSYMBOLIC_ENGINE` so engine-backed tests run rather than skip. Issue #10.
- Rule-fired behaviors default to `stoppable`; a one-shot react rule without `duration_s` falls back to the action's longest trajectory; the hysteresis re-arm token is `rearming`; `say` is forwarded as an event field with no speech actuator in the engine. Issue #11.
- The decision provider is a seam driver writing declared sense fields; rules never reference providers; an absent provider is an absent field. Issue #12.
- Stream composition is `stream.New` → `tick.New` → `srv.Attach`, heartbeats on `srv.Seam`, end-of-stream via `srv.RunWith`; no-TCP is proved by a single wrapped listener factory. Issue #13.
- The bench "success" tests assert report shape and internal consistency, not exit 0, because a real-clock 5 ms budget cannot be asserted under `go test ./...` contention; the one deterministic exit-1 test (`--period 1ns`) stands. No issue filed; recorded in `e83d2c8`.
- t13 ran on opus rather than the split plan's sonnet because its scope grew to include the CI wiring (#10) and the guard exemption (#13).

## Drift From Plan

The deviation records are proposed, not yet approved, so each entry's
classification below is the operator's own, not inherited.

| Plan item | Reason for divergence | Classification |
|-----------|-----------------------|----------------|
| `t3` (`d1`) | config loaded from JSON first for in-wave file disjointness; TOML front end arrived in t9, so the contract ("from a TOML config") is met one task later | acceptable |
| `t3` | first merge reverted: the substring-based donor-name guard produced 17 false positives against t2's schema keywords; re-landed with whole-literal matching and justified exemptions (`330c702`) | acceptable |
| `t4` | admission ported as Reachy's total admission; MicroDuck's refusal semantics are a consumer pattern, not an engine option — the two donors genuinely disagree here | risky |
| `t4` | seam panics are recovered, a deliberate divergence from the donor's propagate-and-die | acceptable |
| `t5` | contention class, one-shot fallback duration and the absence clock decided locally because the adaptor carries less than the donor's library (issue #11) | acceptable |
| `t6` | Nova's payload-conditional overrides omitted from the transcription; `reachy_nova` is a reference, not a consumer | acceptable |
| `t13` (`d2`, `d3`) | three wiring gaps and one lifecycle bug found only at integration; fixed inside the run as t13b and t13c | acceptable |
| `t14` | pose VALUES are not compared (trajectories are fixture choices, not ported motion); contention classes beyond stoppable/passive and data-only abstention are not fixture-expressible and are listed as not reproduced | needs-follow-up |
| `t15` | first merge reverted on a timing flake under parallel test load; re-landed with consistency assertions (`e83d2c8`) | acceptable |
| `t16` | CHANGELOG deferred to the version bump; consumer issues drafted, not posted (outward-facing) | needs-follow-up |

## Evidence

All checks were run read-only at head `743ace9` on 2026-09-05T08:12:26Z
unless a different commit is named.

- tests: `go test ./...` — 15 packages ok; `go test -race` on `internal/tick`, `internal/stream`, `internal/compose`, `internal/provider`, `internal/ruleeval` — ok
- tests: `NEUROSYMBOLIC_ENGINE=<built at head> uv run pytest -n auto` — 74 passed (48 of them in `tests/test_cli_engine.py`, `tests/test_engine_client.py`, `tests/test_e2e_toy_robot.py`, `tests/test_repo_boundary.py`)
- tests per obligation (devague evidence `e1`–`e9`, proposed): `internal/rules` donor/refusal/layer tests (e1); `internal/tick` rate and no-wall-clock tests + `internal/ruleeval` timing tests (e2); `internal/senselog` streak/grammar/stdout tests + `internal/conformance.TestSenseLogIsTheOnlyDiagnosticSurface` (e3); `internal/allowlist` (e4); `internal/mgmt` error-contract tests + Python relay tests (e5); `internal/conformance.TestDonorConformance` 9 cases (e6); the bench record at `3d8292d` (e7); `internal/stream` loopback/mgmt/backpressure/socket tests + e2e status/settle tests (e8); `internal/tick` completeness/abstention/settle tests (e9)
- lint: `uv run black --check`, `isort --check-only`, `flake8`, `bandit -c pyproject.toml -r neurosymbolic_system` — clean; `gofmt -l` (non-vendor) — clean; `go vet ./...` — clean; `markdownlint-cli2 "**/*.md" …` — 0 errors; `uv run teken cli doctor . --strict` — all PASS
- build: `CGO_ENABLED=0 GOARCH=arm64 go build -mod=vendor ./...` — ok; `file` on the arm64 artifact — statically linked
- commits: `48e6e9e..743ace9` (merges `b630f09`, `9f1a702`, `7485f65`, `db8f658`, `330c702`, `44f10ca`, `d97b71c`, `d65c5fd`, `ed49503`, `fd72d6b`, `7c746ca`, `307babc`, `b341319`, `f5dcd3a`, `0df3be2`, `871b6e1`, `af164ba`, `743ace9`)
- issues: #4 – #15 (label `decision`)
- records: `docs/verification/2026-09-05-arm64-bench.md`, `docs/verification/2026-09-05-donor-conformance.md`

## Delivery Claims

| Claim | Confidence | Evidence |
|-------|------------|----------|
| One static linux/arm64 binary with no third-party dependency but the allowlisted TOML decoder (c2, c16, c17) | high | `internal/allowlist` test · `go.yml` static-link assertion · `file` output at `743ace9` |
| Both donors' `schema_version = 1` rules files load unchanged; every documented invalid case is refused naming the rule id; schema 2 adds `all`/`any` (c3, c11, c33) | high | `internal/rules` tests (e1) · `internal/rules/testdata/{reachy,microduck}` |
| Channels, senses and actions are adaptor configuration; the engine carries no robot literal (c10, c36) | high | `internal/adaptor.TestNoDonorLiteralsInEngineSources` · `tests/toy_robot/adaptor.toml` |
| Timing semantics are cadence-independent (c13) | high | `internal/tick` and `internal/ruleeval` timing tests at 20/50/100 Hz (e2) |
| One admission registry serves rule fires and agent intents; inhibits block both (c14) | high | `internal/ruleeval/registry_test.go` |
| Every drop names its reason per episode on stderr; stdout stays clean (c15) | high | `internal/senselog` tests · `internal/conformance` stdio test (e3) |
| The engine owns the whole tick and streams complete poses; abstention yields, never freezes (c30, c20) | high | `internal/tick` tests (e9) · `tests/test_e2e_toy_robot.py` |
| Two transports: socket and stdio with the same framing, heartbeat, end-of-stream, event frames, protocol version, 0600 socket, no TCP by default (c28, c37, c38, c39, c42, c43) | high | `internal/stream` tests (e8) · e2e SIGTERM/SIGKILL/stdin-close tests |
| Management verbs share the CLI error contract on both sides; `rules check` works with no robot and no engine (c19, c7) | high | `internal/mgmt` tests · `tests/test_cli_engine.py` (e5) |
| Event-keyed rules load through the same validator with a default entry and dedupe (c12) | high | `internal/events/router_test.go` · `internal/rules/events_test.go` |
| The decision provider is off-thread, warmed, bounded, and an abstention when absent (c4, c18) | high | `internal/provider` overrun/integration/race tests |
| A third robot integrates with a config, a rules file and a 189-line stdlib client, no engine code (c1, c5, c25) | high | `tests/toy_robot/` · `test_toy_client_stays_a_client` |
| Per-tick evaluation of 200 rules over 20 fields on arm64: p99 616 µs, 0 overruns, RSS 10.3 MB (c6, c27) | high | `docs/verification/2026-09-05-arm64-bench.md` at `3d8292d` (e7) |
| Donor conformance: 9 fixture cases replay identically (engine side of c26) | high | `internal/conformance.TestDonorConformance` · `docs/verification/2026-09-05-donor-conformance.md` (e6) |
| microduck-cli and reachy-mini-cli load their overlays through the engine with zero behaviour change on the robot (consumer side of c26) | unverified | consumers' work, in their repos; not claimed done |
| Rules hot reload keeps the old set on a failed validation (q7) | medium | `internal/compose` `rulesLane` test; not exercised on a live robot |
| Only this repository was touched (c31) | high | `tests/test_repo_boundary.py` · sibling checkouts show no commits from this run |
| Motor safety stays on the consumer's motor-control surface (c32) | high | README safety-boundary section; the engine has no limit or collision code by construction (allowlist) |

## Remaining Work / Follow-up

- `t16` — write the CHANGELOG entry via the version bump when the PR opens; post the three drafted issues (`docs/handoff/*.md`) to reachy-mini-cli, microduck-cli and arm101-cli after the operator reviews them. Owner: operator (outward-facing).
- Adjudicate the proposed records: deviations `d1`–`d3`, evidence `e1`–`e9`, deltas `b1`–`b8`; then `devague today` renders `docs/current-spec.md` from confirmed state. Owner: the human.
- Consumer swaps (c26 consumer side) — each consumer's own agent, own PRs, own schedule; the hand-off drafts name the fixtures and divergences (issues #8, #11, and the conformance record's not-reproduced list).
- MicroDuck's blocking-class admission as an engine option (`Config.RefuseBlocked`) if the consumer wants it (issue #8) — decision pending.
- Contention class per action in the adaptor schema (issue #11) — if a robot needs per-action defaults.
- Conformance fixtures cannot express contention classes beyond stoppable/passive or data-only abstention (t14) — a fixture-format extension if the consumer swaps need it.
- Wire-protocol tokens and rules-schema keywords are guard exemptions; a stricter scoping (schema keys as named constants in one file) is a small follow-up (issue #6).
- arm64 CI leg under QEMU is unverified until a real Actions run; the local arm64 box is the only measured target.
- Provider latency and memory behind a real on-device model remain parked (frame v1); the bench measured the engine alone.

## Review round (after the summary was written)

PR #16's automated review left 21 inline threads. All are answered and
resolved: 18 fixed in `6abfcfa`, `f2270c3`, `dc3a458`, `b7048e6`; 3 pushed
back citing the spec (the branch prefix; two requests for per-tick drop lines
where c15 requires per-episode logging). Behaviour that changed after head
`743ace9`, in the order a consumer would notice:

- stream: every inbound frame kind is decoded strictly (unknown field refused
  naming it); a client name over 64 bytes is refused, not truncated;
  management verbs in flight are bounded per session (default 4, refusal
  `mgmt-busy`); the tick path enqueues payloads and the writer encodes; the
  queued-frame accounting is race-free against shutdown; the hello reply
  precedes any telemetry and every tick after the greeting is delivered.
- tick: an explicit behavior id already active is refused (`duplicate-id`).
- senselog: `detail` is sanitized; `Parse` refuses control characters.
- rules: a `source/type` field of a declared `[[event]]` entry is a valid
  predicate field independent of the vocabulary (`is_true` / `is_false` /
  `absent_for` only).
- mgmt: `rules migrate` writes a temp file, verifies it, renames; never
  truncates or deletes the destination; rewrites only the top-level
  `schema_version`; verification compares the whole loaded config.
  `neurosymbolic-engine` with no command, or a noun with no sub-verb, emits
  the two-line error contract; a `help` verb prints usage with exit 0.
- compose: a first SIGTERM restores default disposition so a second one
  terminates; a written non-positive `timeout_s` / `queue_depth` / `cadence`
  is refused; JSON loaders refuse trailing data.
- Python: `DEFAULT_TIMEOUT_S`; every launch `OSError` is an environment
  `CliError`; a malformed error `code` falls back to the client-authored
  error; every verb has an `explain` entry, pinned by a parser-walk test.

CI on `b7048e6`: all checks green including arm64 under QEMU; SonarCloud
quality gate OK with 0 open issues; 21 of 21 threads resolved. The
`.devague` evidence records still cite the `743ace9` run; the review-round
tests ran in the same gates and are listed here rather than re-filed.
