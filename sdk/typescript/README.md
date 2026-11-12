# @brendatamaar/arcana

TypeScript client for [Arcana](../../README.md), a self-hosted sandbox orchestrator for AI agents.
Requires Node 18 or newer. Types are generated from `api/openapi.yaml`.

```ts
import { Arcana, ArcanaError } from "@brendatamaar/arcana";

const arcana = new Arcana({ url: "http://127.0.0.1:7070", apiKey: process.env.ARCANA_API_KEY! });

const sandbox = await arcana.sandboxes.create({ template: "python", ttl_seconds: 600 });
try {
  const run = await arcana.sandboxes.exec(sandbox.id, { cmd: ["python", "-c", "print(1 + 1)"] });
  console.log(run.exit_code, run.stdout); // the command's real exit status

  await arcana.files.write(sandbox.id, "/workspace/hello.txt", "hello");
  const bytes = await arcana.files.read(sandbox.id, "/workspace/hello.txt");

  // Live, binary-safe output and stdin over WebSocket.
  const stream = await arcana.sandboxes.execStream(sandbox.id, { cmd: ["/bin/cat"] });
  await stream.write("echo me");
  await stream.closeStdin();
  for await (const event of stream) process.stdout.write(event.data); // stdout and stderr events
  console.log((await stream.result).exitCode);

  // Snapshot /workspace and try three variants in parallel.
  const snapshot = await arcana.snapshots.create(sandbox.id);
  const forked = await arcana.snapshots.fork(snapshot.id, { count: 3 });
  console.log(forked.items.map((s) => s.id), forked.failures); // partial failures are reported, not thrown
} catch (error) {
  if (error instanceof ArcanaError) console.error(error.code, error.requestId, error.retryAfterSeconds);
  throw error;
} finally {
  await arcana.sandboxes.destroy(sandbox.id);
}
```

Behaviour worth knowing: API failures throw `ArcanaError` (with `code`, `status`, `requestId`, and
`retryAfterSeconds` for `rate_limited`); redirects are never followed; mutating calls are never retried;
`exec` sends `cmd` as argv (no shell) unless you pass `shell`; snapshots capture `/workspace` only. Every
method takes an optional `{ signal }` to cancel. `ExecStream` buffers output until you iterate it, bounded by
the server's per-exec output cap; call `stream.close()` to disconnect, which cancels a running command.

## Development

```sh
corepack enable pnpm            # or: npm i -g pnpm
pnpm install --frozen-lockfile
pnpm run generate               # regenerate src/schema.d.ts from ../../api/openapi.yaml
pnpm run typecheck
pnpm test                       # unit tests against an in-process fake server
```

Live tests run against a real Arcana. From the repository root, after `make build` and `make templates`:

```sh
make sdk-test-typescript        # starts a throwaway Arcana, runs the live test, cleans up
```

or point them at your own server: `ARCANA_LIVE=1 ARCANA_URL=... ARCANA_API_KEY=... pnpm run test:live`.
Without those variables the live test is skipped with a message.
