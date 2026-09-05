# Go Dependencies Policy

The engine maintains strict import hygiene to stay a pure library without
hard dependencies on robot SDKs, transports, media systems, or subprocess
machinery.

## Policy

Only three sources are allowed in the dependency graph:

1. **Standard library** (stdlib) packages — except those in the denylist
2. **This module** — `github.com/agentculture/neurosymbolic-system/`
3. **Allowlisted third-party packages** — must have `// allow: <reason>` in go.mod

Any third-party package not in the allowlist will be rejected by the test.

## Denylist

The following standard packages are forbidden:

- **`os/exec`** — prevents subprocess spawning (keep the engine single-threaded)
- **`plugin`** — prevents dynamic code loading
- **`database/sql`** — no SQL or persistent storage in the engine
- **`net/rpc`, `net/rpc/jsonrpc`** — no RPC machinery
- **`syscall/js`** — WebAssembly only; not portable
- **`runtime/cgo`** — no C bindings (CGO_ENABLED=0)
- **`image/*`** — no image processing in the core loop
- **`debug/*`** — no debugging hooks in production

## Running the Guard

Run the allowlist test directly:

```bash
go test ./internal/allowlist/
```

Or as part of the full suite:

```bash
go test ./...
```

The test fails with a clear hint if an unapproved package appears:

```text
unapproved third-party package in graph: example.com/pkg
hint: add `// allow: <argument>` next to the require line in go.mod or
      remove the dependency
```

## Adding an Allowed Package

If you must add a third-party dependency, edit `go.mod` and add the
`// allow:` comment next to the `require` line:

```go
require (
    example.com/pkg v1.2.3 // allow: temporal compatibility with legacy API
)
```

The comment should explain *why* the dependency is necessary — this forces
intentionality and documents the decision for reviewers.
