"""Streaming exec over WebSocket (binary-safe output and stdin)."""

from __future__ import annotations

import base64
import json
import threading
from collections.abc import Iterator
from dataclasses import dataclass
from typing import Any, Literal

from websockets.exceptions import ConnectionClosed, InvalidStatus
from websockets.sync.client import ClientConnection, connect

from .errors import ArcanaError, error_from_response

MAX_FRAME = 65536
STDIN_CHUNK = 16384


@dataclass(frozen=True)
class StreamEvent:
    """One chunk of command output. ``data`` is raw bytes, not decoded text."""

    kind: Literal["stdout", "stderr"]
    data: bytes


@dataclass(frozen=True)
class ExecExit:
    """The terminal result. ``exit_code`` is the command's real exit status."""

    exit_code: int
    timed_out: bool
    truncated: bool
    cancelled: bool
    duration_ms: int


class ExecStream:
    """A running command. Iterate it for output; use it as a context manager to guarantee cleanup.

    Reading and writing may happen on different threads. Output is read on demand, so a consumer that
    never iterates applies backpressure to the server rather than buffering in memory. Disconnecting
    before ``exit`` makes the server cancel the command.
    """

    def __init__(self, connection: ClientConnection, exec_id: str, context: Any = None) -> None:
        self.exec_id = exec_id
        self._connection = connection
        self._context = context
        self._exit: ExecExit | None = None
        self._stdin_closed = False
        self._send_lock = threading.Lock()

    # -- receiving -----------------------------------------------------------------------------

    def __iter__(self) -> Iterator[StreamEvent]:
        while self._exit is None:
            try:
                raw = self._connection.recv()
            except ConnectionClosed as error:
                raise ConnectionError("exec stream closed before the command exited") from error
            frame = _parse(raw)
            kind = frame.get("type")
            if kind in ("stdout", "stderr"):
                yield StreamEvent(kind, base64.b64decode(str(frame.get("data", ""))))
            elif kind == "exit":
                self._exit = ExecExit(
                    exit_code=int(frame["exit_code"]),
                    timed_out=bool(frame["timed_out"]),
                    truncated=bool(frame["truncated"]),
                    cancelled=bool(frame["cancelled"]),
                    duration_ms=int(frame["duration_ms"]),
                )
            elif kind == "error":
                raise ArcanaError(0, str(frame.get("code", "internal")), str(frame.get("message", "exec failed")))
            else:
                raise ConnectionError("unknown exec message")

    def collect(self) -> tuple[str, str, ExecExit]:
        """Drain the command and return ``(stdout, stderr, exit)`` as UTF-8 text (invalid bytes replaced)."""
        out, err = bytearray(), bytearray()
        for event in self:
            (out if event.kind == "stdout" else err).extend(event.data)
        assert self._exit is not None
        return out.decode("utf-8", "replace"), err.decode("utf-8", "replace"), self._exit

    def wait(self) -> ExecExit:
        """Drain and discard remaining output, then return the exit result."""
        for _ in self:
            pass
        assert self._exit is not None
        return self._exit

    # -- sending -------------------------------------------------------------------------------

    def _send(self, frame: dict[str, Any]) -> None:
        text = json.dumps(frame)
        if len(text.encode()) > MAX_FRAME:
            raise ValueError("frame exceeds 64 KiB")
        with self._send_lock:
            self._connection.send(text)

    def write(self, data: bytes | str) -> None:
        """Send binary-safe stdin in bounded frames."""
        if self._stdin_closed:
            raise ValueError("stdin is closed")
        raw = data.encode() if isinstance(data, str) else bytes(data)
        for offset in range(0, len(raw), STDIN_CHUNK):
            chunk = base64.b64encode(raw[offset : offset + STDIN_CHUNK]).decode()
            self._send({"type": "stdin", "data": chunk})

    def close_stdin(self) -> None:
        """Signal end of input without cancelling the command."""
        if not self._stdin_closed:
            self._stdin_closed = True
            self._send({"type": "stdin_close"})

    def resize(self, cols: int, rows: int) -> None:
        """Update a TTY's size."""
        self._send({"type": "resize", "cols": cols, "rows": rows})

    def cancel(self) -> None:
        """Ask the server to kill the whole process tree; keep draining until the exit arrives."""
        self._send({"type": "cancel"})

    def close(self) -> None:
        """Disconnect. A command still running is cancelled by the server."""
        if self._context is not None:
            context, self._context = self._context, None
            context.__exit__(None, None, None)
        else:
            self._connection.close()

    def __enter__(self) -> ExecStream:
        return self

    def __exit__(self, *_exc: object) -> None:
        self.close()


def _parse(raw: str | bytes) -> dict[str, Any]:
    try:
        frame = json.loads(raw)
    except ValueError as error:
        raise ConnectionError("invalid exec message") from error
    if not isinstance(frame, dict):
        raise ConnectionError("invalid exec message")
    return frame


def open_stream(base_url: str, api_key: str, sandbox_id: str, request: dict[str, Any], timeout: float | None) -> ExecStream:
    """Dial the exec stream, send ``start`` and wait for ``started``. Internal: use ``sandboxes.exec_stream``."""
    if ("cmd" in request) == ("shell" in request):
        raise ValueError("set exactly one of cmd or shell")
    url = base_url.rstrip("/").replace("https://", "wss://", 1).replace("http://", "ws://", 1)
    url = f"{url}/v1/sandboxes/{sandbox_id}/exec/stream"
    # websockets >= 17.1 deprecates using connect() without its context manager, and older versions
    # support it, so enter the context by hand and keep it for ExecStream.close().
    try:
        context = connect(
            url,
            additional_headers={"Authorization": f"Bearer {api_key}"},
            max_size=MAX_FRAME * 2,
            open_timeout=timeout,
        )
        connection = context.__enter__()
    except InvalidStatus as error:
        response = error.response
        raise error_from_response(response.status_code, response.body or b"", response.headers) from None
    try:
        connection.send(json.dumps({"type": "start", **request}))
        frame = _parse(connection.recv())
        if frame.get("type") == "error":
            raise ArcanaError(0, str(frame.get("code", "internal")), str(frame.get("message", "exec failed")))
        if frame.get("type") != "started" or not frame.get("exec_id"):
            raise ConnectionError("invalid started message")
        return ExecStream(connection, str(frame["exec_id"]), context)
    except BaseException:
        context.__exit__(None, None, None)
        raise
