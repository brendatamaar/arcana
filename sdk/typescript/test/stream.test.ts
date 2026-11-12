import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, it } from "node:test";
import { Arcana, ArcanaError } from "../src/index.js";
import { fakeArcana, type Fake } from "./fake.js";

const b64 = (text: string | Uint8Array): string => Buffer.from(text).toString("base64");

describe("execStream", () => {
  let fake: Fake;
  let arcana: Arcana;
  beforeEach(async () => {
    fake = await fakeArcana();
    arcana = new Arcana({ url: fake.url, apiKey: "test-key" });
  });
  afterEach(async () => fake.close());

  it("sends the start frame, streams binary output in order and returns the real exit", async () => {
    fake.onSocket = (socket) => {
      socket.on("message", (data) => {
        if ((JSON.parse(data.toString()) as { type: string }).type !== "start") return;
        socket.send(JSON.stringify({ type: "started", exec_id: "exe_1" }));
        // Same tick as `started`: must not be lost before the stream object exists.
        socket.send(JSON.stringify({ type: "stdout", data: b64(new Uint8Array([0, 255])) }));
        socket.send(JSON.stringify({ type: "stderr", data: b64("warn") }));
        socket.send(JSON.stringify({ type: "exit", exit_code: 7, timed_out: false, truncated: false, cancelled: false, duration_ms: 12 }));
      });
    };
    const stream = await arcana.sandboxes.execStream("sbx_a", { cmd: ["sh", "-c", "x"], cwd: "/workspace", env: { A: "1" }, timeoutSeconds: 9 });
    assert.equal(stream.execId, "exe_1");
    const events: Array<[string, number[]]> = [];
    for await (const event of stream) events.push([event.type, [...event.data]]);
    assert.deepEqual(events, [["stdout", [0, 255]], ["stderr", [...Buffer.from("warn")]]]);
    assert.deepEqual(await stream.result, { exitCode: 7, timedOut: false, truncated: false, cancelled: false, durationMs: 12 });
    assert.deepEqual(fake.frames[0], { type: "start", cmd: ["sh", "-c", "x"], cwd: "/workspace", env: { A: "1" }, timeout_seconds: 9 });
  });

  it("frames stdin in bounded chunks, closes it and cancels", async () => {
    fake.onSocket = (socket) => {
      socket.on("message", (data) => {
        const frame = JSON.parse(data.toString()) as { type: string };
        if (frame.type === "start") socket.send(JSON.stringify({ type: "started", exec_id: "exe_2" }));
        if (frame.type === "cancel") socket.send(JSON.stringify({ type: "exit", exit_code: 137, timed_out: false, truncated: false, cancelled: true, duration_ms: 1 }));
      });
    };
    const stream = await arcana.sandboxes.execStream("sbx_a", { shell: "cat", tty: true, cols: 80, rows: 24 });
    await stream.write(Buffer.alloc(40000, 1));
    await stream.write("tail");
    await stream.resize(100, 30);
    await stream.closeStdin();
    await stream.closeStdin(); // idempotent
    await assert.rejects(stream.write("late"), /closed/);
    await stream.cancel();
    const exit = await stream.result;
    assert.equal(exit.cancelled, true);
    const stdin = fake.frames.filter((f) => f.type === "stdin");
    assert.equal(stdin.length, 4, "40000 bytes = 3 chunks of at most 16384, plus the tail");
    assert.ok(stdin.every((f) => Buffer.from(String(f.data), "base64").length <= 16384));
    assert.deepEqual(fake.frames[0], { type: "start", shell: "cat", tty: true, cols: 80, rows: 24 });
    assert.deepEqual(fake.frames.map((f) => f.type).slice(-4), ["stdin", "resize", "stdin_close", "cancel"]);
  });

  it("collects text output and exit", async () => {
    fake.onSocket = (socket) => {
      socket.on("message", () => {
        socket.send(JSON.stringify({ type: "started", exec_id: "exe_3" }));
        socket.send(JSON.stringify({ type: "stdout", data: b64("héllo") }));
        socket.send(JSON.stringify({ type: "exit", exit_code: 0, timed_out: false, truncated: false, cancelled: false, duration_ms: 1 }));
      });
    };
    const stream = await arcana.sandboxes.execStream("sbx_a", { cmd: ["echo"] });
    const done = await stream.collect();
    assert.equal(done.stdout, "héllo");
    assert.equal(done.exit.exitCode, 0);
  });

  it("surfaces HTTP failures before the upgrade as ArcanaError", async () => {
    const bad = new Arcana({ url: fake.url, apiKey: "wrong" });
    await assert.rejects(bad.sandboxes.execStream("sbx_a", { cmd: ["ls"] }), (error: unknown) => error instanceof ArcanaError && error.status === 401 && error.code === "unauthorized");
  });

  it("rejects result and iteration when the server drops the connection before exit", async () => {
    fake.onSocket = (socket) => {
      socket.on("message", () => {
        socket.send(JSON.stringify({ type: "started", exec_id: "exe_4" }));
        socket.send(JSON.stringify({ type: "stdout", data: b64("partial") }));
        setTimeout(() => socket.terminate(), 20);
      });
    };
    const stream = await arcana.sandboxes.execStream("sbx_a", { cmd: ["sleep"] });
    await assert.rejects(stream.collect(), /closed before the command exited/);
    await assert.rejects(stream.result, /closed before/);
  });

  it("surfaces server error frames and validates arguments", async () => {
    fake.onSocket = (socket) => {
      socket.on("message", () => socket.send(JSON.stringify({ type: "error", code: "sandbox_not_running", message: "sandbox not running" })));
    };
    await assert.rejects(arcana.sandboxes.execStream("sbx_a", { cmd: ["ls"] }), (error: unknown) => error instanceof ArcanaError && error.code === "sandbox_not_running");
    await assert.rejects(arcana.sandboxes.execStream("sbx_a", {}), TypeError);
  });
});
