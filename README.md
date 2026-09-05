# neurosymbolic-system

The runtime that lets agents control robots. Senses, rules, arbitration and
motion composed onto **one 50 Hz tick** — a Go engine binary with no robot
literal compiled into it, imported by robot CLIs such as `reachy-mini-cli` and
`microduck-cli` rather than re-implemented by each one.

## What it is

`neurosymbolic-system` ships two surfaces. The **Go engine binary**
(`cmd/neurosymbolic-engine`) is the runtime itself: it holds a robot's
vocabulary (channels, senses, actions), arbitrates ownership of those channels
tick by tick, evaluates a data-only rules file against a live sense snapshot,
composes a complete pose, and streams it out over a small wire protocol — with
zero knowledge of what a "Reachy" or a "MicroDuck" is. The **Python CLI**
(`neurosymbolic-system` / `neurosymbolic_system`) is management and
introspection: identity, doctoring, and thin delegates (`engine`, `rules`)
that shell out to the built binary. Robot operation — spawning the engine,
turning its poses into servo commands, turning readings into sense frames —
lives in the consumer CLI, never here.

## Build and install

```bash
CGO_ENABLED=0 go build \
  -ldflags "-X main.version=$(git describe --tags --always) -X main.revision=$(git rev-parse --short HEAD)" \
  -o neurosymbolic-engine ./cmd/neurosymbolic-engine
```

`CGO_ENABLED=0` is load-bearing, not a style choice: it is what makes the
binary statically linked, so it runs unmodified on a bare box or a
from-scratch container (CI asserts this with `file` on both `linux/amd64` and
`linux/arm64`). The two `-ldflags` stamp `version`/`revision`, which
`neurosymbolic-engine version` reports back — with no ldflags at all it
reports `0.0.0-dev (unknown)`.

The Python CLI locates the binary via the `NEUROSYMBOLIC_ENGINE` environment
variable (if it names an executable file) or `neurosymbolic-engine` on `PATH`
otherwise (`neurosymbolic_system/engine_client.py`). Neither is required for
the identity/agent verbs (`whoami`, `learn`, `doctor`, …); only `engine`/
`rules` verbs need a located binary.

```bash
uv sync
uv run neurosymbolic-system whoami      # identity from culture.yaml — no engine needed
export NEUROSYMBOLIC_ENGINE=$(pwd)/neurosymbolic-engine
uv run neurosymbolic-system engine version
uv run neurosymbolic-system rules check tests/toy_robot/rules.toml
```

## Adaptor guide

This section is the whole integration surface a maintainer needs to bring up
a new robot — the worked example throughout is
[`tests/toy_robot/adaptor.toml`](tests/toy_robot/adaptor.toml), a third,
fictional plant used precisely so nothing here can be a Reachy Mini or
MicroDuck special case.

### Channels

A channel is a group of degrees of freedom claimed and resolved atomically —
`wheels`, `beacon`, `arm` on the toy robot; `head`, `antennas`, `body_yaw` on
Reachy Mini. Each channel declares:

- `name` — unique within the config.
- `arity` — how many numbers one sample carries (`wheels` is 2, `beacon` is 1,
  `arm` is 3 in the fixture — deliberately three different arities).
- `neutral` — exactly `arity` finite numbers: the value an unclaimed channel
  falls to, so a composed pose is always **complete**, never partial.

### Senses

A sense is one field of the per-tick snapshot a rule predicate may read:

- `name` — unique within the config.
- `type` — one of `float`, `bool`, `string`, `vec3`.
- `age_field` (optional) — names another declared **float** sense carrying
  this reading's freshness in seconds. The fixture's `tag` sense links
  `tag_age_s`: freshness is a first-class declared field, not something
  computed at call time, so a one-shot admitted mid-tick and the rule
  predicate that admitted it agree about how old the reading was.

A field a rule keys on that the adaptor config does not declare is a load-time
refusal (`internal/adaptor`'s `CheckReferences`), not a rule that silently
never fires.

### Actions

An action is an opaque name the engine never interprets — that opacity is
what lets the same binary run `orient-to-sound` on one robot and `waddle` on
another:

- `claims` — the channels this action claims; at least one, no duplicates.
- `loops` — whether the action repeats (a base layer like the fixture's `hum`)
  or runs once (`retreat`, `wave`).
- `params` — named knobs, each with an inclusive `[min, max]` domain. An
  out-of-domain or unknown param is **refused, never clamped**
  (`Vocabulary.ValidateParams`).
- `trajectories` — one entry per claimed channel, no more, no fewer: a claim
  with no trajectory, or a trajectory for an unclaimed channel, is refused at
  load.

A trajectory is exactly one of:

- **keyframes** — a list of `{ t, value }` points, linearly interpolated
  between them. The first keyframe must be at `t = 0`; times must be
  non-decreasing (two frames at the same `t` are a deliberate step). Past the
  last keyframe, the trajectory **holds** that last value rather than
  snapping to neutral, so an action that outlives its trajectory settles.
- **easing** — `{ kind, from, to, duration_s }`, one closed-form move from
  `from` to `to` over `duration_s` seconds. `kind` is `linear`, `ease_in_out`
  (a cosine ease — zero velocity at both endpoints, so a servo neither snaps
  nor overshoots), or `hold` (a step: `from` for the whole duration, `to` once
  it elapses — the honest shape for a discrete channel like a canned skill or
  an audio cue, where an interpolated in-between value would be meaningless).

An action declared `loops = true` wraps its trajectory by its duration; a
one-shot holds its last value once elapsed.

### A complete minimal adaptor

`tests/toy_robot/adaptor.toml`, quoted in full — three channels of three
different arities, four senses covering three of the four declared types, and
three actions covering both trajectory shapes plus the looping/one-shot
split:

```toml
[[channels]]
  name = "wheels"
  arity = 2
  neutral = [0.0, 0.0]

[[channels]]
  name = "beacon"
  arity = 1
  neutral = [0.0]

[[channels]]
  name = "arm"
  arity = 3
  neutral = [0.0, 0.0, 0.0]

[[senses]]
  name = "bumper"
  type = "bool"

[[senses]]
  name = "light_level"
  type = "float"

[[senses]]
  name = "charge"
  type = "float"

[[senses]]
  name = "tag"
  type = "string"
  age_field = "tag_age_s"

[[senses]]
  name = "tag_age_s"
  type = "float"

# A decision provider's output field (see "The decision provider" below) —
# declared here like any other sense, since a provider never touches this
# package and a rule keys on its output exactly like a reading off a sensor.
[[senses]]
  name = "mood"
  type = "string"

# hum — the base layer: looping, claims only the beacon, admitted passive so
# it owns the beacon whenever nothing else claims it.
[[actions]]
  name = "hum"
  claims = ["beacon"]
  loops = true

  [[actions.params]]
    name = "brightness"
    min = 0.0
    max = 1.0

  [actions.trajectories]
    [actions.trajectories.beacon]
      [[actions.trajectories.beacon.keyframes]]
        t = 0.0
        value = [0.0]
      [[actions.trajectories.beacon.keyframes]]
        t = 0.5
        value = [1.0]
      [[actions.trajectories.beacon.keyframes]]
        t = 1.0
        value = [0.0]

# retreat — a one-shot claiming TWO channels at once, eased on both.
[[actions]]
  name = "retreat"
  claims = ["wheels", "arm"]
  loops = false

  [[actions.params]]
    name = "speed"
    min = 0.0
    max = 2.0

  [actions.trajectories]
    [actions.trajectories.wheels]
      [actions.trajectories.wheels.easing]
        kind = "ease_in_out"
        from = [0.0, 0.0]
        to = [-1.0, -1.0]
        duration_s = 0.5

    [actions.trajectories.arm]
      [actions.trajectories.arm.easing]
        kind = "linear"
        from = [0.0, 0.0, 0.0]
        to = [0.0, -10.0, 5.0]
        duration_s = 0.5

# wave — a one-shot on the arm alone, keyframed out and back.
[[actions]]
  name = "wave"
  claims = ["arm"]
  loops = false

  [[actions.params]]
    name = "amplitude"
    min = 0.0
    max = 30.0

  [actions.trajectories]
    [actions.trajectories.arm]
      [[actions.trajectories.arm.keyframes]]
        t = 0.0
        value = [0.0, 0.0, 0.0]
      [[actions.trajectories.arm.keyframes]]
        t = 0.2
        value = [15.0, 0.0, 0.0]
      [[actions.trajectories.arm.keyframes]]
        t = 0.4
        value = [0.0, 0.0, 0.0]
```

Both `.json` and `.toml` adaptor configs are accepted (`LoadJSON`/`LoadTOML`
share every validation rule; only the decoder differs).

### The rules file

A rules file is TOML, evaluated against the live sense snapshot, with four
kinds of content:

- `schema_version` — required, `1` or `2`. **v1** predicates are a single leaf
  `{ field, op, value }` — one signal per rule. **v2** additionally allows a
  group, `{ all = [...] }` or `{ any = [...] }`, nesting one level deep (an
  `all` inside an `any` or vice versa; deeper nesting is refused — a rule
  nobody can read at a glance is a rule an operator cannot safely override). A
  v1 file using `all`/`any` is refused naming the version, never
  silently reinterpreted.
- `[[react]]` — when `when` holds, run the named `run` action, optionally with
  `params` overriding its declared knobs. `cooldown_s` is the minimum seconds
  between two firings (default `5.0`); `hysteresis` requires the predicate to
  hold continuously false for that long before it may re-arm (default `0.0`);
  `duration_s` bounds the admitted behavior's lifetime (a looping action with
  no bound of its own is refused unless the rule supplies one); `say` is
  optional text, capped at 500 characters — refused over the limit, never
  truncated.
- `[[inhibit]]` — when `when` holds, `disable` names a set of actions,
  refusing their admission from every source (a rule fire, an injected
  intent) and evicting one already running.
- `[modes.<name>]` — a flat, purely declarative parameter bag, selected by a
  top-level `active_mode` key. A mode key an action does not declare is
  ignored, not refused — a mode is a broad tuning applied across many
  actions, and one irrelevant key must not break every one of them.

Predicate ops: `lt`, `gt`, `ge`, `le` (ordered, finite non-negative numeric),
`eq`, `ne` (equality, any scalar), `is_true`, `is_false` (boolean, no value),
and `absent_for` (a duration op — "this field has been continuously absent
for at least N seconds", measured from engine start so a field never seen
does not fire on tick one). A transport states absence explicitly by sending
a field as `null`; it is never inferred from a frame's silence.

`tests/toy_robot/rules.toml`, quoted in full, exercises every one of these:

```toml
schema_version = 2
active_mode = "gentle"

[[react]]
id = "bump-retreat"
when = { all = [
  { field = "bumper", op = "is_true" },
  { field = "light_level", op = "lt", value = 0.5 },
] }
run = "retreat"
duration_s = 0.5
cooldown_s = 0.2
params = { speed = 1.0 }

[[react]]
id = "lost-tag-wave"
when = { field = "tag", op = "absent_for", value = 0.3 }
run = "wave"
duration_s = 0.4
cooldown_s = 0.2

[[inhibit]]
id = "dark-inhibit"
when = { field = "light_level", op = "lt", value = 0.05 }
disable = ["wave"]

[modes.gentle]
speed = 0.4
amplitude = 8.0

[modes.brisk]
speed = 1.8
amplitude = 25.0
```

Every rule's `id` is a public interface: it is what an overlay layer
overrides or tombstones. A rules file entry carrying `enabled = false` and
nothing else is a **tombstone** — it contributes no rule of its own and
disables the rule of that id contributed by a lower layer, so disabling a
shipped rule is a one-line overlay edit, never a fork of its body. Layers
merge **per rule id**: `--rules shipped.toml --rules local.toml` means the
box-local overlay overrides the shipped defaults id by id, which is the only
arrangement where an operator's tuning survives an upgrade and newly shipped
rules still reach a deployed box.

#### `rules check` and `rules migrate`, on both sides

`rules check` validates a rules file's **shape** with no robot attached and
no engine running — an empty `--adaptor` (or none at all) skips only the
robot-specific name checks (does this field/action exist), never the schema
validation itself:

```bash
# Go side, no robot attached, no engine running:
neurosymbolic-engine rules check tests/toy_robot/rules.toml
# -> rules: 2 react, 1 inhibit, 0 event entries, schema_version 2

# with a vocabulary attached, for the field/action name checks too:
neurosymbolic-engine rules check tests/toy_robot/rules.toml --adaptor tests/toy_robot/adaptor.toml

# Python side — the identical delegate, same output:
NEUROSYMBOLIC_ENGINE=./neurosymbolic-engine \
  neurosymbolic-system rules check tests/toy_robot/rules.toml --adaptor tests/toy_robot/adaptor.toml
```

`rules migrate <file> [--out PATH] [--force]` writes a `schema_version = 2`
twin of a v1 file (same predicates, re-expressed in the v2 shape) so an
operator can move a file forward without hand-editing it. `rules list
<file>...` prints every rule's id, kind and predicate; `rules reload
<file>...` asks a **live** engine (over its socket, via the management
protocol) to re-read its rules files — the only one of the four verbs that
needs a running engine.

### Running

```bash
neurosymbolic-engine run \
  --adaptor tests/toy_robot/adaptor.toml \
  --rules tests/toy_robot/rules.toml \
  --socket-dir /tmp/toy-robot \
  --period 20ms \
  --base-action hum
```

Flags:

| Flag | Meaning |
|---|---|
| `--adaptor <path>` | the robot's adaptor config (`.json` or `.toml`); required |
| `--rules <path>` | a rules file; repeatable, each occurrence is one layer, later overrides earlier per rule id |
| `--provider <path>` | a decision-provider config; repeatable, one provider per occurrence |
| `--socket-dir <dir>` | serve on `<dir>/engine.sock`, mode `0600` |
| `--stdio` | serve the same protocol over stdin/stdout instead |
| `--insecure-tcp <addr>` | serve over TCP instead — named "insecure" deliberately; see below |
| `--period <duration>` | tick period, default `20ms` (50 Hz) |
| `--heartbeat <duration>` | heartbeat interval, default `1s`; negative disables it |
| `--base-action <name>` | seed this action as the passive base layer, so an idle robot keeps moving |

Exactly one of `--socket-dir`, `--stdio`, `--insecure-tcp` must be given; zero
or two is a refusal, never a silently invented default — a socket nobody
chose is a socket nobody owns.

#### The two transports, and when each fits

- **`--socket-dir`** — a unix socket, `0600`, inside a consumer-provided
  directory. Fits a long-lived consumer **process**: the consumer holds the
  hardware and the media session across the engine's lifetime and connects to
  it like any other local service.
- **`--stdio`** — the identical protocol over the child's stdin/stdout. Fits a
  **spawned child**: the consumer gets process lifecycle for free (closing
  stdin, or the child exiting, is directly observable) without a socket path
  to clean up. `tests/toy_robot/client.py` and `stdio_client.py` are both
  worked examples.
- **`--insecure-tcp <addr>`** is refused unless explicitly chosen — a TCP
  listener lets anything that can route to the robot admit intents, while a
  socket file at least inherits the filesystem's answer to "who is allowed."
  There is deliberately no plain `--tcp` flag: the insecurity is named in the
  only spelling that exists, not left to a footnote.

#### The wire protocol

Every frame is a 4-byte big-endian length prefix followed by that many bytes
of a JSON object carrying `"v"` (protocol version, currently `1`) and
`"kind"`. The first frame a client sends must be `hello`.

| Kind | Direction | Carries |
|---|---|---|
| `hello` | in → out | client name in; `engine_version` back |
| `sense` | in | a `fields` map; a `null` value **clears** a field (explicit absence) |
| `intent` | in | `admit`/`evict` — the same admission path a rule fire uses |
| `mgmt` | in → out | a one-shot request (`status`, `rules check`, …) correlated by `id`; answered by `mgmt_result` |
| `pose` | out | this tick's complete `channels` map |
| `event` | out | a named event with optional `data` |
| `heartbeat` | out | liveness, at `--heartbeat`'s interval |
| `end` | out | the engine is stopping, with a `reason` |
| `error` | out | a structured refusal: `code`, `message`, `remediation` — `code` mirrors the CLI contract (`1` user error, `2` environment error) |

Outbound telemetry (`pose`, `event`, `heartbeat`) is enqueued on a **bounded**
queue (default depth 8) and dropped, newest-first, with a named senselog line
when full — a slow consumer must cost the robot nothing, so a socket write
never blocks the tick thread and never returns an error to it. Control frames
(`hello`, `mgmt_result`, `error`, `end`) take a direct, blocking write instead,
because a dropped refusal or a dropped end frame leaves a peer waiting for
something that never arrives. A frame whose `"v"` differs from the engine's is
refused naming both versions, and the connection is closed.

#### What the consumer must do on disconnect

The engine's own tick loop keeps running until it is told to stop (a signal,
or — over `--stdio` — its stdin closing, which stops the tick loop and emits
the `[SENSE stage=compose source=run event=stopped]` line; *unverified*
whether the process itself also exits on stdin closing alone — the
conformance record notes that today it does not, and a supervising consumer
sends `SIGTERM`, which does exit cleanly). A consumer that loses its
connection (socket closed, an `end` frame received, or two heartbeat
intervals pass with no beat — the watchdog case, for a hard-killed engine
that sends no `end`) must settle its robot to neutral itself: the engine
holds no hardware, so it cannot do this for you. `tests/toy_robot/client.py`'s
`ToyRobot.settle()` is the reference shape — a single idempotent hook run at
most once, from either the `end` frame or the heartbeat watchdog.

### The decision provider

A provider is a small-model or embeddings client — warmed once at startup,
called from a background worker behind a bounded queue, never on the tick
thread — whose output is written back into the engine's live sense snapshot
as an ordinary field. The rules layer never learns a provider exists: it sees
a sense field turn a value, exactly like a reading off a sensor. **An absent
provider is an absent sense field** — no provider configured, a full queue, an
HTTP timeout or error, a malformed response, or a warm-up failure are all
abstentions (nothing written, a rule bound to the field simply never sees it
fire), each one a single named senselog drop line
(`queue-full`, `timeout`, `http-<status>`, `malformed`, `unconfigured`) rather
than a crash or a stall.

Config fields (`--provider <path>`, TOML or JSON): `name`, `kind`
(`embedding` or `completion`), `base_url`, `model`, `api_key_env` (an
environment variable name, resolved once at startup, never on the tick
thread), `inputs` (sense fields rendered into the request), `output` (the
sense field written on success — must be separately declared as a sense in
the adaptor config, or the provider would run and nothing could ever react to
it), `labels` (required for `embedding` — the closed set it classifies
against), `timeout_s`, `queue_depth`, `cadence` (enqueue every N ticks),
`system_prompt` and `max_tokens` (for `completion`). The rule that predicates
on the output field fires on a **later** tick than the one that triggered the
request, never the same tick — the write races the tick loop and can only
land after it.

### Observability

- **The SENSE stderr grammar** — every stage of the pipeline (capture, gate,
  drop, provider, compose, …) writes one grep-able, parseable line to
  **stderr only**:

  ```text
  [SENSE stage=<stage> source=<source> event=<event>] <detail>
  ```

  A drop always names its reason in `<detail>` (e.g.
  `dropped reason=self-mute`). Suppressions are logged per **episode**, not
  per tick — one entry line, one line per reason change, one summary line
  when the streak ends — which is what keeps a chattering rule from writing
  thousands of near-identical lines into a log.
- **`event` frames** carry routing metadata (priority, urgency, a voice hint)
  that a consumer's own event router interprets — an event never feeds
  arbitration.
- **`status`** (`neurosymbolic-engine status`, over a live engine's mgmt
  channel) reports cumulative counters only — ticks, overruns, drops,
  sink errors, seam panics, frames in/out, stream drops, refusals — plus the
  last tick's `active` behaviors and `ownership`, the number of rule layers in
  force, the active mode, and per-provider request/result/drop counters.
  Nothing is computed on demand or sampled off the tick, so asking the
  question never spends the robot's 20 ms budget.
- **`bench`** (`neurosymbolic-engine bench [--json]`) runs a synthetic tick
  load (200 rules, 20 fields, 10,000 ticks, 20 ms period by default) and
  reports p50/p99/max tick latency, overrun count and peak RSS against a 32 MB
  ceiling:

  ```bash
  $ neurosymbolic-engine bench --json
  {"ticks":10000,"period":"20ms","p50_us":186.145,"p99_us":609.541,"max_us":1792.829,"overruns":0,"rss_mb":12.18,"rss_ceiling_mb":32,"ok":true}
  ```

  See
  [`docs/verification/2026-09-05-arm64-bench.md`](docs/verification/2026-09-05-arm64-bench.md)
  for a recorded run on a named arm64 box, with roughly 32x headroom under the
  20 ms budget and zero overruns across 10,000 ticks.

## Safety boundary

**The engine performs no motor-level collision or limit check.** It arbitrates
channel ownership and samples a declared trajectory; it has no model of a
physical joint's range of motion, no notion of two links occupying the same
space, and no closed-loop feedback from the plant. Motor-state rules (a react
or inhibit keyed on, say, a joint angle or a stall current) work **only** when
the adaptor declares those readings as senses — the engine has no implicit
access to hardware state — and even then, a rule is evaluated once per 20 ms
tick over declared fields, not a hard real-time control loop. **The fast path
for motor safety is the consumer's motor-control surface**: the robot CLI that
holds the actual SDK client and the servo bus is the only place that can
enforce a torque limit, a joint range, or an emergency stop fast enough and
close enough to the hardware to matter. This runtime composes *intent*; the
consumer is responsible for making that intent safe to execute.

## Who changes what

**Only this repository is touched by this repository's own work.** Consumers
(`reachy-mini-cli`, `microduck-cli`, and eventually `arm101-cli`) pull this
engine on their own side, in their own PRs, on their own schedule — this repo
files draft issues (see [`docs/handoff/`](docs/handoff/)) describing what
shipped and what adopting it would look like, and stops there. It opens no PR
and commits no code in any sibling repository. The evidence a consumer needs
to evaluate the swap — conformance fixtures replaying cases derived from each
donor's own test suite, and a recorded bench run — lives in
[`docs/verification/`](docs/verification/):

- [`2026-09-05-donor-conformance.md`](docs/verification/2026-09-05-donor-conformance.md)
  — what was replayed, against which donor test, and what is explicitly *not*
  reproduced (the donors' STOPPING-class admission path, abstention as a
  purely data-driven fixture, any ported trajectory/easing/motion primitive).
- [`2026-09-05-arm64-bench.md`](docs/verification/2026-09-05-arm64-bench.md) —
  one recorded `bench` run's numbers, box and commit.
- [`docs/go-dependencies.md`](docs/go-dependencies.md) — the import allowlist
  policy that keeps the engine free of robot SDKs, transports and subprocess
  machinery.

## Design in one page

- **One tick, one owner per channel.** Each tick arbitrates a single owner per
  channel, asks each owner for its contribution once, and composes a
  *complete* pose — unclaimed channels fall to neutral, so the target is
  never partial.
- **Four contention classes** — `passive`, `stoppable`, `unstoppable`,
  `stopping` — resolved by `(class priority, recency)` in two pure functions,
  no I/O, no clock. Arbitration is abstention-aware: a behavior with nothing
  to say this tick yields the channel rather than freezing it.
- **One seam.** Everything besides arbitration and composition — rules, the
  status rider, the stream's own heartbeat — rides one per-tick fan-out that
  isolates a panicking rider (it loses its turn, named; the others still run)
  and never lets a consumer import the engine's internals.
- **Senses are peek reads**, never a consuming pull, and a rule's snapshot is
  read ungated — an admitted mid-tick one-shot and the rule predicate that
  admitted it see the same values.
- **Rules are data, never code** — react/inhibit/modes, loaded as layers
  merged per rule id, so operator tuning survives an upgrade and newly
  shipped rules still reach a deployed box.
- **Drop, don't block.** The 20 ms budget is the product: outbound telemetry
  is bounded and dropped, never blocking; every drop names its reason on a
  grep-able stderr line.
- **Validate fail-closed.** An out-of-range param, an unknown field, a
  malformed trajectory or a runaway duration is refused, never clamped.

## Consumers

| Repo | Relationship |
|---|---|
| [`reachy-mini-cli`](https://github.com/agentculture/reachy-mini-cli) | donor and first consumer — `behavior engine run` becomes a thin composition root over this engine |
| [`microduck-cli`](https://github.com/agentculture/microduck-cli) | second robot, different plant, same tick |
| `arm101-cli` | greenfield third consumer — no existing rules layer to migrate |
| [`reachy_nova`](https://github.com/agentculture/reachy_nova) | reference only — an independent ~50 Hz loop several of these patterns were invented in |

## CLI (Python side)

| Verb | What it does |
|------|--------------|
| `whoami` | Report this agent's nick, version, backend, and model from `culture.yaml`. |
| `learn` | Print a structured self-teaching prompt. |
| `explain <path>` | Markdown docs for any noun/verb path. |
| `overview` | Read-only descriptive snapshot of the agent. |
| `doctor` | Check the agent-identity invariants plus the engine's protocol version, if a binary is found. |
| `cli overview` | Describe the CLI surface itself. |
| `engine {overview,status,version,doctor}` | Thin delegate to the built `neurosymbolic-engine` binary. |
| `rules {overview,check,list,migrate,reload}` | Thin delegate to the engine's rules verbs. |

Every command supports `--json`. Results go to stdout, errors/diagnostics to
stderr (never mixed). Exit codes: `0` success, `1` user error, `2` environment
error, `3+` reserved.

## Contributing

See [`CLAUDE.md`](CLAUDE.md) for the full conventions: version-bump-every-PR,
the `cicd` PR lane, the worktree layout, the Go dependency policy, and the
rules for reading a sibling repo without writing to it.

## License

Apache 2.0 — see [`LICENSE`](LICENSE).
