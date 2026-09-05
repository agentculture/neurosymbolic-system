"""``neurosymbolic-system doctor`` — check the agent-identity invariants.

Mirrors the two invariants ``steward doctor`` verifies for a mesh agent:

* **prompt-file-present** — the repo declares an agent in ``culture.yaml`` and
  has the matching prompt file on disk;
* **backend-consistency** — the declared ``backend`` matches the prompt file
  (``claude`` → ``CLAUDE.md``, ``colleague`` → ``AGENTS.colleague.md``,
  ``acp`` → ``AGENTS.md``, ``gemini`` → ``GEMINI.md``).

Plus a **skills-present** check (the vendored ``.claude/skills/`` kit), and two
engine checks (t12): **engine_present** (is ``neurosymbolic-engine`` locatable
at all) and **engine_protocol** (spec h35 — does its reported protocol version
match this client's :data:`~neurosymbolic_system.engine_client.EXPECTED_PROTOCOL`).
Read-only.

Only ``severity: error`` checks affect the overall ``healthy`` verdict — the
engine checks (and the pre-existing ``skills_present`` check) are
``severity: warning`` deliberately, so a box with no engine built yet still
reports the agent-identity invariants as healthy; ``engine``/``rules`` verbs
still refuse with an environment error (exit 2) when the engine itself is
missing (see :mod:`neurosymbolic_system.engine_client`).

Reports the rubric-shaped contract
``{healthy, checks: [{id, passed, severity, message, remediation}]}`` so the
agent-first rubric's bundle 7 passes. When run from a wheel install (no
``culture.yaml`` alongside the package), it reports a single info check and
exits 0 — there is nothing to diagnose.
"""

from __future__ import annotations

import argparse

from neurosymbolic_system.cli._commands.whoami import find_culture_yaml, read_agent_fields
from neurosymbolic_system.cli._output import emit_result
from neurosymbolic_system.engine_client import check_protocol, find_engine

# backend → required prompt file (the backend-consistency mapping).
_PROMPT_FILE = {
    "claude": "CLAUDE.md",
    "colleague": "AGENTS.colleague.md",
    "acp": "AGENTS.md",
    "gemini": "GEMINI.md",
}

_ENGINE_BUILD_REMEDIATION = (
    "build it: CGO_ENABLED=0 go build -o ~/.local/bin/neurosymbolic-engine "
    "./cmd/neurosymbolic-engine, or set NEUROSYMBOLIC_ENGINE"
)


def _diagnose() -> dict[str, object]:
    cfg = find_culture_yaml()
    if cfg is None:
        check = {
            "id": "source_checkout",
            "passed": True,
            "severity": "info",
            "message": "no culture.yaml found alongside the package; identity checks skipped",
            "remediation": "",
        }
        return {"healthy": True, "checks": [check]}

    root = cfg.parent
    fields = read_agent_fields()
    backend = fields["backend"]
    checks: list[dict[str, object]] = []

    # 1. backend-consistency: the prompt file for the declared backend exists.
    expected = _PROMPT_FILE.get(backend)
    if expected is None:
        checks.append(
            {
                "id": "backend_consistency",
                "passed": False,
                "severity": "error",
                "message": f"unknown backend '{backend}' in culture.yaml",
                "remediation": f"set backend to one of: {', '.join(sorted(_PROMPT_FILE))}",
            }
        )
    else:
        present = (root / expected).is_file()
        checks.append(
            {
                "id": "prompt_file_present",
                "passed": present,
                "severity": "error",
                "message": (
                    f"backend '{backend}' requires {expected} — "
                    + ("present" if present else "missing")
                ),
                "remediation": "" if present else f"create {expected} at the repo root",
            }
        )

    # 2. skills-present: the vendored skill kit is on disk.
    skills_dir = root / ".claude" / "skills"
    has_skills = skills_dir.is_dir() and any(skills_dir.iterdir())
    checks.append(
        {
            "id": "skills_present",
            "passed": has_skills,
            "severity": "warning",
            "message": (
                ".claude/skills/ vendored" if has_skills else ".claude/skills/ missing or empty"
            ),
            "remediation": (
                "" if has_skills else "vendor the skill kit (see docs/skill-sources.md)"
            ),
        }
    )

    # 3. engine-present: is `neurosymbolic-engine` locatable at all (t12).
    engine_path = find_engine()
    checks.append(
        {
            "id": "engine_present",
            "passed": engine_path is not None,
            "severity": "warning",
            "message": (
                f"neurosymbolic-engine found at {engine_path}"
                if engine_path is not None
                else "neurosymbolic-engine not found on PATH or NEUROSYMBOLIC_ENGINE"
            ),
            "remediation": "" if engine_path is not None else _ENGINE_BUILD_REMEDIATION,
        }
    )

    # 4. engine-protocol: spec h35 — the engine's reported protocol version,
    # when it is present at all. Never fails on an absent `protocol` field
    # (the Go side has not shipped it yet); a genuine mismatch is surfaced by
    # name but still warning-severity, consistent with engine_present above.
    if engine_path is not None:
        checks.append(check_protocol(engine_path))

    # Only error-severity checks gate the overall verdict: the engine checks
    # (and skills_present) are advisory, so a box with no engine yet still
    # reports the agent-identity invariants as healthy.
    healthy = all(c["passed"] for c in checks if c["severity"] == "error")
    return {"healthy": healthy, "checks": checks}


def cmd_doctor(args: argparse.Namespace) -> int:
    report = _diagnose()
    json_mode = bool(getattr(args, "json", False))
    if json_mode:
        emit_result(report, json_mode=True)
    else:
        status = "healthy" if report["healthy"] else "unhealthy"
        lines = [f"neurosymbolic-system doctor: {status}", ""]
        for check in report["checks"]:
            mark = "ok" if check["passed"] else "FAIL"
            lines.append(f"[{mark}] {check['id']}: {check['message']}")
            if not check["passed"] and check["remediation"]:
                lines.append(f"  hint: {check['remediation']}")
        emit_result("\n".join(lines), json_mode=False)
    return 0 if report["healthy"] else 1


def register(sub: argparse._SubParsersAction) -> None:
    p = sub.add_parser(
        "doctor",
        help="Check the agent-identity invariants (prompt-file-present, backend-consistency).",
    )
    p.add_argument("--json", action="store_true", help="Emit structured JSON.")
    p.set_defaults(func=cmd_doctor)
