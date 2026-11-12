"""Errors raised by the Arcana client."""

from __future__ import annotations

import json
from typing import Any


class ArcanaError(Exception):
    """An API failure.

    ``code`` is Arcana's error code (for example ``quota_exceeded``). ``retry_after_seconds`` is set on
    ``rate_limited`` responses. The API key is never part of the message.
    """

    def __init__(
        self,
        status: int,
        code: str,
        message: str,
        request_id: str = "",
        retry_after_seconds: int | None = None,
    ) -> None:
        super().__init__(f"{code}: {message}")
        self.status = status
        self.code = code
        self.message = message
        self.request_id = request_id
        self.retry_after_seconds = retry_after_seconds


def error_from_response(status: int, body: bytes | str, headers: Any = None) -> ArcanaError:
    """Build an error from a non-2xx response, tolerating bodies that are not Arcana's envelope."""
    code, message, request_id = "internal", f"HTTP {status}", ""
    try:
        envelope = json.loads(body)["error"]
        code = str(envelope.get("code", code))
        message = str(envelope.get("message", message))
        request_id = str(envelope.get("request_id", ""))
    except (ValueError, KeyError, TypeError, AttributeError):
        # A proxy may have answered instead of Arcana; keep the generic message.
        pass
    retry: int | None = None
    if headers is not None:
        try:
            value = int(headers.get("Retry-After", ""))
            retry = value if value > 0 else None
        except (TypeError, ValueError):
            retry = None
    return ArcanaError(status, code, message, request_id, retry)
