package compose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
	"github.com/agentculture/neurosymbolic-system/internal/mgmt"
	"github.com/agentculture/neurosymbolic-system/internal/ruleeval"
	"github.com/agentculture/neurosymbolic-system/internal/rules"
	"github.com/agentculture/neurosymbolic-system/internal/sense"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/stream"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// stage is the senselog stage token every line this package emits carries.
const stage = "compose"

// Build identifies the binary to a connecting client, so a consumer can refuse
// an engine it does not understand BY NAME rather than by symptom.
type Build struct {
	Version  string
	Revision string
}

// Runtime is one composed engine: every part wired, nothing started.
//
// It exists as a value rather than as one long function so a test can build
// the real thing, inspect the pieces, and drive it — the composition root is
// where a wiring mistake hides, and a wiring that can only be exercised by
// launching a process is a wiring nobody tests.
type Runtime struct {
	Vocabulary *adaptor.Vocabulary
	Snapshot   *sense.Snapshot
	Registry   *ruleeval.Registry
	Rules      *rulesLane
	Engine     *tick.Engine
	Server     *stream.Server
	Handler    *mgmt.Handler
	Providers  []namedProvider

	status *statusRider
	log    *senselog.Logger
	period time.Duration
}

// New builds the whole runtime from one Options.
//
// Order matters in exactly one place and it is worth naming: tick.New needs
// its Sink at construction and the stream Server IS that sink, so the two
// cannot each be built with the other in hand. The server is built first with
// a nil engine, the engine over it, and the cycle closed with Attach.
//
// stderr receives every senselog line. Nothing here writes to stdout — in
// --stdio mode stdout is the wire, and in every mode a consumer piping it must
// get protocol frames or nothing at all.
func New(opts Options, build Build, stdin io.Reader, stdout, stderr io.Writer) (*Runtime, error) {
	if err := opts.check(); err != nil {
		return nil, err
	}
	log := senselog.New(stderr)

	voc, err := loadVocabulary(opts.AdaptorPath)
	if err != nil {
		return nil, err
	}

	// The perception surface. The stream's reader goroutine feeds it and the
	// tick goroutine reads it; neither learns anything about the other.
	snapshot := sense.New()
	registry := ruleeval.NewRegistry()

	lane, err := newRulesLane(voc, snapshot, registry, log, opts.layers())
	if err != nil {
		return nil, err
	}

	// The snapshot is BOTH a provider's sink and its view: a provider's answer
	// is an ordinary sense field, written back where every other reading lives.
	providers, err := buildProviders(opts.ProviderPaths, voc, snapshot, log)
	if err != nil {
		return nil, err
	}

	status := &statusRider{providers: providers}
	handler := &mgmt.Handler{
		Version:  build.Version,
		Revision: build.Revision,
		Reloader: lane,
		Status:   status,
	}

	cfg := opts.streamConfig(build.Version)
	cfg.Vocabulary = voc
	var srv *stream.Server
	if opts.Stdio {
		srv, err = stream.NewStdio(cfg, nil, snapshot, jsonFront{handler}, log, stdin, stdout)
	} else {
		srv, err = stream.New(cfg, nil, snapshot, jsonFront{handler}, log)
	}
	if err != nil {
		return nil, clifmt.NewEnvError(err.Error(), "")
	}

	// Refuse an undeclared base action HERE rather than leaving it to the
	// engine's own check. tick.Engine refuses it too — at the top of Run,
	// after the socket exists and a client may already have connected — and a
	// process that comes up, accepts a connection and then dies is a worse
	// failure than one that never started. Refuse before anything is listening.
	if opts.BaseAction != "" && !voc.HasAction(opts.BaseAction) {
		return nil, clifmt.NewUserError(
			fmt.Sprintf("the base action %q is not declared by %s", opts.BaseAction, voc.Origin()),
			"name an action the adaptor config declares, or drop --"+flagBaseAction)
	}

	engine, err := tick.New(voc, tick.Config{
		Period:     opts.Period,
		BaseLayer:  opts.BaseAction != "",
		BaseAction: opts.BaseAction,
		Log:        log,
	}, srv)
	if err != nil {
		closeProviders(providers)
		return nil, clifmt.NewUserError(err.Error(),
			"name an action the adaptor config declares, or drop --"+flagBaseAction)
	}
	srv.Attach(engine)
	status.bind(engine, srv, lane)

	// The ONE seam, fanned out with each rider isolated from the others.
	//
	// Order: providers first, then rules, then the status rider, and the
	// stream's heartbeat last. Providers lead because they are sense
	// PRODUCERS — a request enqueued this tick is then built from the same
	// view the rules go on to evaluate, so one tick is one coherent reading.
	// (It changes nothing about when an answer arrives: a provider's worker
	// writes back from its own goroutine, so the rule keyed on its output
	// necessarily fires on a LATER tick.) The heartbeat rides last so a beat
	// means "this tick completed", not "this tick started".
	bus := ruleeval.NewBus(log)
	for _, p := range providers {
		bus.Add(providerRider(p), p.Provider.Driver())
	}
	seam := bus.
		Add(ruleeval.StageRule, lane.Tick).
		Add("status", status.Tick).
		Add(stream.KindHeartbeat, srv.Seam).
		Compose()
	if !engine.Send(tick.SetSeamCmd{Seam: seam}) {
		closeProviders(providers)
		return nil, clifmt.NewEnvError(
			"the engine refused the tick seam before the first tick",
			"this is a bug in the composition root, not a mistake in the config")
	}

	return &Runtime{
		Vocabulary: voc, Snapshot: snapshot, Registry: registry, Rules: lane,
		Engine: engine, Server: srv, Handler: handler, Providers: providers,
		status: status, log: log, period: opts.Period,
	}, nil
}

// providerRider is the name a provider's panic drop is filed under on the bus.
// It carries the provider's own name so a grep of the log finds the offender
// rather than "driver-2".
func providerRider(p namedProvider) string { return providerStage + ":" + p.Name }

// Close releases everything the runtime holds that outlives a Run: today, one
// worker goroutine and one HTTP client per provider.
//
// It is separate from Run because a caller may build a Runtime and never run it
// (a test, a dry-run check), and a provider's worker would leak either way. It
// is idempotent in the only sense that matters here — call it once, from the
// same place that built the runtime.
func (r *Runtime) Close() { closeProviders(r.Providers) }

// Run listens, serves and drives the loop until ctx is done.
//
// The peer is told when the engine stops, WITH THE REASON, through
// Server.RunWith: "the engine stopped" is not something a stream can observe on
// its own (the sink is written to, never called back), and a consumer that
// learns about a stopped engine only from a heartbeat lapse settles its robot a
// heartbeat late.
//
// The listener outlives the run on purpose. Serve gets its own context,
// cancelled only after Run returns, so the engine's settling neutral pose is
// composed and streamed before the endpoint tears the session down — a robot
// left holding whatever the last tick happened to compose is the failure the
// settle exists to prevent.
func (r *Runtime) Run(ctx context.Context) error {
	if err := r.Server.Listen(); err != nil {
		return clifmt.NewEnvError(err.Error(), "")
	}

	serveCtx, stopServing := context.WithCancel(context.WithoutCancel(ctx))
	defer stopServing()
	go r.Server.Serve(serveCtx)

	r.log.Stage(stage, Verb, "started", fmt.Sprintf(
		"period=%v channels=%d actions=%d rule_layers=%d providers=%d addr=%s",
		r.period, len(r.Vocabulary.Channels()), len(r.Vocabulary.Actions()),
		r.Rules.Layers(), len(r.Providers), r.Server.Addr()))

	err := r.Server.RunWith(ctx, r.Engine.Run)
	stats := r.Engine.Stats()
	r.log.Stage(stage, Verb, "stopped", fmt.Sprintf(
		"ticks=%d overruns=%d drops=%d", stats.Ticks, stats.Overruns, stats.Drops))
	if err != nil {
		return clifmt.NewEnvError(err.Error(),
			"the transport the engine streams to failed; check the consumer is reading")
	}
	return nil
}

// loadVocabulary reads the adaptor config by extension. The two front ends
// share every validation rule; only the decoder differs.
func loadVocabulary(path string) (*adaptor.Vocabulary, error) {
	isTOML, err := isTOMLConfig(path)
	if err != nil {
		return nil, err
	}
	var voc *adaptor.Vocabulary
	if isTOML {
		voc, err = adaptor.LoadTOML(path)
	} else {
		voc, err = adaptor.LoadJSON(path)
	}
	if err != nil {
		return nil, clifmt.NewUserError(err.Error(), "")
	}
	return voc, nil
}

// loadRules loads one layer stack against the vocabulary, converting the
// loader's own refusal into the CLI error contract with its fix half intact.
func loadRules(layers [][]string, voc *adaptor.Vocabulary) (*rules.Config, error) {
	cfg, err := rules.Load(layers, voc)
	if err != nil {
		return nil, asUserError(err)
	}
	return cfg, nil
}

// asUserError renders a rules.Error's what/fix halves as the two lines of the
// CLI error contract. A refusal an operator cannot act on is a refusal they
// will work around by disabling the check.
func asUserError(err error) *clifmt.CliError {
	var rerr *rules.Error
	if errors.As(err, &rerr) {
		message := "rules: " + rerr.Path + ": "
		if rerr.RuleID != "" {
			message += fmt.Sprintf("rule '%s': ", rerr.RuleID)
		}
		return clifmt.NewUserError(message+rerr.What, rerr.Fix)
	}
	var cliErr *clifmt.CliError
	if errors.As(err, &cliErr) {
		return cliErr
	}
	return clifmt.NewUserError(err.Error(), "")
}
