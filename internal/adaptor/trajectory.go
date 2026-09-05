package adaptor

import (
	"fmt"
	"math"
	"strings"
)

// Keyframe is one sampled point of a trajectory: Value at T seconds of
// behavior-local time. Value's length is the claimed channel's arity.
type Keyframe struct {
	T     float64   `json:"t"`
	Value []float64 `json:"value"`
}

// Easing is the closed-form alternative to a keyframe list: go From to To over
// DurationS seconds with the named shape. It exists because most real motion is
// one eased move, and spelling that as two keyframes plus an interpolation rule
// loses the intent.
type Easing struct {
	Kind      EasingKind `json:"kind"`
	From      []float64  `json:"from"`
	To        []float64  `json:"to"`
	DurationS float64    `json:"duration_s"`
}

// Trajectory is how one action drives one claimed channel over behavior-local
// time. It is exactly one of a keyframe list (linearly interpolated) or an
// Easing — never both, never neither.
//
// A Trajectory is immutable once the vocabulary loads, and At is pure, which is
// what makes motion reproducible and the whole core unit-testable without
// hardware: the same t_local always yields the same value, so a tick can be
// replayed off-robot.
type Trajectory struct {
	Keyframes []Keyframe `json:"keyframes,omitempty"`
	Easing    *Easing    `json:"easing,omitempty"`

	// Filled in at load from the owning action and channel. Unexported so a
	// decoded-but-unvalidated Trajectory can never be sampled by accident.
	loops    bool
	duration float64
	arity    int
}

// Duration is the trajectory's length in seconds: the last keyframe's t, or the
// easing's duration_s. A single-keyframe trajectory has duration 0 — it is a
// constant.
func (tr *Trajectory) Duration() float64 { return tr.duration }

// Arity is the number of values every sample carries; it equals the claimed
// channel's arity.
func (tr *Trajectory) Arity() int { return tr.arity }

// At samples the trajectory at tLocal seconds of behavior-local time.
//
// It is pure and total: the returned slice is freshly allocated (so a caller
// may keep or mutate it), the trajectory is never modified, and no input
// panics. Before the start it holds the first value; PAST THE END it holds the
// last value rather than snapping to neutral — an action whose lifetime outlives
// its trajectory settles rather than jumping. An action declared `loops` wraps
// by Duration instead, so t = duration is the start of the next cycle.
func (tr *Trajectory) At(tLocal float64) []float64 {
	t := tLocal
	if math.IsNaN(t) {
		// A NaN clock is a caller bug, but the tick thread must not panic on
		// one: treat it as the start of the trajectory.
		t = 0
	}
	if tr.loops && tr.duration > 0 {
		t = math.Mod(t, tr.duration)
		if t < 0 {
			t += tr.duration
		}
	}
	if t < 0 {
		t = 0
	}
	if tr.Easing != nil {
		return tr.sampleEasing(t)
	}
	return tr.sampleKeyframes(t)
}

func (tr *Trajectory) sampleKeyframes(t float64) []float64 {
	frames := tr.Keyframes
	last := frames[len(frames)-1]
	if t >= last.T {
		return cloneValues(last.Value)
	}
	for i := 0; i+1 < len(frames); i++ {
		a, b := frames[i], frames[i+1]
		if t < a.T {
			return cloneValues(a.Value)
		}
		if t < b.T {
			span := b.T - a.T
			if span <= 0 {
				// Two keyframes at the same t are a deliberate STEP; the later
				// one wins, which is what a discrete channel needs.
				return cloneValues(b.Value)
			}
			return lerp(a.Value, b.Value, (t-a.T)/span)
		}
	}
	return cloneValues(last.Value)
}

func (tr *Trajectory) sampleEasing(t float64) []float64 {
	e := tr.Easing
	if t >= e.DurationS {
		return cloneValues(e.To)
	}
	u := t / e.DurationS
	switch e.Kind {
	case EasingHold:
		return cloneValues(e.From)
	case EasingEaseInOut:
		// Cosine ease: zero velocity at both endpoints, symmetric about the
		// midpoint, so a servo neither snaps at the start nor overshoots.
		u = 0.5 * (1 - math.Cos(math.Pi*u))
	case EasingLinear:
	}
	return lerp(e.From, e.To, u)
}

// compile validates the trajectory against its claimed channel and freezes the
// derived fields. Every refusal names both the action and the channel, because
// "a value has the wrong arity" is useless without knowing where.
func (tr *Trajectory) compile(origin string, a *Action, channel string, arity int) error {
	where := fmt.Sprintf("action %q channel %q", a.Name, channel)
	hasKeyframes := len(tr.Keyframes) > 0
	hasEasing := tr.Easing != nil

	switch {
	case hasKeyframes && hasEasing:
		return newError(origin, where+" declares both keyframes and an easing",
			"keep exactly one of them")
	case !hasKeyframes && !hasEasing:
		return newError(origin, where+" declares neither keyframes nor an easing",
			"give it a non-empty keyframes list, or an easing")
	}

	tr.loops = a.Loops
	tr.arity = arity

	if hasEasing {
		return tr.compileEasing(origin, where, arity)
	}
	return tr.compileKeyframes(origin, where, arity)
}

func (tr *Trajectory) compileEasing(origin, where string, arity int) error {
	e := tr.Easing
	if !isValidEasingKind(e.Kind) {
		return newError(origin,
			fmt.Sprintf("%s declares easing kind %q", where, string(e.Kind)),
			"use one of: "+joinEasingKinds())
	}
	for label, values := range map[string][]float64{"from": e.From, "to": e.To} {
		if len(values) != arity {
			return newError(origin,
				fmt.Sprintf("%s easing %q carries %d values but the channel arity is %d",
					where, label, len(values), arity),
				fmt.Sprintf("give it exactly %d values", arity))
		}
		for i, value := range values {
			if !isFinite(value) {
				return newError(origin,
					fmt.Sprintf("%s easing %q index %d is %v", where, label, i, value),
					"every trajectory value must be a finite number")
			}
		}
	}
	if !isFinite(e.DurationS) || e.DurationS <= 0 {
		return newError(origin,
			fmt.Sprintf("%s declares duration_s %v", where, e.DurationS),
			"an easing needs a finite duration_s greater than zero")
	}
	tr.duration = e.DurationS
	return nil
}

func (tr *Trajectory) compileKeyframes(origin, where string, arity int) error {
	if tr.Keyframes[0].T != 0 {
		return newError(origin,
			fmt.Sprintf("%s starts at t = %v", where, tr.Keyframes[0].T),
			"the first keyframe must be at t = 0")
	}
	previous := math.Inf(-1)
	for i, frame := range tr.Keyframes {
		if !isFinite(frame.T) || frame.T < 0 {
			return newError(origin,
				fmt.Sprintf("%s keyframe %d is at t = %v", where, i, frame.T),
				"every keyframe t must be a finite number at or above zero")
		}
		if frame.T < previous {
			return newError(origin,
				fmt.Sprintf("%s keyframe %d goes back to t = %v after %v",
					where, i, frame.T, previous),
				"keyframe times must be non-decreasing")
		}
		previous = frame.T
		if len(frame.Value) != arity {
			return newError(origin,
				fmt.Sprintf("%s keyframe %d carries %d values but the channel arity is %d",
					where, i, len(frame.Value), arity),
				fmt.Sprintf("give every keyframe exactly %d values", arity))
		}
		for j, value := range frame.Value {
			if !isFinite(value) {
				return newError(origin,
					fmt.Sprintf("%s keyframe %d index %d is %v", where, i, j, value),
					"every trajectory value must be a finite number")
			}
		}
	}
	tr.duration = tr.Keyframes[len(tr.Keyframes)-1].T
	return nil
}

func lerp(from, to []float64, u float64) []float64 {
	out := make([]float64, len(from))
	for i := range from {
		out[i] = from[i] + (to[i]-from[i])*u
	}
	return out
}

func cloneValues(values []float64) []float64 {
	out := make([]float64, len(values))
	copy(out, values)
	return out
}

func isValidEasingKind(k EasingKind) bool {
	for _, valid := range validEasingKinds() {
		if k == valid {
			return true
		}
	}
	return false
}

func joinEasingKinds() string {
	names := make([]string, 0, len(validEasingKinds()))
	for _, k := range validEasingKinds() {
		names = append(names, string(k))
	}
	return strings.Join(names, ", ")
}
