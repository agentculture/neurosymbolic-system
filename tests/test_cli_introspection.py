"""Tests for the introspection verbs: overview, cli overview, doctor."""

from __future__ import annotations

import json

import pytest

from neurosymbolic_system.cli import main

# --- overview -------------------------------------------------------------


def test_overview_text(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["overview"])
    assert rc == 0
    out = capsys.readouterr().out
    assert "# neurosymbolic-system" in out
    assert "Identity" in out


def test_overview_json_shape(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["overview", "--json"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["subject"] == "neurosymbolic-system"
    assert isinstance(payload["sections"], list)
    assert payload["sections"]


def test_overview_graceful_on_bad_path(capsys: pytest.CaptureFixture[str]) -> None:
    # Rubric contract: descriptive verbs never hard-fail on a missing target.
    rc = main(["overview", "/no/such/path/here"])
    assert rc == 0
    assert capsys.readouterr().out.strip()


# --- cli overview ---------------------------------------------------------


def test_cli_overview_text(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["cli", "overview"])
    assert rc == 0
    assert "# neurosymbolic-system cli" in capsys.readouterr().out


def test_cli_overview_json_shape(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["cli", "overview", "--json"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["subject"] == "neurosymbolic-system cli"
    assert isinstance(payload["sections"], list)


def test_cli_noun_bare_is_non_empty(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["cli"])
    assert rc == 0
    assert capsys.readouterr().out.strip()


def test_cli_overview_unknown_flag_structured_error(
    capsys: pytest.CaptureFixture[str],
) -> None:
    # `cli overview` parse errors must route through the structured error
    # contract (error:/hint: + exit 1), not argparse's default stderr/exit 2.
    with pytest.raises(SystemExit) as exc:
        main(["cli", "overview", "--bogus"])
    assert exc.value.code == 1
    err = capsys.readouterr().err
    assert err.startswith("error:")
    assert "hint:" in err


# --- engine overview / rules overview (t12) --------------------------------


def test_engine_overview_json_shape(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["engine", "overview", "--json"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["subject"] == "neurosymbolic-system engine"
    assert isinstance(payload["sections"], list)
    assert payload["sections"]


def test_rules_overview_json_shape(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["rules", "overview", "--json"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["subject"] == "neurosymbolic-system rules"
    assert isinstance(payload["sections"], list)
    assert payload["sections"]


# --- doctor ---------------------------------------------------------------


def test_doctor_text(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["doctor"])
    assert rc in (0, 1)
    assert "neurosymbolic-system doctor" in capsys.readouterr().out


def test_doctor_json_shape(capsys: pytest.CaptureFixture[str]) -> None:
    rc = main(["doctor", "--json"])
    assert rc in (0, 1)
    payload = json.loads(capsys.readouterr().out)
    assert isinstance(payload["healthy"], bool)
    assert isinstance(payload["checks"], list)
    assert payload["checks"]
    for check in payload["checks"]:
        assert {"id", "passed", "severity", "message", "remediation"} <= set(check)


def test_doctor_check_count(capsys: pytest.CaptureFixture[str]) -> None:
    """t12: backend/prompt-file + skills_present + engine_present, plus
    engine_protocol only when an engine binary is actually locatable — so the
    count is 3 or 4 depending on the box, never fewer or more.
    """
    main(["doctor", "--json"])
    payload = json.loads(capsys.readouterr().out)
    check_ids = {c["id"] for c in payload["checks"]}
    assert "skills_present" in check_ids
    assert "engine_present" in check_ids
    if "engine_protocol" in check_ids:
        assert len(payload["checks"]) == 4
    else:
        assert len(payload["checks"]) == 3


def test_doctor_engine_checks_are_warning_severity(capsys: pytest.CaptureFixture[str]) -> None:
    """The engine checks never gate the overall `healthy` verdict (t12 design:
    only severity=='error' checks do), so a box with no engine built yet still
    reports the agent-identity invariants as healthy.
    """
    rc = main(["doctor", "--json"])
    payload = json.loads(capsys.readouterr().out)
    for check in payload["checks"]:
        if check["id"] in ("engine_present", "engine_protocol"):
            assert check["severity"] == "warning"
    assert rc == 0
    assert payload["healthy"] is True


def test_doctor_recognizes_declared_backend(capsys: pytest.CaptureFixture[str]) -> None:
    """The repo's own declared backend must be a known one — doctor stays healthy.

    Guards the backend-consistency invariant: a promotion that changes
    ``culture.yaml``'s backend without teaching ``doctor`` the matching prompt
    file would otherwise slip through (the shape tests above tolerate rc==1).
    """
    rc = main(["doctor", "--json"])
    payload = json.loads(capsys.readouterr().out)
    messages = " ".join(str(c["message"]) for c in payload["checks"])
    assert "unknown backend" not in messages
    assert rc == 0
    assert payload["healthy"] is True
