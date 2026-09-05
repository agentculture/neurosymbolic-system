"""``neurosymbolic-system rules`` — thin delegate over the Go engine's rules verbs.

Every action verb here (``check``, ``list``, ``migrate``, ``reload``) is one
subprocess call to ``neurosymbolic-engine rules <verb>`` (see
:mod:`neurosymbolic_system.engine_client`): no rule-loading, validation or
migration logic lives in this module, only argv-shaping and result
rendering. A missing binary surfaces as
:func:`~neurosymbolic_system.engine_client.missing_engine_error` — an
environment error (exit 2) with the ``go build`` remediation — from every
delegating verb; ``overview`` is descriptive and never touches the binary.
"""

from __future__ import annotations

import argparse

from neurosymbolic_system.cli._commands.overview import emit_overview
from neurosymbolic_system.cli._output import emit_result
from neurosymbolic_system.engine_client import (
    EngineClient,
    EngineResult,
    find_engine,
    missing_engine_error,
)

_VERBS = [
    "rules overview — this descriptive snapshot",
    "rules check <file>... [--adaptor PATH] — validate rules files",
    "rules list <file>... [--adaptor PATH] — list every rule's id, kind, predicate",
    "rules migrate <file> [--out PATH] [--force] — write a schema_version-2 twin",
    "rules reload <file>... — ask a live engine to re-read its rules files",
]


def _rules_sections() -> list[dict[str, object]]:
    return [{"title": "Verbs", "items": list(_VERBS)}]


def cmd_rules_overview(args: argparse.Namespace) -> int:
    emit_overview(
        "neurosymbolic-system rules",
        _rules_sections(),
        json_mode=bool(getattr(args, "json", False)),
    )
    return 0


def _client() -> EngineClient:
    path = find_engine()
    if path is None:
        raise missing_engine_error()
    return EngineClient(path)


def _emit(res: EngineResult, json_mode: bool) -> None:
    if json_mode:
        emit_result(res.result, json_mode=True)
    else:
        emit_result(res.stdout.rstrip("\n"), json_mode=False)


def _file_argv(args: argparse.Namespace) -> list[str]:
    argv = list(args.files)
    if getattr(args, "adaptor", None):
        argv += ["--adaptor", args.adaptor]
    return argv


def cmd_rules_check(args: argparse.Namespace) -> int:
    json_mode = bool(getattr(args, "json", False))
    res = _client().run("rules check", *_file_argv(args), json=json_mode)
    _emit(res, json_mode)
    return 0


def cmd_rules_list(args: argparse.Namespace) -> int:
    json_mode = bool(getattr(args, "json", False))
    res = _client().run("rules list", *_file_argv(args), json=json_mode)
    _emit(res, json_mode)
    return 0


def cmd_rules_migrate(args: argparse.Namespace) -> int:
    json_mode = bool(getattr(args, "json", False))
    argv = [args.file]
    if args.out:
        argv += ["--out", args.out]
    if args.force:
        argv.append("--force")
    res = _client().run("rules migrate", *argv, json=json_mode)
    _emit(res, json_mode)
    return 0


def cmd_rules_reload(args: argparse.Namespace) -> int:
    json_mode = bool(getattr(args, "json", False))
    res = _client().run("rules reload", *args.files, json=json_mode)
    _emit(res, json_mode)
    return 0


def _no_verb(args: argparse.Namespace) -> int:
    return cmd_rules_overview(args)


def register(sub: argparse._SubParsersAction) -> None:
    p = sub.add_parser(
        "rules",
        help="Delegate to the neurosymbolic-engine binary's rules verbs (see 'rules overview').",
    )
    p.add_argument("--json", action="store_true", help="Emit structured JSON.")
    p.set_defaults(func=_no_verb, json=False)
    noun_sub = p.add_subparsers(dest="rules_command", parser_class=type(p))

    ov = noun_sub.add_parser("overview", help="Describe the rules noun group.")
    ov.add_argument("--json", action="store_true", help="Emit structured JSON.")
    ov.set_defaults(func=cmd_rules_overview)

    check = noun_sub.add_parser("check", help="Validate rules files with no robot attached.")
    check.add_argument("files", nargs="+", help="Rules file(s) to load as one layer.")
    check.add_argument("--adaptor", help="A .json/.toml adaptor config to attach a vocabulary.")
    check.add_argument("--json", action="store_true", help="Emit structured JSON.")
    check.set_defaults(func=cmd_rules_check)

    ls = noun_sub.add_parser("list", help="List every rule's id, kind and predicate.")
    ls.add_argument("files", nargs="+", help="Rules file(s) to load as one layer.")
    ls.add_argument("--adaptor", help="A .json/.toml adaptor config to attach a vocabulary.")
    ls.add_argument("--json", action="store_true", help="Emit structured JSON.")
    ls.set_defaults(func=cmd_rules_list)

    migrate = noun_sub.add_parser("migrate", help="Write a schema_version-2 twin of a rules file.")
    migrate.add_argument("file", help="Input rules file (schema_version 1).")
    migrate.add_argument("--out", help="Output path (default: <name>.v2.<ext>).")
    migrate.add_argument("--force", action="store_true", help="Overwrite an existing --out.")
    migrate.add_argument("--json", action="store_true", help="Emit structured JSON.")
    migrate.set_defaults(func=cmd_rules_migrate)

    reload_p = noun_sub.add_parser("reload", help="Ask a live engine to re-read its rules files.")
    reload_p.add_argument("files", nargs="+", help="Rules file(s) the live engine should re-read.")
    reload_p.add_argument("--json", action="store_true", help="Emit structured JSON.")
    reload_p.set_defaults(func=cmd_rules_reload)
