"""Unit tests for :mod:`neurosymbolic_system.engine_client` (t12)."""

from __future__ import annotations

import os
import stat
import textwrap
from pathlib import Path

import pytest

from neurosymbolic_system.cli._errors import EXIT_ENV_ERROR, CliError
from neurosymbolic_system.engine_client import (
    EXPECTED_PROTOCOL,
    EngineClient,
    check_protocol,
    find_engine,
    missing_engine_error,
)

ENGINE_PATH = os.environ.get("NEUROSYMBOLIC_ENGINE")

requires_built_engine = pytest.mark.skipif(
    not ENGINE_PATH or not Path(ENGINE_PATH).is_file(),
    reason="NEUROSYMBOLIC_ENGINE must point at a built neurosymbolic-engine binary",
)


def _write_executable(path: Path, script: str) -> None:
    path.write_text(script, encoding="utf-8")
    mode = path.stat().st_mode
    path.chmod(mode | stat.S_IEXEC | stat.S_IXGRP | stat.S_IXOTH)


# --- find_engine -----------------------------------------------------------


def test_find_engine_prefers_env_var_when_executable(tmp_path: Path) -> None:
    fake = tmp_path / "neurosymbolic-engine"
    _write_executable(fake, "#!/bin/sh\necho hi\n")
    found = find_engine(env={"NEUROSYMBOLIC_ENGINE": str(fake)}, which=lambda name: "/never/used")
    assert found == str(fake)


def test_find_engine_falls_back_to_path_when_env_var_unset() -> None:
    found = find_engine(env={}, which=lambda name: "/usr/local/bin/neurosymbolic-engine")
    assert found == "/usr/local/bin/neurosymbolic-engine"


def test_find_engine_ignores_env_var_pointing_at_nonexistent_file(tmp_path: Path) -> None:
    bogus = tmp_path / "does-not-exist"
    found = find_engine(
        env={"NEUROSYMBOLIC_ENGINE": str(bogus)},
        which=lambda name: "/usr/local/bin/neurosymbolic-engine",
    )
    assert found == "/usr/local/bin/neurosymbolic-engine"


def test_find_engine_ignores_env_var_pointing_at_non_executable_file(tmp_path: Path) -> None:
    not_exec = tmp_path / "not-executable"
    not_exec.write_text("nope", encoding="utf-8")
    found = find_engine(
        env={"NEUROSYMBOLIC_ENGINE": str(not_exec)},
        which=lambda name: "/usr/local/bin/neurosymbolic-engine",
    )
    assert found == "/usr/local/bin/neurosymbolic-engine"


def test_find_engine_returns_none_when_nothing_found() -> None:
    found = find_engine(env={}, which=lambda name: None)
    assert found is None


def test_missing_engine_error_shape() -> None:
    err = missing_engine_error()
    assert isinstance(err, CliError)
    assert err.code == EXIT_ENV_ERROR
    assert err.message == "neurosymbolic-engine not found"
    assert "go build" in err.remediation
    assert "NEUROSYMBOLIC_ENGINE" in err.remediation


# --- EngineClient.run against the real built binary -------------------------


@requires_built_engine
def test_run_version_json() -> None:
    client = EngineClient(ENGINE_PATH)
    res = client.run("version", json=True)
    assert res.code == 0
    assert res.result["version"]
    assert "revision" in res.result


@requires_built_engine
def test_run_rules_check_success() -> None:
    client = EngineClient(ENGINE_PATH)
    rules_path = "internal/rules/testdata/microduck/default_rules.toml"
    res = client.run("rules check", rules_path, json=True)
    assert res.code == 0
    assert res.result["react"] == 1


@requires_built_engine
def test_run_rules_check_failure_relays_error_verbatim() -> None:
    client = EngineClient(ENGINE_PATH)
    with pytest.raises(CliError) as excinfo:
        client.run("rules check", "tests/fixtures/broken_rules.toml", json=True)
    err = excinfo.value
    assert err.code == 1
    assert "schema_version" in err.message
    assert err.remediation


@requires_built_engine
def test_run_rules_check_failure_text_mode() -> None:
    client = EngineClient(ENGINE_PATH)
    with pytest.raises(CliError) as excinfo:
        client.run("rules check", "tests/fixtures/broken_rules.toml", json=False)
    err = excinfo.value
    assert err.code == 1
    assert "schema_version" in err.message


@requires_built_engine
def test_run_status_with_no_live_engine_relays_env_error() -> None:
    client = EngineClient(ENGINE_PATH)
    with pytest.raises(CliError) as excinfo:
        client.run("status", json=True)
    err = excinfo.value
    assert err.code == EXIT_ENV_ERROR
    assert err.message == "no live engine"


def test_run_missing_binary_raises_missing_engine_error() -> None:
    client = EngineClient("/no/such/binary/anywhere")
    with pytest.raises(CliError) as excinfo:
        client.run("version")
    err = excinfo.value
    assert err.code == EXIT_ENV_ERROR
    assert err.message == "neurosymbolic-engine not found"


# --- check_protocol (spec h35) ----------------------------------------------


@requires_built_engine
def test_check_protocol_matches_the_built_engine() -> None:
    # `version --json` carries `protocol` (t13, sourced from the Go side's
    # stream.Version): the real binary and this client must agree, and a skew
    # between them is exactly what this check exists to name.
    check = check_protocol(ENGINE_PATH)
    assert check["id"] == "engine_protocol"
    assert check["passed"] is True
    assert check["severity"] == "warning"
    assert f"expected {EXPECTED_PROTOCOL}" in check["message"]
    assert "matches expected" in check["message"]


def test_check_protocol_reports_absence_as_warning_without_failing(tmp_path: Path) -> None:
    # An OLDER engine, built before `protocol` was reported, is not a failure:
    # doctor warns rather than refusing to run against it.
    fake = tmp_path / "neurosymbolic-engine"
    _write_executable(
        fake,
        textwrap.dedent("""\
            #!/bin/sh
            echo '{"version":"0.1.0","revision":"deadbeef"}'
            """),
    )
    check = check_protocol(str(fake))
    assert check["id"] == "engine_protocol"
    assert check["passed"] is True
    assert check["severity"] == "warning"
    assert f"expected {EXPECTED_PROTOCOL}" in check["message"]
    assert "no protocol version" in check["message"]


def test_check_protocol_matches_when_present(tmp_path: Path) -> None:
    fake = tmp_path / "neurosymbolic-engine"
    _write_executable(
        fake,
        textwrap.dedent("""\
            #!/bin/sh
            echo '{"version":"1.0.0","revision":"deadbeef","protocol":1}'
            """),
    )
    check = check_protocol(str(fake))
    assert check["passed"] is True
    assert check["severity"] == "warning"
    assert "matches expected" in check["message"]


def test_check_protocol_reports_mismatch_by_name(tmp_path: Path) -> None:
    fake = tmp_path / "neurosymbolic-engine"
    _write_executable(
        fake,
        textwrap.dedent("""\
            #!/bin/sh
            echo '{"version":"2.0.0","revision":"deadbeef","protocol":99}'
            """),
    )
    check = check_protocol(str(fake))
    assert check["passed"] is False
    assert check["severity"] == "warning"
    assert "99" in check["message"]
    assert str(EXPECTED_PROTOCOL) in check["message"]
    assert "go build" in check["remediation"]


def test_check_protocol_never_raises_on_engine_failure(tmp_path: Path) -> None:
    fake = tmp_path / "neurosymbolic-engine"
    _write_executable(fake, "#!/bin/sh\nexit 2\n")
    check = check_protocol(str(fake))
    assert check["id"] == "engine_protocol"
    assert check["passed"] is False
    assert check["severity"] == "warning"
