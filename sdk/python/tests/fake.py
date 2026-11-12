"""In-process HTTP and WebSocket servers standing in for Arcana."""

from __future__ import annotations

import json
import threading
from collections.abc import Callable
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from websockets.http11 import Response as WsResponse
from websockets.sync.server import ServerConnection, serve

API_KEY = "test-key"


@dataclass
class Recorded:
    method: str
    path: str
    headers: dict[str, str]
    body: bytes


Reply = tuple[int, dict[str, str], bytes]


@dataclass
class Fake:
    url: str = ""
    requests: list[Recorded] = field(default_factory=list)
    frames: list[dict[str, Any]] = field(default_factory=list)
    handler: Callable[[Recorded], Reply] = lambda _r: json_reply(404, {"error": {"code": "internal", "message": "unhandled", "request_id": "req_x"}})
    on_socket: Callable[[ServerConnection], None] = lambda _s: None
    _http: ThreadingHTTPServer | None = None
    _ws: Any = None

    def close(self) -> None:
        if self._http:
            self._http.shutdown()
            self._http.server_close()
        if self._ws:
            self._ws.shutdown()


def json_reply(status: int, value: Any, headers: dict[str, str] | None = None) -> Reply:
    return status, {"Content-Type": "application/json", **(headers or {})}, json.dumps(value).encode()


def start() -> tuple[Fake, str]:
    """Return a running fake plus the WebSocket base URL (HTTP and WebSocket use separate ports)."""
    fake = Fake()

    class Handler(BaseHTTPRequestHandler):
        def _serve(self) -> None:
            length = int(self.headers.get("Content-Length", "0") or 0)
            body = self.rfile.read(length) if length else b""
            recorded = Recorded(self.command, self.path, {k.lower(): v for k, v in self.headers.items()}, body)
            fake.requests.append(recorded)
            status, headers, payload = fake.handler(recorded)
            self.send_response(status)
            for key, value in headers.items():
                self.send_header(key, value)
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        do_GET = do_POST = do_PUT = do_DELETE = _serve  # noqa: N815

        def log_message(self, *_args: object) -> None:
            pass

    http = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    fake._http = http
    threading.Thread(target=http.serve_forever, daemon=True).start()
    fake.url = f"http://127.0.0.1:{http.server_address[1]}"

    def authorize(_connection: ServerConnection, request: Any) -> WsResponse | None:
        if request.headers.get("Authorization") != f"Bearer {API_KEY}":
            body = json.dumps({"error": {"code": "unauthorized", "message": "authentication required", "request_id": "req_ws"}}).encode()
            return WsResponse(401, "Unauthorized", _headers({"Content-Type": "application/json"}), body)
        return None

    def session(connection: ServerConnection) -> None:
        # Record frames as they arrive, then let the scenario react.
        original = connection.recv

        def recording(*args: Any, **kwargs: Any) -> Any:
            message = original(*args, **kwargs)
            fake.frames.append(json.loads(message))
            return message

        connection.recv = recording  # type: ignore[method-assign]
        fake.on_socket(connection)

    ws = serve(session, "127.0.0.1", 0, process_request=authorize)
    fake._ws = ws
    threading.Thread(target=ws.serve_forever, daemon=True).start()
    return fake, f"http://127.0.0.1:{ws.socket.getsockname()[1]}"


def _headers(values: dict[str, str]) -> Any:
    from websockets.datastructures import Headers

    return Headers(values)
