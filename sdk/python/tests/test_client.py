import json
import unittest
from urllib.parse import parse_qs, urlparse

from arcana import Arcana, ArcanaError

from . import fake as fakes

SANDBOX = {
    "id": "sbx_a", "state": "running", "template": "base", "runtime": "runsc", "source": "cold",
    "resources": {}, "network": {"mode": "none"}, "labels": {},
    "created_at": "2026-11-12T00:00:00Z", "expires_at": "2026-11-12T01:00:00Z",
}  # fmt: skip
SNAPSHOT = {
    "id": "snp_a", "source_sandbox_id": "sbx_a", "template": "base", "resources": {}, "network": {"mode": "none"},
    "env_keys": [], "size_bytes": 1, "sha256": "0" * 64, "created_at": "2026-11-12T00:00:00Z",
}  # fmt: skip


class ClientTests(unittest.TestCase):
    def setUp(self) -> None:
        self.fake, _ = fakes.start()
        self.addCleanup(self.fake.close)
        self.arcana = Arcana(self.fake.url, fakes.API_KEY)
        self.addCleanup(self.arcana.close)

    def test_typed_json_request_carries_bearer_key(self) -> None:
        self.fake.handler = lambda _r: fakes.json_reply(201, SANDBOX)
        sandbox = self.arcana.sandboxes.create("base", ttl_seconds=60, labels={"task": "t"})
        self.assertEqual(sandbox.id, "sbx_a")
        request = self.fake.requests[0]
        self.assertEqual((request.method, request.path), ("POST", "/v1/sandboxes"))
        self.assertEqual(request.headers["authorization"], f"Bearer {fakes.API_KEY}")
        self.assertEqual(json.loads(request.body), {"template": "base", "ttl_seconds": 60, "labels": {"task": "t"}})

    def test_redirects_are_not_followed(self) -> None:
        self.fake.handler = lambda _r: (302, {"Location": "http://127.0.0.1:1/steal"}, b"")
        with self.assertRaises(ArcanaError) as caught:
            self.arcana.sandboxes.get("sbx_a")
        self.assertEqual(caught.exception.status, 302)
        self.assertEqual(len(self.fake.requests), 1)

    def test_error_envelope_and_retry_after(self) -> None:
        self.fake.handler = lambda _r: fakes.json_reply(
            429, {"error": {"code": "rate_limited", "message": "sandbox creation rate exceeded", "request_id": "req_1"}}, {"Retry-After": "7"}
        )
        with self.assertRaises(ArcanaError) as caught:
            self.arcana.sandboxes.create("base")
        error = caught.exception
        self.assertEqual((error.code, error.status, error.request_id, error.retry_after_seconds), ("rate_limited", 429, "req_1", 7))
        self.assertNotIn(fakes.API_KEY, str(error))
        self.assertEqual(len(self.fake.requests), 1, "mutations must not be retried")

    def test_non_arcana_error_bodies(self) -> None:
        self.fake.handler = lambda _r: (502, {"Content-Type": "text/html"}, b"<html>bad gateway</html>")
        with self.assertRaises(ArcanaError) as caught:
            self.arcana.files.read("sbx_a", "/workspace/a")
        self.assertEqual((caught.exception.status, caught.exception.code), (502, "internal"))

    def test_arguments_are_validated_before_sending(self) -> None:
        for call in (
            lambda: self.arcana.sandboxes.get("../etc"),
            lambda: self.arcana.sandboxes.exec("sbx_a"),
            lambda: self.arcana.sandboxes.exec("sbx_a", cmd=["ls"], shell="ls"),
            lambda: Arcana("", "k"),
            lambda: Arcana("http://x", ""),
        ):
            with self.assertRaises(ValueError):
                call()
        self.assertEqual(self.fake.requests, [])

    def test_exec_keeps_argv_and_encodes_stdin(self) -> None:
        self.fake.handler = lambda _r: fakes.json_reply(
            200, {"exec_id": "exe_1", "exit_code": 7, "stdout": "o", "stderr": "", "timed_out": False, "truncated": False, "duration_ms": 3}
        )
        result = self.arcana.sandboxes.exec("sbx_a", cmd=["echo", "; not a shell"], timeout_seconds=5, stdin=b"\x00\xff")
        self.assertEqual(result.exit_code, 7)
        self.assertEqual(json.loads(self.fake.requests[0].body), {"cmd": ["echo", "; not a shell"], "timeout_seconds": 5, "stdin": "AP8="})

    def test_binary_files_roundtrip_with_query_encoded_paths(self) -> None:
        data = bytes([0, 255, 1, 128])

        def handler(request: fakes.Recorded) -> fakes.Reply:
            if request.method == "PUT":
                return 204, {}, b""
            return 200, {"Content-Type": "application/octet-stream"}, data

        self.fake.handler = handler
        self.arcana.files.write("sbx_a", "/workspace/a ? &", data)
        put = self.fake.requests[0]
        self.assertEqual(put.headers["content-type"], "application/octet-stream")
        self.assertEqual(put.body, data)
        self.assertEqual(parse_qs(urlparse(put.path).query)["path"], ["/workspace/a ? &"])
        self.assertEqual(self.arcana.files.read("sbx_a", "/workspace/a ? &"), data)
        self.arcana.files.upload_archive("sbx_a", "/workspace", data)
        self.assertEqual(self.fake.requests[2].headers["content-type"], "application/gzip")

    def test_snapshots_and_partial_fork(self) -> None:
        def handler(request: fakes.Recorded) -> fakes.Reply:
            if request.path.endswith("/fork"):
                return fakes.json_reply(201, {"items": [SANDBOX], "failures": [{"index": 1, "code": "quota_exceeded", "message": "sandbox quota exceeded"}]})
            if request.method == "DELETE":
                return 204, {}, b""
            if request.path.startswith("/v1/snapshots?"):
                return fakes.json_reply(200, {"items": [SNAPSHOT]})
            return fakes.json_reply(201 if request.method == "POST" else 200, SNAPSHOT)

        self.fake.handler = handler
        self.assertEqual(self.arcana.snapshots.create("sbx_a").id, "snp_a")
        self.assertEqual(len(self.arcana.snapshots.get("snp_a").sha256), 64)
        self.assertEqual(len(self.arcana.snapshots.list(limit=5).items), 1)
        forked = self.arcana.snapshots.fork("snp_a", 2, labels={"attempt": "x"})
        self.assertEqual((len(forked.items), forked.failures[0].code), (1, "quota_exceeded"))
        self.arcana.snapshots.delete("snp_a")
        self.assertEqual(
            [f"{r.method} {r.path}" for r in self.fake.requests],
            ["POST /v1/sandboxes/sbx_a/snapshots", "GET /v1/snapshots/snp_a", "GET /v1/snapshots?limit=5", "POST /v1/snapshots/snp_a/fork", "DELETE /v1/snapshots/snp_a"],
        )
        self.assertEqual(json.loads(self.fake.requests[3].body), {"count": 2, "labels": {"attempt": "x"}})

    def test_health_is_unauthenticated(self) -> None:
        self.fake.handler = lambda _r: fakes.json_reply(200, {"status": "ok"})
        self.assertEqual(self.arcana.health().status.value, "ok")
        self.assertEqual(self.fake.requests[0].headers.get("authorization", ""), "")


if __name__ == "__main__":
    unittest.main()
