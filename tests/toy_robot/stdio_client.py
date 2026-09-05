"""The toy robot's OTHER consumer: the same wire, over a child's pipes.

`client.py` connects to a unix socket the engine created. This one spawns the
engine with ``--stdio`` and speaks the identical framing over its stdin and
stdout, which is the transport a consumer gets for free when it owns the
engine's lifecycle: the parent's exit closes the pipes, the engine's reader
ends, and no socket file, directory or stale-socket cleanup exists to get wrong.

It is a SEPARATE file rather than a mode on `client.py` for one reason: that
file was already 189 lines against a 200-line ceiling, and the ceiling is an
acceptance criterion ("a consumer writes almost nothing"), not a style rule.
Raising it to fit a second transport in would have been quietly deleting the
thing being asserted. Both files are held to the same ceiling and the same
grep for engine logic that a consumer must never reimplement.

stdout is the WIRE here, so it carries protocol frames and nothing else; every
senselog line goes to stderr. That separation is what lets a consumer pipe the
engine's stdout straight into a frame reader.
"""

from __future__ import annotations

import json
import struct
import subprocess  # nosec B404 - argv-list spawn of a local built binary, no shell
import threading
import time
from typing import Any, Callable

PROTOCOL = 1
LENGTH = struct.Struct(">I")


class StdioToyRobot:
    """One engine child process, spoken to over its own pipes."""

    def __init__(
        self,
        engine: str,
        args: list[str],
        settle: Callable[[str], None] | None = None,
        heartbeat_s: float = 0.1,
    ) -> None:
        self.engine, self.args = engine, args
        self.heartbeat_s = heartbeat_s
        self._settle = settle
        self.frames: list[dict[str, Any]] = []
        self.settled: list[str] = []
        self.proc: subprocess.Popen[bytes] | None = None
        self.stderr = b""
        self._last_beat = 0.0
        self._lock = threading.Lock()
        self._stop = threading.Event()

    # --- lifecycle ---------------------------------------------------------

    def start(self) -> None:
        self.proc = subprocess.Popen(  # nosec B603 - argv list, shell=False
            [self.engine, "run", "--stdio", *self.args],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            shell=False,
        )
        self._last_beat = time.monotonic()
        threading.Thread(target=self._read_loop, daemon=True).start()
        threading.Thread(target=self._watchdog, daemon=True).start()

    def hello(self, version: int = PROTOCOL, name: str = "toy-robot-stdio") -> None:
        self.send({"v": version, "kind": "hello", "client": name})

    def signal(self, sig: int) -> None:
        assert self.proc is not None
        self.proc.send_signal(sig)

    def stop(self) -> None:
        """Reap the child and collect its stderr, whatever state it is in.

        stderr is read only AFTER the process has exited, so this never races
        the reader thread that owns stdout.
        """
        self._stop.set()
        proc = self.proc
        if proc is None:
            return
        if proc.poll() is None:
            proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)
        for stream in (proc.stdin, proc.stdout):
            if stream is not None:
                stream.close()
        if proc.stderr is not None:
            self.stderr = proc.stderr.read() or b""
            proc.stderr.close()

    # --- wire --------------------------------------------------------------

    def send(self, frame: dict[str, Any]) -> None:
        assert self.proc is not None and self.proc.stdin is not None
        body = json.dumps(frame).encode()
        self.proc.stdin.write(LENGTH.pack(len(body)) + body)
        self.proc.stdin.flush()

    def sense(self, fields: dict[str, Any]) -> None:
        """Publish one sense frame. A null value CLEARS a field."""
        self.send({"v": PROTOCOL, "kind": "sense", "fields": fields})

    def _read_loop(self) -> None:
        stdout = self.proc.stdout if self.proc else None
        while stdout is not None and not self._stop.is_set():
            try:
                header = stdout.read(LENGTH.size)
                if not header or len(header) < LENGTH.size:
                    return
                (size,) = LENGTH.unpack(header)
                body = stdout.read(size)
            except (OSError, ValueError):
                return
            if not body or len(body) < size:
                return
            self._record(json.loads(body))

    def _record(self, frame: dict[str, Any]) -> None:
        with self._lock:
            frame["at"] = time.monotonic()
            self.frames.append(frame)
            if frame.get("kind") == "heartbeat":
                self._last_beat = frame["at"]
        if frame.get("kind") == "end":
            self.settle(f"end:{frame.get('reason', '')}")

    def _watchdog(self) -> None:
        """Settle when two heartbeat intervals pass with no beat."""
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
