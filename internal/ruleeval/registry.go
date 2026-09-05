package ruleeval

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// Intent kinds — the four standing decisions an agent, a CLI or a rule can push
// at the runtime. They are the registry's own vocabulary, identical on every
// robot, and name nothing plant-specific.
const (
	KindRunOnce      = "run-once"
	KindStandingGoal = "standing-goal"
	KindSetMode      = "set-mode"
	KindInhibit      = "inhibit"
)

// Origins — where a request came from. It is RECORDED, never consulted: a rule
// firing and an agent's tool call are the same act, and if the two paths judged
// it differently the drift would be discovered by the robot.
const (
	OriginRule  = "rule"
	OriginAgent = "agent"
)

// Registry event names, published through TickContext.Emit.
const (
	EventApplied = "intent.applied"
	EventBlocked = "intent.blocked"
)

// ErrNoEvaluator is returned by SetMode when no evaluator is wired: there is
// nothing live to swap, and pretending otherwise would be a silent no-op.
var ErrNoEvaluator = errors.New("ruleeval: no evaluator is wired to this registry")

// Request is one admission asked of the registry, whatever pushed it.
//
// Origin and RuleID are PROVENANCE ONLY. Nothing about how a request arrived
// changes how it is judged — that is the whole point of there being one
// registry (microduck's intents.py, "Why one registry").
type Request struct {
	// Name is the admitted behavior's library name. Empty means "the action's
	// own name", which is what a rule fire uses.
	Name string
	// Action is the vocabulary action to run.
	Action string
	// Params overrides the action's declared knobs.
	Params map[string]float64
	// DurationS caps the admitted behavior's lifetime. nil means "the action's
	// own bound": its longest declared trajectory for a one-shot, and no bound
	// at all for a looping action (which only a standing goal may ask for).
	DurationS *float64
	// Loops asks for a repeating admission. A standing goal sets it; a rule
	// fire inherits it from the vocabulary.
	Loops bool
	// Class is the contention class. Empty means tick.ClassStoppable.
	Class tick.StopClass

	Origin string
	RuleID string
}

// Admission is the outcome of one admission attempt. A refusal is a VALUE, not
// an error return, so a driver applying many requests is never derailed by one
// bad one; Reason always names why.
type Admission struct {
	Admitted bool
	Reason   string
	ID       string
	// Params is the RESOLVED parameter bag the behavior was admitted with —
	// the active mode's knobs layered under the request's own. It is carried
	// onto the fire/applied event so an export feed can see what the robot was
	// actually asked to do, not just which action it was asked to run.
	Params  map[string]float64
	Evicted []string
	Blocked []string
}

// Refusal reasons. ReasonInhibited is shared with rule evaluation on purpose:
// an inhibit blocks a rule fire and an injected intent with the same word.
const (
	ReasonAdmitted = "admitted"
	ReasonInvalid  = "invalid"
)

// goal is one standing goal: re-admitted every tick until it is withdrawn.
type goal struct {
	name   string
	action string
	params map[string]float64
	class  tick.StopClass
}

// pending is a queued request that needs a TickContext to apply — everything
// that admits or evicts. State that does not (the inhibited set, the standing
// goal ledger) is applied under the mutex immediately and enforced per tick.
type pending struct {
	kind    string
	request Request
	evict   string
}

// Registry is THE single admission path.
//
// Cited from microduck-cli's microduck_cli/behavior/intents.py (KindRegistry —
// "exactly one validate, exactly one admit") crossed with reachy-mini-cli's
// reachy/behavior/intents.py (IntentDriver — the four sustained kinds). A rule
// firing and an agent asking for the same thing reach tick.TickContext.Admit
// through the SAME method here, so they cannot acquire different limits, and an
// inhibit that blocks one blocks the other by construction.
//
// Every mutator is safe to call from any goroutine. Nothing here touches a
// TickContext off the tick goroutine: work that needs one is queued and applied
// at the START of the driver's tick call.
type Registry struct {
	mu sync.Mutex

	evaluator *Evaluator
	queue     []pending
	goals     map[string]goal
	goalOrder []string
	inhibited map[string]bool
	seq       int
}

// NewRegistry returns an empty registry. Bind it to an evaluator with
// Evaluator.Registry, or hand one to Config.Registry at construction.
func NewRegistry() *Registry {
	return &Registry{
		goals:     map[string]goal{},
		inhibited: map[string]bool{},
	}
}

// RunOnce queues a single bounded admission.
//
// duration is in seconds and must be > 0. An UNBOUNDED one-shot is refused —
// a looping action admitted with no bound would hold its channels forever with
// no standing record anyone could later withdraw (the donor's
// _validated_lifetime, verbatim in intent).
func (r *Registry) RunOnce(action string, params map[string]float64, duration float64) error {
	if action == "" {
		return errors.New("ruleeval: run-once names no action — name one the robot declares")
	}
	loops := r.actionLoops(action)
	var bound *float64
	if duration > 0 {
		d := duration
		bound = &d
	} else if duration < 0 {
		return fmt.Errorf(
			"ruleeval: run-once %q was given duration_s %v — it must be > 0", action, duration)
	}
	if bound == nil && loops {
		return fmt.Errorf(
			"ruleeval: run-once %q refuses an unbounded lifetime (a looping action with no "+
				"duration would hold its channels forever) — pass a duration in seconds, "+
				"or run a bounded action", action)
	}
	r.enqueue(pending{kind: KindRunOnce, request: Request{
		Action:    action,
		Params:    cloneParams(params),
		DurationS: bound,
		Loops:     loops,
		Origin:    OriginAgent,
	}})
	return nil
}

// StandingGoal declares a goal the driver re-admits every tick until Withdraw.
//
// name is the goal's handle AND the admitted behavior's library name, so
// ctx.ActiveNames, an inhibit rule and Withdraw all speak about it with one
// word. Re-declaring a live goal with different params evicts the running
// instance so the next tick re-admits it with the new ones.
func (r *Registry) StandingGoal(name, action string, params map[string]float64) error {
	if name == "" || action == "" {
		return errors.New(
			"ruleeval: a standing goal needs both a name and an action — give it both")
	}
	next := goal{name: name, action: action, params: cloneParams(params)}
	r.mu.Lock()
	previous, existed := r.goals[name]
	r.goals[name] = next
	if !existed {
		r.goalOrder = append(r.goalOrder, name)
	}
	replaced := existed && (previous.action != action || !sameParams(previous.params, next.params))
	r.mu.Unlock()

	if replaced {
		// Evict the running instance so _sustain re-admits it THIS tick with
		// the new params, rather than leaving the old ones standing.
		r.enqueue(pending{kind: KindStandingGoal, evict: name})
	}
	return nil
}

// Withdraw clears a standing goal and evicts whatever it is sustaining. It
// reports whether a goal of that name was standing.
func (r *Registry) Withdraw(name string) bool {
	r.mu.Lock()
	_, ok := r.goals[name]
	delete(r.goals, name)
	if ok {
		kept := r.goalOrder[:0]
		for _, n := range r.goalOrder {
			if n != name {
				kept = append(kept, n)
			}
		}
		r.goalOrder = kept
	}
	r.mu.Unlock()
	if ok {
		r.enqueue(pending{kind: KindStandingGoal, evict: name})
	}
	return ok
}

// Goals lists the standing goal names in declaration order.
func (r *Registry) Goals() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.goalOrder...)
}

// SetMode swaps the wired evaluator's active mode, refusing a mode the config
// does not define. An empty name clears the active mode.
//
// It resolves synchronously — a refusal has to reach the caller that asked, not
// a tick two frames later — and the swap is guarded, so a subsequently admitted
// behavior sees the new mode's params.
func (r *Registry) SetMode(name string) error {
	r.mu.Lock()
	evaluator := r.evaluator
	r.mu.Unlock()
	if evaluator == nil {
		return ErrNoEvaluator
	}
	return evaluator.SetActiveMode(name)
}

// Inhibit adds names to the inhibited set. An inhibited name is refused
// admission from EVERY source — a rule fire, a run-once, a standing goal — and
// is evicted if it is already running.
func (r *Registry) Inhibit(names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range names {
		if name != "" {
			r.inhibited[name] = true
		}
	}
}

// Uninhibit removes names from the inhibited set. A standing goal blocked by
// one is re-admitted on the very next tick with no further call.
func (r *Registry) Uninhibit(names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range names {
		delete(r.inhibited, name)
	}
}

// Inhibitions lists the inhibited names, sorted.
func (r *Registry) Inhibitions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.inhibited))
	for name := range r.inhibited {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// blocks reports whether either token is inhibited by the registry's own set or
// by this tick's matching inhibit rules. Both a behavior's NAME and its ACTION
// are tested, so an inhibit written against either word bites.
func (r *Registry) blocks(byRules map[string]bool, name, action string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, token := range []string{name, action} {
		if token == "" {
			continue
		}
		if r.inhibited[token] || byRules[token] {
			return true
		}
	}
	return false
}

func (r *Registry) enqueue(p pending) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queue = append(r.queue, p)
}

func (r *Registry) drain() []pending {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.queue
	r.queue = nil
	return out
}

func (r *Registry) bind(e *Evaluator) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evaluator = e
}

func (r *Registry) actionLoops(action string) bool {
	r.mu.Lock()
	evaluator := r.evaluator
	r.mu.Unlock()
	if evaluator == nil {
		return false
	}
	return evaluator.actionLoops(action)
}

func (r *Registry) nextID(prefix, name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	return fmt.Sprintf("%s:%s:%d", prefix, name, r.seq)
}

// admit is THE one admission path. Rule fires and injected intents both come
// through here, which is what makes an inhibit block them alike and what keeps
// the two from acquiring different limits.
func (r *Registry) admit(
	ctx tick.TickContext, e *Evaluator, req Request, byRules map[string]bool,
) Admission {
	name := req.Name
	if name == "" {
		name = req.Action
	}
	if r.blocks(byRules, name, req.Action) {
		return Admission{Reason: ReasonInhibited}
	}
	behavior := e.build(r.nextID(e.idPrefix, name), name, req)
	result, err := ctx.Admit(behavior)
	if err != nil {
		return Admission{Reason: ReasonInvalid + ": " + err.Error()}
	}
	return Admission{
		Admitted: true,
		Reason:   ReasonAdmitted,
		ID:       behavior.ID,
		Params:   behavior.Params,
		Evicted:  result.Evicted,
		Blocked:  result.Blocked,
	}
}

// apply drains the queued intents at the start of a tick.
func (r *Registry) apply(ctx tick.TickContext, e *Evaluator, byRules map[string]bool) {
	for _, p := range r.drain() {
		if p.evict != "" {
			ctx.Evict(p.evict)
			continue
		}
		outcome := r.admit(ctx, e, p.request, byRules)
		e.observeIntent(ctx, p.kind, p.request, outcome)
	}
}

// enforce evicts anything running under an inhibited name, from ANY source —
// a rule fire, a prior intent, a standing goal. It is continuous: while an
// inhibition holds, its behaviors stay off the robot.
func (r *Registry) enforce(ctx tick.TickContext, e *Evaluator, byRules map[string]bool) {
	blocked := map[string]bool{}
	r.mu.Lock()
	for name := range r.inhibited {
		blocked[name] = true
	}
	goals := r.goalsSnapshotLocked()
	r.mu.Unlock()
	for name := range byRules {
		blocked[name] = true
	}
	if len(blocked) == 0 {
		return
	}
	// A goal named "g" running action "a" is evicted by an inhibit written
	// against EITHER word: the operator should not have to know which one the
	// goal was declared with.
	for _, g := range goals {
		if blocked[g.action] {
			blocked[g.name] = true
		}
	}

	active := map[string]bool{}
	for _, name := range ctx.ActiveNames() {
		active[name] = true
	}
	names := make([]string, 0, len(blocked))
	for name := range blocked {
		if active[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		ctx.Evict(name)
		e.dropIntent(ctx, KindInhibit, name, ReasonInhibited)
	}
}

// sustain re-admits every standing goal that is missing from the active set and
// not currently inhibited. A goal declared while inhibited is RECORDED and
// withheld; the tick the inhibition clears admits it with no further call.
func (r *Registry) sustain(ctx tick.TickContext, e *Evaluator, byRules map[string]bool) {
	r.mu.Lock()
	goals := r.goalsSnapshotLocked()
	r.mu.Unlock()
	if len(goals) == 0 {
		return
	}
	active := map[string]bool{}
	for _, name := range ctx.ActiveNames() {
		active[name] = true
	}
	for _, g := range goals {
		if active[g.name] {
			continue
		}
		req := Request{
			Name:   g.name,
			Action: g.action,
			Params: g.params,
			Loops:  true, // indefinite: "until replaced or withdrawn"
			Class:  g.class,
			Origin: OriginAgent,
		}
		outcome := r.admit(ctx, e, req, byRules)
		e.observeIntent(ctx, KindStandingGoal, req, outcome)
	}
}

func (r *Registry) goalsSnapshotLocked() []goal {
	out := make([]goal, 0, len(r.goalOrder))
	for _, name := range r.goalOrder {
		if g, ok := r.goals[name]; ok {
			out = append(out, g)
		}
	}
	return out
}

func cloneParams(params map[string]float64) map[string]float64 {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]float64, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}

func sameParams(a, b map[string]float64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if other, ok := b[k]; !ok || other != v {
			return false
		}
	}
	return true
}

// nowSeconds renders a tick's clock reading for an event payload. Seconds since
// the epoch keeps the unit uniform with every other number this package emits.
func nowSeconds(now time.Time) float64 {
	return float64(now.UnixNano()) / float64(time.Second)
}
