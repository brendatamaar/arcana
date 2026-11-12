# Fixtures

Ten small buggy projects for evaluating the [fix-agent](../fix-agent/README.md): four Python, three
Node and three Go. Each directory holds:

- `fixture.json`: name, language, template, a description (for humans; never shown to the agent), and
  the `visible_test` and `hidden_test` commands.
- `project/`: what the agent gets. A `README.md` states the intended behaviour, and a visible test fails.
- `hidden/`: extra tests that judge a fix. They are never placed in the agent's sandbox except
  momentarily by the harness while it checks.
- `solution/`: a reference fix, used only by `fix-agent verify-fixtures` to prove the fixture is valid.

Files ending in `.fixture` (the Go hidden tests and solutions) are uploaded without that suffix. They are
stored that way so this repository's own Go toolchain does not compile them. Go projects are their own
modules, so they are not part of the repository's module either.

Tests use only the standard libraries (`unittest`, `node --test`, `go test`), so fixtures run without
network access. In sandboxes `/tmp` is mounted `noexec`, which stops `go test` from running its test
binary; the Go commands therefore set `GOTMPDIR` to a directory in the workspace.

| Fixture | Bug |
|---|---|
| `py-off-by-one` | an inclusive range loop excludes its end |
| `py-mutable-default` | a mutable default argument leaks state between calls |
| `py-integer-division` | mean uses floor division and mishandles empty input |
| `py-word-count` | punctuation stays attached to words |
| `js-missing-await` | an async function forgets to await a promise |
| `js-numeric-sort` | `Array.prototype.sort` compares numbers as strings |
| `js-optional-property` | a nested property read throws when the parent is missing |
| `go-window-off-by-one` | the last sliding window is dropped |
| `go-nil-map` | a counter writes to a nil map |
| `go-append-aliasing` | `append` writes into a shared backing array |
