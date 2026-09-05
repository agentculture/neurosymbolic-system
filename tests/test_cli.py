"""Smoke tests for the neurosymbolic-system CLI entry point and its verbs."""

from __future__ import annotations

import argparse
import json

import pytest

from neurosymbolic_system import __version__
from neurosymbolic_system.cli import _build_parser, main
from neurosymbolic_system.explain import known_paths
from neurosymbolic_system.explain.catalog import ENTRIES


def test_version_flag(capsys: pytest.CaptureFixture[str]) -> None:
    with pytest.raises(SystemExit) as exc:
        main(["--version"])
    assert exc.value.code == 0
    assert __version__ in capsys.readouterr().out


def test_no_args_prints_help(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main([])
    assert rc == 0
    assert "usage: neurosymbolic-system" in capsys.readouterr().out


def test_unknown_command_errors(capsys: pytest.CaptureFixture[str]) -> None:
    with pytest.raises(SystemExit) as exc:
        main(["bogus"])
    assert exc.value.code == 1
    err = capsys.readouterr().err
    assert err.startswith("error:")
    assert "hint:" in err


# --- whoami ---------------------------------------------------------------


def test_whoami_text(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["whoami"])
    assert rc == 0
    out = capsys.readouterr().out
    assert "nick: neurosymbolic-system" in out
    assert "backend: colleague" in out
    assert "model:" in out


def test_whoami_json(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["whoami", "--json"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["nick"] == "neurosymbolic-system"
    assert payload["version"] == __version__
    assert payload["backend"] == "colleague"


# --- learn ----------------------------------------------------------------


def test_learn_text(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["learn"])
    assert rc == 0
    out = capsys.readouterr().out
    assert len(out) >= 200
    assert "neurosymbolic-system" in out
    assert "Exit-code policy" in out
    assert "--json" in out
    assert "explain" in out


def test_learn_json(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["learn", "--json"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["tool"] == "neurosymbolic-system"
    assert payload["version"] == __version__
    assert payload["json_support"] is True


# --- explain --------------------------------------------------------------


def test_explain_root(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["explain"])
    assert rc == 0
    assert "# neurosymbolic-system" in capsys.readouterr().out


def test_explain_self(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["explain", "neurosymbolic-system"])
    assert rc == 0
    assert capsys.readouterr().out.startswith("#")


def test_explain_json(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["explain", "whoami", "--json"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["path"] == ["whoami"]
    assert "neurosymbolic-system whoami" in payload["markdown"]


def test_explain_unknown_path_errors(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["explain", "nonexistent"])
    assert rc == 1
    captured = capsys.readouterr()
    assert captured.err.startswith("error:")
    assert "hint:" in captured.err


def test_every_catalog_path_resolves(capsys: pytest.CaptureFixture[str]) -> None:
    for path in known_paths():
        rc = main(["explain", *path])
        assert rc == 0, f"explain {' '.join(path)} failed"
        capsys.readouterr()


def _registered_command_paths(
    parser: argparse.ArgumentParser, prefix: tuple[str, ...] = ()
) -> set[tuple[str, ...]]:
    """Walk every ``add_subparsers``/``add_parser`` level, returning each path.

    A "path" is a noun/verb sequence someone could type on the command line
    (``("engine", "status")``) — exactly the shape ``explain`` and the
    catalog key on. Positional arguments (``explain``'s ``path``, ``rules
    check``'s ``files``) are not subparsers and are not walked into.
    """
    paths: set[tuple[str, ...]] = set()
    for action in parser._actions:  # noqa: SLF001 - argparse has no public walk API
        if isinstance(action, argparse._SubParsersAction):
            for name, subparser in action.choices.items():
                path = prefix + (name,)
                paths.add(path)
                paths |= _registered_command_paths(subparser, path)
    return paths


def test_every_registered_command_path_has_a_catalog_entry() -> None:
    """Guards against the catalog drifting behind the argparse tree (t12 review).

    Walks the real, fully-registered parser tree rather than a hand-maintained
    list, so a newly added noun/verb without a matching ``explain`` entry
    fails this test instead of silently 404ing at runtime.
    """
    parser = _build_parser()
    paths = _registered_command_paths(parser)
    assert paths, "expected at least one registered command path"
    missing = sorted(p for p in paths if p not in ENTRIES)
    assert not missing, f"missing explain catalog entries for: {missing}"
