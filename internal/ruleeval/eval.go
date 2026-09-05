// Package ruleeval is rule evaluation on the tick — the react / inhibit / mode
// consumer, and the ONE admission registry every path in goes through.
//
// Cited (cite-don't-import) from reachy-mini-cli's
// reachy/behavior/rule_engine.py (the RuleEngine driver, its timing semantics
// and its per-episode suppression ledger) and reachy/behavior/intents.py (the
// four sustained intent kinds), crossed with microduck-cli's
// microduck_cli/behavior/intents.py, whose KindRegistry is where "one
// registry, one admit" is argued.
//
// It is a PURE CONSUMER of tick.TickSeam. Nothing in internal/tick imports this
// package; the engine only ever calls the opaque seam it was handed. That is
// the structural rule the donor learned over a year of features: rules, the
// goto lane, export feeds and metrics all ride the one seam without engine.go
// changing.
//
// # Timing semantics (asserted exactly by the tests)
//
//   - cooldown_s — the minimum seconds between two fires of the same rule, on
//     the injected TickContext.Now clock.
//   - hysteresis — a re-arm guard on top of cooldown: after a rule fires, its
//     predicate must evaluate false CONTINUOUSLY for at least that many
//     seconds before it may fire again. Zero means cooldown alone governs.
//   - absent_for — true once the named field has been absent continuously for
//     at least value seconds, tracked against TickContext.Now on a per-field
//     last-seen clock seeded at the first tick (so absence is measured from
//     engine start, never from minus infinity).
//   - duration_s — caps the admitted behavior's lifetime, overriding the
//     action's own bound.
//
// # Observability
//
// Every decision that fires or inhibits emits one
// "[SENSE stage=rule source=<field> event=<rule id>]" line, and every
// suppression is logged per EPISODE via senselog.Streak — one line at entry
// naming the reason, one more only when the reason CHANGES mid-streak, and one
// summary naming every reason and the streak length when it ends (before the
// fire line when a fire ends it). A tick where nothing matches logs nothing.
// That is the donor's #99 fix: emitting a drop line every tick a gated
// predicate held wrote 6722 lines into a 3 h journal against 42 genuine fires.
// The emitted event stream follows the same transition cadence, so an export
// feed is spared the same flood.
//
// # Units
//
// Seconds, everywhere. The tick period is read from TickContext.Now deltas and
// never assumed: a cadence-dependent tuning that only works at 50 Hz is a bug
// class of its own.
package ruleeval

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/rules"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// Senselog stages. Neither names anything plant-specific: they name which
// layer of this runtime spoke.
const (
	StageRule   = "rule"
	StageIntent = "intent"
)

// Event names published through TickContext.Emit. The donor carried the type
// as a "type" key inside the payload; here it is the tick.Event's Name, and the
// remaining keys are the donor's verbatim.
const (
	EventFire     = "rule.fire"
	EventSuppress = "rule.suppress"
)

// Outcome reasons — the reason= token in every SENSE line this package writes
// and the "reason" key on every event it emits.
//
// ReasonRearming is the donor's verbatim name for the HYSTERESIS guard (its
// REASON_REARMING): the rule fired, and its predicate has not yet been false
// long enough to re-arm. The donor's word is kept rather than renamed to
// "hysteresis" because a reason token is a log-and-export contract, and the
// donor's own tests, journals and dashboards are keyed on it.
const (
	ReasonFired         = "fired"
	ReasonCooldown      = "cooldown"
	ReasonRearming      = "rearming"
	ReasonInhibited     = "inhibited"
	ReasonAlreadyActive = "already-active"
)

// groupField is the senselog source token for a rule whose predicate is an
// all/any GROUP and therefore keys on no single field. The grammar needs a
// non-empty source; inventing one of the group's fields would name a signal the
// rule does not actually key on.
const groupField = "group"

// Viewer is the perception surface this driver reads — satisfied by
// *sense.Snapshot. It is an interface so the evaluator never imports a
// transport and a test can hand it a literal map.
type Viewer interface {
	View(now time.Time) map[string]any
}

// EventFields is the optional event-dialect surface — satisfied by
// *events.Router. When one is wired, the "<source>/<type>" keys it reports for
// the tick are merged into the view, so an [[event]] entry can be predicated on
// exactly like a sense field.
type EventFields interface {
	TickFields(now time.Time) map[string]any
}

// Config is what an Evaluator is built from.
type Config struct {
	// Rules is the validated, merged rules config. Required.
	Rules *rules.Config
	// Vocabulary declares this robot's channels, sense fields and actions.
	// Required: it is what keeps every plant-specific name out of this package.
	Vocabulary *adaptor.Vocabulary
	// Snapshot is the perception the driver views each tick. A nil Snapshot is
	// an empty view — every predicate false — never a panic.
	Snapshot Viewer
	// Router optionally merges routed event keys into the view.
	Router EventFields
	// Registry is the single admission registry. Nil means a fresh one.
	Registry *Registry
	// Logger receives every SENSE line. Nil means senselog.Default(), which is
	// stderr — an export stdout stays pure JSONL.
	Logger *senselog.Logger
	// IDPrefix prefixes every admitted behavior id. Empty means StageRule.
	IDPrefix string
}

// ruleState is one rule's cooldown + hysteresis bookkeeping plus its
// suppression-episode ledger. The ledger only shapes what gets LOGGED and
// EMITTED — the fire/suppress decisions never read it.
type ruleState struct {
	lastFire      time.Time
	hasFired      bool
	falseSince    time.Time
	hasFalseSince bool
	armed         bool

	suppressReason  string
	suppressTicks   int
	suppressReasons []string
	streak          *senselog.Streak
}

// Evaluator compiles a rules config onto the engine's one seam.
type Evaluator struct {
	cfg      *rules.Config
	voc      *adaptor.Vocabulary
	view     Viewer
	router   EventFields
	registry *Registry
	log      *senselog.Logger
	idPrefix string

	// --- owned by the tick goroutine ---
	states      map[string]*ruleState
	lastPresent map[string]time.Time
	startedAt   time.Time
	started     bool

	// --- shared ---
	mu         sync.RWMutex
	activeMode string
}

// New compiles a config into an Evaluator, FAIL-CLOSED: a react rule whose
// admission could never end is refused here rather than admitted and left to
// hold its channels forever.
func New(cfg Config) (*Evaluator, error) {
	if cfg.Rules == nil {
		return nil, fmt.Errorf("ruleeval: no rules config — load one before building an evaluator")
	}
	if cfg.Vocabulary == nil {
		return nil, fmt.Errorf(
			"ruleeval: no vocabulary — nothing declares this robot's fields and actions")
	}
	if cfg.Logger == nil {
		cfg.Logger = senselog.Default()
	}
	if cfg.Registry == nil {
		cfg.Registry = NewRegistry()
	}
	if cfg.IDPrefix == "" {
		cfg.IDPrefix = StageRule
	}

	e := &Evaluator{
		cfg:         cfg.Rules,
		voc:         cfg.Vocabulary,
		view:        cfg.Snapshot,
		router:      cfg.Router,
		registry:    cfg.Registry,
		log:         cfg.Logger,
		idPrefix:    cfg.IDPrefix,
		states:      map[string]*ruleState{},
		lastPresent: map[string]time.Time{},
		activeMode:  cfg.Rules.ActiveMode,
	}
	for _, rule := range append(append([]rules.Rule{}, cfg.Rules.React...), cfg.Rules.Inhibit...) {
		e.states[rule.ID] = &ruleState{
			armed:  true,
			streak: cfg.Logger.NewStreak(StageRule, sourceField(rule), rule.ID),
		}
	}
	for _, rule := range cfg.Rules.React {
		if rule.DurationS == nil && cfg.Vocabulary.ActionLoops(rule.Run) {
			return nil, fmt.Errorf(
				"ruleeval: react rule %q runs %q, a looping action, with no duration_s — "+
					"admitting it would let it hold its channels forever; add duration_s",
				rule.ID, rule.Run)
		}
	}
	cfg.Registry.bind(e)
	return e, nil
}

// Registry is the single admission registry every path in goes through.
func (e *Evaluator) Registry() *Registry { return e.registry }

// Seam returns this evaluator as a tick.TickSeam driver. Install it with
// tick.SetSeamCmd, or compose it with other drivers through a Bus.
func (e *Evaluator) Seam() tick.TickSeam { return e.Tick }

// ActiveMode is the mode currently in force, or "" when none is.
func (e *Evaluator) ActiveMode() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.activeMode
}

// SetActiveMode swaps the live active mode, refusing a name the config does not
// define (never a silent no-op — an operator who mistypes a mode must hear it).
// An empty name clears the mode. Safe from any goroutine.
func (e *Evaluator) SetActiveMode(name string) error {
	if name != "" {
		if _, ok := e.cfg.Modes[name]; !ok {
			valid := make([]string, 0, len(e.cfg.Modes))
			for mode := range e.cfg.Modes {
				valid = append(valid, mode)
			}
			sort.Strings(valid)
			listed := "(none configured)"
			if len(valid) > 0 {
				listed = fmt.Sprintf("%v", valid)
			}
			return fmt.Errorf("ruleeval: no mode named %q — use one of: %s", name, listed)
		}
	}
	e.mu.Lock()
	e.activeMode = name
	e.mu.Unlock()
	return nil
}

// Tick is the seam entry point: it evaluates every rule against this tick's
// perception and applies whatever the registry has queued.
func (e *Evaluator) Tick(ctx tick.TickContext) {
	now := ctx.Now
	view := e.snapshot(now)
	e.trackPresence(view, now)
	absent := e.absentFunc(now)

	byRules := e.currentInhibited(view, absent)
	e.registry.apply(ctx, e, byRules)
	e.runInhibit(ctx, view, absent, now)
	e.runReact(ctx, view, absent, now, byRules)
	e.registry.enforce(ctx, e, byRules)
	e.registry.sustain(ctx, e, byRules)
}

// snapshot is this tick's view: the perception snapshot, plus whatever event
// keys a wired router routed since the previous tick.
func (e *Evaluator) snapshot(now time.Time) map[string]any {
	var view map[string]any
	if e.view != nil {
		view = e.view.View(now)
	}
	if view == nil {
		view = map[string]any{}
	}
	if e.router != nil {
		for key, value := range e.router.TickFields(now) {
			view[key] = value
		}
	}
	return view
}

// trackPresence advances the per-field last-seen clock absent_for reads.
//
// It is the DONOR's clock, not the snapshot's: presence here means the is_true
// reading (a boolean condition fed false is ABSENT, not "seen"), which a
// last-seen stamped on every fed value cannot express. The two agree for every
// non-boolean field.
func (e *Evaluator) trackPresence(view map[string]any, now time.Time) {
	if !e.started {
		// Seed every declared field to the first tick, so absent_for measures
		// absence from engine start rather than from minus infinity.
		for _, s := range e.voc.Senses() {
			e.lastPresent[s.Name] = now
		}
		e.startedAt = now
		e.started = true
	}
	for _, s := range e.voc.Senses() {
		if Present(view, s.Name) {
			e.lastPresent[s.Name] = now
		}
	}
}

func (e *Evaluator) absentFunc(now time.Time) AbsentFunc {
	return func(field string) float64 {
		at, ok := e.lastPresent[field]
		if !ok {
			at = e.startedAt
		}
		return now.Sub(at).Seconds()
	}
}

// currentInhibited is the set of action names blocked by any inhibit rule
// matching THIS tick. It is independent of cooldown: while an inhibit predicate
// holds its actions are blocked from admission — a continuous effect — even
// between the cooldown-gated eviction fires.
func (e *Evaluator) currentInhibited(view map[string]any, absent AbsentFunc) map[string]bool {
	blocked := map[string]bool{}
	for _, rule := range e.cfg.Inhibit {
		if Eval(rule.When, view, absent) {
			for _, name := range rule.Disable {
				blocked[name] = true
			}
		}
	}
	return blocked
}

func (e *Evaluator) runInhibit(
	ctx tick.TickContext, view map[string]any, absent AbsentFunc, now time.Time,
) {
	for _, rule := range e.cfg.Inhibit {
		state := e.states[rule.ID]
		matched := Eval(rule.When, view, absent)
		e.stepArming(rule, state, matched, now)
		if !matched {
			e.release(ctx, rule, state)
			continue
		}
		if reason := e.fireReason(rule, state, now); reason != ReasonFired {
			e.suppress(ctx, rule, state, reason)
			continue
		}
		e.release(ctx, rule, state)
		for _, name := range rule.Disable {
			ctx.Evict(name)
		}
		e.markFired(rule, state, now)
		e.emitFire(ctx, rule, state, "", rule.Disable, nil)
	}
}

func (e *Evaluator) runReact(
	ctx tick.TickContext, view map[string]any, absent AbsentFunc, now time.Time,
	byRules map[string]bool,
) {
	active := map[string]bool{}
	for _, name := range ctx.ActiveNames() {
		active[name] = true
	}
	for _, rule := range e.cfg.React {
		if e.fireReact(ctx, rule, view, absent, now, byRules, active) {
			active[rule.Run] = true
		}
	}
}

// fireReact evaluates one react rule, reporting whether it admitted anything.
func (e *Evaluator) fireReact(
	ctx tick.TickContext, rule rules.Rule, view map[string]any, absent AbsentFunc,
	now time.Time, byRules map[string]bool, active map[string]bool,
) bool {
	state := e.states[rule.ID]
	matched := Eval(rule.When, view, absent)
	e.stepArming(rule, state, matched, now)
	if !matched {
		e.release(ctx, rule, state)
		return false
	}
	// The SAME predicate an injected intent is judged by (Registry.blocks), so
	// an inhibit blocks a rule fire and an agent's request alike.
	if e.registry.blocks(byRules, rule.Run, rule.Run) {
		e.suppress(ctx, rule, state, ReasonInhibited)
		return false
	}
	if reason := e.fireReason(rule, state, now); reason != ReasonFired {
		e.suppress(ctx, rule, state, reason)
		return false
	}
	if active[rule.Run] {
		e.suppress(ctx, rule, state, ReasonAlreadyActive)
		return false
	}

	outcome := e.registry.admit(ctx, e, Request{
		Action:    rule.Run,
		Params:    rule.Params,
		DurationS: rule.DurationS,
		Loops:     e.voc.ActionLoops(rule.Run),
		Origin:    OriginRule,
		RuleID:    rule.ID,
	}, byRules)
	if !outcome.Admitted {
		e.suppress(ctx, rule, state, outcome.Reason)
		return false
	}
	e.release(ctx, rule, state)
	e.markFired(rule, state, now)
	e.emitFire(ctx, rule, state, rule.Run, nil, outcome.Params)
	return true
}

// stepArming advances the re-arm state given this tick's predicate result.
func (e *Evaluator) stepArming(rule rules.Rule, state *ruleState, matched bool, now time.Time) {
	if matched {
		state.hasFalseSince = false // continuity of false is broken
		return
	}
	if !state.hasFalseSince {
		state.falseSince = now
		state.hasFalseSince = true
	}
	if !state.armed && now.Sub(state.falseSince).Seconds() >= rule.Hysteresis {
		state.armed = true
	}
}

// fireReason is whether the rule may fire now, or why it is suppressed.
func (e *Evaluator) fireReason(rule rules.Rule, state *ruleState, now time.Time) string {
	if !state.armed {
		return ReasonRearming
	}
	if state.hasFired && now.Sub(state.lastFire).Seconds() < rule.CooldownS {
		return ReasonCooldown
	}
	return ReasonFired
}

func (e *Evaluator) markFired(rule rules.Rule, state *ruleState, now time.Time) {
	state.lastFire = now
	state.hasFired = true
	if rule.Hysteresis > 0 {
		// Must now see hysteresis seconds of continuous false before re-firing.
		state.armed = false
		state.hasFalseSince = false
	}
}

// -- observability ---------------------------------------------------------

// suppress records one suppressed tick, emitting only on an episode TRANSITION.
func (e *Evaluator) suppress(ctx tick.TickContext, rule rules.Rule, state *ruleState, reason string) {
	state.suppressTicks++
	state.streak.Enter(ctx.Tick, reason)
	if reason == state.suppressReason {
		return // a continuation — counted, not emitted (the flood class)
	}
	if !containsString(state.suppressReasons, reason) {
		state.suppressReasons = append(state.suppressReasons, reason)
	}
	state.suppressReason = reason
	e.emitSuppress(ctx, rule, reason, -1)
}

// release closes an open suppression episode with ONE summary line and event.
// A no-op when no episode is open, so a non-suppressed tick stays silent.
func (e *Evaluator) release(ctx tick.TickContext, rule rules.Rule, state *ruleState) {
	if state.suppressReason == "" {
		return
	}
	summary := fmt.Sprintf("%s suppressed %d ticks",
		joinReasons(state.suppressReasons), state.suppressTicks)
	e.emitSuppress(ctx, rule, summary, state.suppressTicks)
	state.streak.End(ctx.Tick)
	state.suppressReason = ""
	state.suppressTicks = 0
	state.suppressReasons = nil
}

func (e *Evaluator) emitFire(
	ctx tick.TickContext, rule rules.Rule, state *ruleState, run string, disabled []string,
	params map[string]float64,
) {
	detail := "fired kind=" + rule.Kind
	if run != "" {
		detail += " run=" + run
	}
	if len(disabled) > 0 {
		detail += fmt.Sprintf(" disable=%v", disabled)
	}
	if rule.Say != "" {
		detail += fmt.Sprintf(" say=%q", rule.Say)
	}
	state.streak.Fire(ctx.Tick, detail)
	ctx.Emit(tick.Event{Name: EventFire, Data: map[string]any{
		"rule":     rule.ID,
		"kind":     rule.Kind,
		"field":    sourceField(rule),
		"op":       rule.When.Op,
		"behavior": run,
		// say is FORWARDED, never rendered: this package owns no actuator, and
		// a layer that spoke would be spending the tick budget on a speaker.
		"say":     rule.Say,
		"params":  params,
		"disable": append([]string{}, disabled...),
		"reason":  ReasonFired,
		"ts":      nowSeconds(ctx.Now),
		"tick":    ctx.Tick,
	}})
}

// emitSuppress publishes one suppression transition. ticks >= 0 marks the
// episode SUMMARY and carries its structured length.
func (e *Evaluator) emitSuppress(ctx tick.TickContext, rule rules.Rule, reason string, ticks int) {
	data := map[string]any{
		"rule":   rule.ID,
		"kind":   rule.Kind,
		"field":  sourceField(rule),
		"op":     rule.When.Op,
		"reason": reason,
		"ts":     nowSeconds(ctx.Now),
		"tick":   ctx.Tick,
	}
	if ticks >= 0 {
		data["ticks"] = ticks
	}
	ctx.Emit(tick.Event{Name: EventSuppress, Data: data})
}

// observeIntent logs and emits one registry outcome, applied or blocked.
func (e *Evaluator) observeIntent(
	ctx tick.TickContext, kind string, req Request, outcome Admission,
) {
	name := req.Name
	if name == "" {
		name = req.Action
	}
	if !outcome.Admitted {
		e.dropIntent(ctx, kind, name, outcome.Reason)
		return
	}
	e.log.Stage(StageIntent, kind, name, "applied id="+outcome.ID)
	ctx.Emit(tick.Event{Name: EventApplied, Data: map[string]any{
		"kind":   kind,
		"action": req.Action,
		"name":   name,
		"rule":   req.RuleID,
		"origin": req.Origin,
		"id":     outcome.ID,
		"params": outcome.Params,
		"reason": outcome.Reason,
		"ts":     nowSeconds(ctx.Now),
		"tick":   ctx.Tick,
	}})
}

func (e *Evaluator) dropIntent(ctx tick.TickContext, kind, name, reason string) {
	e.log.Drop(StageIntent, kind, name, reason, "")
	ctx.Emit(tick.Event{Name: EventBlocked, Data: map[string]any{
		"kind":   kind,
		"name":   name,
		"reason": reason,
		"ts":     nowSeconds(ctx.Now),
		"tick":   ctx.Tick,
	}})
}

// -- admission helpers -----------------------------------------------------

// build turns one Request into the behavior the engine is asked to admit.
//
// duration_s caps the lifetime; with none, a one-shot falls back to the
// action's own longest declared trajectory, and only a LOOPING request (a
// standing goal) is allowed to carry no bound at all.
func (e *Evaluator) build(id, name string, req Request) tick.Behavior {
	class := req.Class
	if class == "" {
		class = tick.ClassStoppable
	}
	lifetime := tick.Lifetime{Loops: req.Loops}
	switch {
	case req.DurationS != nil:
		bound := *req.DurationS
		lifetime.DurationS = &bound
	case !req.Loops:
		if bound, ok := e.actionDuration(req.Action); ok {
			lifetime.DurationS = &bound
		}
	}
	return tick.Behavior{
		Name:     name,
		ID:       id,
		Class:    class,
		Action:   req.Action,
		Params:   e.mergeParams(req.Action, req.Params),
		Lifetime: lifetime,
	}
}

// mergeParams layers the active mode's params under the request's own.
//
// Precedence, weakest first: the action's declared default, then the active
// mode, then the request. A mode param the action does not declare is IGNORED
// rather than refused — a mode is a broad tuning bag applied across actions,
// and refusing it would make one irrelevant key break every action.
func (e *Evaluator) mergeParams(action string, own map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for key, value := range e.activeModeParams() {
		if _, _, ok := e.voc.ActionParam(action, key); ok {
			out[key] = value
		}
	}
	for key, value := range own {
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (e *Evaluator) activeModeParams() map[string]float64 {
	e.mu.RLock()
	name := e.activeMode
	e.mu.RUnlock()
	if name == "" {
		return nil
	}
	return e.cfg.Modes[name]
}

func (e *Evaluator) actionLoops(action string) bool { return e.voc.ActionLoops(action) }

// actionDuration is the action's own bound: the longest trajectory it declares.
func (e *Evaluator) actionDuration(action string) (float64, bool) {
	for _, a := range e.voc.Actions() {
		if a.Name != action {
			continue
		}
		longest := 0.0
		for _, traj := range a.Trajectories {
			if traj != nil && traj.Duration() > longest {
				longest = traj.Duration()
			}
		}
		return longest, longest > 0
	}
	return 0, false
}

// sourceField is the senselog source token for a rule: the field its predicate
// keys on, or groupField when it keys on several.
func sourceField(rule rules.Rule) string {
	if rule.When.IsLeaf() && rule.When.Field != "" {
		return rule.When.Field
	}
	return groupField
}

func joinReasons(reasons []string) string {
	out := ""
	for i, reason := range reasons {
		if i > 0 {
			out += ","
		}
		out += reason
	}
	return out
}

func containsString(items []string, item string) bool {
	for _, v := range items {
		if v == item {
			return true
		}
	}
	return false
}
