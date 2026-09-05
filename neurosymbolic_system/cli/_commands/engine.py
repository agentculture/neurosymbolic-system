"""``neurosymbolic-system engine`` — thin delegate over the Go engine binary.

Every verb here is one subprocess call to ``neurosymbolic-engine`` (see
:mod:`neurosymbolic_system.engine_client`): no engine logic lives in this
module, only argv-shaping and result rendering. A missing binary surfaces as
:func:`~neurosymbolic_system.engine_client.missing_engine_error` — an
environment error (exit 2) with the ``go build`` remediation — from every
delegating verb; ``overview`` is descriptive and never touches the binary, so
it always succeeds.
"""

from __future__ import annotations

import argparse
from typing import Callable

from neurosymbolic_system.cli._commands.overview import emit_overview
from neurosymbolic_system.cli._output import emit_result
from neurosymbolic_system.engine_client import EngineClient, find_engine, missing_engine_error

_VERBS = [
    "engine overview — this descriptive snapshot",
    "engine status — the live engine's state (needs a running engine)",
    "engine version — the engine binary's version and revision",
    "engine doctor — the engine binary's own environment checks",
]


def _engine_sections() -> list[dict[str, object]]:
    return [
        {"title": "Verbs", "items": list(_VERBS)},
        {
            "title": "Locator",
            "items": [
                "NEUROSYMBOLIC_ENGINE env var, if set and executable",
                "otherwise: neurosymbolic-engine on PATH",
            ],
        },
    ]


def cmd_engine_overview(args: argparse.Namespace) -> int:
    emit_overview(
        "neurosymbolic-system engine",
        _engine_sections(),
        json_mode=bool(getattr(args, "json", False)),
    )
    return 0


def _delegate(binary_verb: str) -> Callable[[argparse.Namespace], int]:
    def _cmd(args: argparse.Namespace) -> int:
        path = find_engine()
        if path is None:
            raise missing_engine_error()
        json_mode = bool(getattr(args, "json", False))
        client = EngineClient(path)
        res = client.run(binary_verb, json=json_mode)
        if json_mode:
            emit_result(res.result, json_mode=True)
        else:
            emit_result(res.stdout.rstrip("\n"), json_mode=False)
        return 0

    return _cmd


cmd_engine_status = _delegate("status")
cmd_engine_version = _delegate("version")
cmd_engine_doctor = _delegate("doctor")


def _no_verb(args: argparse.Namespace) -> int:
    return cmd_engine_overview(args)


def register(sub: argparse._SubParsersAction) -> None:
    p = sub.add_parser(
        "engine",
        help="Delegate to the neurosymbolic-engine binary (see 'engine overview').",
    )
    p.add_argument("--json", action="store_true", help="Emit structured JSON.")
    p.set_defaults(func=_no_verb, json=False)
    noun_sub = p.add_subparsers(dest="engine_command", parser_class=type(p))

    ov = noun_sub.add_parser("overview", help="Describe the engine noun group.")
    ov.add_argument("--json", action="store_true", help="Emit structured JSON.")
    ov.set_defaults(func=cmd_engine_overview)

    status = noun_sub.add_parser("status", help="Report the live engine's state.")
    status.add_argument("--json", action="store_true", help="Emit structured JSON.")
    status.set_defaults(func=cmd_engine_status)

    version = noun_sub.add_parser("version", help="Print the engine binary's version.")
    version.add_argument("--json", action="store_true", help="Emit structured JSON.")
    version.set_defaults(func=cmd_engine_version)

    doctor = noun_sub.add_parser("doctor", help="Run the engine binary's own environment checks.")
    doctor.add_argument("--json", action="store_true", help="Emit structured JSON.")
    doctor.set_defaults(func=cmd_engine_doctor)
