// Package rules is the behavior-rules schema — data-only react/inhibit rules,
// modes, and the layered loader that merges them. It carries no evaluation
// logic: interpreting a Predicate against a live sense snapshot, applying
// cooldown/hysteresis timing, and actually running or disabling behaviors are
// somebody else's job. This package parses, validates, and merges. Nothing else.
//
// A rules file is TOML with four sections:
//
//   - schema_version — a required top-level int, 1 or 2;
//   - [[react]] rules — when a predicate over the live sense snapshot holds,
//     run a named action with optional parameter overrides;
//   - [[inhibit]] rules — when a predicate holds, disable a named set of
//     actions;
//   - [modes.<name>] — named, purely declarative parameter sets, one of which
//     is selected as the file's active_mode.
//
// A rule entry carrying enabled = false is a TOMBSTONE: it contributes no rule
// of its own and DISABLES the rule of that id contributed by a lower layer. id
// is the only field a tombstone needs, so disabling a shipped rule is a
// one-line edit in an overlay rather than a fork of its body.
//
// Validation is FAIL-CLOSED throughout. An unknown field at any level, an
// out-of-range number, a non-finite number, a looping action with no bound —
// each is REFUSED, never clamped, coerced, or truncated, and every message
// names the offending rule id and the file it came from. A robot that quietly
// reinterprets a bad rule is worse than one that says no.
//
// Schema versions:
//
//   - v1 — a predicate is exactly one leaf: { field, op, value }. There is no
//     conjunction, so a v1 rule is by construction keyed on ONE signal.
//   - v2 — a predicate may additionally be a group: { all = [...] } or
//     { any = [...] }, nesting one level (an all inside an any, or vice
//     versa). Deeper nesting is refused: a rule nobody can read at a glance is
//     a rule an operator cannot safely override.
//
// A v1 file using all/any is refused naming the version, so a file cannot
// silently mean something different from what it declares.
package rules

// Schema versions this package accepts.
const (
	SchemaVersion1 = 1
	SchemaVersion2 = 2
)

// Rule kinds.
const (
	KindReact   = "react"
	KindInhibit = "inhibit"
)

// Event priorities, urgencies and voice hints, the donors' verbatim names
// (reachy_nova's config/nervous-system/rules.yaml). These are ROUTING
// METADATA — an event never feeds arbitration.
const (
	PriorityLow      = "LOW"
	PriorityNormal   = "NORMAL"
	PriorityHigh     = "HIGH"
	PriorityCritical = "CRITICAL"

	UrgencyBackground = "BACKGROUND"
	UrgencyDeferrable = "DEFERRABLE"
	UrgencyNow        = "NOW"
	UrgencyImmediate  = "IMMEDIATE"

	// VoiceSilent/VoiceBrief/VoiceFree still always route an event; only
	// VoiceNone is recorded without ever being delivered (see internal/events).
	VoiceSilent = "silent"
	VoiceBrief  = "brief"
	VoiceFree   = "free"
	VoiceNone   = "none"
)

// DefaultVoice is the voice hint an entry gets when it names none.
const DefaultVoice = VoiceFree

// DefaultLLMEvaluate is llm_evaluate's default when an entry names none.
const DefaultLLMEvaluate = true

var (
	priorities = set(PriorityLow, PriorityNormal, PriorityHigh, PriorityCritical)
	urgencies  = set(UrgencyBackground, UrgencyDeferrable, UrgencyNow, UrgencyImmediate)
	voices     = set(VoiceSilent, VoiceBrief, VoiceFree, VoiceNone)
)

// Schema defaults, verbatim from both donors.
const (
	DefaultCooldownS  = 5.0
	DefaultHysteresis = 0.0

	// MaxSayChars is the hard ceiling on a react rule's say text. Bounded
	// fail-closed (refused, never truncated): a rules file is operator data,
	// and an actuator with a real-world cost — seconds of the robot holding
	// the room — must not be handed an unbounded one.
	MaxSayChars = 500
)

// Predicate ops, by comparator class. The names are the donors' verbatim.
var (
	// orderedOps require a finite, non-negative numeric value.
	orderedOps = set("lt", "gt", "ge", "le")
	// equalityOps require a value — any scalar.
	equalityOps = set("eq", "ne")
	// booleanOps take NO value.
	booleanOps = set("is_true", "is_false")
	// durationOps — "has this field been absent for at least N seconds".
	durationOps = set("absent_for")
)

// Comparators is the full set of valid predicate ops.
var Comparators = union(orderedOps, equalityOps, booleanOps, durationOps)

// Vocabulary is the OPTIONAL robot-specific name check. A nil Vocabulary means
// names go unchecked, which is what lets a `rules check` verb validate a file's
// shape with no robot present. The interface is declared here, and takes no
// argument this package does not already own, so nothing else has to be
// imported to satisfy it.
type Vocabulary interface {
	// HasField reports whether name is a sense field a predicate may test.
	HasField(name string) bool
	// HasAction reports whether name is an action a rule may run or disable.
	HasAction(name string) bool
	// ActionLoops reports whether the named action loops — a looping action
	// admitted with no duration_s would hold its channel forever, so a react
	// rule running one is refused unless it carries a bound of its own.
	ActionLoops(name string) bool
	// ActionParam reports the accepted range of one of the action's declared
	// parameters. ok is false when the action declares no such parameter.
	ActionParam(action, param string) (min, max float64, ok bool)
}

// Predicate is a data-only perception predicate: either a LEAF over one sense
// field (Field/Op/Value) or a GROUP (All or Any) in schema v2. It is DATA,
// never a string of code — only an evaluator ever interprets it.
type Predicate struct {
	// Field is the sense-snapshot field this leaf tests. Empty on a group.
	Field string
	// Op is one of Comparators. Empty on a group.
	Op string
	// Value is the operand: nil for is_true/is_false, a float64 for the
	// ordered and duration ops, and a string/bool/float64 for eq/ne.
	Value any
	// All holds the children of an { all = [...] } group; every one must hold.
	All []Predicate
	// Any holds the children of an { any = [...] } group; one must hold.
	Any []Predicate
}

// IsLeaf reports whether p is a Field/Op/Value leaf rather than a group.
func (p Predicate) IsLeaf() bool { return len(p.All) == 0 && len(p.Any) == 0 }

// Rule is one validated rule — react or inhibit, discriminated by Kind.
type Rule struct {
	// ID is the rule's identity and its OVERRIDE SURFACE: an operator
	// overrides or tombstones a shipped rule by id, so an id is a public
	// interface and renaming one orphans every overlay entry naming it.
	ID string
	// Kind is KindReact or KindInhibit.
	Kind string
	// When is the predicate that must hold for the rule to fire.
	When Predicate
	// Run is the action a react rule admits. Empty on an inhibit rule.
	Run string
	// Disable names the actions an inhibit rule disables, sorted. Empty on a
	// react rule.
	Disable []string
	// Params overrides the run action's declared parameters.
	Params map[string]float64
	// CooldownS is the minimum seconds between two firings of this rule.
	CooldownS float64
	// Hysteresis is the anti-flap margin around a threshold.
	Hysteresis float64
	// DurationS bounds the admitted behavior's lifetime, react-only. nil means
	// "no bound of its own"; the action's own default applies.
	DurationS *float64
	// Say is the optional text a react rule speaks, at most MaxSayChars.
	Say string
	// Source is the path of the file this rule was read from — carried so a
	// downstream refusal or diagnostic can still name where a rule came from
	// after layers have merged.
	Source string
}

// Event is one validated [[event]] entry — a source/type-keyed routing
// record (priority, urgency, dedupe window key, inject template and voice
// hint) for the event dialect. It is DATA ONLY: internal/events is the only
// package that interprets it, and it never feeds arbitration.
type Event struct {
	// Source is the event source half of the "source/type" identity, e.g.
	// "tracking", "rule", "slack".
	Source string
	// Type is the event type half of the "source/type" identity.
	Type string
	// Priority is one of the Priority* constants.
	Priority string
	// Urgency is one of the Urgency* constants.
	Urgency string
	// LLMEvaluate is carried as metadata only — no model is called here.
	LLMEvaluate bool
	// InjectTemplate is the optional "{name}"-style template a router
	// renders against the event's payload. Placeholders are opaque text to
	// this package.
	InjectTemplate string
	// Voice is one of the Voice* constants, defaulting to VoiceFree.
	Voice string
	// Sense optionally names the sensory class this event belongs to —
	// classification metadata only, never consulted for dedupe.
	Sense string
	// Dedupe optionally overrides the dedupe identity a router collapses
	// repeats under. Empty means "use source/type".
	Dedupe string
	// Path is the file this event entry was read from.
	Path string
}

// ID is the event's identity and override surface, exactly like a Rule's ID:
// same-layer duplicates are refused, a later layer overrides earlier one
// wholesale, and enabled = false tombstones it.
func (e Event) ID() string { return e.Source + "/" + e.Type }

// EventDefault is the [event_default] table: the priority/urgency/
// llm_evaluate/voice assigned to any event with no matching [[event]] entry.
type EventDefault struct {
	Priority    string
	Urgency     string
	LLMEvaluate bool
	Voice       string
}

// Config is a fully validated, fully merged set of rules.
type Config struct {
	// SchemaVersion is the version declared by the HIGHEST layer that
	// contributed a file — layers may mix versions, each file validated
	// against the version it declares.
	SchemaVersion int
	// ActiveMode names the selected mode, or is empty when none is selected.
	ActiveMode string
	// Modes maps a mode name to its flat parameter bag.
	Modes map[string]map[string]float64
	// React holds the react rules, in merged order.
	React []Rule
	// Inhibit holds the inhibit rules, in merged order.
	Inhibit []Rule
	// Disabled holds the tombstoned ids that no layer defines a live rule for
	// — carried so a still-higher layer's merge can honor them.
	Disabled []string
	// Events holds the merged [[event]] entries, in merged order.
	Events []Event
	// EventDefault is the merged [event_default] table, or nil when no layer
	// defined one.
	EventDefault *EventDefault
}

// ActiveModeParams returns the selected mode's parameters, and whether a mode
// is selected at all.
func (c *Config) ActiveModeParams() (map[string]float64, bool) {
	if c == nil || c.ActiveMode == "" {
		return nil, false
	}
	params, ok := c.Modes[c.ActiveMode]
	return params, ok
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func union(sets ...map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, s := range sets {
		for k := range s {
			out[k] = true
		}
	}
	return out
}
