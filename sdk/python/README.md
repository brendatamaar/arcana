# arcana-sdk

Python client for [Arcana](../../README.md), a self-hosted sandbox orchestrator for AI agents.
Requires Python 3.11 or newer. Models are generated from `api/openapi.yaml`.

```python
from arcana import Arcana, ArcanaError

with Arcana("http://127.0.0.1:7070", "arc_live_...") as arcana:
    sandbox = arcana.sandboxes.create("python", ttl_seconds=600)
    try:
        run = arcana.sandboxes.exec(sandbox.id, cmd=["python", "-c", "print(1 + 1)"])
        print(run.exit_code, run.stdout)  # the command's real exit status

        arcana.files.write(sandbox.id, "/workspace/hello.txt", "hello")
        data = arcana.files.read(sandbox.id, "/workspace/hello.txt")

        # Live, binary-safe output and stdin over WebSocket.
        with arcana.sandboxes.exec_stream(sandbox.id, cmd=["/bin/cat"]) as stream:
            stream.write("echo me")
            stream.close_stdin()
            for event in stream:  # StreamEvent(kind="stdout" | "stderr", data=bytes)
                print(event.kind, event.data)
            print(stream.wait().exit_code)

        # Snapshot /workspace and try three variants in parallel.
        snapshot = arcana.snapshots.create(sandbox.id)
        forked = arcana.snapshots.fork(snapshot.id, 3)
        print([s.id for s in forked.items], forked.failures)  # partial failures are reported, not raised
    except ArcanaError as error:
        print(error.code, error.request_id, error.retry_after_seconds)
        raise
    finally:
        arcana.sandboxes.destroy(sandbox.id)
```

Behaviour worth knowing: API failures raise `ArcanaError` (with `code`, `status`, `request_id`, and
`retry_after_seconds` for `rate_limited`); redirects are never followed; mutating calls are never retried;
`exec` sends `cmd` as argv (no shell) unless you pass `shell`; snapshots capture `/workspace` only. Each HTTP
request has a 60 second timeout by default (`timeout=None` disables it, for long `timeout_seconds`).
Iterating an `ExecStream` reads on demand, so a slow consumer applies backpressure instead of buffering.
Disconnecting (`stream.close()` or leaving the `with` block) cancels a command that is still running.

## Development

```sh
python3 -m venv .venv && . .venv/bin/activate
pip install -e ".[dev]"
make -C ../.. sdk-generate        # regenerates src/arcana/_generated (needs openapi-python-client on PATH)
PYTHONPATH=src python -W error -m unittest discover -s tests -t .
```

Live tests run against a real Arcana. From the repository root, after `make build` and `make templates`:

```sh
make sdk-test-python              # starts a throwaway Arcana, runs the live test, cleans up
```

or point them at your own server: `ARCANA_LIVE=1 ARCANA_URL=... ARCANA_API_KEY=... python -m unittest tests.test_live`.
Without those variables the live test is skipped with a message.
