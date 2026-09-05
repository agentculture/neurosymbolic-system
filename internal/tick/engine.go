package tick

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
)

// defaultInboxSize is the bounded inbox's depth when Config leaves it unset.
// It is a DROP boundary, not a buffer to be tuned upward until nothing is lost:
// a full inbox is a named drop precisely so a slow tick can never be pushed
// into backpressure by a hot producer.
const defaultInboxSize = 64

// defaultMaxErrors matches the donor's EngineConfig.max_errors: five
// consecutive failed writes end the run. One failed write is a transient; five
// in a row is a transport that is gone.
const defaultMaxErrors = 5

// Config is the tunable half of an engine run. The plant-specific half — what
// the channels are, what the actions do — comes from the adaptor.Vocabulary.
type Config struct {
	// Period is the tick period. 20 ms is 50 Hz; the library must work at
	// other rates, so nothing here is allowed to assume one.
	Period time.Duration

	// BaseLayer seeds BaseAction as a passive, looping behavior before the
	// first tick, so an idle robot keeps moving and any channel nothing else
	// claims stays alive. The engine never names the action itself.
	BaseLayer  bool
	BaseAction string

	// MaxErrors is how many CONSECUTIVE sink write failures are tolerated
	// before Run returns the error. Zero means defaultMaxErrors.
	MaxErrors int

	// Settle, when nil, writes one neutral pose as the loop exits. Set it to
	// Bool(false) to leave the last streamed pose standing.
	Settle *bool

	// InboxSize is the bounded inbox's depth. Zero means defaultInboxSize.
	InboxSize int

	// Clock and Ticker are injected. Both default to the wall clock, which is
	// the only thing in this package that reads it.
	Clock  Clock
	Ticker Ticker

	// Log receives every named drop and stage line. Nil means stderr, which is
	// where a SENSE line belongs — an export stdout stays pure.
	Log *senselog.Logger
}

// Bool returns a pointer to b, for Config.Settle.
func Bool(b bool) *bool { return &b }

// Event is a structured message a seam publishes through TickContext.Emit. The
// engine never interprets one; it only fans it out to the consumers registered
// before Run.
type Event struct {
	Name string
	Data map[string]any
}

// TickSeam is the ONE per-tick integration point. The engine calls it exactly
// once per tick, AFTER that tick's pose has streamed, so a consumer observing
// ctx.Pose() sees what the robot was actually told this tick rather than a
// prediction.
//
// Rules, sense drivers, the goto lane, export feeds and metrics are all pure
// consumers of this seam.
//
// A PANIC in the seam is RECOVERED and does not take the loop down. It is
// emitted as one named senselog drop (source "seam", reason "panic"), counted
// in Stats.SeamPanics, and the loop continues to the next tick; the pose this
// tick already streamed is unaffected, because the seam runs after the write.
// The donor let a raising rider propagate out of run(); on a robot that trades
// one broken rider for a dead tick loop and a machine frozen at whatever the
// last pose happened to be, which is the worse failure.
//
// Isolation is not a licence to swallow. The engine recovers, names the drop
// and keeps ticking; it does not retry the seam and it does not repair it. A
// seam composing several riders should still isolate them from each other —
// this recover is a last resort for the tick, not a fan-out.
type TickSeam func(TickContext)

// Command is something another goroutine asks the engine to do. Every state
// mutation happens on the tick goroutine, draining these at tick start; a
// Command is the only way in.
type Command interface{ isCommand() }

// AdmitCmd puts a behavior onto the active set. A behavior the vocabulary
// refuses is a named drop, not a panic and not a silent no-op.
type AdmitCmd struct{ Behavior Behavior }

// EvictCmd stops every active behavior whose id or library name matches Name.
type EvictCmd struct{ Name string }

// SetSeamCmd installs (or, with a nil Seam, removes) the tick seam.
type SetSeamCmd struct{ Seam TickSeam }

func (AdmitCmd) isCommand()   {}
func (EvictCmd) isCommand()   {}
func (SetSeamCmd) isCommand() {}

// Stats is the engine's cumulative accounting, readable from any goroutine.
//
// SeamPanics counts the recovered panics from the tick seam and from event
// consumers. It is a NAMED SUBSET of Drops: every one of them is also a named
// drop line, so a grep of the log and a read of the counters agree.
type Stats struct {
	Ticks      uint64
	Overruns   uint64
	Drops      uint64
	SinkErrors uint64
	SeamPanics uint64
}

// activeBehavior is one live behavior plus the tick-goroutine-owned state the
// engine keeps beside it.
type activeBehavior struct {
	behavior Behavior
	isBase   bool
}

// Engine holds the active behavior set and runs the tick.
//
// All engine state is owned by the goroutine running Run. Every other writer
// enqueues a Command on the bounded inbox, which Run drains at tick start. The
// only fields another goroutine touches are the atomic counters behind Stats
// and the consumer list, which is guarded.
type Engine struct {
	voc      *adaptor.Vocabulary
	cfg      Config
	sink     adaptor.Sink
	clock    Clock
	ticker   Ticker
	log      *dropLog
	channels []string
	inbox    chan Command

	// --- owned by the Run goroutine ---
	active        []activeBehavior
	seq           int
	seam          TickSeam
	lastPose      adaptor.Pose
	lastOwnership map[string]string
	overrun       *senselog.Streak
	inOverrun     bool
	sinkFailures  int

	// --- shared ---
	ticks      atomic.Uint64
	overruns   atomic.Uint64
	drops      atomic.Uint64
	sinkErrors atomic.Uint64
	inboxDrops atomic.Uint64
	seamPanics atomic.Uint64

	mu        sync.RWMutex
	consumers []func(Event)
}

// New builds an engine over one vocabulary, one config and one sink.
//
// The sink is held open for the whole loop by the caller and never constructed
// per tick: at 50 Hz the budget is 20 ms, and building a client on the tick
// thread is the single most expensive mistake the donor made (425-1213 ms, a
// 21-61x overrun).
func New(v *adaptor.Vocabulary, cfg Config, sink adaptor.Sink) (*Engine, error) {
	if v == nil {
		return nil, errors.New("tick: an engine needs a vocabulary — nothing declares its channels")
	}
	if sink == nil {
		return nil, errors.New("tick: an engine needs a sink — a composed pose has nowhere to go")
	}
	if cfg.Period <= 0 {
		return nil, fmt.Errorf("tick: the tick period is %v — it must be greater than zero",
			cfg.Period)
	}
	if cfg.BaseLayer && cfg.BaseAction == "" {
		return nil, errors.New(
			"tick: the base layer is enabled but no base action is named — name one the " +
				"vocabulary declares, or disable the base layer")
	}
	if cfg.MaxErrors <= 0 {
		cfg.MaxErrors = defaultMaxErrors
	}
	if cfg.InboxSize <= 0 {
		cfg.InboxSize = defaultInboxSize
	}
	if cfg.Clock == nil {
		cfg.Clock = RealClock{}
	}
	if cfg.Ticker == nil {
		cfg.Ticker = NewRealTicker(cfg.Period)
	}
	if cfg.Log == nil {
		cfg.Log = senselog.Default()
	}

	channels := make([]string, 0, len(v.Channels()))
	for _, ch := range v.Channels() {
		channels = append(channels, ch.Name)
	}

	e := &Engine{
		voc:      v,
		cfg:      cfg,
		sink:     sink,
		clock:    cfg.Clock,
		ticker:   cfg.Ticker,
		channels: channels,
		inbox:    make(chan Command, cfg.InboxSize),
		lastPose: v.Neutral(),
	}
	e.log = &dropLog{logger: cfg.Log, drops: &e.drops}
	e.overrun = cfg.Log.NewStreak(stage, "budget", "overrun")
	e.lastOwnership = make(map[string]string, len(channels))
	for _, name := range channels {
		e.lastOwnership[name] = Unowned
	}
	return e, nil
}

// Inbox is the bounded command channel. Prefer Send: a producer that writes
// here directly BLOCKS when the inbox is full, and the tick budget belongs to
// the robot, not to the producer.
func (e *Engine) Inbox() chan<- Command { return e.inbox }

// Send enqueues one command without ever blocking. A full inbox is a NAMED
// drop — the command is lost, the reason is on stderr, and Stats counts it —
// because backpressure onto a producer is how a 20 ms budget gets spent
// somewhere nobody is looking. It reports whether the command was accepted.
//
// The count lands in Stats immediately, on the caller's goroutine; the LINE is
// emitted by the next tick, so a flood of drops from many producers is one
// episode written by one writer rather than a race for stderr.
func (e *Engine) Send(cmd Command) bool {
	select {
	case e.inbox <- cmd:
		return true
	default:
		e.drops.Add(1)
		e.inboxDrops.Add(1)
		return false
	}
}

// OnEvent registers a consumer of the events a seam emits. Register consumers
// before Run; registration is safe from any goroutine.
func (e *Engine) OnEvent(fn func(Event)) {
	if fn == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.consumers = append(e.consumers, fn)
}

// Stats returns the cumulative accounting. Safe from any goroutine.
func (e *Engine) Stats() Stats {
	return Stats{
		Ticks:      e.ticks.Load(),
		Overruns:   e.overruns.Load(),
		Drops:      e.drops.Load(),
		SinkErrors: e.sinkErrors.Load(),
		SeamPanics: e.seamPanics.Load(),
	}
}

// Run drives the loop until ctx is done, the ticker stops, or the sink fails
// MaxErrors times in a row.
//
// Each tick, in this order: wait for the tick, drain the inbox, drop expired
// behaviors, ask every live behavior for its contribution once, arbitrate,
// compose a complete pose, write it, call the seam, account the budget. The
// wait comes FIRST, so an engine that is never ticked runs no ticks and a fake
// clock advanced by n periods runs exactly n.
//
// On exit it writes one settling neutral pose unless Config.Settle is
// Bool(false). It returns nil for a clean stop and the sink's error when the
// consecutive-failure ceiling is reached.
func (e *Engine) Run(ctx context.Context) error {
	defer e.ticker.Stop()
	if err := e.seedBaseLayer(); err != nil {
		return err
	}

	var runErr error
	for e.ticker.Wait(ctx) {
		now := e.clock.Now()
		tickNumber := int(e.ticks.Add(1))

		e.drainInbox()
		pose, ownership, contribs := e.composeTick(now)
		if err := e.write(pose); err != nil {
			runErr = err
			break
		}
		e.lastPose, e.lastOwnership = pose, ownership
		e.invokeSeam(now, tickNumber, contribs)
		e.accountBudget(tickNumber, now)
	}

	if e.inOverrun {
		e.overrun.End(int(e.ticks.Load()))
		e.inOverrun = false
	}
	e.settle()
	return runErr
}

// seedBaseLayer admits the configured base action as a passive, looping
// behavior before the first tick. Passive means it owns only what nothing else
// claims, so it keeps an idle robot moving without ever contending.
func (e *Engine) seedBaseLayer() error {
	if !e.cfg.BaseLayer {
		return nil
	}
	if !e.voc.HasAction(e.cfg.BaseAction) {
		return fmt.Errorf(
			"tick: the base action %q is not declared by %s — name a declared action, "+
				"or disable the base layer", e.cfg.BaseAction, e.voc.Origin())
	}
	base := Behavior{
		Action:     e.cfg.BaseAction,
		Class:      ClassPassive,
		Lifetime:   Lifetime{Loops: true},
		AdmittedAt: e.clock.Now(),
	}
	bound, err := Bind(e.voc, base)
	if err != nil {
		return err
	}
	bound.ID = e.nextID(bound.Name)
	e.active = append(e.active, activeBehavior{behavior: bound, isBase: true})
	return nil
}

// drainInbox applies the pending commands. It drains at most the inbox's own
// depth per tick, so a producer hammering the channel can never hold the tick
// goroutine past its budget.
func (e *Engine) drainInbox() {
	if dropped := e.inboxDrops.Swap(0); dropped > 0 {
		e.log.line("inbox", "command", "full",
			fmt.Sprintf("%d commands were dropped rather than blocking a producer", dropped))
	}
	for i := 0; i < cap(e.inbox); i++ {
		select {
		case cmd := <-e.inbox:
			e.apply(cmd)
		default:
			return
		}
	}
}

func (e *Engine) apply(cmd Command) {
	switch c := cmd.(type) {
	case AdmitCmd:
		if _, err := e.admit(c.Behavior); err != nil {
			e.log.drop("admit", behaviorLabel(c.Behavior), "refused", err.Error())
		}
	case EvictCmd:
		e.evict(c.Name)
	case SetSeamCmd:
		e.seam = c.Seam
	default:
		e.log.drop("inbox", commandName(cmd), "unknown-command",
			"the engine has no handler for this command type")
	}
}

// admit binds a behavior, applies the contention outcome and appends it as the
// most recent entry. It runs on the tick goroutine only.
func (e *Engine) admit(b Behavior) (AdmitResult, error) {
	bound, err := Bind(e.voc, b)
	if err != nil {
		return AdmitResult{}, err
	}
	if bound.ID == "" {
		bound.ID = e.nextID(bound.Name)
	}
	if bound.AdmittedAt.IsZero() {
		bound.AdmittedAt = e.clock.Now()
	}

	result := Admit(e.channels, bound, e.behaviors())
	if len(result.Evicted) > 0 {
		evicted := make(map[string]bool, len(result.Evicted))
		for _, id := range result.Evicted {
			evicted[id] = true
		}
		kept := e.active[:0]
		for _, ab := range e.active {
			if evicted[ab.behavior.ID] {
				e.log.note("evict", ab.behavior.ID, "evicted by an admitted stopping behavior")
				continue
			}
			kept = append(kept, ab)
		}
		e.active = kept
	}
	e.active = append(e.active, activeBehavior{behavior: bound})
	return result, nil
}

// evict stops every active behavior matching an id or a library name, and
// reports how many it removed.
func (e *Engine) evict(name string) int {
	if name == "" {
		return 0
	}
	removed := 0
	kept := e.active[:0]
	for _, ab := range e.active {
		if ab.behavior.ID == name || ab.behavior.Name == name {
			removed++
			continue
		}
		kept = append(kept, ab)
	}
	e.active = kept
	return removed
}

func (e *Engine) nextID(name string) string {
	e.seq++
	return fmt.Sprintf("%s-%d", name, e.seq)
}

// behaviors returns the live behaviors oldest-first, which is the order both
// pure contention functions expect.
func (e *Engine) behaviors() []Behavior {
	out := make([]Behavior, 0, len(e.active))
	for _, ab := range e.active {
		out = append(out, ab.behavior)
	}
	return out
}

// composeTick drops expired behaviors, samples every live one exactly once,
// arbitrates and composes a complete pose.
func (e *Engine) composeTick(now time.Time) (adaptor.Pose, map[string]string, map[string]Contribution) {
	live := e.active[:0]
	for _, ab := range e.active {
		if ab.behavior.Lifetime.IsExpired(localTime(ab.behavior, now)) {
			e.log.note("expire", ab.behavior.ID, "its lifetime elapsed")
			continue
		}
		live = append(live, ab)
	}
	e.active = live

	contribs := make(map[string]Contribution, len(e.active))
	for _, ab := range e.active {
		contribs[ab.behavior.ID] = ab.behavior.Contribution(localTime(ab.behavior, now))
	}

	behaviors := e.behaviors()
	ownership := Arbitrate(e.channels, behaviors, contribs)
	return Compose(e.voc, ownership, contribs, e.log), ownership, contribs
}

// localTime is behavior-local time: seconds since the behavior was admitted.
func localTime(b Behavior, now time.Time) float64 {
	return now.Sub(b.AdmittedAt).Seconds()
}

// write streams one pose, tolerating up to MaxErrors CONSECUTIVE failures. A
// success resets the count: one failed write is a transient, five in a row is a
// transport that is gone.
func (e *Engine) write(pose adaptor.Pose) error {
	err := e.sink.Write(pose)
	if err == nil {
		e.sinkFailures = 0
		return nil
	}
	e.sinkFailures++
	e.sinkErrors.Add(1)
	e.log.drop("sink", "write", "write-failed", err.Error())
	if e.sinkFailures >= e.cfg.MaxErrors {
		return fmt.Errorf("tick: the sink failed %d writes in a row: %w", e.sinkFailures, err)
	}
	return nil
}

// invokeSeam calls the installed seam exactly once, isolating a panic in it
// from the loop. A rider that panics loses its turn on this tick and nothing
// else: the pose has already streamed, the drop names the reason, and the next
// tick happens on schedule. A dead tick loop leaves a robot frozen at whatever
// the last pose happened to be, which is a worse failure than one broken
// rider.
func (e *Engine) invokeSeam(now time.Time, tickNumber int, contribs map[string]Contribution) {
	if e.seam == nil {
		return
	}
	defer e.recoverPanic("seam")
	e.seam(TickContext{
		Now:      now,
		Tick:     tickNumber,
		engine:   e,
		contribs: contribs,
	})
}

// recoverPanic turns a panic into one named drop and a counter. It is a
// deferred call, so the caller returns normally and the loop continues.
func (e *Engine) recoverPanic(source string) {
	recovered := recover()
	if recovered == nil {
		return
	}
	e.seamPanics.Add(1)
	e.log.drop(source, "callback", "panic", firstLine(fmt.Sprint(recovered)))
}

// firstLine is the recovered value reduced to something that fits the SENSE
// grammar's one-line shape: a panic value carrying a newline would otherwise
// split one drop into two unparseable lines.
func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	return text
}

// accountBudget counts an overrunning tick and reports overrun EPISODES rather
// than ticks: a streak logs one entry line, one more only when the reason
// changes, and one summary when it ends. The engine never skips or double-ticks
// to catch up — a skipped tick is a seam that silently did not run.
func (e *Engine) accountBudget(tickNumber int, start time.Time) {
	elapsed := e.clock.Now().Sub(start)
	if elapsed > e.cfg.Period {
		e.overruns.Add(1)
		e.overrun.Enter(tickNumber, "overrun")
		e.inOverrun = true
		return
	}
	if e.inOverrun {
		e.overrun.End(tickNumber)
		e.inOverrun = false
	}
}

// settle writes one neutral pose as the loop exits, so a robot does not hold
// whatever the last tick happened to compose.
func (e *Engine) settle() {
	if e.cfg.Settle != nil && !*e.cfg.Settle {
		return
	}
	if err := e.sink.Write(e.voc.Neutral()); err != nil {
		e.log.drop("settle", "write", "write-failed", err.Error())
	}
}

// emit fans an event out to every registered consumer, isolating each one: a
// consumer that panics is a named drop and the REST STILL RUN, so one broken
// consumer cannot silence another.
func (e *Engine) emit(event Event) {
	e.mu.RLock()
	consumers := e.consumers
	e.mu.RUnlock()
	for _, fn := range consumers {
		e.callConsumer(fn, event)
	}
}

func (e *Engine) callConsumer(fn func(Event), event Event) {
	defer e.recoverPanic("event")
	fn(event)
}

// TickContext is the per-tick seam contract. The engine builds one fresh each
// tick, after that tick's pose has streamed, and hands it to the installed
// TickSeam exactly once.
//
// Its mutating methods run on the tick goroutine, so a seam admits and evicts
// synchronously — it does not go through the inbox.
type TickContext struct {
	// Now is this tick's injected clock reading.
	Now time.Time
	// Tick is the 1-based tick counter.
	Tick int

	engine   *Engine
	contribs map[string]Contribution
}

// Ownership is the {channel: owner id} resolved this tick, with Unowned for a
// channel nothing owned. The map is a copy.
func (c TickContext) Ownership() map[string]string {
	out := make(map[string]string, len(c.engine.lastOwnership))
	for channel, owner := range c.engine.lastOwnership {
		out[channel] = owner
	}
	return out
}

// Pose is the COMPLETE pose this tick streamed — the exact values the sink was
// handed, not a prediction. The map is a copy.
func (c TickContext) Pose() adaptor.Pose {
	out := make(adaptor.Pose, len(c.engine.lastPose))
	for channel, values := range c.engine.lastPose {
		out[channel] = cloneValues(values)
	}
	return out
}

// Contributions is what every live behavior offered this tick, keyed by
// behavior id — the evidence behind Ownership, for a rider that needs to see
// who abstained.
func (c TickContext) Contributions() map[string]Contribution { return c.contribs }

// Admit puts a behavior onto the active set immediately, returning the
// contention outcome. A behavior the vocabulary refuses comes back as an error
// and is not admitted.
func (c TickContext) Admit(b Behavior) (AdmitResult, error) { return c.engine.admit(b) }

// Evict stops every active behavior matching an id or a library name, and
// reports how many it removed.
func (c TickContext) Evict(name string) int { return c.engine.evict(name) }

// ActiveNames lists the library names of the currently active behaviors,
// oldest-first.
func (c TickContext) ActiveNames() []string {
	out := make([]string, 0, len(c.engine.active))
	for _, ab := range c.engine.active {
		out = append(out, ab.behavior.Name)
	}
	return out
}

// Emit publishes a structured event to every consumer registered with
// OnEvent. It is a no-op when none are.
func (c TickContext) Emit(event Event) { c.engine.emit(event) }

func behaviorLabel(b Behavior) string {
	if b.ID != "" {
		return b.ID
	}
	if b.Name != "" {
		return b.Name
	}
	if b.Action != "" {
		return b.Action
	}
	return "unnamed"
}

func commandName(cmd Command) string {
	switch cmd.(type) {
	case AdmitCmd:
		return "admit"
	case EvictCmd:
		return "evict"
	case SetSeamCmd:
		return "seam"
	default:
		return "unknown"
	}
}
