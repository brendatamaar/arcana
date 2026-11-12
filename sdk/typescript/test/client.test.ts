import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, it } from "node:test";
import { Arcana, ArcanaError } from "../src/index.js";
import { fakeArcana, json, type Fake } from "./fake.js";

const sandbox = { id: "sbx_a", state: "running", template: "base", runtime: "runsc", source: "cold", resources: {}, network: { mode: "none" }, labels: {}, created_at: "2026-11-12T00:00:00Z", expires_at: "2026-11-12T01:00:00Z" };

describe("Arcana client", () => {
  let fake: Fake;
  let arcana: Arcana;
  beforeEach(async () => {
    fake = await fakeArcana();
    arcana = new Arcana({ url: fake.url, apiKey: "test-key" });
  });
  afterEach(async () => fake.close());

  it("sends typed JSON requests with the bearer key and never follows redirects", async () => {
    fake.handler = (_request, response) => json(response, 201, sandbox);
    const created = await arcana.sandboxes.create({ template: "base", ttl_seconds: 60, labels: { task: "t" } });
    assert.equal(created.id, "sbx_a");
    const request = fake.requests[0]!;
    assert.equal(request.method, "POST");
    assert.equal(request.url, "/v1/sandboxes");
    assert.equal(request.headers.authorization, "Bearer test-key");
    assert.deepEqual(JSON.parse(request.body.toString()), { template: "base", ttl_seconds: 60, labels: { task: "t" } });
    fake.handler = (_request, response) => {
      response.writeHead(302, { Location: "http://127.0.0.1:1/steal" });
      response.end();
    };
    await assert.rejects(arcana.sandboxes.get("sbx_a"));
  });

  it("maps the error envelope and Retry-After into ArcanaError", async () => {
    fake.handler = (_request, response) => json(response, 429, { error: { code: "rate_limited", message: "sandbox creation rate exceeded", request_id: "req_1" } }, { "Retry-After": "7" });
    await assert.rejects(arcana.sandboxes.create({ template: "base" }), (error: unknown) => {
      assert.ok(error instanceof ArcanaError);
      assert.equal(error.code, "rate_limited");
      assert.equal(error.status, 429);
      assert.equal(error.requestId, "req_1");
      assert.equal(error.retryAfterSeconds, 7);
      assert.ok(!error.message.includes("test-key"));
      return true;
    });
    assert.equal(fake.requests.length, 1, "mutations must not be retried");
  });

  it("tolerates non-Arcana error bodies", async () => {
    fake.handler = (_request, response) => {
      response.writeHead(502, { "Content-Type": "text/html" });
      response.end("<html>bad gateway</html>");
    };
    await assert.rejects(arcana.files.read("sbx_a", "/workspace/a"), (error: unknown) => error instanceof ArcanaError && error.status === 502 && error.code === "internal");
  });

  it("validates ids and exec arguments before sending anything", async () => {
    await assert.rejects(async () => arcana.sandboxes.get("../etc"), TypeError);
    assert.throws(() => arcana.sandboxes.exec("sbx_a", {}), TypeError);
    assert.throws(() => arcana.sandboxes.exec("sbx_a", { cmd: ["ls"], shell: "ls" }), TypeError);
    assert.equal(fake.requests.length, 0);
  });

  it("execs with argv untouched and stdin as base64", async () => {
    fake.handler = (_request, response) => json(response, 200, { exec_id: "exe_1", exit_code: 7, stdout: "o", stderr: "", timed_out: false, truncated: false, duration_ms: 3 });
    const result = await arcana.sandboxes.exec("sbx_a", { cmd: ["echo", "; not a shell"], timeoutSeconds: 5, stdin: new Uint8Array([0, 255]) });
    assert.equal(result.exit_code, 7);
    assert.deepEqual(JSON.parse(fake.requests[0]!.body.toString()), { cmd: ["echo", "; not a shell"], timeout_seconds: 5, stdin: "AP8=" });
  });

  it("round-trips binary files and encodes paths as query parameters", async () => {
    const data = new Uint8Array([0, 255, 1, 128]);
    fake.handler = (request, response) => {
      if (request.method === "PUT") {
        response.writeHead(204);
        response.end();
      } else {
        response.writeHead(200, { "Content-Type": "application/octet-stream" });
        response.end(Buffer.from(data));
      }
    };
    await arcana.files.write("sbx_a", "/workspace/a ? &", data);
    const put = fake.requests[0]!;
    assert.equal(put.headers["content-type"], "application/octet-stream");
    assert.deepEqual(new Uint8Array(put.body), data);
    assert.equal(new URL(put.url, "http://x").searchParams.get("path"), "/workspace/a ? &");
    assert.deepEqual(await arcana.files.read("sbx_a", "/workspace/a ? &"), data);
    await arcana.files.uploadArchive("sbx_a", "/workspace", data);
    assert.equal(fake.requests[2]!.headers["content-type"], "application/gzip");
  });

  it("covers snapshots and fork including partial failure", async () => {
    const snapshot = { id: "snp_a", source_sandbox_id: "sbx_a", template: "base", resources: {}, network: { mode: "none" }, env_keys: [], size_bytes: 1, sha256: "0".repeat(64), created_at: "2026-11-12T00:00:00Z" };
    fake.handler = (request, response) => {
      if (request.url.endsWith("/fork")) json(response, 201, { items: [sandbox], failures: [{ index: 1, code: "quota_exceeded", message: "sandbox quota exceeded" }] });
      else if (request.method === "DELETE") response.writeHead(204).end();
      else json(response, request.method === "POST" ? 201 : 200, request.url === "/v1/snapshots?limit=5" ? { items: [snapshot] } : snapshot);
    };
    assert.equal((await arcana.snapshots.create("sbx_a")).id, "snp_a");
    assert.equal((await arcana.snapshots.get("snp_a")).sha256.length, 64);
    assert.equal((await arcana.snapshots.list({ limit: 5 })).items.length, 1);
    const forked = await arcana.snapshots.fork("snp_a", { count: 2 });
    assert.equal(forked.items.length, 1);
    assert.equal(forked.failures[0]?.code, "quota_exceeded");
    await arcana.snapshots.delete("snp_a");
    assert.deepEqual(fake.requests.map((r) => `${r.method} ${r.url}`), ["POST /v1/sandboxes/sbx_a/snapshots", "GET /v1/snapshots/snp_a", "GET /v1/snapshots?limit=5", "POST /v1/snapshots/snp_a/fork", "DELETE /v1/snapshots/snp_a"]);
  });

  it("reports health without credentials and requires configuration", async () => {
    fake.handler = (_request, response) => json(response, 200, { status: "ok" });
    assert.deepEqual(await arcana.health(), { status: "ok" });
    assert.equal(fake.requests[0]!.headers.authorization, undefined);
    assert.throws(() => new Arcana({ url: "", apiKey: "k" }), TypeError);
    assert.throws(() => new Arcana({ url: "http://x", apiKey: "" }), TypeError);
  });
});
