// Package tick is the engine's tick core: one loop, one owner per channel, one
// complete pose per tick, and exactly one integration seam.
//
// Cited (cite-don't-import) from reachy-mini-cli's reachy/behavior/engine.py +
// reachy/behavior/arbitration.py + reachy/behavior/model.py, checked against
// microduck-cli's microduck_cli/behavior/{model,engine}.py. Every tick:
//
//  1. drain the bounded inbox (the ONLY way another goroutine mutates state);
//  2. drop behaviors whose lifetime has elapsed;
//  3. ask every live behavior for its contribution exactly once;
//  4. arbitrate a single owner per channel by (class priority, recency),
//     skipping a claimant that abstained on that channel this tick;
//  5. compose a COMPLETE pose — an unclaimed channel falls to the adaptor's
//     declared neutral, so the target is never partial;
//  6. write it to the Sink exactly once;
//  7. call the installed TickSeam exactly once, AFTER the write;
//  8. account the tick against the budget, then wait for the next tick.
//
// Two structural rules hold this package's shape, and both were paid for on
// hardware by the donor:
//
//   - The seam is the only integration point. Rules, sense drivers, export
//     feeds and metrics are pure CONSUMERS of TickSeam. Nothing here imports
//     rule evaluation or a transport, and nothing here may import a consumer.
//     The donor added features for a year without engine.py changing.
//   - No robot name is compiled in. Channels, actions and their trajectories
//     come from an adaptor.Vocabulary handed in at construction; the engine
//     never spells one. internal/adaptor's donor-literal guard scans these
//     sources and fails the build on a leak.
//
// Time is injected (Clock + Ticker). No wall-clock read happens anywhere in
// the loop — realclock.go is the only file in this package allowed to name
// time.Now/time.Since/time.Sleep/time.Tick, and TestNoWallClockInLoop
// enforces that.
//
// Units are the adaptor's friendly ones, unchanged: nothing here converts.
package tick

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
)

// StopClass is how a behavior contends for the channels it claims.
//
// The four-class model is the donor's, unchanged:
//
//   - ClassPassive — never removes anything, and owns a channel only when no
//     non-passive behavior claims it. The base layer is passive.
//   - ClassStoppable — drives, but is removed by a newly admitted stopping
//     behavior on a shared channel. The polite default.
//   - ClassStopping — on admit, evicts the stoppable behaviors it shares a
//     channel with and takes over.
//   - ClassUnstoppable — highest priority: owns its channels while it is alive
//     and is never removed by an admission, so it holds until it finishes.
type StopClass string

// The four contention classes.
const (
	ClassPassive     StopClass = "passive"
	ClassStoppable   StopClass = "stoppable"
	ClassStopping    StopClass = "stopping"
	ClassUnstoppable StopClass = "unstoppable"
)

// priority is the tick-time rank; higher wins a contested channel.
//
// Unstoppable and stopping both hold a channel against a newcomer; unstoppable
// ranks highest so it also wins a same-tick contest. Stoppable drives but
// yields, and passive only fills a channel nobody else claims.
var priority = map[StopClass]int{
	ClassPassive:     0,
	ClassStoppable:   1,
	ClassStopping:    2,
	ClassUnstoppable: 3,
}

// Priority is the class's tick-time rank. An unrecognized class ranks with
// passive: arbitration is a pure function that must be total, and Bind is
// where an unknown class is actually refused.
func (c StopClass) Priority() int { return priority[c] }

// Valid reports whether c is one of the four declared classes.
func (c StopClass) Valid() bool {
	_, ok := priority[c]
	return ok
}

// classNames lists the four classes in priority order, for error messages.
func classNames() []string {
	return []string{
		string(ClassPassive), string(ClassStoppable),
		string(ClassStopping), string(ClassUnstoppable),
	}
}

// Lifetime is how long a behavior runs.
//
// A one-shot behavior (Loops false) needs a finite DurationS greater than zero
// and expires once that much behavior-local time has elapsed. A looping
// behavior with a nil DurationS runs until it is evicted; with a duration it
// repeats until the duration elapses.
type Lifetime struct {
	DurationS *float64
	Loops     bool
}

// IsExpired reports whether a finite lifetime has elapsed at tLocal seconds of
// behavior-local time. A looping-forever lifetime never expires.
func (l Lifetime) IsExpired(tLocal float64) bool {
	return l.DurationS != nil && tLocal >= *l.DurationS
}

// Contribution is one behavior's desired values at one instant, keyed by
// channel name.
//
// A claimed channel that is ABSENT from the map (or carries a nil slice) is an
// ABSTENTION for this tick: arbitration skips that claimant for that channel,
// so the channel falls through to the next-priority claimant rather than being
// frozen by a behavior with nothing to say. A sound-reactive behavior with no
// sound yields the channel back to the base layer instead of holding it.
type Contribution map[string][]float64

// Behavior is one live behavior: an immutable claim/contention spec plus the
// trajectories it drives its claimed channels with.
//
// The engine assigns ID (as "<Name>-<n>") when it is empty. Action is an
// OPAQUE vocabulary name — the engine never interprets it, which is exactly
// what lets the same engine run one robot's action library and another's.
type Behavior struct {
	Name     string
	ID       string
	Class    StopClass
	Channels []string
	Lifetime Lifetime
	Action   string
	Params   map[string]float64

	// AdmittedAt is the behavior-local time origin. The engine fills it with
	// the admitting tick's clock reading when it is zero.
	AdmittedAt time.Time

	// Trajectories is filled by Bind from the vocabulary's declaration of
	// Action, keyed by channel. It is read-only after Bind and safe to sample
	// from the tick goroutine: a Trajectory is immutable once its vocabulary
	// loaded, and At is pure.
	Trajectories map[string]*adaptor.Trajectory

	// Contribute, when non-nil, REPLACES trajectory sampling for this
	// behavior. It is the sensor-driven exception to purity — the donor's
	// wants_sense entries — and the only way a behavior abstains on a channel
	// it claims: omit the channel from the returned map. It is called on the
	// tick goroutine, exactly once per tick, and must not block.
	Contribute func(tLocal float64) Contribution
}

// Claims reports whether this behavior claims the named channel.
func (b Behavior) Claims(channel string) bool {
	for _, ch := range b.Channels {
		if ch == channel {
			return true
		}
	}
	return false
}

// Contribution samples this behavior at tLocal seconds of behavior-local time.
//
// It is pure whenever Contribute is nil: the same tLocal always yields the same
// values, which is what makes motion reproducible and a tick replayable
// off-robot. Only claimed channels appear in the result; a claimed channel
// with no trajectory (and no Contribute entry) is an abstention.
func (b Behavior) Contribution(tLocal float64) Contribution {
	var raw Contribution
	if b.Contribute != nil {
		raw = b.Contribute(tLocal)
	}
	out := make(Contribution, len(b.Channels))
	for _, ch := range b.Channels {
		if b.Contribute != nil {
			if values, ok := raw[ch]; ok && values != nil {
				out[ch] = cloneValues(values)
			}
			continue
		}
		if traj := b.Trajectories[ch]; traj != nil {
			out[ch] = traj.At(tLocal)
		}
	}
	return out
}

// Bind validates a behavior against a vocabulary and fills in what the
// vocabulary declares: the claimed channels (from the action, when the
// behavior names none) and the trajectory for each claimed channel.
//
// It is FAIL-CLOSED, like the rest of the runtime: an undeclared action, an
// undeclared channel, an out-of-domain param or a lifetime that cannot end is
// REFUSED, never repaired. A behavior the engine quietly reinterpreted would
// be worse than one it declined to admit.
//
// A behavior carrying its own Contribute needs no action, so a consumer can
// admit a sensor-driven behavior the vocabulary has no trajectory for; its
// channels are still checked against the vocabulary.
func Bind(v *adaptor.Vocabulary, b Behavior) (Behavior, error) {
	if v == nil {
		return b, fmt.Errorf("tick: a behavior cannot be bound without a vocabulary")
	}
	if !b.Class.Valid() {
		return b, fmt.Errorf(
			"tick: behavior %q declares contention class %q — use one of: %v",
			b.Name, string(b.Class), classNames())
	}

	action, err := bindAction(v, &b)
	if err != nil {
		return b, err
	}
	if err := bindChannels(v, &b, action); err != nil {
		return b, err
	}
	if err := bindLifetime(v, &b); err != nil {
		return b, err
	}
	return b, nil
}

// bindAction resolves the behavior's action in the vocabulary, defaults its
// name, and validates its params. It returns nil for a Contribute-driven
// behavior that names no action.
func bindAction(v *adaptor.Vocabulary, b *Behavior) (*adaptor.Action, error) {
	if b.Action == "" {
		if b.Contribute == nil {
			return nil, fmt.Errorf(
				"tick: behavior %q names no action — give it an action this robot "+
					"declares, or a Contribute function", b.Name)
		}
		if b.Name == "" {
			return nil, fmt.Errorf(
				"tick: a behavior needs a name when it names no action — give it one")
		}
		return nil, nil
	}
	if !v.HasAction(b.Action) {
		return nil, fmt.Errorf(
			"tick: behavior %q runs action %q, which %s does not declare — declare it, "+
				"or run a declared action", b.Name, b.Action, v.Origin())
	}
	if b.Name == "" {
		b.Name = b.Action
	}
	if len(b.Params) > 0 {
		if err := v.ValidateParams(b.Action, b.Params); err != nil {
			return nil, fmt.Errorf("tick: behavior %q: %w", b.Name, err)
		}
	}
	actions := v.Actions()
	for i := range actions {
		if actions[i].Name == b.Action {
			// The Action value is a copy, but its Trajectories map is shared
			// with the vocabulary and immutable after load — which is exactly
			// what makes it safe to sample from the tick goroutine.
			return &actions[i], nil
		}
	}
	return nil, fmt.Errorf("tick: behavior %q: action %q vanished from the vocabulary",
		b.Name, b.Action)
}

// bindChannels defaults the claimed channels to the action's claims, refuses an
// undeclared or duplicated channel, and attaches each claimed channel's
// trajectory.
func bindChannels(v *adaptor.Vocabulary, b *Behavior, action *adaptor.Action) error {
	if len(b.Channels) == 0 {
		if action == nil {
			return fmt.Errorf(
				"tick: behavior %q claims no channel — list the channels it drives",
				b.Name)
		}
		b.Channels = append([]string(nil), action.Claims...)
	}
	declared := make(map[string]bool, len(v.Channels()))
	for _, ch := range v.Channels() {
		declared[ch.Name] = true
	}
	seen := make(map[string]bool, len(b.Channels))
	for _, ch := range b.Channels {
		if !declared[ch] {
			return fmt.Errorf(
				"tick: behavior %q claims channel %q, which %s does not declare — "+
					"claim a declared channel", b.Name, ch, v.Origin())
		}
		if seen[ch] {
			return fmt.Errorf("tick: behavior %q claims channel %q twice — list it once",
				b.Name, ch)
		}
		seen[ch] = true
	}

	if action == nil {
		return nil
	}
	trajectories := make(map[string]*adaptor.Trajectory, len(b.Channels))
	for _, ch := range b.Channels {
		if traj, ok := action.Trajectories[ch]; ok && traj != nil {
			trajectories[ch] = traj
		}
	}
	b.Trajectories = trajectories
	return nil
}

// bindLifetime refuses a lifetime that can never end and a non-finite or
// non-positive duration. A looping action with neither a duration nor Loops set
// is refused rather than silently made to loop: a lifetime the engine
// reinterpreted is a behavior nobody can predict.
func bindLifetime(v *adaptor.Vocabulary, b *Behavior) error {
	if d := b.Lifetime.DurationS; d != nil {
		if math.IsNaN(*d) || math.IsInf(*d, 0) {
			return fmt.Errorf("tick: behavior %q declares duration_s %v — it must be finite",
				b.Name, *d)
		}
		if *d <= 0 {
			return fmt.Errorf("tick: behavior %q declares duration_s %v — it must be > 0",
				b.Name, *d)
		}
		return nil
	}
	if b.Lifetime.Loops {
		return nil
	}
	hint := ""
	if b.Action != "" && v.ActionLoops(b.Action) {
		hint = " (its action declares that it loops)"
	}
	return fmt.Errorf(
		"tick: behavior %q is a one-shot with no duration_s%s — give it a duration_s, "+
			"or declare it looping", b.Name, hint)
}

// sortedStrings returns a sorted copy of names.
func sortedStrings(names []string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}

func cloneValues(values []float64) []float64 {
	out := make([]float64, len(values))
	copy(out, values)
	return out
}
