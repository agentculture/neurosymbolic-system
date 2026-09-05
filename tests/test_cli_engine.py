"""Tests for the `engine`/`rules` noun groups (t12) — thin delegates over the
Go `neurosymbolic-engine` binary.

Every test that exercises the real binary needs ``NEUROSYMBOLIC_ENGINE`` to
point at a build produced by::

    CGO_ENABLED=0 go build -o <path> ./cmd/neurosymbolic-engine

and is skipped otherwise. Tests that only need argparse wiring / the
missing-binary path always run.
"""

from __future__ import annotations

import json
import os
from pathlib import Path

import pytest

from neurosymbolic_system.cli import main

ENGINE_PATH = os.environ.get("NEUROSYMBOLIC_ENGINE")

requires_built_engine = pytest.mark.skipif(
    not ENGINE_PATH or not Path(ENGINE_PATH).is_file(),
    reason="NEUROSYMBOLIC_ENGINE must point at a built neurosymbolic-engine binary",
)


@pytest.fixture()
def no_engine(monkeypatch: pytest.MonkeyPatch) -> None:
    """Ensure neither the env var nor PATH resolves an engine binary."""
    monkeypatch.delenv("NEUROSYMBOLIC_ENGINE", raising=False)
    monkeypatch.setenv("PATH", "/nonexistent")


@pytest.fixture()
def with_engine(monkeypatch: pytest.MonkeyPatch) -> str:
    assert ENGINE_PATH is not None
    monkeypatch.setenv("NEUROSYMBOLIC_ENGINE", ENGINE_PATH)
    return ENGINE_PATH


# --- engine overview / rules overview (no binary needed) --------------------


def test_engine_overview_text(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["engine", "overview"])
    assert rc == 0
    assert "# neurosymbolic-system engine" in capsys.readouterr().out


def test_engine_bare_is_overview(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["engine"])
    assert rc == 0
    assert capsys.readouterr().out.strip()


def test_rules_overview_text(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["rules", "overview"])
    assert rc == 0
    assert "# neurosymbolic-system rules" in capsys.readouterr().out


def test_rules_bare_is_overview(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["rules"])
    assert rc == 0
    assert capsys.readouterr().out.strip()


# --- missing binary: exit 2, go build remediation ---------------------------


def test_engine_status_missing_binary_exits_two(
    no_engine: None, capsys: pytest.CaptureFixture[str]
) -> None:
    rc = main(["engine", "status"])
    assert rc == 2
    err = capsys.readouterr().err
    assert "error: neurosymbolic-engine not found" in err
    assert "go build" in err


def test_engine_status_missing_binary_json(
    no_engine: None, capsys: pytest.CaptureFixture[str]
) -> None:
    rc = main(["engine", "status", "--json"])
    assert rc == 2
    payload = json.loads(capsys.readouterr().err)
    assert payload["code"] == 2
    assert payload["message"] == "neurosymbolic-engine not found"
    assert "go build" in payload["remediation"]


def test_rules_check_missing_binary_exits_two(
    no_engine: None, capsys: pytest.CaptureFixture[str]
) -> None:
    rc = main(["rules", "check", "somefile.toml"])
    assert rc == 2
    err = capsys.readouterr().err
    assert "error: neurosymbolic-engine not found" in err
    assert "go build" in err


# --- engine status/version/doctor against the real binary -------------------


@requires_built_engine
def test_engine_version_relays_binary_output(
    with_engine: str, capsys: pytest.CaptureFixture[str]
) -> None:
    rc = main(["engine", "version"])
    assert rc == 0
    out = capsys.readouterr().out
    assert "neurosymbolic-engine" in out


@requires_built_engine
def test_engine_version_json(with_engine: str, capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["engine", "version", "--json"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert "version" in payload
    assert "revision" in payload


@requires_built_engine
def test_engine_doctor_relays_binary_output(
    with_engine: str, capsys: pytest.CaptureFixture[str]
) -> None:
    rc = main(["engine", "doctor"])
    assert rc == 0
    assert "healthy" in capsys.readouterr().out


@requires_built_engine
def test_engine_status_no_live_engine_exits_two(
    with_engine: str, capsys: pytest.CaptureFixture[str]
) -> None:
    rc = main(["engine", "status"])
    assert rc == 2
    err = capsys.readouterr().err
    assert "error: no live engine" in err


# --- rules check/list/migrate against the real binary ------------------------


@requires_built_engine
def test_rules_check_success_text(with_engine: str, capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["rules", "check", "internal/rules/testdata/microduck/default_rules.toml"])
    assert rc == 0
    assert "rules:" in capsys.readouterr().out


@requires_built_engine
def test_rules_check_success_json(with_engine: str, capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["rules", "check", "internal/rules/testdata/microduck/default_rules.toml", "--json"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["react"] == 1


@requires_built_engine
def test_rules_check_failure_relayed_verbatim(
    with_engine: str, capsys: pytest.CaptureFixture[str]
) -> None:
    rc = main(["rules", "check", "tests/fixtures/broken_rules.toml", "--json"])
    assert rc == 1
    payload = json.loads(capsys.readouterr().err)
    assert payload["code"] == 1
    assert "schema_version" in payload["message"]
    assert payload["remediation"]


@requires_built_engine
def test_rules_check_failure_text_mode(
    with_engine: str, capsys: pytest.CaptureFixture[str]
) -> None:
    rc = main(["rules", "check", "tests/fixtures/broken_rules.toml"])
    assert rc == 1
    err = capsys.readouterr().err
    assert err.startswith("error:")
    assert "hint:" in err
    assert "schema_version" in err


@requires_built_engine
def test_rules_list_success(with_engine: str, capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["rules", "list", "internal/rules/testdata/microduck/default_rules.toml"])
    assert rc == 0
    out = capsys.readouterr().out
    assert out.strip()


@requires_built_engine
def test_rules_migrate_success(
    with_engine: str, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    src = tmp_path / "rules.toml"
    src.write_text(
        Path("internal/rules/testdata/microduck/default_rules.toml").read_text(encoding="utf-8"),
        encoding="utf-8",
    )
    out_path = tmp_path / "rules.v2.toml"
    rc = main(["rules", "migrate", str(src), "--out", str(out_path), "--json"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["schema_version"] == 2
    assert out_path.is_file()


@requires_built_engine
def test_rules_reload_no_live_engine_exits_two(
    with_engine: str, capsys: pytest.CaptureFixture[str]
) -> None:
    rc = main(["rules", "reload", "internal/rules/testdata/microduck/default_rules.toml"])
    assert rc == 2
    err = capsys.readouterr().err
    assert "error: no live engine" in err
