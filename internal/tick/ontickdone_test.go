package tick

import (
	"sync"
	"testing"
	"time"
)

// TestOnTickDoneReportsAccountBudgetsOwnElapsed is t15's one test for the
// bench hook: OnTickDone must fire exactly once per tick, on the tick
// goroutine, carrying the same elapsed reading accountBudget uses to decide
// an overrun — an external observer (internal/bench) needs that number
// without internal/tick ever reading the wall clock on its own behalf.
func TestOnTickDoneReportsAccountBudgetsOwnElapsed(t *testing.T) {
	var mu sync.Mutex
	var calls int
	var elapsed []time.Duration

	h := newHarness(t, 20*time.Millisecond, func(cfg *Config) {
		cfg.OnTickDone = func(d time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			elapsed = append(elapsed, d)
		}
	})
	h.start()
	h.clock.Advance(3 * h.eng.cfg.Period)
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Fatalf("OnTickDone was called %d times, want 3 (one per tick)", calls)
	}
	for i, d := range elapsed {
		if d < 0 {
			t.Errorf("tick %d elapsed = %v, want >= 0", i, d)
		}
	}
	if got := h.eng.Stats().Ticks; got != 3 {
		t.Fatalf("Stats().Ticks = %d, want 3", got)
	}
}
