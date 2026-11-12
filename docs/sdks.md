# SDKs

Arcana ships clients for Go, Python and TypeScript. All three follow the same rules: API failures are typed
errors that carry Arcana's error code; redirects are never followed; mutating calls (create, exec, fork) are
never retried automatically; the API key never appears in an error; and exec streaming is binary-safe.

| Language | Where | Install |
|---|---|---|
| Go | [`pkg/client`](https://github.com/brendatamaar/arcana/tree/main/pkg/client) | `go get github.com/brendatamaar/arcana/pkg/client` |
| Python (3.11+) | [`sdk/python`](https://github.com/brendatamaar/arcana/tree/main/sdk/python) | `pip install arcana-sdk` (from the repository until published) |
| TypeScript (Node 18+) | [`sdk/typescript`](https://github.com/brendatamaar/arcana/tree/main/sdk/typescript) | `npm install @brendatamaar/arcana` (from the repository until published) |

The Python and TypeScript SDKs are generated from the OpenAPI contract and wrapped by hand where generators
struggle: raw file and archive bodies, WebSocket exec streaming, and a uniform error type. See the
[decision record](decisions/0005-sdk-generation.md).

## Python

```python
from arcana import Arcana

with Arcana("http://127.0.0.1:7070", "arc_live_...") as arcana:
    sandbox = arcana.sandboxes.create("python", ttl_seconds=600)
    try:
        run = arcana.sandboxes.exec(sandbox.id, cmd=["python", "-c", "print(1 + 1)"])
        print(run.exit_code, run.stdout)

        with arcana.sandboxes.exec_stream(sandbox.id, cmd=["/bin/cat"]) as stream:
            stream.write("echo me")
            stream.close_stdin()
            for event in stream:          # event.kind is "stdout" or "stderr"; event.data is bytes
                print(event.kind, event.data)

        snapshot = arcana.snapshots.create(sandbox.id)
        forked = arcana.snapshots.fork(snapshot.id, 3)   # partial failures are in forked.failures
    finally:
        arcana.sandboxes.destroy(sandbox.id)
```

## TypeScript

```ts
import { Arcana } from "@brendatamaar/arcana";

const arcana = new Arcana({ url: "http://127.0.0.1:7070", apiKey: process.env.ARCANA_API_KEY! });
const sandbox = await arcana.sandboxes.create({ template: "python", ttl_seconds: 600 });
try {
  const run = await arcana.sandboxes.exec(sandbox.id, { cmd: ["python", "-c", "print(1 + 1)"] });
  console.log(run.exit_code, run.stdout);

  const stream = await arcana.sandboxes.execStream(sandbox.id, { cmd: ["/bin/cat"] });
  await stream.write("echo me");
  await stream.closeStdin();
  for await (const event of stream) process.stdout.write(event.data);

  const snapshot = await arcana.snapshots.create(sandbox.id);
  const forked = await arcana.snapshots.fork(snapshot.id, { count: 3 });
  console.log(forked.items.map((s) => s.id), forked.failures);
} finally {
  await arcana.sandboxes.destroy(sandbox.id);
}
```

Snapshots capture `/workspace` only, not running processes, files outside it, or environment values.
