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

### State on disk today (be honest about this)

**No runtime code has been extracted yet.** What is checked in is the
`culture-agent-template` scaffold: an agent-first CLI (`whoami`, `learn`,
`explain`, `overview`, `doctor`, `cli overview`), the mesh identity, the vendored
skill kit, and the CI/publish baseline. Everything under
[The runtime being extracted](#the-runtime-being-extracted) describes the
**donor's** architecture and the target shape — it is a design brief, not a
description of this package. Keep it that way: as each piece lands here, move it
out of that section and document it as it exists on disk. If a section drifts
ahead of reality, mark it `(planned)`.

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

Lint stack (the CI `lint` job runs all of these; line length is 100 everywhere):

```bash
uv run black --check neurosymbolic_system tests
uv run isort --check-only neurosymbolic_system tests
uv run flake8 neurosymbolic_system tests
uv run bandit -c pyproject.toml -r neurosymbolic_system   # B101/B404/B603 skipped in pyproject
markdownlint-cli2 "**/*.md" "#node_modules" "#.local" "#.claude/skills" "#.teken"
```

CI is three jobs (`.github/workflows/tests.yml`): `test` (+ SonarCloud, skipped
when `SONAR_TOKEN` is absent, so fork PRs stay green), `lint`, and
`version-check` (PR-only — see the version convention below). Pushing to `main`
publishes to PyPI via Trusted Publishing (`publish.yml`); PRs do a TestPyPI
dry-run.

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

## The runtime being extracted

This is the donor architecture, module by module, with the constraint that
justifies each piece. Read it before you port anything; the file it names in
`reachy-mini-cli` is the authoritative source, and
[`reachy-mini-cli/CLAUDE.md`](../reachy-mini-cli/CLAUDE.md) carries the long-form
evidence (measurements, issue numbers, live probes) for every number quoted here.

### One tick, one owner per channel

`reachy/behavior/engine.py` holds a set of active behaviors and, each tick:
drops expired ones → arbitrates a single owner per channel → asks each owner for
its contribution *once* → composes a **complete** pose (unclaimed channels fall
to neutral, so the target is never partial) → streams it to a `TargetSink` held
open for the whole loop. `feel-alive` is seeded as a passive base layer so an
idle robot keeps breathing.

- **Channels** (`behavior/model.py`) are groups of DOF claimed and resolved
  atomically — for Reachy Mini `("head", "antennas", "body_yaw")`, mirroring the
  daemon's three independent target fields. `CHANNELS` is the single source of
  truth; arbitration and composition iterate it, never the literals. **This tuple
  is robot-specific** — the first thing the extraction has to parameterize.
- **Arbitration** (`behavior/arbitration.py`) is two pure functions, no I/O and
  no clock. `arbitrate` runs every tick and assigns each channel an owner by
  `(class priority, recency)`; `admit` runs on add and decides what a newcomer
  evicts. Four contention classes: `passive` (owns only what nothing else
  claims), `stoppable`, `unstoppable` (owns its channels while alive), `stopping`
  (evicts shared `stoppable`s on admit). Admission is *total* — contention a
  newcomer cannot win by removal is simply resolved per tick.
  Arbitration is **abstention-aware**: a claimant whose contribution leaves a
  channel `None` is skipped, so a sound-reactive behavior with no sound yields
  the head back to `feel-alive` instead of freezing it.
- **Units** are the friendly ones — millimetres, degrees, seconds — converted to
  the transport's metres/radians at the boundary, exactly once.
- **Behaviors are pure functions of behavior-local time** (`behavior/model.py`,
  `library.py`): same `t_local`, same offsets, `sense` ignored. Only an entry
  that declares `wants_sense=True` is minted per-instance and may hold state.
  Purity is what makes motion reproducible and the whole core unit-testable
  without hardware.

### The one seam: `tick_seam`

`engine.run(..., tick_seam=seam)` calls `seam(ctx)` exactly once per tick, after
the pose has streamed. Everything else — rules, agent intents, sense drivers, the
goto lane, export feeds, metrics — is a **pure consumer** of that seam.
`rule_engine.py`'s `TickBus` is the fault-isolating fan-out; `tick_metrics.py`
wraps the whole seam (not a driver on it) so an overrun measures the real
end-to-end tick cost.

This is the single most important structural rule the donor learned: features got
added for a year and `engine.py` never had to change. **Never let a consumer
import the engine's internals, and never let the engine import a consumer.**

`TickContext` exposes `now` / `tick` (injected clock — deterministic under
`max_ticks`), `sense` (this tick's snapshot, read ungated while a seam is
installed), `ownership`, `pose`, `emit(event)`, `admit(behavior)`,
`evict(name)`, `active_names()`.

### Senses are injected callables, never imports

`behavior/sense.py` defines the `Sense` snapshot shape and `SenseProviders` — a
duck-typed bundle of zero-arg **peek** callables (never consuming reads), so
multiple consumers reading the same tick's sample never steal from one another.
The module imports neither the transport nor the SDK; the composition root
supplies the concrete callables. Every provider degrades to `None` rather than
raising: a missing reader, a raising reader and a reader returning `None` all
resolve the same way.

Donor senses worth porting: pat/proprioception (`pat_sense.py` +
`robot/state_reader.py`), loudness (`rms_sense.py`), heard words
(`transcript_sense.py`), face and frame availability (`face_sense.py`), sound
direction (`DoaPoller`). Each carries a freshness field (`*_age_s`) sampled once
per tick, so a one-shot admitted mid-tick sees the same age a rule predicate
would.

### Rules are data, never code

`behavior/rules.py` + `rule_engine.py`: `[[react]]` (when a predicate over the
sense snapshot holds, run a named library entry), `[[inhibit]]` (disable a named
set), `[modes.<name>]` (declarative parameter sets). Loaded in **two layers** — a
shipped read-only package resource, plus a box-local overlay that overrides *per
rule id*, never wholesale. That is the only arrangement where an operator's
tuning survives an upgrade *and* newly shipped rules reach a deployed box. A
rule with `enabled = false` is a tombstone: one line in the overlay disables a
shipped rule without forking its body.

`reachy_nova` solves the same problem with
`config/nervous-system/rules.yaml` (priority/urgency verdicts on
`nova/events/<source>/<type>`). Two independent robots converging on
"declarative rules over a live event snapshot" is the signal that this layer
belongs in the library.

### Drop, don't block — the tick budget is the product

At 50 Hz the budget is 20 ms, and every donor incident of note was something
stealing it:

- Constructing an SDK client blocks **425–1213 ms** — a 21–61x overrun. Clients
  are warmed synchronously at composition, *before* the first tick, and re-warmed
  off-thread by a background keeper. The tick thread never constructs.
- Speech is `put_nowait` on a depth-2 bounded queue with a worker doing synthesis
  and playback. A full queue, a wedged TTS or a dead speaker is a **named drop**,
  never backpressure.
- Export legs (`audio_tee.py`, `clip_rider.py`) are O(1) on the tick thread — a
  timestamp and a bounded append; no socket I/O, no encoding, no filesystem.
  Measured: 0 overrun ticks with no consumer, an active one, or a *wedged* one.
- Audio is drained by a background pump at production pace, not pulled at tick
  rate (a pulled FIFO serves seconds-stale audio and blocks 20 ms on empty).

**Every drop names its reason.** `senselog.stage` / `senselog.drop` emit one
grep-able `[SENSE stage=<stage> source=<source> event=<event>] <detail>` line
(`reachy_nova`'s `sensory_log.py` is the same idea, and the ancestor). A layer
whose drops are invisible is indistinguishable from a layer that silently
no-ops — so this is required, not optional, and it is stderr-only by
construction so an export stdout stays pure JSONL.

### Validate fail-closed

An unknown field, an out-of-range axis, a non-numeric value or a runaway duration
is **refused, never clamped** (`goto_intent.py`; `MAX_DURATION_S = 10.0`, per-axis
bounds as module constants). Same for a rule's `say` (capped, refused, never
truncated). A robot that quietly reinterprets a bad command is worse than one
that says no.

### Two lessons that cost real time — do not re-learn them

- **An energy predicate is a LOCATOR, never a content filter.** It may say *when*
  to start listening; it may never decide *which audio is worth keeping*. The
  donor gated individual chunks on RMS, excising every stop closure inside a
  sentence — `'Richie, are you there?'` became `'Reaching there.'` then
  `'Return.'` as the room got louder. The threshold constant was cited from
  `reachy_nova` and quietly repurposed from locator to filter across the port.
  **When you port a constant, port what the donor used it for.**
- **Unit names must never be silently reinterpreted.** A per-tick epsilon
  respecified in deg/s got a new variable name (`REACHY_PAT_STILL_EPS` →
  `..._DEG_S`) and the old one is *ignored with a named log line*, not
  reinterpreted. Cadence-dependent tunings are a bug class of their own: the
  library must work at tick rates other than 50 Hz.
- Related: pat detection is **ownership-gated** (suspends while the engine owns
  the head, so commanded motion can't read as a phantom pat) and
  **stillness-gated** (a velocity tolerance, not exact constancy). The
  separation between a pat and the noise floor is 12–20x with the head still
  and 0.7–2.0x while it wanders — so a moving robot declines to guess. Shipped
  thresholds move **together or not at all**.

### What stays in the consumer, not here

The donor's hardest constraint is **hardware ownership**, and it is the consumer's
problem by definition:

- **One SDK client, one single-consumer media session, one head.** Two sense
  processes contend and the loser throttles to ~1 Hz. Hence: compose every sense
  onto ONE tick seam, never as two processes.
- **Cross-process arbitration is a heartbeat, not a flag file.** A flag cannot
  expire; the engine's `state.json` heartbeat does. A foreground verb beside a
  live engine should be a clean exit-1 refusal (`behavior/liveness.py`), not a
  silently useless process.

So the library defines the seams — `TargetSink`, `SenseProviders`,
`speech(text)`, `media_session_provider`, `start_pose_provider` — and the robot
CLI supplies every concrete implementation, holds every client, and owns the
process supervision, the command spool paths, and the state dir. **If a module
here imports a robot SDK, a transport, or a CLI error type, the extraction went
wrong.** (The donor's `behavior/` still imports `reachy.cli._errors` in 11 files
and `reachy.daemon` in 5 — those imports are exactly the seam work this repo
exists to do.)

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
neurosymbolic_system/   agent-first CLI (cited from teken's python-cli reference)
  cli/                  parser, error/output contract, _commands/ (verbs)
  explain/              markdown catalog for `explain`
tests/                  pytest smoke + introspection tests
.claude/skills/         vendored guildmaster skill kit (cite-don't-import)
docs/skill-sources.md   skill provenance ledger
culture.yaml            mesh identity (suffix + backend)
.github/workflows/      tests + deploy (PyPI Trusted Publishing)
```

The runtime packages (`engine/`, `senses/`, `rules/`, `motion/` or whatever the
first extraction PR names them) do not exist yet. When the first one lands, add
it here and move the corresponding piece out of
[The runtime being extracted](#the-runtime-being-extracted).
