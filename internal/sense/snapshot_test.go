package sense_test

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/sense"
)

// The toy field names every test here uses. They deliberately name nothing
// either donor robot names: a snapshot learns its fields from whatever a
// transport feeds it, and a fixture borrowing a real robot's words would make
// the package look plant-specific.
const (
	fieldLux  = "lux"
	fieldWarm = "warm"
	fieldTag  = "tag"
)

func epoch() time.Time { return time.Unix(0, 0).UTC() }

// A *Snapshot must satisfy the interface the stream task consumes, and nothing
// wider: the transport goroutine may feed it and may do nothing else to it.
func TestSnapshotIsASenseSink(t *testing.T) {
	var sink sense.SenseSink = sense.New()
	sink.Update(map[string]any{fieldLux: 1.0}, epoch())
}

func TestUpdateSetsValuesAndLastSeen(t *testing.T) {
	s := sense.New()
	now := epoch()
	s.Update(map[string]any{fieldLux: 12.5, fieldWarm: true}, now)

	value, ok := s.Get(fieldLux)
	if !ok || value != 12.5 {
		t.Fatalf("Get(%q) = (%v, %v), want (12.5, true)", fieldLux, value, ok)
	}
	age, ok := s.AgeS(fieldLux, now.Add(2*time.Second))
	if !ok || math.Abs(age-2.0) > 1e-9 {
		t.Fatalf("AgeS = (%v, %v), want (2, true)", age, ok)
	}
}

func TestUnknownFieldIsNil(t *testing.T) {
	s := sense.New()
	if value, ok := s.Get(fieldLux); ok || value != nil {
		t.Fatalf("Get on an unfed field = (%v, %v), want (nil, false)", value, ok)
	}
	if age, ok := s.AgeS(fieldLux, epoch()); ok {
		t.Fatalf("AgeS on an unfed field = (%v, true), want ok=false", age)
	}
}

// The DONOR peek semantics: a frame is a partial update, not a replacement. A
// field the frame does not mention keeps the value it had — what makes it
// stale is its growing age, not a vanished value.
func TestFieldAbsentFromAFrameIsLeftAsIs(t *testing.T) {
	s := sense.New()
	now := epoch()
	s.Update(map[string]any{fieldLux: 1.0, fieldWarm: true}, now)
	s.Update(map[string]any{fieldWarm: false}, now.Add(time.Second))

	if value, ok := s.Get(fieldLux); !ok || value != 1.0 {
		t.Fatalf("Get(%q) = (%v, %v), want (1, true) — an unmentioned field must survive",
			fieldLux, value, ok)
	}
	age, _ := s.AgeS(fieldLux, now.Add(time.Second))
	if math.Abs(age-1.0) > 1e-9 {
		t.Fatalf("AgeS(%q) = %v, want 1 — an unmentioned field's age must grow", fieldLux, age)
	}
	warmAge, _ := s.AgeS(fieldWarm, now.Add(time.Second))
	if warmAge != 0 {
		t.Fatalf("AgeS(%q) = %v, want 0 — a re-fed field's age must reset", fieldWarm, warmAge)
	}
}

// An explicit nil in the frame is the ONE way a transport says "this reading is
// gone". The last-seen stamp survives it, so absence has a measurable length.
func TestNilInAFrameClearsTheFieldButKeepsItsLastSeen(t *testing.T) {
	s := sense.New()
	now := epoch()
	s.Update(map[string]any{fieldLux: 1.0}, now)
	s.Update(map[string]any{fieldLux: nil}, now.Add(time.Second))

	if value, ok := s.Get(fieldLux); ok || value != nil {
		t.Fatalf("Get after a nil frame = (%v, %v), want (nil, false)", value, ok)
	}
	age, ok := s.AgeS(fieldLux, now.Add(3*time.Second))
	if !ok || math.Abs(age-3.0) > 1e-9 {
		t.Fatalf("AgeS after a nil frame = (%v, %v), want (3, true) — "+
			"clearing a field must not erase when it was last seen", age, ok)
	}
}

func TestNilForANeverSeenFieldStampsNothing(t *testing.T) {
	s := sense.New()
	s.Update(map[string]any{fieldLux: nil}, epoch())
	if _, ok := s.AgeS(fieldLux, epoch()); ok {
		t.Fatal("a field that was only ever cleared must have no last-seen")
	}
}

func TestViewCarriesValuesAndDerivedAges(t *testing.T) {
	s := sense.New()
	now := epoch()
	s.Update(map[string]any{fieldLux: 4.0, fieldTag: "x"}, now)

	view := s.View(now.Add(500 * time.Millisecond))
	if view[fieldLux] != 4.0 || view[fieldTag] != "x" {
		t.Fatalf("View = %v, want the fed values", view)
	}
	age, ok := view[fieldLux+sense.AgeSuffix].(float64)
	if !ok || math.Abs(age-0.5) > 1e-9 {
		t.Fatalf("View[%q] = %v, want 0.5", fieldLux+sense.AgeSuffix, view[fieldLux+sense.AgeSuffix])
	}
}

// A cleared field is absent from the view's VALUES but keeps its age, which is
// exactly what an absent_for predicate needs to read.
func TestViewKeepsTheAgeOfAClearedField(t *testing.T) {
	s := sense.New()
	now := epoch()
	s.Update(map[string]any{fieldLux: 4.0}, now)
	s.Update(map[string]any{fieldLux: nil}, now.Add(time.Second))

	view := s.View(now.Add(2 * time.Second))
	if _, present := view[fieldLux]; present {
		t.Fatalf("View still carries a cleared field: %v", view)
	}
	if age := view[fieldLux+sense.AgeSuffix]; age != 2.0 {
		t.Fatalf("View[%q] = %v, want 2", fieldLux+sense.AgeSuffix, age)
	}
}

// A transport that feeds its own freshness field WINS over the derived one: the
// robot measured it, and a snapshot that silently overwrote a real measurement
// with its own arithmetic would be reinterpreting a reading.
func TestAFedAgeFieldIsNotOverwritten(t *testing.T) {
	s := sense.New()
	now := epoch()
	s.Update(map[string]any{fieldLux: 4.0, fieldLux + sense.AgeSuffix: 99.0}, now)

	view := s.View(now.Add(time.Second))
	if view[fieldLux+sense.AgeSuffix] != 99.0 {
		t.Fatalf("View[%q] = %v, want the fed 99", fieldLux+sense.AgeSuffix,
			view[fieldLux+sense.AgeSuffix])
	}
}

func TestViewIsACopy(t *testing.T) {
	s := sense.New()
	now := epoch()
	s.Update(map[string]any{fieldLux: 1.0}, now)

	view := s.View(now)
	view[fieldLux] = 999.0
	delete(view, fieldLux+sense.AgeSuffix)

	if value, _ := s.Get(fieldLux); value != 1.0 {
		t.Fatalf("mutating a View changed the snapshot: %v", value)
	}
	if s.View(now)[fieldLux+sense.AgeSuffix] != 0.0 {
		t.Fatal("mutating a View changed the snapshot's ages")
	}
}

// spec c35: no frame for N ticks is a STALE snapshot with growing ages, never a
// blocked tick. Nothing here can block, so the test asserts the observable half:
// the values stand and every age grows monotonically.
func TestNoFrameLeavesAStaleSnapshotWithGrowingAges(t *testing.T) {
	s := sense.New()
	now := epoch()
	s.Update(map[string]any{fieldLux: 7.0}, now)

	previous := -1.0
	for tick := 1; tick <= 50; tick++ {
		at := now.Add(time.Duration(tick) * 20 * time.Millisecond)
		view := s.View(at)
		if view[fieldLux] != 7.0 {
			t.Fatalf("tick %d: the stale value changed: %v", tick, view[fieldLux])
		}
		age := view[fieldLux+sense.AgeSuffix].(float64)
		if age <= previous {
			t.Fatalf("tick %d: age %v did not grow past %v", tick, age, previous)
		}
		previous = age
	}
	if math.Abs(previous-1.0) > 1e-9 {
		t.Fatalf("after 50 ticks of 20 ms the age is %v, want 1", previous)
	}
}

// A transport goroutine feeds while the tick goroutine reads. Run under -race.
func TestConcurrentFeedAndRead(t *testing.T) {
	s := sense.New()
	now := epoch()
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			s.Update(map[string]any{fieldLux: float64(i), fieldWarm: i%2 == 0}, now.Add(time.Duration(i)))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = s.View(now.Add(time.Duration(i)))
			_, _ = s.Get(fieldLux)
			_, _ = s.AgeS(fieldWarm, now)
		}
	}()
	wg.Wait()
}
