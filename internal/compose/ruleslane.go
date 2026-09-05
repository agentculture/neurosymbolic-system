package compose

import (
	"sync"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
	"github.com/agentculture/neurosymbolic-system/internal/ruleeval"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// rulesLane is the seam rider that runs rule evaluation, plus the indirection
// that makes `rules reload` possible at all.
//
// The engine is handed ONE seam before its first tick and never handed another,
// so a reload cannot swap the seam — it swaps what the seam calls. The lane
// holds the live Evaluator behind an RWMutex: the tick goroutine takes a read
// lock once per tick, and a reload takes the write lock for exactly the length
// of a pointer assignment.
//
// The contract `rules reload` promises, and the only reason this exists:
//
//	the previously active rule set STAYS in effect if the new one fails to
//	load or validate.
//
// A reload that broke a running robot because one operator typo made it into an
// overlay would be worse than the reload never happening. So the new evaluator
// is built COMPLETELY — files read, merged, checked against this robot's
// vocabulary, and compiled by ruleeval.New, which is itself fail-closed — and
// only a fully built one is ever installed.
//
// What a reload deliberately does NOT preserve: per-rule timing state. The new
// evaluator starts with every rule armed and no fire history, so a rule whose
// body changed is judged by its new timing rather than by its predecessor's
// half-elapsed one. Standing goals and inhibitions DO survive, because they
// live in the Registry, which is shared across reloads — an operator's standing
// decision is not a rules-file fact and must not be silently withdrawn by an
// edit to one.
type rulesLane struct {
	voc      *adaptor.Vocabulary
	snapshot ruleeval.Viewer
	registry *ruleeval.Registry
	log      *senselog.Logger

	mu     sync.RWMutex
	eval   *ruleeval.Evaluator
	layers [][]string
}

// newRulesLane loads the initial layer stack and compiles it. A refusal here
// is a refusal to START: an engine that came up ignoring a rules file the
// operator asked for would be a robot quietly doing less than it was told to.
func newRulesLane(
	voc *adaptor.Vocabulary, snapshot ruleeval.Viewer, registry *ruleeval.Registry,
	log *senselog.Logger, layers [][]string,
) (*rulesLane, error) {
	lane := &rulesLane{voc: voc, snapshot: snapshot, registry: registry, log: log}
	eval, err := lane.build(layers)
	if err != nil {
		return nil, err
	}
	lane.eval, lane.layers = eval, layers
	return lane, nil
}

// build reads and compiles one layer stack without installing it.
func (l *rulesLane) build(layers [][]string) (*ruleeval.Evaluator, error) {
	cfg, err := loadRules(layers, l.voc)
	if err != nil {
		return nil, err
	}
	eval, err := ruleeval.New(ruleeval.Config{
		Rules:      cfg,
		Vocabulary: l.voc,
		Snapshot:   l.snapshot,
		Registry:   l.registry,
		Logger:     l.log,
	})
	if err != nil {
		return nil, clifmt.NewUserError(err.Error(),
			"fix the rule the message names; a rule that could never end is refused "+
				"rather than admitted and left holding its channels")
	}
	return eval, nil
}

// Tick is the seam rider. It takes the read lock for the length of one pointer
// read, so a reload racing a tick can never hand the tick a half-built
// evaluator, and a slow reload can never hold the tick past its budget.
func (l *rulesLane) Tick(ctx tick.TickContext) {
	l.mu.RLock()
	eval := l.eval
	l.mu.RUnlock()
	if eval == nil {
		return
	}
	eval.Tick(ctx)
}

// Reload implements mgmt.Reloader: re-read the named files and swap the
// compiled result in, keeping the old set on ANY failure.
//
// The paths are the layer stack, one file per layer in the order given —
// exactly the spelling `run --rules a.toml --rules b.toml` uses, so an operator
// reloads with the same words they started with. Passing none re-reads the
// stack the engine started with, which is the common case: an overlay was
// edited in place and should be picked up.
func (l *rulesLane) Reload(paths []string) error {
	layers := l.layerStack(paths)
	eval, err := l.build(layers)
	if err != nil {
		// Named, not silent: a reload that was refused must be visible in the
		// log of the box it was refused on, not only in the operator's
		// terminal.
		l.log.Drop(ruleeval.StageRule, Verb, "reload", "refused", err.Error())
		return err
	}
	l.mu.Lock()
	l.eval, l.layers = eval, layers
	l.mu.Unlock()
	l.log.Stage(ruleeval.StageRule, Verb, "reload", "the new rule set is in force")
	return nil
}

// layerStack is paths as a layer stack, or the startup stack when none given.
func (l *rulesLane) layerStack(paths []string) [][]string {
	if len(paths) == 0 {
		l.mu.RLock()
		defer l.mu.RUnlock()
		return l.layers
	}
	out := make([][]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, []string{path})
	}
	return out
}

// Layers is how many rule layers are currently in force.
func (l *rulesLane) Layers() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.layers)
}

// ActiveMode is the live evaluator's active mode, or "" when none is.
func (l *rulesLane) ActiveMode() string {
	l.mu.RLock()
	eval := l.eval
	l.mu.RUnlock()
	if eval == nil {
		return ""
	}
	return eval.ActiveMode()
}
