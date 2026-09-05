# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

`neurosymbolic-system` is **the runtime that lets agents control robots** — the
library half of the Reachy Mini stack. Senses, rules, arbitration and motion
composed onto **one 50 Hz tick**, with no CLI and no hardware SDK of its own, so
that a robot CLI can import it instead of re-implementing it.

Its source material is the `reachy/behavior/` + `reachy/motion/` packages of
[`reachy-mini-cli`](../reachy-mini-cli) (~20k lines, versions 0.1 → 0.51), which
is where every design decision below was actually paid for on hardware. Its
second reference is [`reachy_nova`](../reachy_nova), the Amazon-Nova brain whose
own ~50 Hz loop is where several of these patterns (the per-stage sensory log,
the pat detector's thresholds, the mono-channel audio choice, the priority
cascade) were invented before they were ported.

Two consumers are named, and the second is the reason this repo exists:

| Consumer | Relationship |
|---|---|
| [`reachy-mini-cli`](../reachy-mini-cli) | the donor and first consumer — `behavior engine run` becomes a thin composition root over this library |
| [`microduck-cli`](../microduck-cli) | the second robot, different plant, same tick — its README and `pyproject.toml` already declare "built on the neurosymbolic-system runtime" |

**One physical robot generalizes to nothing; two is what forces the seam.** When
you are deciding whether a piece belongs here, ask whether MicroDuck would want
it — not whether Reachy Mini has it.

### State on disk today

The runtime has been extracted. It is a Go module
(`github.com/agentculture/neurosymbolic-system`, `go.mod`) built around one
binary, `cmd/neurosymbolic-engine`, plus the Python agent-first CLI that was
here from the start:

```text
cmd/neurosymbolic-engine/   the runtime's entry point — thin, see main.go's own doc comment
internal/
  adaptor/                 the robot vocabulary: channels, senses, actions, trajectories (JSON + TOML front ends)
  rules/                   the react/inhibit/modes/event schema and layered loader — data only, no evaluation
  ruleeval/                predicate evaluation + the one admission registry, over a live sense snapshot
  sense/                   the per-tick Snapshot/SenseSink shape
  tick/                    arbitration + composition — the engine core, pure, no I/O
  stream/                  the wire endpoint: unix socket / stdio, frame codec, backpressure
  events/                  event routing metadata (priority/urgency/voice) — never feeds arbitration
  provider/                the OpenAI-compatible decision-provider seam driver
  senselog/                the "[SENSE stage=... ]" stderr grammar
  mgmt/                    one-shot verbs (version, whoami, doctor, status, rules ...), shared by argv and the stream's mgmt frames
  clifmt/                  the CLI error/output contract (error:/hint:, --json)
  compose/                 the composition root: `run`'s flags, and the only package allowed to import all the others
  bench/                   the synthetic tick-load benchmark (`bench` verb)
  conformance/             fixtures replaying donor rule sets/sense traces, checked against the donors' own tests
  allowlist/               the dependency-graph guard docs/go-dependencies.md describes
neurosymbolic_system/
  engine_client.py          stdlib subprocess client for the built binary (locator, argv, error relay)
  cli/_commands/engine.py   `engine` noun — status/version/doctor delegates
  cli/_commands/rules.py    `rules` noun — check/list/migrate/reload delegates
tests/toy_robot/             a third, fictional plant (adaptor.toml + rules.toml + client.py) proving the engine holds no robot literal
```

Everything under
[The runtime as built](#the-runtime-as-built) below now describes what
exists on disk, replacing the earlier design brief; it links
[`docs/specs/2026-09-05-go-rule-engine-core.md`](docs/specs/2026-09-05-go-rule-engine-core.md)
rather than restating it. The Python CLI section below it (`## The CLI that
exists today`) is unchanged in shape — `engine`/`rules` are two more noun
groups registered the same way `whoami`/`learn`/`explain` always were.

## Identity

Declared in `culture.yaml`:

```yaml
agents:
- suffix: neurosymbolic-system
  backend: colleague
  model: sakamakismile/Qwen3.6-27B-Text-NVFP4-MTP
```

`backend: colleague` fixes the resident prompt file to **`AGENTS.colleague.md`**
(this file stays the Claude Code guidance file). Together they satisfy the two
invariants `steward doctor` checks — prompt-file-present and
backend-consistency — which `neurosymbolic-system doctor` re-implements locally.

Names, so a rename is never done piecemeal: distribution and console script are
`neurosymbolic-system`, the import package is `neurosymbolic_system`, the Sonar
project key is `agentculture_neurosymbolic-system`. `git grep -nF -e
'neurosymbolic-system' -e 'neurosymbolic_system'` finds every occurrence.

## Commands

```bash
uv sync                                              # create .venv, install dev deps (incl. teken)
uv run pytest -n auto                                # full suite (parallel)
uv run pytest tests/test_cli.py::test_whoami_text    # a single test
uv run pytest --cov=neurosymbolic_system --cov-report=term   # CI gate: fail_under=60
uv run teken cli doctor . --strict                   # the agent-first rubric gate CI enforces
uv run neurosymbolic-system whoami                   # or: python -m neurosymbolic_system
```

The `engine`/`rules` verbs, and the pytest suites that exercise them
(`tests/test_cli_engine.py`, `tests/test_e2e_toy_robot.py`,
`tests/test_engine_client.py`), need a **built** binary on `NEUROSYMBOLIC_ENGINE`
first — see the Go gate below:

```bash
CGO_ENABLED=0 go build -o /tmp/neurosymbolic-engine ./cmd/neurosymbolic-engine
export NEUROSYMBOLIC_ENGINE=/tmp/neurosymbolic-engine
uv run pytest -n auto
```

Lint stack (the CI `lint` job runs all of these; line length is 100 everywhere):

```bash
uv run black --check neurosymbolic_system tests
uv run isort --check-only neurosymbolic_system tests
uv run flake8 neurosymbolic_system tests
uv run bandit -c pyproject.toml -r neurosymbolic_system   # B101/B404/B603 skipped in pyproject
markdownlint-cli2 "**/*.md" "#node_modules" "#.local" "#.claude/skills" "#.teken"
```

CI is three Python jobs (`.github/workflows/tests.yml`): `test` (+ SonarCloud,
skipped when `SONAR_TOKEN` is absent, so fork PRs stay green), `lint`, and
`version-check` (PR-only — see the version convention below). Pushing to `main`
publishes to PyPI via Trusted Publishing (`publish.yml`); PRs do a TestPyPI
dry-run.

### The Go gate (`.github/workflows/go.yml`)

```bash
go vet ./...
go test ./...
go build -o dist/neurosymbolic-engine ./cmd/neurosymbolic-engine
file dist/neurosymbolic-engine | grep -q "statically linked"   # CGO_ENABLED=0 must hold on amd64 AND arm64
```

CI runs the build+test+static-link-assert leg on `linux/amd64` and
`linux/arm64` (`CGO_ENABLED=0`, the arm64 leg under QEMU), plus one exercise
of the `-ldflags` version-stamp contract end to end. `go test -race` is not
wired into `go.yml` yet, but is a live spec acceptance criterion
(`docs/specs/2026-09-05-go-rule-engine-core.md`'s concurrency-lens honesty
condition: "a race detector run over a test that hammers the stream, the
management endpoint and the provider worker concurrently for 10,000 ticks
reports no data race") and should be run by hand over the three packages with
concurrent access before touching any of them:

```bash
go test -race ./internal/stream/... ./internal/tick/... ./internal/compose/...
```

An arm64 vendor build (what a Jetson-class consumer box actually runs) is
worth a manual check before a release, since CI's arm64 leg builds with the
module cache, not `vendor/`:

```bash
CGO_ENABLED=0 GOARCH=arm64 go build -mod=vendor -o dist/neurosymbolic-engine ./cmd/neurosymbolic-engine
```

`neurosymbolic-engine bench [--json]` is the tick-budget regression check —
run it after any change to `internal/tick`, `internal/ruleeval` or
`internal/stream`; see
[`docs/verification/2026-09-05-arm64-bench.md`](docs/verification/2026-09-05-arm64-bench.md)
for a recorded baseline (p99 ~0.6 ms, 0 overruns at the 20 ms budget).

## The CLI that exists today

Everything routes through `neurosymbolic_system.cli.main()` → `_build_parser()`.
It is cited (cite-don't-import) from teken's `python-cli` reference, which is why
the **runtime package has zero third-party dependencies** — keep it that way (see
[Dependency policy](#dependency-policy)).

- **Adding a verb:** create `neurosymbolic_system/cli/_commands/<verb>.py`
  exposing `register(sub)` (add a `--json` flag, `set_defaults(func=...)`), then
  import and call it in `_build_parser()`. That is the only wiring step —
  `whoami.py` is the canonical example. Add the matching key to
  `explain/catalog.py`'s `ENTRIES` in the same change: the tests verify every
  catalog entry *resolves*, but nothing fails if a verb has no entry.
- **Noun groups** (a subcommand with sub-verbs, like `cli`): pass
  `parser_class=type(p)` to `add_subparsers` so nested parse errors keep the
  structured error contract, and give the noun an `overview` verb (rubric
  requirement).
- **Error contract** (`cli/_errors.py`, `cli/_output.py`): every failure raises
  `CliError(code, message, remediation)`; `_dispatch` catches it and wraps *any*
  other exception, so no Python traceback ever leaks. `main()` pre-scans argv for
  `--json` into `_CliArgumentParser._json_hint` so argparse parse-time errors
  (which fire before `args.json` exists) still render as JSON. Text errors are
  two lines — `error: …` then `hint: …` (the `hint:` prefix is rubric-required).
- **Output split:** results → stdout, errors and diagnostics → stderr, never
  mixed, in both text and JSON modes. Exit codes: `0` success, `1` user error,
  `2` environment error, `3+` reserved.

A library needs a CLI at all because the rubric gate and the mesh identity ride
on it, and because the runtime's own introspection (list the behavior library,
lint a rules file, replay a tick) is genuinely useful off-robot. It is **not**
where robot operation lives — that stays in the consumer CLIs.

## The runtime as built

This replaces the earlier design brief with what actually exists on disk. The
full acceptance-criterion-level record is
[`docs/specs/2026-09-05-go-rule-engine-core.md`](docs/specs/2026-09-05-go-rule-engine-core.md)
and its companion plan; this section is the constraints that still hold plus
what changed on the way from donor Python to this Go engine, not a restatement
of either document.

### One tick, one owner per channel

`internal/tick.Engine.Run` holds a set of active behaviors and, each tick:
drops expired ones → arbitrates a single owner per channel
(`internal/tick`'s arbitration, ported from `reachy/behavior/arbitration.py`)
→ asks each owner for its contribution once → composes a **complete** pose
(unclaimed channels fall to their declared neutral, so the target is never
partial) → streams it to the `adaptor.Sink` the composition root wired in
(`internal/stream.Server` in `run`; the toy-robot fixture's own sink in
tests). A `--base-action` is seeded as a passive base layer so an idle robot
keeps moving.

- **Channels, senses and actions are declared, not compiled in**
  (`internal/adaptor`) — see the [Adaptor guide](README.md#adaptor-guide) in
  the README for the full schema. This is the piece the design brief called
  "the first thing the extraction has to parameterize"; it is now the
  vocabulary every other package asks instead of holding a robot literal.
  `TestNoDonorLiteralsInEngineSources` enforces this mechanically — a name
  either donor robot uses, appearing as a whole string literal in non-test
  engine source, fails the build (issue #6 below is its one exemption list).
- **Arbitration** (`internal/tick`) is two pure functions, no I/O and no
  clock, same four contention classes as the donor (`passive`, `stoppable`,
  `unstoppable`, `stopping`) resolved by `(class priority, recency)`, and
  abstention-aware exactly as designed: a claimant whose contribution leaves a
  channel unset is skipped rather than freezing it.
- **Behaviors are pure functions of behavior-local time.** A `Trajectory`'s
  `At(tLocal)` (`internal/adaptor/trajectory.go`) is pure and total: same
  `t_local`, same value, every time — reproducible, testable off-robot. See
  "Data trajectories instead of a motion library" below for what this
  replaced.

### The one seam, and what rides it

`internal/compose` wires arbitration/composition to a fault-isolating fan-out
(`ruleeval.Bus`) that runs the rules evaluator, then the status rider, then
the stream's own heartbeat — every rider getting exactly one call per tick,
after the pose has streamed. This is still the single most important
structural rule: `internal/tick` imports none of the packages that ride it,
and the engine only ever calls the opaque seam it was handed. **Seam panic
isolation is new** relative to the donor's Python semantics — issue #9:

> A panicking tick-seam rider is recovered as a named drop, never fatal.
> `internal/tick.Engine.Run` recovers a panic raised inside the installed
> `TickSeam` (and inside each `OnEvent` consumer), emits one senselog line
> (`[SENSE stage=tick source=seam event=panic]`), counts it in
> `Stats.SeamPanics`, and continues with the next tick. In Go an unrecovered
> panic kills the process, and on a robot that means the pose stream stops
> mid-motion — the donor's Python exception unwinds one rider and the process
> survives, so this recover is the Go-specific safety net that restores the
> same guarantee.

### Senses are peek reads over a live snapshot

`internal/sense.Snapshot`/`SenseSink` is the injected-callable shape the
design brief called for: multiple consumers reading one tick's sample never
steal from one another, and a missing or erroring reader degrades to the
field simply not being present rather than raising. `internal/ruleeval`
reads it ungated, so a one-shot admitted mid-tick and the rule predicate that
admitted it agree on every value, including `*_age_s` freshness.

### Rules are data, never code

`internal/rules` + `internal/ruleeval`: `[[react]]`, `[[inhibit]]`,
`[modes.<name>]`, exactly as designed, plus a schema v1/v2 split (v2 adds one
level of `all`/`any` nesting) and an `[[event]]` dialect (see below). Layers
merge **per rule id** via repeated `--rules` flags; `enabled = false` is a
tombstone. Full schema and worked example: the README's
[rules file](README.md#the-rules-file) section.

**Events are routing metadata, never arbitration input — issue #7:**

> In the event-keyed rule dialect an entry's identity for lookup, layering and
> override is exactly `source/type`. Priority and urgency are routing
> metadata plus a one-tick sense field; they never feed arbitration. Lost in
> the port: `reachy_nova`'s payload-conditional rule overrides
> (`rule/fire:<rule-id>` selected by `payload["rule"]`) are not representable
> without making event identity payload-dependent, which the schema's per-id
> merge and tombstone semantics cannot express cleanly — and `reachy_nova` is
> a reference, not a consumer, so nothing shipped depends on it.

**Total admission, not MicroDuck's blocking refusal — issue #8:**

> The two donor engines differ on admission: reachy-mini-cli's is *total* (a
> newcomer is always admitted; contention it cannot win is resolved per tick),
> MicroDuck's *refuses* a newcomer sharing a channel with an `unstoppable` or
> `stopping` incumbent. `internal/tick.Admit` ports Reachy's total admission —
> the more general contract, since MicroDuck's refusal is derivable from the
> `Blocked` list `Admit` returns but the reverse is not. **A consumer wanting
> MicroDuck's blocking semantics reads `Blocked` and evicts the newcomer
> itself** — this is consumer-side work, not a gap in the engine.

### Drop, don't block — the tick budget is the product

Unchanged in spirit from the design brief, verified rather than assumed: see
[`docs/verification/2026-09-05-arm64-bench.md`](docs/verification/2026-09-05-arm64-bench.md)
for a recorded 10,000-tick run at p99 ~0.6 ms against the 20 ms budget, zero
overruns. Outbound stream telemetry is bounded (default depth 8) and dropped
newest-first rather than blocking the tick goroutine; every drop is a named
`senselog` line, stderr-only, so stdout carries protocol frames and nothing
else.

**The decision provider is the seam-driver shape the "constructing an SDK
client blocks 425-1213ms" lesson demanded — issue #12:**

> The embedding/small-model "fast path" is a seam driver (`internal/provider`)
> that each tick reads the sense snapshot, enqueues a request on a depth-2
> bounded channel, and returns; a worker goroutine performs the HTTP call
> under a timeout and writes the result into the sense snapshot as fields the
> adaptor config separately declares as senses. A rule fires on the tick
> *after* the result lands, never the same tick. Queue full, timeout, HTTP
> error, malformed body, or no provider configured are named senselog drops.
> This keeps the rules schema free of any provider concept, keeps the tick
> thread free of I/O, and makes "no provider on this box" a plain absent
> field a rule abstains on — the alternative, a `provider:` predicate op,
> would have put a network call inside predicate evaluation.

### Validate fail-closed

Unchanged: an unknown field, an out-of-domain param, a malformed trajectory or
a runaway `duration_s` is refused at load, never clamped
(`internal/adaptor.Vocabulary.ValidateParams`, `internal/rules`'s validators).
A `say` string over `MaxSayChars` (500) is refused, never truncated.

### Two lessons that still apply — not yet exercised by this engine

Audio-domain senses (loudness gating, pat/proprioception detection) have not
been ported here; when they are, these two donor lessons apply unchanged:

- **An energy predicate is a LOCATOR, never a content filter.** It may say
  *when* to start listening; it may never decide *which* audio is worth
  keeping — the donor gated individual chunks on RMS and excised speech mid-
  sentence when a threshold was quietly repurposed from locator to filter.
  When you port a constant, port what the donor used it for.
- **Unit names must never be silently reinterpreted.** A per-tick epsilon
  respecified in deg/s must get a new variable name, with the old one ignored
  via a named log line, never reinterpreted. Cadence-dependent tunings are a
  bug class of their own: a tuning that only works at one tick rate is a bug,
  since `--period` is a real flag here, not an assumption.

### Data trajectories instead of a motion library

The design brief's "behaviors are pure functions of behavior-local time,"
`library.py`-style, is now `internal/adaptor.Trajectory`: keyframes or a
closed-form easing (`linear`, `ease_in_out`, `hold`), declared per claimed
channel in the adaptor config — data, not Go code. **Nothing here ports the
donors' actual motion**: `feel-alive`'s breathing, `pet-reaction`'s settle,
`orient-to-sound`'s graded ladder, and MicroDuck's `_contribute_*` functions
all stay in the donors, which is why the conformance suite
([`docs/verification/2026-09-05-donor-conformance.md`](docs/verification/2026-09-05-donor-conformance.md))
deliberately does not compare pose *values* — only events, ownership and pose
completeness.

### The guard exemptions — issues #6 and #13

Two narrow, explicit exemption lists keep the no-donor-literal guard from
either producing false positives or forcing an ugly workaround, and both are
closed lists a reviewer can read in one sitting:

> **Issue #6.** The guard originally matched donor names as *substrings*,
> which caught TOML schema keywords (`enabled`, `active_mode`) and English
> prose coincidentally matching MicroDuck's `mode`/`move` or Reachy's `speak`.
> Fixed: whole-literal equality only, minus an explicit exemption list
> (`internal/adaptor/testdata/schema_keywords.txt`: `enabled`, `mode`,
> `active_mode`, each with a one-line justification). No channel or action
> name may ever be exempted.
>
> **Issue #13.** The wire token `"pose"` collides with MicroDuck's `pose`
> channel name. Rather than derive the constant via reflection (correct but
> unreadable), a second, deliberately narrow exemption list
> (`internal/adaptor/testdata/protocol_tokens.txt`) carries wire frame-kind
> tokens *only*, so `internal/stream.KindPose` can be a plain, grep-able
> constant. Channel and action names stay non-exemptable on this list too.

### What stays in the consumer, not here

Unchanged, and now enforced by what the engine literally cannot do: it has no
SDK client, no media session, and no process supervision. `internal/compose`
is the composition root and the *only* package allowed to import every other
one; nothing here imports a robot SDK, a transport beyond the endpoint, or a
CLI error type belonging to a consumer. The seams a consumer implements are:
`adaptor.Sink` (where a pose goes), the stream's socket or stdio endpoint (how
senses come in and poses go out), and — per the
[safety boundary](README.md#safety-boundary) — the actual motor-control
surface, since this engine performs no motor-level collision or limit check
of its own.

### Dependency policy

Runtime dependencies stay **empty or near-empty**; `teken` and the test/lint
stack are dev-only. The donor learned this the expensive way: `reachy-mini`
(pycairo / gstreamer / pyaudio) is an *extra*, not a base dep, because a hard
dependency breaks `uv sync` on a bare box and in CI. A library imported by two
robot CLIs must install everywhere, so anything heavier than stdlib needs an
argument recorded in `pyproject.toml` next to the pin — the donor's comment block
above its three base deps is the model.

## Conventions

- **Every PR bumps the version** — even docs/config/CI. Use the `version-bump`
  skill; the `version-check` CI job blocks merge otherwise.
- **PRs go through the `cicd` skill** (`devex pr` + SonarCloud gating). Standing
  default when a branch is done: push and open a PR — do not stop to offer a
  merge/keep/discard menu. Sign online posts as `- neurosymbolic-system
  (Claude)`; the scripts resolve the nick from `culture.yaml`.
- **Reach for `ask-colleague` reflexively** — a second, *independent* mind (a
  different backend/model), not a stronger one. `review` before presenting a
  non-trivial diff, `explore` for a fresh read of an unfamiliar area; both are
  read-only in a throwaway worktree, so the reflex is always safe.
  `write --apply` / `write --pr` still needs the user's go-ahead. Its output is
  a second opinion to verify and own, never authority.
- **Vendored `.claude/skills/` are cited verbatim** — do not reformat their
  scripts; re-sync from guildmaster and record any deliberate local divergence in
  `docs/skill-sources.md`. (The `cicd` and `communicate` skills carry
  consumer-identifying adaptations by design; both are already logged there.)
- **Memory — recall before, remember after.** `/recall` the area you are about
  to touch before non-trivial work; `/remember` a non-obvious decision, a
  constraint, or a gotcha as it surfaces. A plain `/remember` lands in
  `<repo-root>/.eidetic/memory` — committed and mesh-shared, so the `claude` and
  `colleague` backends read the same store; `--visibility private` keeps a record
  in `$HOME`. Don't store what the repo already records.
- **Worktrees you create live in `../.worktrees.neurosymbolic-system/<name>/`** —
  one repo-named directory beside the checkout, never a shared `../worktrees/`
  (this workspace holds many sibling projects; a shared folder accumulates
  orphans nobody can attribute). Scope the branch prefix to the work
  (`extract/t2`, not `agent/t2`) — plain `agent/*` collides with leftovers and
  `git worktree add -b` fails on an existing branch. Remove with `git worktree
  remove <path>`, never `rm -rf`. The vendored `assign-to-workforce` skill's
  example uses both things this rule forbids; override it on both counts.
  `ask-colleague`'s own `$TMPDIR` worktrees are exempt — tool-managed, deleted on
  an EXIT trap.

## Working across the sibling repos — read them, never write them

The sibling paths are `../reachy-mini-cli`, `../microduck-cli`, `../arm101-cli`,
`../reachy_nova` and `../culture-nodes` (per-machine overrides live in
`.claude/skills.local.yaml`; see the committed `.example`).

**This repo is the only repo this agent changes.** Every sibling has its own
agent, and code in a sibling is that agent's to write. We open the siblings to
*learn* — a donor module's docstring carries the constraint that shaped it,
`reachy-mini-cli/CLAUDE.md` plus its `docs/verification/` + `docs/evidence/`
carry the hardware measurements, `culture-nodes` carries the Python-over-Go
pattern — and we ship the engine here. Consumers pull the new engine and test it
on their side, in their own PRs, on their own schedule (frame decision c31 in
`.devague/`). Concretely:

- **Never open a PR, commit, or edit a file in a sibling checkout.** Not even
  a one-line doc fix. If a sibling needs something from us (a fixture, a
  conformance trace, a schema note), it ships *here* and the sibling is told.
- **Tell, don't do.** When the next step lives in a sibling — "microduck-cli can
  now load its overlay through the engine", "reachy-mini-cli's `engine.py` is
  covered by o6's conformance fixtures" — file the issue there with the
  `communicate` skill (`post-issue.sh`; it auto-signs) and stop. The sibling's
  agent decides when and how.
- **Port the reason, not just the code.** A number quoted from a donor comes
  with what the donor used it for (see the locator-vs-filter lesson above). A
  seam shape is checked against *both* robots' engines — `reachy/behavior/` and
  `microduck_cli/behavior/` — before it is declared general; a seam serving only
  one robot is not extracted yet.
- **`reachy_nova` and `culture-nodes` are references, not consumers.**
  `reachy_nova` is a `ReachyMiniApp` plugin with its own ~50 Hz loop; cite its
  sensory log, nervous-system rules and priority cascade, never plan to make it
  import this package. `culture-nodes` is the Python-CLI-over-Go-daemon
  precedent (`culture_nodes/api_client.py`, `internal/clifmt`); cite the shape,
  don't share code.

## Layout

```text
cmd/neurosymbolic-engine/  the Go engine's entry point (main.go — thin, see its own doc comment)
internal/
  adaptor/                  robot vocabulary: channels, senses, actions, trajectories (JSON + TOML)
  rules/                    react/inhibit/modes/event schema + layered loader — data only
  ruleeval/                 predicate evaluation + the one admission registry
  sense/                    per-tick Snapshot/SenseSink shape
  tick/                     arbitration + composition — the engine core, pure
  stream/                   wire endpoint: unix socket / stdio, frame codec, backpressure
  events/                   event routing metadata — never feeds arbitration
  provider/                 OpenAI-compatible decision-provider seam driver
  senselog/                 the "[SENSE stage=...]" stderr grammar
  mgmt/                     one-shot verbs, shared by argv and the stream's mgmt frames
  clifmt/                   CLI error/output contract
  compose/                  composition root — the only package that may import all the others
  bench/                    synthetic tick-load benchmark
  conformance/              donor-derived replay fixtures
  allowlist/                the dependency-graph guard (see docs/go-dependencies.md)
neurosymbolic_system/       agent-first CLI (cited from teken's python-cli reference)
  cli/                      parser, error/output contract, _commands/ (verbs incl. engine, rules)
  engine_client.py          stdlib subprocess client for the built binary
  explain/                  markdown catalog for `explain`
tests/                      pytest suites, incl. tests/toy_robot/ (third-plant fixture + e2e test)
.claude/skills/              vendored guildmaster skill kit (cite-don't-import)
docs/verification/           bench + conformance evidence records
docs/handoff/                draft hand-off issue bodies for the three consumer repos
docs/skill-sources.md        skill provenance ledger
culture.yaml                 mesh identity (suffix + backend)
go.mod / go.sum / vendor/    the Go module and its one vendored dependency
.github/workflows/           Python tests+deploy, and the Go build/test/static-link matrix
```
