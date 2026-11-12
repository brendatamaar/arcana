import assert from "node:assert/strict";
import { after, describe, it } from "node:test";
import { Arcana } from "../src/index.js";

// Runs against a real Arcana (`make sdk-test` starts one). Skipped, never silently passed, otherwise.
const live = process.env.ARCANA_LIVE === "1" && !!process.env.ARCANA_URL && !!process.env.ARCANA_API_KEY;
const skip = live ? false : "set ARCANA_LIVE=1, ARCANA_URL and ARCANA_API_KEY to run against a live Arcana";

describe("live Arcana", { skip }, () => {
  const arcana = new Arcana({ url: process.env.ARCANA_URL ?? "http://127.0.0.1:7070", apiKey: process.env.ARCANA_API_KEY ?? "unset" });
  const template = process.env.ARCANA_TEMPLATE ?? "base";
  const sandboxes: string[] = [];
  const snapshots: string[] = [];
  after(async () => {
    for (const id of sandboxes) await arcana.sandboxes.destroy(id).catch(() => undefined);
    for (const id of snapshots) await arcana.snapshots.delete(id).catch(() => undefined);
  });

  it("covers lifecycle, exec, files, streaming, snapshot and fork", async () => {
    assert.equal((await arcana.health()).status, "ok");
    assert.ok((await arcana.templates()).some((t) => t.name === template));
    const created = await arcana.sandboxes.create({ template, labels: { sdk: "typescript" } });
    sandboxes.push(created.id);
    assert.equal(created.state, "running");

    const ran = await arcana.sandboxes.exec(created.id, { shell: "echo out; echo err >&2; exit 3" });
    assert.equal(ran.exit_code, 3);
    assert.equal(ran.stdout.trim(), "out");

    const payload = new Uint8Array([0, 255, 1, 128, 10]);
    await arcana.files.write(created.id, "/workspace/sub/data.bin", payload);
    assert.deepEqual(await arcana.files.read(created.id, "/workspace/sub/data.bin"), payload);
    assert.ok((await arcana.files.list(created.id, "/workspace", 2)).some((entry) => entry.path === "/workspace/sub/data.bin"));

    const stream = await arcana.sandboxes.execStream(created.id, { cmd: ["/bin/cat"] });
    await stream.write("hello ");
    await stream.write(new Uint8Array([0x77, 0x6f, 0x72, 0x6c, 0x64]));
    await stream.closeStdin();
    const streamed = await stream.collect();
    assert.equal(streamed.stdout, "hello world");
    assert.equal(streamed.exit.exitCode, 0);

    const snapshot = await arcana.snapshots.create(created.id);
    snapshots.push(snapshot.id);
    const forked = await arcana.snapshots.fork(snapshot.id, { count: 2 });
    assert.equal(forked.items.length, 2);
    for (const member of forked.items) {
      sandboxes.push(member.id);
      assert.equal(member.source, "snapshot");
      assert.deepEqual(await arcana.files.read(member.id, "/workspace/sub/data.bin"), payload);
    }
    await arcana.files.write(forked.items[0]!.id, "/workspace/only-here", "x");
    await assert.rejects(arcana.files.read(forked.items[1]!.id, "/workspace/only-here"), (error: { code?: string }) => error.code === "file_not_found");
  });
});
