// Package compose is the engine's composition root: the one place that knows
// how every other package is wired together, and the only place that reads the
// process's argv, signals and filesystem to do it.
//
// `neurosymbolic-engine run` is its whole surface. It turns
//
//	--adaptor <path> --rules <path>... --socket-dir <dir> --period 20ms
//
// into a live 50 Hz loop with a rules-driven pose stream on a socket, and that
// is deliberately the ENTIRE story: acceptance criterion h16/h4 is that a fresh
// consumer goes from zero to a running rules-driven pose stream using only the
// built binary, an adaptor config and a TOML rules file, with no engine code
// written at all. If a consumer has to write Go to start a robot, this package
// has failed regardless of what it compiles into.
//
// # The wiring, and why it is one direction
//
//	adaptor.Vocabulary   the plant: channels, senses, actions. Nothing else in
//	                     the process spells a robot name.
//	        |
//	sense.Snapshot       fed by the stream's reader goroutine, read by the tick
//	                     goroutine. Peek semantics: a read never consumes.
//	        |
//	ruleeval.Evaluator   rules over that snapshot, admitting through the ONE
//	  + ruleeval.Registry admission registry.
//	        |
//	ruleeval.Bus         the fault-isolating fan-out of the engine's one seam:
//	                     rules, then the status rider, then the stream's
//	                     heartbeat. A rider that panics loses its turn, named,
//	                     and the others still run.
//	        |
//	tick.Engine          arbitration + composition, streaming to
//	        |            stream.Server as its adaptor.Sink.
//	stream.Server        the wire.
//
// Every arrow points one way. internal/tick imports none of this; the engine
// only ever calls the opaque seam it was handed, which is the structural rule
// the donor learned over a year of features (see CLAUDE.md, "The one seam").
// This package is therefore the only one that may import them all, and nothing
// may import this package back.
//
// # What this package does NOT do
//
// It owns no hardware. `run` streams poses to a consumer over a socket and
// reads senses back; converting a pose into servo commands, and a robot's
// readings into sense frames, is the consumer CLI's problem by definition —
// that is the seam this repository exists to draw. There is no SDK here, no
// transport beyond the endpoint, and no process supervision.
//
// # Streams
//
// stdout carries NOTHING while running (in --stdio mode it carries protocol
// frames and nothing else). Every drop, every stage line and every diagnostic
// is a senselog line on stderr, so a consumer piping the engine's stdout into
// a JSONL reader gets a pure stream.
package compose

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
	"github.com/agentculture/neurosymbolic-system/internal/stream"
)

// Verb is the argv token that reaches this package from main.
const Verb = "run"

// DefaultPeriod is the tick period when --period names none: 20 ms, 50 Hz.
//
// It is a DEFAULT, not an assumption. Nothing downstream is allowed to depend
// on it — a cadence-dependent tuning that only works at one tick rate is a bug
// class of its own, and the library must work at other rates.
const DefaultPeriod = 20 * time.Millisecond

// UsageText is `run`'s usage, rendered as the remediation of any flag refusal.
const UsageText = `run [flags]

  --adaptor <path>       the robot's adaptor config (.json or .toml); required
  --rules <path>         a rules file; repeatable, each occurrence is ONE layer
                         and a later layer overrides an earlier one per rule id
  --provider <path>      a decision-provider config (.toml or .json);
                         repeatable, one provider per occurrence
  --socket-dir <dir>     serve on <dir>/` + stream.DefaultSocketName + ` (0600)
  --stdio                serve the same protocol over stdin/stdout instead
  --insecure-tcp <addr>  serve over TCP instead; see the flag's own refusal
  --period <duration>    tick period (default 20ms)
  --heartbeat <duration> heartbeat interval (default 1s; negative disables)
  --base-action <name>   seed this action as the passive base layer`

// Options is one `run` invocation, already parsed and checked for internal
// consistency but not yet resolved against the filesystem.
type Options struct {
	// AdaptorPath is the robot's adaptor config. Required: without it nothing
	// in the process knows what a channel is.
	AdaptorPath string

	// RuleLayers are the rules files, ONE LAYER PER OCCURRENCE, in the order
	// they were given. That is what makes `--rules shipped.toml --rules
	// local.toml` mean "the box-local overlay overrides the shipped defaults
	// per rule id" — the arrangement where an operator's tuning survives an
	// upgrade AND newly shipped rules reach a deployed box.
	RuleLayers []string

	// ProviderPaths are the decision-provider configs, one provider per
	// occurrence. A provider writes its answer back into the perception
	// snapshot as an ordinary sense field, so a rule predicates on it exactly
	// like any other reading and neither the rules layer nor the engine ever
	// learns a provider exists.
	ProviderPaths []string

	// SocketDir, Stdio and TCPAddr are the three transports, exactly one of
	// which must be chosen.
	SocketDir string
	Stdio     bool
	TCPAddr   string

	Period    time.Duration
	Heartbeat time.Duration

	// BaseAction, when set, is seeded as a PASSIVE looping behavior before the
	// first tick, so an idle robot keeps moving and any channel nothing else
	// claims stays alive. Empty means no base layer.
	BaseAction string
}

// pathListFlag collects a repeatable path flag in the order it was given.
// Order is load-bearing for --rules (each occurrence is one layer, and a later
// layer overrides an earlier one), so a flag that quietly reordered would
// change what a rules stack means.
type pathListFlag struct {
	name  string
	paths *[]string
}

func (f pathListFlag) String() string {
	if f.paths == nil {
		return ""
	}
	return strings.Join(*f.paths, ",")
}

func (f pathListFlag) Set(value string) error {
	if value == "" {
		return fmt.Errorf("--%s was given an empty path", f.name)
	}
	*f.paths = append(*f.paths, value)
	return nil
}

// The flag names, spelled once so a refusal and the usage text cannot drift.
const (
	flagAdaptor     = "adaptor"
	flagRules       = "rules"
	flagProvider    = "provider"
	flagSocketDir   = "socket-dir"
	flagStdio       = "stdio"
	flagInsecureTCP = "insecure-tcp"
	flagPeriod      = "period"
	flagHeartbeat   = "heartbeat"
	flagBaseAction  = "base-action"
)

// ParseArgs parses `run`'s flags, refusing FAIL-CLOSED: a missing adaptor, a
// non-positive period, no transport or two transports is a refusal naming what
// to do, never a default quietly invented on the operator's behalf.
func ParseArgs(args []string) (Options, error) {
	var opts Options
	fs := flag.NewFlagSet(Verb, flag.ContinueOnError)
	// The flag package's own usage/error text is not this repo's two-line
	// error contract, so it is discarded and every refusal is rebuilt below.
	fs.SetOutput(io.Discard)

	fs.StringVar(&opts.AdaptorPath, flagAdaptor, "", "")
	fs.Var(pathListFlag{name: flagRules, paths: &opts.RuleLayers}, flagRules, "")
	fs.Var(pathListFlag{name: flagProvider, paths: &opts.ProviderPaths}, flagProvider, "")
	fs.StringVar(&opts.SocketDir, flagSocketDir, "", "")
	fs.BoolVar(&opts.Stdio, flagStdio, false, "")
	fs.StringVar(&opts.TCPAddr, flagInsecureTCP, "", "")
	fs.DurationVar(&opts.Period, flagPeriod, DefaultPeriod, "")
	fs.DurationVar(&opts.Heartbeat, flagHeartbeat, stream.DefaultHeartbeatEvery, "")
	fs.StringVar(&opts.BaseAction, flagBaseAction, "", "")

	if err := fs.Parse(args); err != nil {
		return Options{}, clifmt.NewUserError(Verb+": "+err.Error(), UsageText)
	}
	if extra := fs.Args(); len(extra) > 0 {
		return Options{}, clifmt.NewUserError(
			fmt.Sprintf("%s takes no positional arguments, got %q", Verb, extra[0]),
			UsageText)
	}
	return opts, opts.check()
}

// check is the fail-closed half of ParseArgs, separated so a consumer building
// Options in Go gets the identical refusals argv does.
func (o Options) check() error {
	if o.AdaptorPath == "" {
		return clifmt.NewUserError(
			"no adaptor config was given, so nothing declares this robot's channels",
			"pass --"+flagAdaptor+" <path> naming a .json or .toml adaptor config")
	}
	if err := o.checkTransport(); err != nil {
		return err
	}
	if o.Period <= 0 {
		return clifmt.NewUserError(
			fmt.Sprintf("--%s is %v — it must be greater than zero", flagPeriod, o.Period),
			"pass a positive duration, e.g. --"+flagPeriod+" 20ms (50 Hz)")
	}
	return nil
}

// checkTransport refuses no transport and two transports alike. A default
// picked here would be a socket path the library invented — and a socket
// nobody chose is a socket nobody owns.
func (o Options) checkTransport() error {
	chosen := make([]string, 0, 3)
	if o.SocketDir != "" {
		chosen = append(chosen, "--"+flagSocketDir)
	}
	if o.Stdio {
		chosen = append(chosen, "--"+flagStdio)
	}
	if o.TCPAddr != "" {
		chosen = append(chosen, "--"+flagInsecureTCP)
	}
	switch len(chosen) {
	case 1:
		return nil
	case 0:
		return clifmt.NewUserError(
			"no transport was chosen, so a composed pose would have nowhere to go",
			fmt.Sprintf("pass one of --%s <dir>, --%s, or --%s <addr>",
				flagSocketDir, flagStdio, flagInsecureTCP))
	default:
		return clifmt.NewUserError(
			fmt.Sprintf("%s were all given; the engine serves exactly one transport",
				strings.Join(chosen, " and ")),
			"choose one; two endpoints onto one tick is a contention with no owner")
	}
}

// streamConfig turns Options into the endpoint's config.
//
// --insecure-tcp carries its own acceptance: there is no plain --tcp flag, so
// the ONLY spelling of a TCP listen address says out loud what it costs. On a
// robot a TCP listener lets anything that can route to the box admit intents,
// while a socket file at least inherits the filesystem's answer to "who is
// allowed" — so the insecurity is named in the flag rather than in a footnote
// somebody will not read.
func (o Options) streamConfig(engineVersion string) stream.Config {
	return stream.Config{
		Dir:            o.SocketDir,
		TCPAddr:        o.TCPAddr,
		InsecureTCP:    o.TCPAddr != "",
		HeartbeatEvery: o.Heartbeat,
		EngineVersion:  engineVersion,
	}
}

// layers is RuleLayers as the loader's layer stack: one file per layer, in the
// order given.
func (o Options) layers() [][]string {
	out := make([][]string, 0, len(o.RuleLayers))
	for _, path := range o.RuleLayers {
		out = append(out, []string{path})
	}
	return out
}

// extOf is filepath.Ext, named here so the two extension switches in this
// package read the same way.
func extOf(path string) string { return filepath.Ext(path) }

// isTOMLConfig maps an adaptor config's extension to whether it is TOML. The
// two front ends share every validation rule; only the decoder differs.
func isTOMLConfig(path string) (bool, error) {
	switch strings.ToLower(extOf(path)) {
	case ".json":
		return false, nil
	case ".toml":
		return true, nil
	default:
		return false, clifmt.NewUserError(
			fmt.Sprintf("--%s %q has an unrecognized extension", flagAdaptor, path),
			"pass a .json or .toml adaptor config")
	}
}
