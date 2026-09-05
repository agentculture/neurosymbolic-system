# Go rule engine core

> neurosymbolic-system ships as one static Go binary: the rule engine that powers rule-based CLI actions for any robot — event-based and tick-based rules from one or many YAML files, an optional embedding/small-model fast path, driven from Python by reachy-mini-cli, microduck-cli and arm101-cli through a thin stdlib client the way culture-nodes drives its Go control plane; each robot adds only adaptors and configuration

## Audience

- Robot CLI maintainers (reachy-mini-cli, microduck-cli, arm101-cli) and the agents and humans who author rules for a deployed robot.

## Before → After

- Before: Today each robot CLI carries its own copy of the rules layer: reachy-mini-cli (~20k lines across behavior/ + motion/, 18 files importing CLI or daemon internals), microduck-cli (a second, extraction-first engine with `schema_version` = 1), and arm101-cli has none and would be the third copy.
- After: A robot CLI depends on one binary and one rules schema; adding a robot means writing adaptors for its channels, senses and sink, plus a rules file — no rule engine, validator, arbitration or drop-logging code of its own.

## Why it matters

- On a robot, power, memory and CPU are scarce and latency is the product; a Go core keeps the rules layer on-device with a low memory footprint and a predictable per-tick cost.

## Requirements

- The core is written in Go and ships as one static binary for on-device use; robot CLIs stay Python and drive it, as culture-nodes' Python nodes CLI drives its Go control plane.
  - instruction: CI builds with GOOS=linux GOARCH=arm64 `CGO_ENABLED`=0 and asserts the artifact is static; a smoke run on the arm64 runner or a self-hosted Jetson starts the engine and evaluates a fixture rules file.
  - honesty: A single statically linked linux/arm64 binary (`CGO_ENABLED`=0) built from this repo runs the engine on a Jetson-class box with no Python, libc version or SDK on the load path; ldd reports 'not a dynamic executable'.
- Rules are event-based and tick-based, scalable to many rules, loaded from one TOML file or many.
  - honesty: The same loader reads a tick-predicate rules file and an event-keyed rules file, from one path or a list of paths, and merges them per rule id in listed order; a duplicate id across files is refused naming both sources.
- An optional embeddings + small-model path lets a rule decide or react fast when a symbolic predicate is not enough.
  - honesty: A rule can name a decision provider (embedding or small model) as its predicate source, the provider is an injected client with a timeout, and when it is absent, slow or erroring the rule abstains with a named drop and the tick is not delayed.
- reachy-mini-cli, microduck-cli and arm101-cli run on it without re-implementing the rule/action/neurosymbolic layer; each robot adds only adaptors and configuration.
  - honesty: A consumer integrates by supplying exactly: a channel list, a sense-field vocabulary with providers, an action vocabulary with a sink, and rules files — and touches no engine code. Proven by a fixture 'toy robot' adaptor in this repo's tests that is neither Reachy nor MicroDuck.
- Channels, sense fields and action names are per-robot configuration supplied by the adaptor, never constants in the core: reachy has ('head','antennas','`body_yaw`') and 13 sense fields, microduck has ('twist','head','pose','mouth','sound','skill') and 20+ sense fields, arm101 has neither yet.
  - honesty: The engine binary contains no channel name, sense field or action name as a literal; all three arrive at startup from the adaptor's configuration, and a rules file naming an undeclared field or action is refused at load.
- Rule semantics port from the donors: per-id override across a shipped layer and a box-local overlay, enabled = false tombstones, `cooldown_s` / hysteresis / `duration_s`, data predicates {field, op, value} with all / any conjunction in `schema_version` = 2, fail-closed validation naming the offending rule id, `schema_version` refused when missing, NaN/inf refused, looping actions required to carry `duration_s`, and rule ids treated as a public interface.
  - honesty: Both donors' shipped default rules files (reachy-mini-cli `default_rules.toml`, microduck-cli `default_rules.toml`, `schema_version` = 1) load unchanged, and each documented refusal case (unknown field, missing `schema_version`, NaN cooldown, looping action without `duration_s`, duplicate id) is refused with the rule id in the message.
- A second rule dialect is expressible in the same loader: event-keyed rules (source/type -> priority, urgency, `llm_evaluate`, dedupe, inject template, a default: entry) as in `reachy_nova`'s config/nervous-system/rules.yaml, so 'event based' and 'tick based' rules share one file format and one validator.
  - honesty: `reachy_nova`'s config/nervous-system/rules.yaml, transcribed to TOML, loads through the same validator as a tick rules file, and an event with no matching entry resolves to the default: entry's priority and urgency.
- The tick rate is a parameter, not 50 Hz baked in: reachy-mini-cli's `rule_engine.py` records ~23 Hz measured on the deployed robot (#99), and cadence-dependent tunings are a named bug class in the donor CLAUDE.md.
  - honesty: The tick period is a startup parameter; the engine passes its rule-timing tests (`cooldown_s`, hysteresis, `duration_s`, `absent_for`) identically at 20 Hz, 50 Hz and 100 Hz with an injected clock.
- One admission inbox serves rule-fired and agent-injected intents alike (run once, standing goal, set mode, inhibit): the donor's intents.py namespaced spool and microduck's single admission registry are the two existing shapes.
  - honesty: A rule-fired action and an agent-injected intent (run once, standing goal, set mode, inhibit) enter the same admission registry and are arbitrated by the same class and recency rules; an inhibit blocks both sources alike.
- Every drop names its reason on stderr in the \[SENSE stage=... source=... event=...\] grammar (`reachy_nova` `sensory_log.py` -> donor senselog), and gated streaks log per episode, not per tick (donor #99: 6722 drop lines in a 3 h window before the fix).
  - honesty: Every suppression and every provider drop writes exactly one stderr line in the \[SENSE stage=<stage> source=<source> event=<event>\] grammar at streak entry, on reason change, and at streak end with the tick count; stdout carries nothing but results.
- linux/arm64 is the first-class build target (Jetson Orin and Thor, DGX Spark, the Reachy wireless unit — the only platforms microduck-cli's README records), linux/amd64 for dev; CGO off so the binary has no libc coupling.
  - honesty: The release build matrix is linux/arm64 and linux/amd64 only, CGO off, and go vet / go test run on both in CI.
- Two transports, each serving a purpose: a persistent stream (unix socket or stdio) for constant communication and control at tick rate, and a one-off per-command call for management verbs. Which one a robot uses depends on its integration surface.
  - honesty: The engine exposes both a persistent stream endpoint (unix socket, length-prefixed JSON frames, senses in and pose out at tick rate) and a one-off request endpoint for management verbs (rules check, rules list, status); a consumer can use either without the other.
- The Python CLI stays the management surface, as today, and shrinks; the engine is Go, built on-device with go build for now. Wheel-embedded binaries or release assets are a later decision.
  - honesty: The Python package gains no runtime dependency and no bundled binary; it locates the engine on PATH or via `NEUROSYMBOLIC_ENGINE`, and doctor reports a missing or version-mismatched engine as a named environment error with the go build remediation.
- The Go engine owns the whole tick — arbitration, composition, pose streaming — the heavy surface; consumers keep only adaptors and configuration.
  - honesty: Given a channel list and behaviors claiming channels, the engine's per-tick output is a complete pose (every channel filled, unclaimed ones neutral), streamed at the configured period; a consumer supplies only the sink.

## Honesty conditions

- A fresh consumer — the fixture toy robot in this repo's tests, neither Reachy nor MicroDuck — goes from zero to a running rules-driven pose stream using only the built binary, a channel / sense / action config, and a TOML rules file, with no engine code written.
- Measured on linux/arm64 and recorded under docs/verification: steady-state RSS of the engine under a 200-rule, 20-field load stays under a published ceiling (proposed 32 MB), and per-tick evaluation stays inside the configured budget with zero overruns over 10,000 ticks; both numbers are printed by an 'engine bench' verb.
- A robot CLI maintainer can integrate from the README's adaptor guide alone, and a rules author can validate a file with 'rules check' with no robot attached and no engine running; both paths are exercised by tests.
- go list -deps of the engine command contains no robot SDK, transport or media package; a test asserts an import allowlist (stdlib plus this module) and fails on any addition.
- The decision provider client is warmed at startup and called from a worker behind a bounded queue; a fixture provider that sleeps 2 s produces zero tick overruns and exactly one named drop line per abstention.
- The Go binary's whoami / doctor / version verbs emit error: and hint: lines on stderr, results on stdout, exit 0 / 1 / 2, and honor --json — asserted by the same contract tests the Python CLI already runs.
- The engine ships no motion primitive: an action is an opaque name plus params validated against the adaptor's declared domain, and the fixture sink receives the name and params unchanged.
- The cited counts hold at the recorded commits: reachy-mini-cli behavior/ + motion/ about 20k lines with 18 files importing reachy.cli.`_errors` or reachy.daemon; microduck-cli behavior/ requiring `schema_version` = 1; arm101-cli with no behavior package — re-verifiable by wc and grep at those commits.
- The fixture toy robot adaptor in tests/ is under 200 lines and contains no arbitration, validation, timing or drop-logging code of its own.
- A conformance fixture set derived from both donors' rule tests (rules file + sense trace in, expected fire / suppress / pose trace out) replays byte-identically through the engine; the consumer-side swap is explicitly out of scope and listed as the consumers' work.
- An 'engine bench' verb prints tick p50 / p99 and overrun count for the 200-rule / 20-field load plus steady-state RSS, and one recorded run on an arm64 box is committed under docs/verification with the commit sha.
- No commit in this work touches a path outside this repository; every sibling-facing need is filed as an issue through the communicate skill and listed in the delivery summary.
- The engine performs no motor-level collision or limit check; motor-state rules work only when the adaptor declares those fields as senses, and the adaptor guide states that the fast path for motor safety is the consumer's motor-control surface.
- Schema version 2 accepts all: and any: lists of predicates (nesting to one level), rejects empty lists and an unknown key beside them, and every schema-1 file still loads under the schema-2 validator; no rule text is executed as code.

## Success signals

- microduck-cli and reachy-mini-cli, on their own side and in their own PRs, load their existing `schema_version` = 1 rules.toml overlays through the Go engine with zero behavior change on the robot (their live suites pass); this repo ships the engine and a conformance fixture set, not the consumer swap.
- Per-tick evaluation of a loaded rule set on the target arm64 box stays under a measured budget (a number set by the tick-rate decision, well inside the donor's 20 ms at 50 Hz), with steady-state RSS reported alongside.

## Scope / boundaries

- The Go core never links a robot SDK, transport or media stack; the pose sink, sense peeks, speech and intent inbox are adaptors in the consumer, as the donor's TargetSink / SenseProviders seams already prescribe (18 donor files still import reachy.cli.`_errors` or reachy.daemon — that debt stays on the donor side).
- No model inference runs inside the core process: the embedding / small-model fast path is an injected provider behind a timeout with a named drop, off the tick thread — the donor's reachy/stash/embeddings.py already talks to a local OpenAI-compatible /v1/embeddings gateway (Qwen3-Embedding-0.6B) over stdlib HTTP.
- The Python agent-first CLI (whoami / learn / explain / doctor, the teken rubric gate) stays as the introspection surface; the Go binary mirrors its error contract (exit 0/1/2, error: / hint: lines, --json) the way culture-nodes' internal/clifmt does.
- Motion rendering and the behavior library stay in the consumer (reachy's 39 LIBRARY entries with Param domains, microduck's skills); the core arbitrates named actions it does not know how to render.
- Only this repo is touched by this work. Donor repos (reachy-mini-cli, microduck-cli, arm101-cli, `reachy_nova`) are read to learn; they pull the new engine and test on their side, in their own PRs.
- Collision and allowed-motion safety for the motors lives on the lower-level surface that controls the motors, where it is faster. The engine supports safety-style reactive rules only when motor state is fed in as events, and that is not the fast path.

## Non-goals

- `reachy_nova` does not import or run this binary; it is cited for the event/priority rule shape and the sensory log, per its own CLAUDE.md ('a reference, not a consumer'), and its dependency set (boto3, ultralytics, `nemo_toolkit`) is out of the question here.
- No cross-process hardware arbitration in the core: one SDK client, one media session, heartbeat liveness (behavior/liveness.py in both donors) stay the consumer's problem.
- Consumers gain no Python runtime dependency: pyproject.toml stays dependencies = \[\] in all three CLIs (a stated rule in each CLAUDE.md); what they gain is a binary plus a stdlib client, vendored cite-don't-import or imported from this package.

## Assumptions

- The Python side of each consumer is a thin stdlib client of the Go binary, mirroring culture-nodes' `culture_nodes`/`api_client.py`: urllib only, zero deps, the {code, message, remediation} error body relayed verbatim as a CliError.
- Two donors, not one: the extraction source is reachy-mini-cli's reachy/behavior/ AND microduck-cli's `microduck_cli`/behavior/ (its decision c20, extraction-first), and the Go loader must accept both robots' existing `schema_version` = 1 rules.toml overlays or ship a documented migration.

## Scope exploration

- `s1` — `../culture-nodes (culture_nodes/api_client.py, cmd/nodes/{main,serve}.go, internal/compiler/cel.go, .github/workflows/release.yml, go.mod)`: The Python 'nodes' CLI is a zero-dependency urllib client of a long-lived Go 'nodes serve' daemon on :8080 (spec decision c28: no engine logic in Python); the Go binary mirrors the CLI error contract via internal/clifmt.CliError and ships as a multi-arch OCI image, never inside the wheel; guards are CEL via cel-go. The pattern is per-command REST to a daemon, not a per-tick stream.
  - seeds: `c2`, `c8`, `c19`
- `s2` — `../reachy-mini-cli/reachy/behavior/{rules.py,default_rules.toml,rule_engine.py}`: Rules are two TOML layers merged per rule id (shipped package resource + box-local overlay), enabled=false tombstones, exactly one data predicate {field,op,value} per rule (no conjunction — a corroboration rule the shipped file explains), `cooldown_s`/hysteresis/`duration_s`/say, fail-closed validation, `MAX_SAY_CHARS` refused never truncated; rule ids are pinned by test as a public interface; suppressions log once per episode (#99); the engine ran ~23 Hz on the deployed robot, not 50.
  - seeds: `c11`, `c13`, `c15`
- `s3` — `../reachy-mini-cli/reachy/behavior/{engine.py,model.py,arbitration.py,sense.py}`: CHANNELS = ('head','antennas','`body_yaw`') is a module constant the whole engine iterates; Sense is a 13-field dataclass with \*`_age_s` freshness; SenseProviders are peek callables that degrade to None; engine.py imports reachy.cli.`_errors` and reachy.robot.transport — 18 behavior/motion files carry CLI or daemon imports, the seam debt the extraction has to leave behind.
  - seeds: `c10`, `c17`, `c24`
- `s4` — `../microduck-cli/microduck_cli/behavior/ + CLAUDE.md 'neurosymbolic-system — the later home' (decision c20)`: A second Python engine already exists, built extraction-first behind the same six seams (TargetSink, SenseProviders, `tick_seam`/TickBus, rules-as-data, one admission registry, heartbeat liveness); CHANNELS = ('twist','head','pose','mouth','sound','skill'); rules.toml requires `schema_version` = 1 and refuses NaN/inf and looping actions without `duration_s`; shipped defaults are safety inhibits (fallen-inhibit, low-battery-inhibit); its CLAUDE.md forbids depending on this package until runtime modules ship.
  - seeds: `c9`, `c10`, `c11`, `c14`, `c23`
- `s5` — `../arm101-cli (CLAUDE.md, pyproject.toml, arm101/{hardware,explore,cli/_consent.py})`: No rules or behavior layer at all: the package is hardware/ (motion, safety, limits, `soft_limit_store`, gentle, bus), explore/ (reachmap engine, budget) and the TTY / dry-run / --apply consent gate; runtime deps empty with a 'seeed' extra for the Feetech servo SDK. It is a greenfield third consumer with a safety vocabulary but nothing to migrate.
  - seeds: `c5`, `c10`, `c24`
- `s6` — `../reachy_nova/config/nervous-system/rules.yaml + CLAUDE.md (nova_mqtt.py, harness/bus.py)`: A different rule dialect: entries keyed by source/type event (tracking/`snap_detected` ...) with priority, urgency, `llm_evaluate`, `inject_template`, voice, sense, dedupe and a default: entry; events arrive over MQTT topics nova/events/SOURCE/TYPE and route to the voice model. `reachy_nova` is a ReachyMiniApp with heavy deps (boto3, ultralytics, `nemo_toolkit`) and is a reference, not a consumer.
  - seeds: `c12`, `c15`, `c21`
- `s7` — `../reachy-mini-cli/reachy/stash/embeddings.py + reachy_nova pyproject.toml model deps`: The only embedding client in the family is a stdlib urllib call to a local OpenAI-compatible /v1/embeddings gateway (default model Qwen/Qwen3-Embedding-0.6B, 30 s timeout); no CLI runs an on-device embedding or small-model runtime in-process; `reachy_nova` lazy-loads YOLO / parakeet in daemon threads to keep them off its 50 Hz loop.
  - seeds: `c3`, `c18`
- `s8` — `Hardware targets: ../microduck-cli/README.md proof table, ../reachy-mini-cli/README.md install matrix, local go version`: The only platforms on record are Linux aarch64 — DGX Spark (GB10), Jetson AGX Thor, Jetson AGX Orin — plus the Reachy wireless unit reached over ssh and installed with 'uv tool install reachy-mini-cli\[daemon\]'; the dev box has go1.26.5 linux/arm64. Nothing here targets a non-Linux or non-arm64 robot.
  - seeds: `c6`, `c16`
- `s9` — `This repo: pyproject.toml, .github/workflows/{publish.yml,tests.yml}, neurosymbolic_system/`: 0.8.x is the bare agent-first Python CLI (cli/ + explain/, 22 tests, zero deps), a hatchling wheel of `neurosymbolic_system` only, PyPI Trusted Publishing on push to main, and no Go toolchain anywhere in CI — culture-nodes has a separate go.yml (build/test) and a Dockerfile this repo lacks.
  - seeds: `c19`, `c23`
- `s10` — `../reachy-mini-cli/reachy/behavior/{intents.py,library.py}`: Agent intents (run once, standing goal, set mode, inhibit) arrive through a namespaced on-disk command spool and ride the same tick seam as rules; the behavior LIBRARY is 39 robot-specific motion entries with Param domains (nod, shake, orient-to-sound, gaze-hold ...) — animation the core has no business rendering.
  - seeds: `c14`, `c20`
- `s11` — `Both donors' behavior/liveness.py + reachy-mini-cli CLAUDE.md hardware-ownership section`: Cross-process arbitration is a state.json heartbeat, not a flag file; one SDK client and one single-consumer media session per robot; a foreground verb beside a live engine exits 1. All of it is process supervision the consumer owns.
  - seeds: `c22`

## Decisions

- Predicate language: the donors' data predicate {field, op, value}, extended in `schema_version` = 2 with all / any conjunction lists (still data); `schema_version` stays the migration key; CEL is left as a possible `when_expr` behind an explicit opt-in only if a real rule ever needs it. Keeps the on-device footprint and the agent-authorable property.

## Hard questions

- risk: microduck's Sense has 20+ fields and reachy's 13, with different names for overlapping concepts (`self_moving` in both, but pat vs limp/fallen); a shared engine must not silently unify them — the field vocabulary is per adaptor, and a rules file authored for one robot must be refused by the other.
- Do event-keyed rules run on the tick (an event is a sense field that is true for one tick) or on an event bus beside the tick? `reachy_nova` routes events to a voice model over MQTT, not to a pose; the answer decides whether 'priority/urgency' are arbitration inputs or just routing metadata.
- risk: A provider call on the tick thread is the 425-1213 ms client-construction incident again; the fast path must be a pre-warmed client called off-thread with a bounded queue, and its result consumed on a later tick.
- Who owns the socket lifecycle when both the stream and a one-off management call are live: does a management call go through the running engine (one process, one owner of the hardware seam) or start a second short-lived process? The donors' liveness rule says a foreground verb beside a live engine must refuse.
- risk: go build on a robot box needs a Go toolchain (~250 MB) and module cache on-device; Jetson boxes have the disk, but a fresh box with no network cannot fetch modules — vendor the module tree or ship go.sum-pinned builds from CI later.

## Open parks

- [unknown_nonblocking] The latency and memory budget of the embedding / small-model fast path on the target arm64 box is unmeasured: no consumer runs an on-device embedding runtime today (reachy's stash calls a gateway serving Qwen3-Embedding-0.6B; `reachy_nova` lazy-loads YOLO and parakeet in Python threads), so the 'decide fast' promise has no number behind it yet.
