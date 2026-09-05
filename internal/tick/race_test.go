package tick

import (
	"io"
	"sync"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
)

// TestInboxUnderConcurrentProducers is acceptance criterion 4's ownership
// half, and is meant to be run under `go test -race`.
//
// All engine state is owned by the tick goroutine; every other writer enqueues
// on the bounded inbox, which Run drains at tick start. Four producers hammer
// that inbox — admissions, evictions and seam swaps — while the loop runs 1000
// fake ticks. Nothing may block, nothing may race, and the loop must still run
// exactly the ticks it was given.
func TestInboxUnderConcurrentProducers(t *testing.T) {
	const (
		period    = 20 * time.Millisecond
		ticks     = 1000
		producers = 4
		perSender = 500
	)

	h := newHarness(t, period, func(c *Config) {
		c.InboxSize = 8
		// The log is stderr-shaped and this test writes thousands of lines;
		// discard them so the assertion is about the loop, not the buffer.
		c.Log = senselog.New(io.Discard)
	})
	h.start()

	var wg sync.WaitGroup
	stopProducers := make(chan struct{})
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perSender; i++ {
				select {
				case <-stopProducers:
					return
				default:
				}
				switch i % 3 {
				case 0:
					h.eng.Send(AdmitCmd{Behavior: Behavior{
						Name: "churn", Class: ClassStoppable, Channels: []string{"ch_a"},
						Lifetime: Lifetime{DurationS: seconds(0.04)},
						Contribute: func(float64) Contribution {
							return Contribution{"ch_a": {1, 1}}
						},
					}})
				case 1:
					h.eng.Send(EvictCmd{Name: "churn"})
				case 2:
					h.eng.Send(SetSeamCmd{Seam: func(ctx TickContext) {
						// Read the whole seam surface from the tick goroutine.
						_ = ctx.Ownership()
						_ = ctx.Pose()
						_ = ctx.ActiveNames()
						ctx.Emit(Event{Name: "churn"})
					}})
				}
			}
		}(p)
	}

	h.clock.Advance(ticks * period)
	close(stopProducers)
	wg.Wait()
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	stats := h.eng.Stats()
	if stats.Ticks != ticks {
		t.Fatalf("Stats().Ticks = %d, want exactly %d", stats.Ticks, ticks)
	}
	// Every pose is still complete: a hot producer cannot make the engine
	// stream a partial target.
	h.assertComplete(h.poses())
	if len(h.poses()) != ticks+1 {
		t.Fatalf("streamed %d poses, want %d ticks plus the settle", len(h.poses()), ticks+1)
	}
}

// Stats is readable from another goroutine while the loop runs — it is the
// bench task's only window into a running engine.
func TestStatsReadableWhileRunning(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, nil)
	h.start()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			_ = h.eng.Stats()
		}
	}()
	h.clock.Advance(50 * period)
	<-done
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := h.eng.Stats().Ticks; got != 50 {
		t.Fatalf("Stats().Ticks = %d, want 50", got)
	}
}

// The fake clock's contract: Advance delivers exactly one tick per whole
// period and returns only once the loop has finished them, so a test never has
// to sleep or poll.
func TestFakeClockDeliversExactlyOneTickPerPeriod(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, nil)
	h.start()

	h.clock.Advance(3*period + period/2)
	if got := h.eng.Stats().Ticks; got != 3 {
		t.Fatalf("Stats().Ticks = %d after advancing 3.5 periods, want 3", got)
	}
	if got := len(h.sink.Written()); got != 3 {
		t.Fatalf("the sink holds %d poses, want 3: Advance must return only once the "+
			"loop has finished the ticks it delivered", got)
	}
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// An engine that is never ticked runs no ticks — and still settles, so a robot
// is left at a known target rather than wherever it happened to be.
func TestAnUntickedEngineRunsNoTicks(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, nil)
	h.start()
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := h.eng.Stats().Ticks; got != 0 {
		t.Fatalf("Stats().Ticks = %d, want 0", got)
	}
	written := h.sink.Written()
	if len(written) != 1 {
		t.Fatalf("the sink holds %d poses, want just the settle", len(written))
	}
	if !equalPose(written[0], h.voc.Neutral()) {
		t.Fatalf("the settling pose = %v, want the declared neutral", written[0])
	}
}

func equalPose(a, b adaptor.Pose) bool {
	if len(a) != len(b) {
		return false
	}
	for channel, values := range a {
		other, ok := b[channel]
		if !ok || len(other) != len(values) {
			return false
		}
		for i := range values {
			if values[i] != other[i] {
				return false
			}
		}
	}
	return true
}
