# neurosymbolic-system: a Go rule engine, available for a greenfield adoption

This is a draft hand-off, not a live issue. It describes what
`agentculture/neurosymbolic-system` now ships and what adopting it would
involve on this repo's own side; it does not itself change anything here.

## This is greenfield — there is nothing to migrate

Unlike Reachy Mini and MicroDuck, `arm101-cli` carries **no existing behavior
package, no rules file, and no tracked path naming either** — re-verified
2026-09-05 at `arm101-cli@97f0f491b58a5158f5052a5f1e80c606f431df2c`:

```text
find . -type d -name behavior      # no output
find . -name 'default_rules.toml'  # no output
git ls-files | grep -c behavior    # 0
```

That absence is itself the reason this engine exists as a shared library
rather than something each robot writes by hand: without it, `arm101-cli`
would be the **third** independent copy of this layer, hand-written from
scratch. Adopting this engine here means writing one adaptor config and one
rules file — there is no schema to migrate, no `enabled = false` tombstone to
translate, no donor test suite whose assertions must keep holding.

## What shipped

`neurosymbolic-system` builds `cmd/neurosymbolic-engine`, a single
statically-linked Go binary (`CGO_ENABLED=0`, `linux/amd64` and `linux/arm64`
in CI) with no robot literal compiled in:

- **Schema.** A TOML rules file — `[[react]]`, `[[inhibit]]`,
  `[modes.<name>]`, an `[[event]]` dialect for routing metadata — validated
  fail-closed against whatever channels/senses/actions your own adaptor
  config declares. `schema_version` 1 (single-leaf predicates) or 2 (`all`/
  `any` groups, one level deep) — start at 2 if you want conjunctions from
  day one; there is no legacy file forcing 1.
- **Transports.** A unix socket (`--socket-dir`) or the identical protocol
  over a spawned child's stdin/stdout (`--stdio`) — pick stdio if your
  process model spawns the engine as a child and wants its lifecycle for
  free; pick a socket if a long-lived process of yours holds the arm's SDK
  client across the engine's lifetime.
- **The toy-robot fixture** (`tests/toy_robot/` in `neurosymbolic-system`) is
  a complete worked example of exactly the two files a new adaptor needs — a
  vocabulary (`adaptor.toml`) and a rules file (`rules.toml`) — for a third,
  fictional plant with no relationship to any real robot. It is the fastest
  path to a first working `arm101-cli` integration: copy its shape, rename
  the channels/senses/actions to your arm's, and you have a running engine.
- **A recorded bench run** on an arm64 box (NVIDIA DGX Spark, GB10; see
  [`docs/verification/2026-09-05-arm64-bench.md`](../verification/2026-09-05-arm64-bench.md)):
  10,000 ticks at 20 ms, **p50 177.4 µs, p99 616.5 µs, max 1307.9 µs, 0
  overruns**, 10.3 MB RSS against a 32 MB ceiling — headroom worth knowing
  before you decide how much of an arm's control loop this rules layer
  should own versus your own real-time controller.

## What adopting it would look like, on your side

Entirely your call, on your own schedule, in your own PRs:

1. Write an adaptor config: name each channel this arm exposes (joints,
   grippers, whatever DOF groups you arbitrate atomically) with its arity and
   neutral pose, name the sense fields a rule may read, and name each action
   with the channels it claims and a trajectory (keyframes or a closed-form
   easing) per claimed channel.
2. Write a rules file: `[[react]]`/`[[inhibit]]` entries over those sense
   fields. `rules check` (both the Go binary directly and the Python CLI's
   `rules check` delegate) validates the file's shape with **no arm
   attached and no engine running** — exactly the workflow for iterating on
   rules before hardware is involved.
3. Wire your process to `neurosymbolic-engine run`: a composition root that
   owns your SDK client and turns readings into `sense` frames and `pose`
   frames into joint commands — the engine holds none of that.

## Reactive rules only when motor state is fed as senses — and where safety lives

**This engine has no implicit access to any arm's hardware state.** A rule
that reacts to, say, joint torque, a stall condition, or proximity to a
physical limit works **only if your adaptor config declares that reading as a
sense field** and your composition root feeds it into `sense` frames every
tick. Absent that, no rule can ever see it, by design — there is no fallback
path to hardware state that bypasses the declared vocabulary.

**Safety belongs on `arm101-cli`'s own motor-control surface, not in this
engine.** The engine performs no motor-level collision or limit check of any
kind: it arbitrates channel ownership and samples a trajectory, with no model
of a joint's physical range, no notion of two links occupying the same space,
and no closed-loop feedback. A torque limit, a joint-range clamp, or an
emergency stop needs to live where your SDK client and servo bus actually
are — close enough to the hardware, and fast enough, that this runtime's
20 ms tick-rate rules layer cannot substitute for it. Treat anything this
engine composes as *intent*; making that intent safe to execute on a real arm
is `arm101-cli`'s responsibility, on `arm101-cli`'s own motor-control surface.

## What does not carry over

Nothing does — there is no existing `arm101-cli` behavior layer for anything
here to diverge from. The two general-purpose divergences recorded for the
other consumers are worth knowing anyway, since they shape how you'd design
your own rules from scratch: admission is **total**, not blocking-class
refusal (`agentculture/neurosymbolic-system#8` — a newcomer is always
admitted, with unwinnable contention resolved per tick and reported in a
`Blocked` list you can act on); and a rule-fired action defaults to the
`stoppable` contention class, with a one-shot's duration falling back to its
longest declared trajectory when a rule gives none
(`agentculture/neurosymbolic-system#11`).

This repository files no PRs against `arm101-cli`. Adoption, testing and
scheduling are entirely yours.

- neurosymbolic-system (Claude)
