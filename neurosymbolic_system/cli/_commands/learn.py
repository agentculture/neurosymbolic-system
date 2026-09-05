"""``neurosymbolic-system learn`` — the learnability affordance.

Prints a structured self-teaching prompt. Must satisfy the agent-first rubric:
>=200 chars and mention purpose, command map, exit codes, --json, and explain.
"""

from __future__ import annotations

import argparse

from neurosymbolic_system import __version__
from neurosymbolic_system.cli._output import emit_result

_TEXT = """\
neurosymbolic-system — the runtime that lets agents control robots.

Purpose
-------
The library half of the Reachy Mini stack: senses, rules, arbitration and motion
composed onto one 50 Hz tick, imported by robot CLIs such as reachy-mini-cli and
microduck-cli rather than re-implemented in each. Extracted from reachy-mini-cli;
the runtime modules have not landed yet, so today this CLI carries the agent
baseline only — identity (culture.yaml + AGENTS.colleague.md), the agent-first
verbs below (cited from the teken `python-cli` reference), the guildmaster skill
kit under .claude/skills/, and a deploy/CI baseline. See CLAUDE.md for the
architecture the extraction is cutting a seam around.

Commands
--------
  neurosymbolic-system whoami             Identity from culture.yaml.
  neurosymbolic-system learn              This self-teaching prompt.
  neurosymbolic-system explain <path>...  Markdown docs for any noun/verb path.
  neurosymbolic-system overview           Descriptive snapshot of the agent.
  neurosymbolic-system doctor             Check the agent-identity invariants.
  neurosymbolic-system cli overview       Describe the CLI surface itself.
  neurosymbolic-system engine overview    Describe the engine noun group.
  neurosymbolic-system rules overview     Describe the rules noun group.

Machine-readable output
-----------------------
Every command supports --json. Errors in JSON mode emit
{"code", "message", "remediation"} to stderr. Stdout and stderr never mix.

Exit-code policy
----------------
  0 success
  1 user-input error (bad flag, bad path, missing arg)
  2 environment / setup error
  3+ reserved

More detail
-----------
  neurosymbolic-system explain neurosymbolic-system
"""


def _as_json_payload() -> dict[str, object]:
    return {
        "tool": "neurosymbolic-system",
        "version": __version__,
        "purpose": "Clonable scaffold for a new AgentCulture mesh agent.",
        "commands": [
            {"path": ["whoami"], "summary": "Identity probe from culture.yaml."},
            {"path": ["learn"], "summary": "Self-teaching prompt."},
            {"path": ["explain"], "summary": "Markdown docs by path."},
            {"path": ["overview"], "summary": "Descriptive snapshot of the agent."},
            {"path": ["doctor"], "summary": "Check the agent-identity invariants."},
            {"path": ["cli", "overview"], "summary": "Describe the CLI surface."},
            {"path": ["engine", "overview"], "summary": "Describe the engine noun group."},
            {"path": ["rules", "overview"], "summary": "Describe the rules noun group."},
        ],
        "exit_codes": {
            "0": "success",
            "1": "user-input error",
            "2": "environment/setup error",
        },
        "json_support": True,
        "explain_pointer": "neurosymbolic-system explain <path>",
    }


def cmd_learn(args: argparse.Namespace) -> int:
    if getattr(args, "json", False):
        emit_result(_as_json_payload(), json_mode=True)
    else:
        emit_result(_TEXT, json_mode=False)
    return 0


def register(sub: argparse._SubParsersAction) -> None:
    p = sub.add_parser(
        "learn",
        help="Print a structured self-teaching prompt for agent consumers.",
    )
    p.add_argument("--json", action="store_true", help="Emit structured JSON.")
    p.set_defaults(func=cmd_learn)
