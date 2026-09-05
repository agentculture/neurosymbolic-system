"""Markdown catalog for ``neurosymbolic-system explain <path>``.

Each entry is verbatim markdown. Keys are command-path tuples. The empty tuple
and ``("neurosymbolic-system",)`` both resolve to the root entry.

Keep bodies self-contained: an agent reading one entry should get enough
context without chaining reads.
"""

from __future__ import annotations

_ROOT = """\
# neurosymbolic-system

The runtime that lets agents control robots — senses, rules, arbitration and
motion composed onto one 50 Hz tick, imported as a library by robot CLIs such as
`reachy-mini-cli` and `microduck-cli`. Extracted from `reachy-mini-cli`; the
runtime modules have not landed yet, so what ships today is the agent baseline:
an agent-first CLI (cited from the teken `python-cli` reference), a mesh identity
(`culture.yaml` + `AGENTS.colleague.md`), the guildmaster skill kit under
`.claude/skills/`, and a buildable/deployable package baseline. `CLAUDE.md`
carries the architecture brief.

## Verbs

- `neurosymbolic-system whoami` — identity probe from `culture.yaml`.
- `neurosymbolic-system learn` — structured self-teaching prompt.
- `neurosymbolic-system explain <path>` — markdown docs for any noun/verb.
- `neurosymbolic-system overview` — descriptive snapshot of the agent.
- `neurosymbolic-system doctor` — check the agent-identity invariants.
- `neurosymbolic-system cli overview` — describe the CLI surface.
- `neurosymbolic-system engine overview` — describe the engine noun group.
- `neurosymbolic-system rules overview` — describe the rules noun group.

## Exit-code policy

- `0` success
- `1` user-input error
- `2` environment / setup error
- `3+` reserved

## See also

- `neurosymbolic-system explain whoami`
- `neurosymbolic-system explain doctor`
"""

_WHOAMI = """\
# neurosymbolic-system whoami

Reports the agent's identity from `culture.yaml`: nick (`suffix`), backend,
served model, and the package version. Read-only.

## Usage

    neurosymbolic-system whoami
    neurosymbolic-system whoami --json
"""

_LEARN = """\
# neurosymbolic-system learn

Prints a structured self-teaching prompt covering purpose, command map,
exit-code policy, `--json` support, and the `explain` pointer.

## Usage

    neurosymbolic-system learn
    neurosymbolic-system learn --json
"""

_EXPLAIN = """\
# neurosymbolic-system explain <path>

Prints markdown documentation for any noun/verb path. Unlike `--help` (terse,
positional), `explain` is global and addressable by path.

## Usage

    neurosymbolic-system explain neurosymbolic-system
    neurosymbolic-system explain whoami
    neurosymbolic-system explain --json <path>
"""

_OVERVIEW = """\
# neurosymbolic-system overview

Read-only descriptive snapshot of the agent: identity (from `culture.yaml`), the
verb surface, and the sibling-pattern artifacts the template carries. Accepts an
ignored `target` so a stray path never hard-fails.

## Usage

    neurosymbolic-system overview
    neurosymbolic-system overview --json
"""

_DOCTOR = """\
# neurosymbolic-system doctor

Checks the agent-identity invariants `steward doctor` verifies:
prompt-file-present and backend-consistency (`colleague` → `AGENTS.colleague.md`), plus a
skills-present check, and two engine checks (`engine_present`,
`engine_protocol` — spec h35). Only `severity: error` checks affect the
overall verdict, so a box with no `neurosymbolic-engine` built yet still
reports healthy. Exits 1 when unhealthy.

## Usage

    neurosymbolic-system doctor
    neurosymbolic-system doctor --json
"""

_CLI = """\
# neurosymbolic-system cli

Noun group for CLI-surface introspection. `cli overview` describes the CLI
itself (distinct from the global `overview`, which describes the agent).

## Usage

    neurosymbolic-system cli overview
    neurosymbolic-system cli overview --json
"""

_ENGINE = """\
# neurosymbolic-system engine

Thin delegate over the `neurosymbolic-engine` Go binary (see
`neurosymbolic_system.engine_client`): `status`, `version` and `doctor` each
shell out to the matching engine verb and relay its `{code, message,
remediation}` verbatim on failure. A missing binary is an environment error
(exit 2) naming the `go build` remediation. `overview` is descriptive and
never touches the binary.

## Locator

- `NEUROSYMBOLIC_ENGINE` env var, if set and executable
- otherwise: `neurosymbolic-engine` on `PATH`

## Usage

    neurosymbolic-system engine overview
    neurosymbolic-system engine status
    neurosymbolic-system engine version --json
    neurosymbolic-system engine doctor
"""

_RULES = """\
# neurosymbolic-system rules

Thin delegate over the `neurosymbolic-engine rules` verbs: `check`, `list`,
`migrate` and `reload` each shell out to the matching engine verb and relay
its result (or its `{code, message, remediation}` failure) verbatim. A
missing binary is an environment error (exit 2) naming the `go build`
remediation. `overview` is descriptive and never touches the binary.

## Usage

    neurosymbolic-system rules overview
    neurosymbolic-system rules check rules.toml [--adaptor adaptor.toml]
    neurosymbolic-system rules list rules.toml
    neurosymbolic-system rules migrate rules.toml [--out rules.v2.toml] [--force]
    neurosymbolic-system rules reload rules.toml
"""

_ENGINE_STATUS = """\
# neurosymbolic-system engine status

Delegates to `neurosymbolic-engine status`: reports the live engine's
current state. Needs an engine actually running — with none reachable, the
binary's own error body is relayed verbatim (see `engine_client.EngineClient.run`).

## Usage

    neurosymbolic-system engine status
    neurosymbolic-system engine status --json

## Flags

- `--json` — emit structured JSON instead of text.

## Exit codes

- `0` — the engine answered with its status.
- `2` — `neurosymbolic-engine` is not locatable (`NEUROSYMBOLIC_ENGINE`/`PATH`),
  or the running binary itself raised an environment-class error (e.g. "no
  live engine") — relayed verbatim either way.
- `1` (or another code the binary reports) — the engine's own non-environment
  failure, relayed verbatim.
"""

_ENGINE_VERSION = """\
# neurosymbolic-system engine version

Delegates to `neurosymbolic-engine version`: prints the engine binary's
version, revision, and (spec h35) its `protocol` field, used by
`neurosymbolic-system doctor`'s `engine_protocol` check to detect a
client/engine skew. No live engine process is required — this is a one-shot
exec of the binary itself.

## Usage

    neurosymbolic-system engine version
    neurosymbolic-system engine version --json

## Flags

- `--json` — emit structured JSON instead of text.

## Exit codes

- `0` — the binary reported its version.
- `2` — `neurosymbolic-engine` is not locatable, relayed as an environment
  error naming the `go build` remediation.
"""

_ENGINE_DOCTOR = """\
# neurosymbolic-system engine doctor

Delegates to `neurosymbolic-engine doctor`: the engine binary's own
environment checks. Distinct from `neurosymbolic-system doctor`, which runs
the Python-side agent-identity checks plus `engine_present`/`engine_protocol`
checks *against* this same binary rather than asking it to check itself.

## Usage

    neurosymbolic-system engine doctor
    neurosymbolic-system engine doctor --json

## Flags

- `--json` — emit structured JSON instead of text.

## Exit codes

- `0` — the engine binary reports itself healthy.
- `2` — `neurosymbolic-engine` is not locatable, relayed as an environment
  error naming the `go build` remediation.
- `1` (or another code the binary reports) — the engine's own unhealthy
  verdict, relayed verbatim.
"""

_RULES_CHECK = """\
# neurosymbolic-system rules check

Delegates to `neurosymbolic-engine rules check`: validates one or more rules
files, loaded together as a single layer, with no robot attached.

## Usage

    neurosymbolic-system rules check rules.toml [more.toml ...] [--adaptor adaptor.json]
    neurosymbolic-system rules check rules.toml --json

## Flags

- `files` (positional, one or more, required) — rules file(s) to load as one layer.
- `--adaptor PATH` — a `.json`/`.toml` adaptor config attaching a vocabulary.
- `--json` — emit structured JSON instead of text.

## Exit codes

- `0` — the rules files validated.
- `2` — `neurosymbolic-engine` is not locatable, relayed as an environment error.
- `1` (or another code the binary reports) — the engine's own validation
  failure (e.g. a `schema_version` mismatch), relayed verbatim.
"""

_RULES_LIST = """\
# neurosymbolic-system rules list

Delegates to `neurosymbolic-engine rules list`: lists every rule's id, kind
and predicate for the given file(s), loaded together as one layer.

## Usage

    neurosymbolic-system rules list rules.toml [more.toml ...] [--adaptor adaptor.json]
    neurosymbolic-system rules list rules.toml --json

## Flags

- `files` (positional, one or more, required) — rules file(s) to load as one layer.
- `--adaptor PATH` — a `.json`/`.toml` adaptor config attaching a vocabulary.
- `--json` — emit structured JSON instead of text.

## Exit codes

- `0` — the file(s) loaded and their rules are listed.
- `2` — `neurosymbolic-engine` is not locatable, relayed as an environment error.
- `1` (or another code the binary reports) — the engine's own load/parse
  failure, relayed verbatim.
"""

_RULES_MIGRATE = """\
# neurosymbolic-system rules migrate

Delegates to `neurosymbolic-engine rules migrate`: writes a
`schema_version`-2 twin of a `schema_version`-1 rules file, leaving the
original untouched.

## Usage

    neurosymbolic-system rules migrate rules.toml [--out rules.v2.toml] [--force]
    neurosymbolic-system rules migrate rules.toml --json

## Flags

- `file` (positional, required) — the input rules file (`schema_version` 1).
- `--out PATH` — output path (default: `<name>.v2.<ext>`).
- `--force` — overwrite an existing `--out` path.
- `--json` — emit structured JSON instead of text.

## Exit codes

- `0` — the migrated twin was written.
- `2` — `neurosymbolic-engine` is not locatable, relayed as an environment error.
- `1` (or another code the binary reports) — the engine's own refusal (e.g.
  an existing `--out` without `--force`), relayed verbatim.
"""

_RULES_RELOAD = """\
# neurosymbolic-system rules reload

Delegates to `neurosymbolic-engine rules reload`: asks a *live* engine
process to re-read the given rules file(s) — distinct from `rules check`,
which only validates without a running engine.

## Usage

    neurosymbolic-system rules reload rules.toml [more.toml ...]
    neurosymbolic-system rules reload rules.toml --json

## Flags

- `files` (positional, one or more, required) — rules file(s) the live engine should re-read.
- `--json` — emit structured JSON instead of text.

## Exit codes

- `0` — the live engine reloaded its rules.
- `2` — `neurosymbolic-engine` is not locatable, or the engine itself raised an
  environment-class error (e.g. no live engine), relayed verbatim either way.
- `1` (or another code the binary reports) — the engine's own reload failure
  (e.g. invalid rules), relayed verbatim.
"""


ENTRIES: dict[tuple[str, ...], str] = {
    (): _ROOT,
    ("neurosymbolic-system",): _ROOT,
    ("whoami",): _WHOAMI,
    ("learn",): _LEARN,
    ("explain",): _EXPLAIN,
    ("overview",): _OVERVIEW,
    ("doctor",): _DOCTOR,
    ("cli",): _CLI,
    ("cli", "overview"): _CLI,
    ("engine",): _ENGINE,
    ("engine", "overview"): _ENGINE,
    ("engine", "status"): _ENGINE_STATUS,
    ("engine", "version"): _ENGINE_VERSION,
    ("engine", "doctor"): _ENGINE_DOCTOR,
    ("rules",): _RULES,
    ("rules", "overview"): _RULES,
    ("rules", "check"): _RULES_CHECK,
    ("rules", "list"): _RULES_LIST,
    ("rules", "migrate"): _RULES_MIGRATE,
    ("rules", "reload"): _RULES_RELOAD,
}
