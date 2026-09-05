# neurosymbolic-system

The runtime that lets agents control robots. Senses, rules, arbitration and
motion composed onto **one 50 Hz tick** — extracted from
[reachy-mini-cli](https://github.com/agentculture/reachy-mini-cli) and imported
as a library by robot CLIs such as `reachy-mini-cli` and `microduck-cli`.

The robot CLI owns the hardware — the SDK client, the media session, the process
supervision. This package owns the loop: who gets the head this tick, what the
rules say about the sense snapshot, and how a behavior's contribution composes
into a complete pose.

## Status

**Day zero.** The runtime has not been extracted yet. What ships today is the
agent baseline — the CLI, the mesh identity, and CI — plus the design brief in
[`CLAUDE.md`](CLAUDE.md) that describes the donor architecture and the seams the
extraction has to cut. Track progress there: each section moves out of "being
extracted" as it lands on disk.

## What's here today

- **An agent-first CLI** cited from [teken](https://github.com/agentculture/teken)
  (`afi-cli`) — the runtime package has **no third-party dependencies**, and
  keeping it that way is a design rule (a library two robot CLIs import must
  install on a bare box).
- **A mesh identity** — `culture.yaml` (`suffix` + `backend`) and the matching
  resident prompt file (`AGENTS.colleague.md`, since this agent runs
  `backend: colleague`).
- **The canonical guildmaster skill kit** under `.claude/skills/`, vendored
  cite-don't-import. See [`docs/skill-sources.md`](docs/skill-sources.md).
- **A build + deploy baseline** — pytest, lint, the agent-first rubric gate, and
  PyPI Trusted Publishing wired into GitHub Actions.

## Go engine

`cmd/neurosymbolic-engine` is a stdlib-only Go module scaffold (t1) — a
`version` verb today, later tasks add the tick engine. Build with a stamped
version/revision:

```bash
CGO_ENABLED=0 go build \
  -ldflags "-X main.version=$(git describe --tags --always) -X main.revision=$(git rev-parse --short HEAD)" \
  -o neurosymbolic-engine ./cmd/neurosymbolic-engine
```

CI (`.github/workflows/go.yml`) builds and tests `linux/amd64` and
`linux/arm64` with `CGO_ENABLED=0` and asserts the artifact is statically
linked.

## Quickstart

```bash
uv sync
uv run pytest -n auto                   # run the test suite
uv run neurosymbolic-system whoami      # identity from culture.yaml
uv run neurosymbolic-system learn       # self-teaching prompt (add --json)
uv run teken cli doctor . --strict      # the agent-first rubric gate CI runs
```

## CLI

| Verb | What it does |
|------|--------------|
| `whoami` | Report this agent's nick, version, backend, and model from `culture.yaml`. |
| `learn` | Print a structured self-teaching prompt. |
| `explain <path>` | Markdown docs for any noun/verb path. |
| `overview` | Read-only descriptive snapshot of the agent. |
| `doctor` | Check the agent-identity invariants (prompt-file-present, backend-consistency). |
| `cli overview` | Describe the CLI surface itself. |

Every command supports `--json`. Results go to stdout, errors/diagnostics to
stderr (never mixed). Exit codes: `0` success, `1` user error, `2` environment
error, `3+` reserved.

## Design in one page

The pieces below come from the donor and are what the extraction is cutting a
seam around. The long version, with the hardware measurements behind each
number, is in [`CLAUDE.md`](CLAUDE.md).

- **One tick, one owner per channel.** Each tick drops expired behaviors,
  arbitrates a single owner per channel, asks each owner for its contribution
  once, and composes a *complete* pose — unclaimed channels fall to neutral, so
  the target is never partial.
- **Four contention classes** — `passive`, `stoppable`, `unstoppable`,
  `stopping` — resolved by `(class priority, recency)`, in two pure functions
  with no I/O and no clock. Arbitration is abstention-aware: a behavior with
  nothing to say this tick yields the channel rather than freezing it.
- **One seam.** Everything else — rules, agent intents, sense drivers, export
  feeds, metrics — rides a single per-tick `tick_seam` callable as a pure
  consumer. The engine never imports them; they never import its internals.
- **Senses are injected peek callables**, never imports, and every one degrades
  to `None` instead of raising.
- **Rules are data, never code** — react / inhibit / modes, loaded as a shipped
  layer plus a box-local overlay that overrides per rule id, so operator tuning
  survives an upgrade and new shipped rules still reach a deployed box.
- **Drop, don't block.** The 20 ms budget is the product: work that can block
  goes to a worker, and every drop names its reason on a grep-able
  `[SENSE stage=… source=… event=…]` line.
- **Validate fail-closed.** An out-of-range axis or a runaway duration is
  refused, never clamped.

## Consumers

| Repo | Relationship |
|---|---|
| [`reachy-mini-cli`](https://github.com/agentculture/reachy-mini-cli) | donor and first consumer — its `behavior engine run` becomes a thin composition root over this library |
| [`microduck-cli`](https://github.com/agentculture/microduck-cli) | second robot, different plant, same tick |
| [`reachy_nova`](https://github.com/agentculture/reachy_nova) | reference only — an independent ~50 Hz loop several of these patterns were invented in |

## Contributing

See [`CLAUDE.md`](CLAUDE.md) for the full conventions: version-bump-every-PR, the
`cicd` PR lane, the worktree layout, and the rules for porting a module out of
the donor.

## License

Apache 2.0 — see [`LICENSE`](LICENSE).
