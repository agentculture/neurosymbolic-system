// Package sense is the engine-owned per-tick perception snapshot.
//
// Cited (cite-don't-import) from reachy-mini-cli's reachy/behavior/sense.py —
// its Sense dataclass and SenseProviders bundle — with one deliberate change of
// ownership. In the donor, a snapshot is rebuilt each tick by calling a bundle
// of injected zero-arg PEEK callables; here the snapshot is a mutable, mutex-
// guarded object a transport goroutine FEEDS and the tick goroutine READS. The
// peek property that mattered survives: a read never consumes, so any number of
// consumers reading the same tick see the same sample and never steal from one
// another (spec c35).
//
// The frame contract, and why it is a partial update:
//
//   - A frame (the map handed to Update) sets a value and stamps a last-seen
//     for every field it names.
//   - A field the frame does NOT name is LEFT AS IS — the donor peek semantics.
//     A transport that publishes one sensor at its own rate must not blank out
//     every other sensor by omission.
//   - A field the frame carries as nil CLEARS it. That is the one way a
//     transport says "this reading is gone", and it is deliberately explicit:
//     absence has to be stated, never inferred from a frame's silence.
//   - Clearing a field keeps its last-seen stamp, so absence has a measurable
//     length — which is exactly what an absent_for predicate reads.
//
// Freshness is therefore derivable rather than pushed: View adds a
// "<field>_age_s" entry for every field with a last-seen, computed ONCE per
// call, so everything reading one tick agrees about how old a reading was. A
// transport that measures its own freshness and feeds "<field>_age_s" as a real
// field wins over the derived value: the robot measured it, and overwriting a
// measurement with arithmetic would be reinterpreting a reading.
//
// Nothing here blocks a tick. No frame for N ticks is a STALE snapshot with
// growing ages, never a stalled loop: View is a read under an RWMutex held for
// the length of a map copy and nothing else.
//
// Stdlib only. It imports no transport, no SDK, and no rules or tick package —
// the composition root is what wires a reader to Update.
package sense

import (
	"sync"
	"time"
)

// AgeSuffix is appended to a field name to form its derived freshness field.
// It is a SUFFIX, not a robot name: the plant declares the fields, and this
// package only declares how their ages are spelled.
const AgeSuffix = "_age_s"

// SenseSink is the narrow write half of a Snapshot — everything a transport
// task needs and nothing else. A stream task consumes only this interface, so
// it can never read, reshape, or reinterpret the perception it feeds.
type SenseSink interface {
	Update(fields map[string]any, now time.Time)
}

// Snapshot is the live perception a tick reads. The zero value is not usable;
// build one with New.
type Snapshot struct {
	mu       sync.RWMutex
	values   map[string]any
	lastSeen map[string]time.Time
}

// New returns an empty Snapshot: every field absent, no field ever seen.
func New() *Snapshot {
	return &Snapshot{
		values:   map[string]any{},
		lastSeen: map[string]time.Time{},
	}
}

// Update applies one frame at now. See the package docstring for the contract:
// a named field is set and stamped, a nil-valued field is cleared (keeping its
// stamp), and an unnamed field is left exactly as it was.
func (s *Snapshot) Update(fields map[string]any, now time.Time) {
	if s == nil || len(fields) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, value := range fields {
		if value == nil {
			// Clearing keeps the last-seen stamp on purpose: "gone for 3 s" is
			// a different fact from "never here", and absent_for needs both.
			delete(s.values, name)
			continue
		}
		s.values[name] = value
		s.lastSeen[name] = now
	}
}

// Get returns the field's current value. ok is false when the field has never
// been fed or was cleared by a nil frame — a missing field is nil, never a
// zero value that a predicate could mistake for a reading.
func (s *Snapshot) Get(field string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.values[field]
	return value, ok
}

// AgeS is how many seconds ago the field was last seen with a value. ok is
// false when it never was. A cleared field still reports an age, and that age
// keeps growing — the length of its absence.
func (s *Snapshot) AgeS(field string, now time.Time) (float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	at, ok := s.lastSeen[field]
	if !ok {
		return 0, false
	}
	return now.Sub(at).Seconds(), true
}

// LastSeen is when the field last carried a value, and whether it ever did.
func (s *Snapshot) LastSeen(field string) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	at, ok := s.lastSeen[field]
	return at, ok
}

// View is this tick's read-only copy: every live value, plus a
// "<field>_age_s" float for every field with a last-seen, computed once at now
// so no two readers of the same tick disagree about freshness.
//
// The returned map is the caller's own; mutating it cannot reach the snapshot.
func (s *Snapshot) View(now time.Time) map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]any, len(s.values)+len(s.lastSeen))
	for name, value := range s.values {
		out[name] = value
	}
	for name, at := range s.lastSeen {
		key := name + AgeSuffix
		if _, fed := out[key]; fed {
			// A transport that measures its own freshness wins: never
			// overwrite a measurement with arithmetic.
			continue
		}
		out[key] = now.Sub(at).Seconds()
	}
	return out
}
