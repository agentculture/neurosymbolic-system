package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/sense"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// stage is the senselog stage token every line this package emits carries.
const stage = "provider"

// Viewer is the perception surface the driver reads each tick — satisfied by
// *sense.Snapshot. An interface so this package never imports a transport and
// a test can hand it a literal map-backed fake.
type Viewer interface {
	View(now time.Time) map[string]any
}

// Stats is a provider's cumulative accounting, safe to read from any
// goroutine.
type Stats struct {
	// Requests is how many calls the worker ATTEMPTED (successful enqueue).
	Requests uint64
	// Results is how many of those calls wrote a result to the sink.
	Results uint64
	// Drops is the count of abstentions, keyed by reason: "queue-full",
	// "timeout", "http-<status>", "malformed", "unconfigured".
	Drops map[string]uint64
}

// request is one enqueued call: the input view captured at enqueue time, so
// the worker never re-reads a snapshot that may have moved on.
type request struct {
	view map[string]any
	now  time.Time
}

// Provider is a warmed, worker-backed OpenAI-compatible decision provider.
// The zero value is not usable; build one with New.
type Provider struct {
	cfg    Config
	view   Viewer
	sink   sense.SenseSink
	log    *senselog.Logger
	client *http.Client
	clock  tick.Clock
	apiKey string

	// unconfigured is set once, at New, and never mutated again — so reading
	// it from the tick goroutine needs no synchronization.
	unconfigured bool

	// labelRefs holds each label's warmed reference embedding. Built once at
	// New and read-only afterward: safe to read from the worker goroutine
	// with no lock.
	labelRefs map[string][]float64

	queue chan request
	done  chan struct{}
	wg    sync.WaitGroup

	// -- owned by the tick goroutine only --
	tickCount  uint64
	tickStreak *senselog.Streak // queue-full / unconfigured, entered/left only from tick()

	// workerStreak is owned by the worker goroutine only — its own internal
	// state (open/reason/ticks) needs no lock since nothing else touches it.
	workerStreak *senselog.Streak // timeout / http-* / malformed

	// logMu serializes the two streaks' underlying WRITES. senselog.Logger
	// does not itself synchronize concurrent writers (see internal/tick's own
	// dropLog, which wraps it with exactly this kind of mutex for the same
	// reason: its Send() drop line comes from a producer goroutine while
	// everything else comes from the tick goroutine). tickStreak and
	// workerStreak share one *senselog.Logger and run on two different
	// goroutines, so their calls into it — not their own state — need this.
	logMu sync.Mutex

	requests atomic.Uint64
	results  atomic.Uint64

	dropsMu sync.Mutex
	drops   map[string]uint64
}

// New builds a Provider: it resolves the API key, builds the HTTP client (or
// uses the one passed in — a nil client means a fresh one carrying cfg's
// Timeout as a hard ceiling), warms it, and starts one worker goroutine.
//
// A warm-up failure does NOT fail New: it marks the provider unconfigured (one
// named drop on the first tick that would otherwise have used it) so the
// engine still starts and the bound rule simply abstains forever. Only a bad
// Config is refused outright, fail-closed, before anything is built.
func New(cfg Config, sink sense.SenseSink, log *senselog.Logger, client *http.Client) (*Provider, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if sink == nil {
		return nil, errNoSink(cfg.Name)
	}
	if log == nil {
		log = senselog.Default()
	}
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}

	var apiKey string
	if cfg.APIKeyEnv != "" {
		apiKey = os.Getenv(cfg.APIKeyEnv)
	}

	p := &Provider{
		cfg:          cfg,
		sink:         sink,
		log:          log,
		client:       client,
		clock:        tick.RealClock{},
		apiKey:       apiKey,
		queue:        make(chan request, cfg.QueueDepth),
		done:         make(chan struct{}),
		tickStreak:   log.NewStreak(stage, cfg.Name, cfg.Output),
		workerStreak: log.NewStreak(stage, cfg.Name, cfg.Output),
		drops:        map[string]uint64{},
	}

	if cfg.Kind == KindEmbedding {
		refs, err := p.warmEmbeddings()
		if err != nil {
			p.unconfigured = true
			p.log.Drop(stage, cfg.Name, cfg.Output, "unconfigured", "warm-up failed: "+err.Error())
		} else {
			p.labelRefs = refs
		}
	}

	p.wg.Add(1)
	go p.work()

	return p, nil
}

// SetClock overrides the clock the worker stamps sink writes with. Production
// code never needs this; it exists so a test can hand the worker the same
// tick.FakeClock the engine advances, keeping a test's timeline single-clocked.
func (p *Provider) SetClock(clock tick.Clock) {
	if clock != nil {
		p.clock = clock
	}
}

// warmEmbeddings fetches every configured label's reference embedding once,
// synchronously, at New — before the first tick, never on the tick goroutine.
func (p *Provider) warmEmbeddings() (map[string][]float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Timeout)
	defer cancel()

	refs := make(map[string][]float64, len(p.cfg.Labels))
	for _, label := range p.cfg.Labels {
		vec, err := fetchEmbedding(ctx, p.client, p.cfg.BaseURL, p.apiKey, p.cfg.Model, label)
		if err != nil {
			return nil, err
		}
		refs[label] = vec
	}
	return refs, nil
}

// Driver returns this provider as a tick.TickSeam driver, installable directly
// on tick.SetSeamCmd or composed into a ruleeval.Bus.
func (p *Provider) Driver() tick.TickSeam { return p.tick }

// Close stops the worker goroutine and waits for it to exit. A request still
// in flight is allowed to finish; nothing new is accepted after Close returns.
func (p *Provider) Close() {
	close(p.done)
	p.wg.Wait()
}

// Stats returns a snapshot of this provider's cumulative accounting. Safe
// from any goroutine.
func (p *Provider) Stats() Stats {
	p.dropsMu.Lock()
	drops := make(map[string]uint64, len(p.drops))
	for reason, n := range p.drops {
		drops[reason] = n
	}
	p.dropsMu.Unlock()
	return Stats{
		Requests: p.requests.Load(),
		Results:  p.results.Load(),
		Drops:    drops,
	}
}

// tick is the seam entry point: it decides, in bounded O(1) time, whether to
// enqueue a request this tick. It never blocks and never performs I/O — the
// worker goroutine is the only place a network call happens.
func (p *Provider) tick(ctx tick.TickContext) {
	p.tickCount++

	if p.unconfigured {
		p.tickStreakEnter(ctx.Tick, "unconfigured")
		return
	}
	if p.cfg.Cadence > 1 && p.tickCount%uint64(p.cfg.Cadence) != 0 {
		return
	}

	var view map[string]any
	if p.view != nil {
		view = p.view.View(ctx.Now)
	}

	select {
	case p.queue <- request{view: view, now: ctx.Now}:
		p.requests.Add(1)
		p.tickStreakEnd(ctx.Tick)
	default:
		p.countDrop("queue-full")
		p.tickStreakEnter(ctx.Tick, "queue-full")
	}
}

// tickStreakEnter and tickStreakEnd hold logMu for the call into
// senselog.Streak, which is not itself synchronized against a concurrent
// writer on the SAME *senselog.Logger — see logMu's field comment.
func (p *Provider) tickStreakEnter(tickNumber int, reason string) {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	p.tickStreak.Enter(tickNumber, reason)
}

func (p *Provider) tickStreakEnd(tickNumber int) {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	p.tickStreak.End(tickNumber)
}

// SetView installs the snapshot the driver reads each tick. Composition roots
// call this once before the engine starts running; it is not safe to call
// concurrently with a running tick loop.
func (p *Provider) SetView(view Viewer) { p.view = view }

// work is the single worker goroutine: it drains the bounded queue and
// performs the ONE HTTP call this package ever makes off the tick thread.
func (p *Provider) work() {
	defer p.wg.Done()
	for {
		select {
		case req := <-p.queue:
			p.handle(req)
		case <-p.done:
			return
		}
	}
}

// handle performs one request's HTTP round trip and either writes a result to
// the sink or logs one named abstention. It owns workerStreak's STATE
// exclusively — nothing else ever touches it — but the streak's underlying
// writes still share logMu with the tick goroutine's tickStreak.
func (p *Provider) handle(req request) {
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Timeout)
	defer cancel()

	fields, err := p.call(ctx, req)
	if err != nil {
		reason := reasonOf(err)
		p.countDrop(reason)
		p.workerStreakEnter(reason)
		return
	}
	p.workerStreakEnd()
	p.results.Add(1)
	p.sink.Update(fields, p.clock.Now())
}

func (p *Provider) workerStreakEnter(reason string) {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	p.workerStreak.Enter(0, reason)
}

func (p *Provider) workerStreakEnd() {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	p.workerStreak.End(0)
}

// call dispatches to the configured kind's HTTP call and shapes its result as
// the fields Update should write.
func (p *Provider) call(ctx context.Context, req request) (map[string]any, error) {
	switch p.cfg.Kind {
	case KindEmbedding:
		return p.callEmbedding(ctx, req)
	case KindCompletion:
		return p.callCompletion(ctx, req)
	default:
		// validate() refuses any other Kind at New; unreachable in practice.
		return nil, malformedErr("unknown provider kind " + p.cfg.Kind)
	}
}

func (p *Provider) callEmbedding(ctx context.Context, req request) (map[string]any, error) {
	input := renderEmbeddingInput(p.cfg.Inputs, req.view)
	vector, err := fetchEmbedding(ctx, p.client, p.cfg.BaseURL, p.apiKey, p.cfg.Model, input)
	if err != nil {
		return nil, err
	}
	label, score := bestLabel(p.labelRefs, vector)
	return map[string]any{
		p.cfg.Output:            label,
		p.cfg.Output + "_score": score,
	}, nil
}

func (p *Provider) callCompletion(ctx context.Context, req request) (map[string]any, error) {
	started := p.clock.Now()
	user := renderInputs(p.cfg.Inputs, req.view)
	reply, err := fetchCompletion(
		ctx, p.client, p.cfg.BaseURL, p.apiKey, p.cfg.Model, p.cfg.SystemPrompt, user, p.cfg.MaxTokens)
	if err != nil {
		return nil, err
	}
	word := firstWordLower(reply)
	if word == "" {
		return nil, malformedErr("reply carried no usable word")
	}
	latency := p.clock.Now().Sub(started).Seconds()
	return map[string]any{
		p.cfg.Output:                word,
		p.cfg.Output + "_latency_s": latency,
	}, nil
}

func (p *Provider) countDrop(reason string) {
	p.dropsMu.Lock()
	p.drops[reason]++
	p.dropsMu.Unlock()
}

func errNoSink(name string) error {
	return fmt.Errorf("provider %q: a config needs a sense.SenseSink", name)
}
