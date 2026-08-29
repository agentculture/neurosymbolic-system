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
skills-present check. Exits 1 when unhealthy.

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
}
