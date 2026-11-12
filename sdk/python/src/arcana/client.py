"""Hand-written client over the generated models in ``arcana._generated``."""

from __future__ import annotations

import base64
from collections.abc import Mapping, Sequence
from typing import Any, TypeVar

import httpx2

from ._generated.client import AuthenticatedClient
from ._generated.models.audit_list import AuditList
from ._generated.models.created_key import CreatedKey
from ._generated.models.exec_result import ExecResult
from ._generated.models.file_entry import FileEntry
from ._generated.models.fork_result import ForkResult
from ._generated.models.health import Health
from ._generated.models.sandbox import Sandbox
from ._generated.models.sandbox_list import SandboxList
from ._generated.models.snapshot import Snapshot
from ._generated.models.snapshot_list import SnapshotList
from ._generated.models.template import Template
from ._generated.models.template_list import TemplateList
from .errors import error_from_response
from .stream import ExecStream, open_stream

T = TypeVar("T")


def _check_id(value: str) -> str:
    if not value or len(value) > 128 or any(c in value for c in "/\\?#%"):
        raise ValueError("invalid resource ID")
    return value


def _drop_none(values: Mapping[str, Any]) -> dict[str, Any]:
    return {key: value for key, value in values.items() if value is not None}


class Arcana:
    """Client for the Arcana API.

    Methods raise :class:`arcana.ArcanaError` for API failures. Mutating calls (create, exec, fork, ...)
    are never retried automatically. Use as a context manager, or call :meth:`close`, to release the
    HTTP connection pool.

    ``timeout`` (seconds, default 60) bounds each HTTP request; pass ``None`` to disable it, for example
    when running a command with a long ``timeout_seconds``.
    """

    def __init__(self, url: str, api_key: str, *, timeout: float | None = 60.0, verify_ssl: bool = True) -> None:
        if not url:
            raise ValueError("url is required")
        if not api_key:
            raise ValueError("api_key is required")
        self._url = url
        self._key = api_key
        self._timeout = timeout
        # Redirects are never followed, so credentials cannot be forwarded to another host.
        self._generated = AuthenticatedClient(
            base_url=url.rstrip("/"),
            token=api_key,
            timeout=httpx2.Timeout(timeout) if timeout is not None else httpx2.Timeout(None),
            verify_ssl=verify_ssl,
            follow_redirects=False,
        )
        self.sandboxes = Sandboxes(self)
        self.files = Files(self)
        self.snapshots = Snapshots(self)
        self.admin = Admin(self)

    # -- plumbing ------------------------------------------------------------------------------

    def _request(
        self,
        method: str,
        path: str,
        *,
        json: Any = None,
        params: Mapping[str, Any] | None = None,
        content: bytes | None = None,
        content_type: str | None = None,
        authenticated: bool = True,
    ) -> httpx2.Response:
        headers = {"Content-Type": content_type} if content_type else None
        client = self._generated.get_httpx_client()
        kwargs: dict[str, Any] = {"params": _drop_none(params or {})}
        if json is not None:
            kwargs["json"] = json
        if content is not None:
            kwargs["content"] = content
        if headers:
            kwargs["headers"] = headers
        if not authenticated:
            kwargs.setdefault("headers", {})["Authorization"] = ""
        response = client.request(method, path, **kwargs)
        if response.status_code >= 400 or 300 <= response.status_code < 400:
            raise error_from_response(response.status_code, response.content, response.headers)
        return response

    def close(self) -> None:
        """Release the connection pool."""
        self._generated.get_httpx_client().close()

    def __enter__(self) -> Arcana:
        return self

    def __exit__(self, *_exc: object) -> None:
        self.close()

    # -- top level -----------------------------------------------------------------------------

    def health(self) -> Health:
        """Liveness check; the only unauthenticated endpoint."""
        return Health.from_dict(self._request("GET", "/v1/health", authenticated=False).json())

    def templates(self) -> list[Template]:
        """List configured templates."""
        return TemplateList.from_dict(self._request("GET", "/v1/templates").json()).items


class Sandboxes:
    """Sandbox lifecycle and command execution."""

    def __init__(self, root: Arcana) -> None:
        self._root = root

    def create(
        self,
        template: str,
        *,
        ttl_seconds: int | None = None,
        idle_timeout_seconds: int | None = None,
        resources: Mapping[str, Any] | None = None,
        network: Mapping[str, Any] | None = None,
        env: Mapping[str, str] | None = None,
        labels: Mapping[str, str] | None = None,
        from_snapshot: str | None = None,
    ) -> Sandbox:
        """Create a sandbox. ``network`` is ``{"mode": "none" | "allowlist", "allow": [...], "presets": [...]}``."""
        body = _drop_none(
            {
                "template": template,
                "ttl_seconds": ttl_seconds,
                "idle_timeout_seconds": idle_timeout_seconds,
                "resources": dict(resources) if resources else None,
                "network": dict(network) if network else None,
                "env": dict(env) if env else None,
                "labels": dict(labels) if labels else None,
                "from_snapshot": from_snapshot,
            }
        )
        return Sandbox.from_dict(self._root._request("POST", "/v1/sandboxes", json=body).json())

    def get(self, sandbox_id: str) -> Sandbox:
        return Sandbox.from_dict(self._root._request("GET", f"/v1/sandboxes/{_check_id(sandbox_id)}").json())

    def list(self, *, state: str | None = None, label: str | None = None, limit: int | None = None, cursor: str | None = None) -> SandboxList:
        params = {"state": state, "label": label, "limit": limit, "cursor": cursor}
        return SandboxList.from_dict(self._root._request("GET", "/v1/sandboxes", params=params).json())

    def destroy(self, sandbox_id: str) -> None:
        """Idempotently destroy a sandbox."""
        self._root._request("DELETE", f"/v1/sandboxes/{_check_id(sandbox_id)}")

    def keepalive(self, sandbox_id: str) -> Sandbox:
        """Reset the idle timer without extending the absolute expiry."""
        return Sandbox.from_dict(self._root._request("POST", f"/v1/sandboxes/{_check_id(sandbox_id)}/keepalive").json())

    @staticmethod
    def _exec_body(
        cmd: Sequence[str] | None,
        shell: str | None,
        cwd: str | None,
        env: Mapping[str, str] | None,
        timeout_seconds: int | None,
    ) -> dict[str, Any]:
        if (cmd is None) == (shell is None):
            raise ValueError("set exactly one of cmd or shell")
        return _drop_none(
            {
                "cmd": list(cmd) if cmd is not None else None,
                "shell": shell,
                "cwd": cwd,
                "env": dict(env) if env else None,
                "timeout_seconds": timeout_seconds,
            }
        )

    def exec(
        self,
        sandbox_id: str,
        *,
        cmd: Sequence[str] | None = None,
        shell: str | None = None,
        cwd: str | None = None,
        env: Mapping[str, str] | None = None,
        timeout_seconds: int | None = None,
        stdin: bytes | str | None = None,
    ) -> ExecResult:
        """Run a command and wait. ``result.exit_code`` is the command's real exit status.

        Pass ``cmd`` (argv, no shell) or ``shell`` (``/bin/sh -c``), not both.
        """
        body = self._exec_body(cmd, shell, cwd, env, timeout_seconds)
        if stdin is not None:
            raw = stdin.encode() if isinstance(stdin, str) else bytes(stdin)
            body["stdin"] = base64.b64encode(raw).decode()
        response = self._root._request("POST", f"/v1/sandboxes/{_check_id(sandbox_id)}/exec", json=body)
        return ExecResult.from_dict(response.json())

    def exec_stream(
        self,
        sandbox_id: str,
        *,
        cmd: Sequence[str] | None = None,
        shell: str | None = None,
        cwd: str | None = None,
        env: Mapping[str, str] | None = None,
        timeout_seconds: int | None = None,
        tty: bool = False,
        cols: int | None = None,
        rows: int | None = None,
    ) -> ExecStream:
        """Start a command with live, binary-safe output and stdin. Use the result as a context manager."""
        body = self._exec_body(cmd, shell, cwd, env, timeout_seconds)
        if tty:
            body.update(_drop_none({"tty": True, "cols": cols, "rows": rows}))
        return open_stream(self._root._url, self._root._key, _check_id(sandbox_id), body, self._root._timeout)


class Files:
    """File transfer. All operations run inside the sandbox."""

    def __init__(self, root: Arcana) -> None:
        self._root = root

    def read(self, sandbox_id: str, path: str) -> bytes:
        return self._root._request("GET", f"/v1/sandboxes/{_check_id(sandbox_id)}/files", params={"path": path}).content

    def write(self, sandbox_id: str, path: str, data: bytes | str) -> None:
        """Atomically write a file, creating parent directories."""
        raw = data.encode() if isinstance(data, str) else bytes(data)
        self._root._request(
            "PUT", f"/v1/sandboxes/{_check_id(sandbox_id)}/files", params={"path": path}, content=raw, content_type="application/octet-stream"
        )

    def delete(self, sandbox_id: str, path: str, *, recursive: bool = False) -> None:
        self._root._request(
            "DELETE", f"/v1/sandboxes/{_check_id(sandbox_id)}/files", params={"path": path, "recursive": "true" if recursive else "false"}
        )

    def list(self, sandbox_id: str, path: str, *, depth: int = 1) -> list[FileEntry]:
        response = self._root._request("GET", f"/v1/sandboxes/{_check_id(sandbox_id)}/files/list", params={"path": path, "depth": depth})
        return [FileEntry.from_dict(item) for item in response.json()["items"]]

    def download_archive(self, sandbox_id: str, path: str) -> bytes:
        """Download a directory as a ``.tar.gz``."""
        return self._root._request("GET", f"/v1/sandboxes/{_check_id(sandbox_id)}/files/archive", params={"path": path}).content

    def upload_archive(self, sandbox_id: str, path: str, archive: bytes) -> None:
        """Upload a ``.tar.gz``, extracted inside the sandbox."""
        self._root._request(
            "PUT", f"/v1/sandboxes/{_check_id(sandbox_id)}/files/archive", params={"path": path}, content=archive, content_type="application/gzip"
        )


class Snapshots:
    """Workspace snapshots and forks. Snapshots capture ``/workspace`` only."""

    def __init__(self, root: Arcana) -> None:
        self._root = root

    def create(self, sandbox_id: str) -> Snapshot:
        return Snapshot.from_dict(self._root._request("POST", f"/v1/sandboxes/{_check_id(sandbox_id)}/snapshots").json())

    def list(self, *, limit: int | None = None, cursor: str | None = None) -> SnapshotList:
        return SnapshotList.from_dict(self._root._request("GET", "/v1/snapshots", params={"limit": limit, "cursor": cursor}).json())

    def get(self, snapshot_id: str) -> Snapshot:
        return Snapshot.from_dict(self._root._request("GET", f"/v1/snapshots/{_check_id(snapshot_id)}").json())

    def delete(self, snapshot_id: str) -> None:
        self._root._request("DELETE", f"/v1/snapshots/{_check_id(snapshot_id)}")

    def fork(
        self,
        snapshot_id: str,
        count: int,
        *,
        ttl_seconds: int | None = None,
        idle_timeout_seconds: int | None = None,
        resources: Mapping[str, Any] | None = None,
        network: Mapping[str, Any] | None = None,
        env: Mapping[str, str] | None = None,
        labels: Mapping[str, str] | None = None,
    ) -> ForkResult:
        """Create ``count`` sandboxes from a snapshot.

        Returns when at least one was created; inspect ``failures`` for the rest. Raises the first
        failure as :class:`arcana.ArcanaError` when none could be created.
        """
        body = _drop_none(
            {
                "count": count,
                "ttl_seconds": ttl_seconds,
                "idle_timeout_seconds": idle_timeout_seconds,
                "resources": dict(resources) if resources else None,
                "network": dict(network) if network else None,
                "env": dict(env) if env else None,
                "labels": dict(labels) if labels else None,
            }
        )
        return ForkResult.from_dict(self._root._request("POST", f"/v1/snapshots/{_check_id(snapshot_id)}/fork", json=body).json())


class Admin:
    """Key management and the audit log; these require an admin key."""

    def __init__(self, root: Arcana) -> None:
        self._root = root

    def create_key(self, name: str, *, is_admin: bool = False) -> CreatedKey:
        return CreatedKey.from_dict(self._root._request("POST", "/v1/keys", json={"name": name, "is_admin": is_admin}).json())

    def revoke_key(self, key_id: str) -> None:
        self._root._request("DELETE", f"/v1/keys/{_check_id(key_id)}")

    def audit(
        self,
        *,
        sandbox_id: str | None = None,
        key_id: str | None = None,
        action: str | None = None,
        since: str | None = None,
        until: str | None = None,
        limit: int | None = None,
        cursor: str | None = None,
    ) -> AuditList:
        params = {"sandbox_id": sandbox_id, "key_id": key_id, "action": action, "since": since, "until": until, "limit": limit, "cursor": cursor}
        return AuditList.from_dict(self._root._request("GET", "/v1/audit", params=params).json())
