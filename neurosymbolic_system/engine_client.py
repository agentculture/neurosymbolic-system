"""Stdlib client for the ``neurosymbolic-engine`` Go binary.

Spec decision (t12): the Python CLI's ``engine``/``rules`` noun groups are a
thin front over ``cmd/neurosymbolic-engine`` — no engine logic (rule loading,
validation, migration) lives in this package. Every verb here shells out to
the binary with an explicit argv list (never a shell string), parses its
already-structured output, and relays a failure's ``{code, message,
remediation}`` verbatim as a :class:`~neurosymbolic_system.cli._errors.CliError`
— the same relay-don't-reclassify contract
:mod:`culture_nodes.api_client` follows for its HTTP calls (see
``../culture-nodes/culture_nodes/api_client.py`` in the sibling checkout,
cited for its shape, not its code).

Locator order (:func:`find_engine`): the ``NEUROSYMBOLIC_ENGINE`` environment
variable, when it names an executable file, wins; otherwise
``neurosymbolic-engine`` is looked up on ``PATH``. Neither is required — a box
with no engine still gets every identity/agent verb; only ``engine``/``rules``
verbs (and the ``engine_protocol`` doctor check) need one.

Bandit B603 note
-----------------
:func:`EngineClient.run` calls ``subprocess.run`` with an explicit argv list
and ``shell=False`` — never a shell string — so the B603 "subprocess call:
check for execution of untrusted input" finding does not apply; this repo's
``[tool.bandit]`` already skips B603 (see ``pyproject.toml``), and the
``# nosec B603`` marker below records the same justification for anyone
reading the code without that context.

Protocol versioning (spec h35)
-------------------------------
:data:`EXPECTED_PROTOCOL` is this client's expectation of the engine's wire
protocol. ``neurosymbolic-engine version --json`` carries it as ``protocol``
(t13, sourced on the Go side from ``internal/stream``'s ``Version``), so a
skew between a client and an engine is named from a one-shot exec rather
than discovered as a stream whose every frame comes back refused. An OLDER
engine, built before that field existed, omits it; :func:`check_protocol`
reports the absence as a warning, never a failure, so an upgrade in either
direction degrades rather than breaks.
"""

from __future__ import annotations

import json as json_module
import os
import shutil
import subprocess  # nosec B404 - argv-list subprocess calls only, no shell
from dataclasses import dataclass
from typing import Any, Callable

from neurosymbolic_system.cli._errors import EXIT_ENV_ERROR, CliError

#: Environment variable naming an explicit engine binary path (first in the
#: resolution order — see :func:`find_engine`).
ENV_ENGINE_PATH = "NEUROSYMBOLIC_ENGINE"

#: Fallback name looked up on PATH when the env var is unset or unusable.
ENGINE_BINARY_NAME = "neurosymbolic-engine"

#: Default subprocess timeout, seconds. Every verb here is a one-shot local
#: process (rule validation, a version print) — 30s is generous headroom, not
#: a tuned budget.
DEFAULT_TIMEOUT = 30.0

#: This client's expectation of the engine's wire protocol (spec h35). Bump
#: this only in lockstep with a matching bump on the Go side.
EXPECTED_PROTOCOL = 1

#: Remediation shared by every "no engine" error (doctor's ``engine_present``
#: check, and the missing-binary CliError any noun verb raises).
_BUILD_REMEDIATION = (
    "build it: CGO_ENABLED=0 go build -o ~/.local/bin/neurosymbolic-engine "
    "./cmd/neurosymbolic-engine, or set NEUROSYMBOLIC_ENGINE"
)


def find_engine(
    env: dict[str, str] | None = None,
    which: Callable[[str], str | None] | None = None,
) -> str | None:
    """Locate the engine binary: ``NEUROSYMBOLIC_ENGINE`` (if executable), else PATH.

    ``env``/``which`` are injectable for tests; callers normally pass neither
    and get ``os.environ``/``shutil.which``.
    """
    env = env if env is not None else os.environ
    which = which if which is not None else shutil.which

    override = env.get(ENV_ENGINE_PATH)
    if override and os.path.isfile(override) and os.access(override, os.X_OK):
        return override

    return which(ENGINE_BINARY_NAME)


def missing_engine_error() -> CliError:
    """The CliError every caller raises when :func:`find_engine` returns ``None``."""
    return CliError(
        code=EXIT_ENV_ERROR,
        message="neurosymbolic-engine not found",
        remediation=_BUILD_REMEDIATION,
    )


@dataclass
class EngineResult:
    """One verb call's outcome: exit code, raw streams, and the parsed result.

    ``result`` is the JSON-decoded stdout when the call was made with
    ``json=True`` and stdout parsed cleanly; otherwise ``None``.
    """

    code: int
    stdout: str
    stderr: str
    result: Any = None


def _error_from_json_stderr(exit_code: int, stderr: str) -> CliError:
    """Parse the engine's ``{code, message, remediation}`` JSON error body.

    Relayed verbatim (per the module docstring); a stderr that fails to parse
    as that shape falls back to a client-authored CliError so a caller never
    sees a raw parse traceback.
    """
    try:
        payload = json_module.loads(stderr)
    except json_module.JSONDecodeError:
        payload = None
    if isinstance(payload, dict) and "message" in payload:
        return CliError(
            code=int(payload.get("code", exit_code)),
            message=str(payload["message"]),
            remediation=str(payload.get("remediation", "")),
        )
    return CliError(
        code=exit_code,
        message=stderr.strip() or "neurosymbolic-engine failed with no error body",
        remediation="",
    )


def _error_from_text_stderr(exit_code: int, stderr: str) -> CliError:
    """Parse the engine's two-line ``error:``/``hint:`` text error body."""
    message = ""
    remediation = ""
    for line in stderr.splitlines():
        if line.startswith("error:"):
            message = line[len("error:") :].strip()
        elif line.startswith("hint:"):
            remediation = line[len("hint:") :].strip()
    if not message:
        message = stderr.strip() or "neurosymbolic-engine failed with no error body"
    return CliError(code=exit_code, message=message, remediation=remediation)


class EngineClient:
    """Thin subprocess client for one located ``neurosymbolic-engine`` binary."""

    def __init__(self, path: str, *, timeout: float = DEFAULT_TIMEOUT) -> None:
        self.path = path
        self.timeout = timeout

    def run(
        self,
        verb: str,
        *args: str,
        json: bool = False,
        timeout: float | None = None,
    ) -> EngineResult:
        """Run ``<binary> <verb-tokens...> <args...> [--json]``.

        ``verb`` may be a single word (``"status"``) or a space-separated
        noun/sub-verb pair (``"rules check"``), split into separate argv
        tokens before ``args``. Raises :class:`CliError` verbatim (relayed
        from the binary's own error body) on a non-zero exit, or a
        client-authored environment error when the binary cannot even be
        started or exceeds ``timeout``.
        """
        argv = [self.path, *verb.split(), *args]
        if json:
            argv.append("--json")

        effective_timeout = timeout if timeout is not None else self.timeout
        try:
            proc = subprocess.run(  # nosec B603 - argv list, shell=False, see module docstring
                argv,
                capture_output=True,
                text=True,
                timeout=effective_timeout,
                shell=False,
            )
        except FileNotFoundError:
            raise missing_engine_error() from None
        except subprocess.TimeoutExpired:
            raise CliError(
                code=EXIT_ENV_ERROR,
                message=f"neurosymbolic-engine timed out after {effective_timeout}s "
                f"running '{verb}'",
                remediation="the engine process may be wedged; retry or investigate it directly",
            ) from None

        result: Any = None
        if json and proc.stdout.strip():
            try:
                result = json_module.loads(proc.stdout)
            except json_module.JSONDecodeError:
                result = None

        if proc.returncode != 0:
            if json:
                raise _error_from_json_stderr(proc.returncode, proc.stderr)
            raise _error_from_text_stderr(proc.returncode, proc.stderr)

        return EngineResult(
            code=proc.returncode, stdout=proc.stdout, stderr=proc.stderr, result=result
        )


def check_protocol(
    path: str, *, client_factory: Callable[[str], EngineClient] = EngineClient
) -> dict[str, object]:
    """Run the h35 doctor check: does the engine's protocol match expectations?

    Never raises — a doctor check reports, it does not fail the process. Any
    error running ``version --json`` (missing binary, bad exit, malformed
    JSON) is folded into a failed-but-non-fatal check rather than propagated,
    since :mod:`neurosymbolic_system.cli._commands.doctor` needs a check dict
    even when the engine cannot answer.
    """
    try:
        client = client_factory(path)
        res = client.run("version", json=True)
    except CliError as err:
        return {
            "id": "engine_protocol",
            "passed": False,
            "severity": "warning",
            "message": f"could not read engine protocol version: {err.message}",
            "remediation": err.remediation,
        }

    protocol = res.result.get("protocol") if isinstance(res.result, dict) else None
    if protocol is None:
        return {
            "id": "engine_protocol",
            "passed": True,
            "severity": "warning",
            "message": f"engine reports no protocol version (expected {EXPECTED_PROTOCOL})",
            "remediation": "",
        }
    if protocol != EXPECTED_PROTOCOL:
        return {
            "id": "engine_protocol",
            "passed": False,
            "severity": "warning",
            "message": (
                f"engine protocol version {protocol} does not match "
                f"expected {EXPECTED_PROTOCOL}"
            ),
            "remediation": _BUILD_REMEDIATION,
        }
    return {
        "id": "engine_protocol",
        "passed": True,
        "severity": "warning",
        "message": f"engine protocol version {protocol} matches expected {EXPECTED_PROTOCOL}",
        "remediation": "",
    }


__all__ = [
    "ENV_ENGINE_PATH",
    "ENGINE_BINARY_NAME",
    "DEFAULT_TIMEOUT",
    "EXPECTED_PROTOCOL",
    "find_engine",
    "missing_engine_error",
    "EngineResult",
    "EngineClient",
    "check_protocol",
]
