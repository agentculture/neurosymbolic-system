"""A fixture consumer for the toy robot: spawn the engine, speak the wire.

This file is the h16/h4 evidence and it is SUPPOSED to be small and dumb. A
consumer wires a robot to the engine by writing an adaptor config, a rules
file, and a loop that turns readings into sense frames and pose frames into
actuator commands. Everything else — arbitration, rule timing, validation, the
tick budget, drop accounting — belongs to the engine, and a consumer that
reimplemented any of it would be proving the opposite of what this fixture is
for.

`test_toy_client_stays_a_client` enforces exactly that: a line-count ceiling
and a grep for the identifiers a consumer must never need. Both are the point,
not a style rule.

Wire format (`internal/stream`): a 4-byte big-endian length, then that many
bytes of a JSON object. Every frame carries "v" and "kind"; the first frame a
client sends must be a hello.
"""

from __future__ import annotations

import json
import socket
import struct
import subprocess  # nosec B404 - argv-list spawn of a local built binary, no shell
import threading
import time
from typing import Any, Callable

PROTOCOL = 1
LENGTH = struct.Struct(">I")


class ToyRobot:
    """One engine process and one connection to it, plus a settle hook."""

    def __init__(
        self,
        engine: str,
        socket_dir: str,
        args: list[str],
        settle: Callable[[str], None] | None = None,
        heartbeat_s: float = 0.1,
    ) -> None:
        self.engine, self.socket_dir, self.args = engine, socket_dir, args
        self.heartbeat_s = heartbeat_s
        self._settle = settle
        self.frames: list[dict[str, Any]] = []
        self.settled: list[str] = []
        self.proc: subprocess.Popen[bytes] | None = None
        self.sock: socket.socket | None = None
        self.stderr = b""
        self._last_beat = 0.0
        self._lock = threading.Lock()
        self._stop = threading.Event()

    # --- lifecycle ---------------------------------------------------------

    def start(self, timeout: float = 10.0) -> None:
        """Spawn the engine and connect once its socket appears."""
        path = f"{self.socket_dir}/engine.sock"
        self.proc = subprocess.Popen(  # nosec B603 - argv list, shell=False
            [self.engine, "run", "--socket-dir", self.socket_dir, *self.args],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            shell=False,
        )
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                self.sock.connect(path)
                break
            except OSError:
                self.sock.close()
                self.sock = None
                if self.proc.poll() is not None:
                    raise RuntimeError(f"engine exited: {self._drain_stderr()}") from None
                time.sleep(0.02)
        if self.sock is None:
            raise RuntimeError(f"no socket at {path} after {timeout}s")

        self._last_beat = time.monotonic()
        threading.Thread(target=self._read_loop, daemon=True).start()
        threading.Thread(target=self._watchdog, daemon=True).start()

    def hello(self, version: int = PROTOCOL, name: str = "toy-robot") -> None:
        self.send({"v": version, "kind": "hello", "client": name})

    def stop(self) -> None:
        """Close the connection and reap the engine, whatever state it is in."""
        self._stop.set()
        if self.sock is not None:
            self.sock.close()
        if self.proc is not None and self.proc.poll() is None:
            self.proc.terminate()
        self._drain_stderr()

    def signal(self, sig: int) -> None:
        assert self.proc is not None
        self.proc.send_signal(sig)

    def _drain_stderr(self) -> str:
        if self.proc is None:
            return ""
        try:
            _, err = self.proc.communicate(timeout=5)
            self.stderr += err or b""
        except (subprocess.TimeoutExpired, ValueError):
            self.proc.kill()
        return self.stderr.decode(errors="replace")

    # --- wire --------------------------------------------------------------

    def send(self, frame: dict[str, Any]) -> None:
        assert self.sock is not None
        body = json.dumps(frame).encode()
        self.sock.sendall(LENGTH.pack(len(body)) + body)

    def sense(self, fields: dict[str, Any]) -> None:
        """Publish one sense frame. A null value CLEARS a field."""
        self.send({"v": PROTOCOL, "kind": "sense", "fields": fields})

    def mgmt(self, verb: str, call_id: str, args: list[str] | None = None) -> None:
        self.send({"v": PROTOCOL, "kind": "mgmt", "id": call_id, "verb": verb, "args": args or []})

    def _read_loop(self) -> None:
        buffer = b""
        while not self._stop.is_set():
            try:
                chunk = self.sock.recv(65536) if self.sock else b""
            except OSError:
                return
            if not chunk:
                return
            buffer += chunk
            while len(buffer) >= LENGTH.size:
                (size,) = LENGTH.unpack(buffer[: LENGTH.size])
                if len(buffer) < LENGTH.size + size:
                    break
                frame = json.loads(buffer[LENGTH.size : LENGTH.size + size])
                buffer = buffer[LENGTH.size + size :]
                self._record(frame)

    def _record(self, frame: dict[str, Any]) -> None:
        with self._lock:
            frame["at"] = time.monotonic()
            self.frames.append(frame)
            if frame.get("kind") == "heartbeat":
                self._last_beat = frame["at"]
        if frame.get("kind") == "end":
            self.settle(f"end:{frame.get('reason', '')}")

    def _watchdog(self) -> None:
        """Settle when two heartbeat intervals pass with no beat.

        This is the half that does not need the engine's cooperation: a
        SIGKILLed engine sends no end frame, so silence is the only signal
        there is.
        """
        while not self._stop.wait(self.heartbeat_s / 4):
            with self._lock:
                quiet = time.monotonic() - self._last_beat
            if quiet > 2 * self.heartbeat_s:
                self.settle("heartbeat-lapse")
                return

    def settle(self, reason: str) -> None:
        """Bring the robot to a safe resting state. Runs at most once."""
        with self._lock:
            if self.settled:
                return
            self.settled.append(reason)
        if self._settle is not None:
            self._settle(reason)

    # --- reading what arrived ----------------------------------------------

    def of_kind(self, kind: str) -> list[dict[str, Any]]:
        with self._lock:
            return [f for f in self.frames if f.get("kind") == kind]

    def wait_for(self, kind: str, count: int = 1, timeout: float = 5.0) -> bool:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if len(self.of_kind(kind)) >= count:
                return True
            time.sleep(0.01)
        return False
