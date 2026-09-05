# Build Plan — Go rule engine core

slug: `go-rule-engine-core` · status: `exported` · from frame: `go-rule-engine-core`

> neurosymbolic-system ships as one static Go binary: the rule engine that powers rule-based CLI actions for any robot — event-based and tick-based rules from one or many TOML files, an optional embedding/small-model fast path, driven from Python by reachy-mini-cli, microduck-cli and arm101-cli through a thin stdlib client the way culture-nodes drives its Go control plane; each robot adds only adaptors and configuration

## Tasks

### t1 — Go module scaffold and CI: go.mod at repo root, cmd/neurosymbolic-engine, internal/ layout, go.yml job (vet, test, build) on a linux/amd64 + linux/arm64 matrix with `CGO_ENABLED`=0, and a static-binary assertion

- covers: c2, h1, c16, h11
- acceptance:
  - go build ./... and go test ./... pass on both GOARCH=amd64 and GOARCH=arm64 in CI with `CGO_ENABLED`=0
  - CI runs 'file' (or readelf) on the arm64 artifact and fails unless it is statically linked; the engine prints its version and revision from -ldflags
  - The Python test suite, teken rubric gate and lint job stay green; pyproject.toml gains no runtime dependency

### t2 — Rules schema and loader (internal/rules): TOML, `schema_version` 1 and 2, data predicates with all/any conjunction in v2, per-id merge of shipped + overlay + N files in listed order, enabled=false tombstones, fail-closed validation naming the rule id

- depends on: t1
- covers: c3, h2, c11, h6, c34, h29
- acceptance:
  - Both donors' `default_rules.toml` files (copied verbatim into testdata/) load with `schema_version` = 1 and produce the expected rule ids, kinds and parameters
  - Each documented refusal (unknown field, unknown op, missing `schema_version`, NaN/inf cooldown, looping action without `duration_s`, duplicate id within or across files, empty all/any list, unknown key beside all/any) is refused with the rule id and source path in the message
  - A list of paths merges per rule id in listed order; a tombstone in a later file disables an id from an earlier file; schema-1 files still load under the schema-2 validator
  - A fixture overlay carrying \[modes.\*\] with `active_mode`, params overrides and a say string loads with every field preserved; a say over 500 chars, a params key outside the action's domain, and modes without `active_mode` are each refused naming the rule id

### t3 — Adaptor vocabulary (internal/adaptor): channels, sense fields with types, action names with param domains declared at startup from a TOML config; the engine holds no robot literal; a rules file naming an undeclared field or action is refused at load

- depends on: t1
- covers: c10, h5, c20, h22, c36, h30, h36
- acceptance:
  - grep of the engine sources finds no channel, sense field or action name from either donor as a string literal (a test enforces the list)
  - Loading reachy's rules against microduck's vocabulary is refused naming the undeclared field; loading against its own vocabulary succeeds
  - An action is passed to the sink as an opaque name plus params validated against the declared domain; the fixture sink receives them unchanged
  - The adaptor config declares per-channel arity and neutral, and per-action trajectories (keyframes or easing over `t_local`) for every channel the action claims; an action missing a trajectory for a claimed channel, or a neutral of the wrong arity, is refused at load

### t4 — Tick core (internal/tick): parameterized period, injected clock, per-channel arbitration by class and recency with abstention, complete-pose composition with neutral fill, TargetSink interface

- depends on: t3
- covers: c13, h8, c30, h14, c41, h36
- acceptance:
  - Every emitted pose fills every declared channel; an unclaimed channel is neutral; an owner returning nil for a channel yields it to the next claimant the same tick
  - Arbitration tests for passive / stoppable / unstoppable / stopping classes match the donor's arbitration.py cases
  - The same timing tests pass at 20, 50 and 100 Hz with the injected clock; no wall-clock read exists inside the tick loop
  - Pose frames over an action's duration match its declared trajectory sampled at the tick period within float tolerance; all engine state is owned by the tick goroutine and every other writer enqueues on a bounded channel drained at tick start

### t5 — Rule evaluation on the tick (internal/ruleeval): `cooldown_s`, hysteresis, `duration_s`, `absent_for`, per-episode suppression state; one admission registry for rule-fired actions and agent intents (run once, standing goal, set mode, inhibit)

- depends on: t2, t4
- covers: c14, h9
- acceptance:
  - cooldown, hysteresis, `duration_s` and `absent_for` semantics match the donor `rule_engine.py` docstring, asserted with an injected clock
  - A rule-fired action and an injected intent enter the same registry; an inhibit blocks both; a standing goal is re-admitted each tick until withdrawn; set mode swaps the active mode

### t6 — Event-keyed rule dialect (internal/events): source/type entries with priority, urgency, dedupe window, template and a default: entry in the same TOML loader and validator; events surface on the tick as one-tick sense fields and as a routed event record

- depends on: t2
- covers: c12, h7
- acceptance:
  - `reachy_nova`'s rules.yaml transcribed to TOML loads through the shared validator; an event with no entry resolves to the default: priority and urgency
  - Two events sharing a dedupe key within the window collapse to one routed record; distinct keys do not

### t7 — senselog (internal/senselog): the \[SENSE stage=.. source=.. event=..\] stderr grammar, per-episode suppression logging (entry, reason change, end with tick count), stdout never written

- depends on: t1
- covers: c15, h10, h37
- acceptance:
  - A gated streak of 200 ticks emits exactly one entry line, one line per reason change, and one summary line carrying the tick count
  - Every drop site names its reason; a test parses every emitted line against the grammar and stdout stays empty

### t8 — Stream endpoint (internal/stream): unix socket with length-prefixed JSON frames, sense frames in and pose frames out at tick rate, backpressure as a named drop, one owner per socket

- depends on: t4
- covers: c28, h12, c37, c38, c39, c43, h38
- acceptance:
  - A sense frame written on the socket yields a pose frame within one tick period in a loopback test
  - A slow reader causes pose frames to be dropped with a named senselog line, never a blocked tick
  - A second connection while one is live is refused with a structured error
  - The stream carries heartbeat frames at the declared interval, an end-of-stream frame on graceful stop, and event frames (fire, per-episode suppress, ownership change, intent admit/evict) beside pose frames; every frame carries a protocol version and a mismatch is refused naming both versions
  - The socket is created 0600 in the consumer-provided directory; a TCP listen address is refused without --insecure-tcp; a test asserts no TCP listener exists after startup

### t9 — Management endpoint and Go CLI verbs (internal/mgmt, internal/clifmt): request/response over the same socket or a one-off exec, verbs whoami / version / doctor / rules check / rules list / status, the {code, message, remediation} error body, exit 0/1/2, --json, error:/hint: on stderr

- depends on: t2, t3
- covers: c19, h21, c40, h33
- acceptance:
  - Every verb passes the same output-contract tests the Python CLI runs (results stdout, errors stderr, --json, exit codes)
  - A management request during a live stream is answered without pausing the stream (measured: no tick overrun in the loopback test)
  - rules check validates a file with no robot attached and no engine running
  - rules migrate writes <name>.v2.toml and leaves the input byte-identical; rules reload re-reads the configured files on a live engine and refuses (keeping the old set) if the new set fails validation; no verb writes to a path it received as input

### t10 — Decision provider (internal/provider): OpenAI-compatible embeddings and small-model client, warmed at startup, called from a worker behind a bounded queue with a timeout; a rule can name it as a predicate source; absence, timeout or error is an abstention with a named drop

- depends on: t5
- covers: c4, h3, c18, h20, h34
- acceptance:
  - A fixture provider that sleeps 2 s produces zero tick overruns and exactly one named drop line per abstention
  - A rule bound to the provider fires on the tick after the result arrives, never on the tick that requested it
  - With no provider configured the rule abstains and the engine starts normally
  - go test -race over a test that drives the stream, the management endpoint and the provider worker concurrently for 10,000 ticks reports no data race; a full inbox appears as a named drop line

### t11 — Import allowlist guard: a test that walks go list -deps of the engine command and fails on any package outside the stdlib plus this module; documents the allowlist

- depends on: t1
- covers: c17, h19
- acceptance:
  - The test fails when a robot SDK, transport, media or third-party package is added to the engine's dependency graph
  - The only I/O packages in the graph are net, os, io and encoding/json (plus a TOML decoder, if vendored, listed explicitly)

### t12 — Python management surface: `neurosymbolic_system`/`engine_client.py` (stdlib socket/exec client relaying the error body verbatim as CliError), engine locator (PATH then `NEUROSYMBOLIC_ENGINE`), a doctor check for a missing or version-mismatched engine, and engine / rules noun groups in the Python CLI that delegate to the binary

- depends on: t9
- covers: c29, h13, h35
- acceptance:
  - pyproject.toml keeps dependencies = \[\] and no binary is bundled in the wheel
  - doctor reports a missing engine as an environment error (exit 2) with the go build remediation, and a version mismatch by name
  - rules check and engine status in the Python CLI relay the binary's {code, message, remediation} verbatim with the matching exit code; explain catalog, overview and learn are updated in lockstep and teken cli doctor --strict stays green
  - The Python doctor check surfaces an engine protocol-version mismatch by name with the go build remediation

### t13 — Toy-robot fixture adaptor and end-to-end test: a third robot (neither Reachy nor MicroDuck) with its own channels, senses and actions, a TOML rules file, a fixture sink, driven zero-to-pose-stream through the stream endpoint

- depends on: t5, t8, t9
- covers: c1, h16, c5, h4, c25, h24, h31
- acceptance:
  - The fixture adaptor is under 200 lines and contains no arbitration, validation, timing or drop-logging code
  - The end-to-end test starts the built binary, streams a sense trace, and asserts the pose trace and the senselog lines
  - The end-to-end test kills the engine with SIGTERM and with SIGKILL; the fixture consumer sees an end-of-stream frame or a heartbeat lapse within two intervals and its settle-to-neutral handler runs in both cases

### t14 — Conformance fixtures from both donors: rules files plus sense traces in, expected fire / suppress / pose traces out, derived from reachy-mini-cli and microduck-cli rule tests (read-only in those repos), replayed through the engine; records the donor commit shas and line counts cited in the spec

- depends on: t13
- covers: c24, h23, c26, h25, h32
- acceptance:
  - Every fixture replays byte-identically; a diff names the rule id and tick on mismatch
  - docs/verification records the donor commits, the wc/grep counts behind c24, and states that the consumer-side swap is the consumers' work
  - Every fire, per-episode suppression, ownership change and intent admit/evict in the conformance replay appears as exactly one event frame with the donor's `EVENT_FIRE` / `EVENT_SUPPRESS` keys, and engine stdout is empty

### t15 — Bench verb and arm64 verification record: engine bench prints tick p50 / p99, overrun count and steady-state RSS for a 200-rule / 20-field load over 10,000 ticks; one run on an arm64 box is committed under docs/verification with the commit sha

- depends on: t13
- covers: c6, h17, c27, h26
- acceptance:
  - bench exits non-zero when any tick overruns the configured budget or RSS exceeds the configured ceiling
  - docs/verification/<date>-arm64-bench.md records the box, commit, p50, p99, overruns and RSS

### t16 — Docs and hand-off: README adaptor guide (channels, senses, actions, sink, rules, the two transports, motor safety stays on the motor-control surface), CLAUDE.md moved from design brief to as-built, CHANGELOG, and one communicate issue per consumer repo announcing the engine — with a test asserting no commit in the branch touches a path outside this repo

- depends on: t12, t14, t15
- covers: c7, h18, c31, h27, c32, h28
- acceptance:
  - A maintainer can follow the README guide to integrate the toy robot without reading engine sources (reviewed by ask-colleague explore)
  - A repo test asserts the branch's diff against main contains no path outside the repository root
  - Issues are filed on reachy-mini-cli, microduck-cli and arm101-cli via communicate, linked in the CHANGELOG; the README states that motor collision and limit safety is the consumer's motor-control surface

## Risks

- [unknown_nonblocking] Event-keyed rules: whether priority/urgency feed arbitration or are routing metadata only is undecided (frame hard question q2); t6 implements them as routing metadata plus a one-tick sense field, which is reversible (task t6)
- [unknown_nonblocking] A Go toolchain and module cache are needed on the robot box for go build; a fresh box without network cannot fetch modules — vendor the module tree in-repo (go mod vendor) so the build is offline-capable (task t1)
- [unknown_nonblocking] Bench numbers on arm64 require a real Orin/Thor/Spark run, which CI cannot provide; t15's record is produced by an operator on a box and committed (task t15)
- [unknown_nonblocking] A TOML decoder is not in Go's stdlib; either vendor a minimal decoder or accept one pinned third-party module, recorded in go.mod with the argument next to the pin (dependency policy) (task t2)
