package senselog_test

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/senselog"
)

// TestLoggerIsSafeForConcurrentStreaks is t15's recorded follow-up from t10:
// internal/senselog.Logger was not goroutine-safe, and t10 worked around it
// with a provider-local mutex. This test drives two goroutines logging
// streaks against the SAME Logger concurrently — run with -race, it fails on
// the pre-fix Logger (a data race on the underlying io.Writer) and would also
// catch an interleaved write corrupting the "[SENSE ...] " line grammar.
func TestLoggerIsSafeForConcurrentStreaks(t *testing.T) {
	var buf bytes.Buffer
	logger := senselog.New(&buf)

	const perGoroutine = 200
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		streak := logger.NewStreak("stageA", "sourceA", "eventA")
		for i := 0; i < perGoroutine; i++ {
			streak.Enter(i, "reasonA")
			if i%10 == 0 {
				streak.End(i)
			}
		}
		streak.End(perGoroutine)
	}()

	go func() {
		defer wg.Done()
		streak := logger.NewStreak("stageB", "sourceB", "eventB")
		for i := 0; i < perGoroutine; i++ {
			streak.Enter(i, "reasonB")
			logger.Stage("stageB", "sourceB", "eventB", "fired")
		}
		streak.End(perGoroutine)
	}()

	wg.Wait()

	// Every line the two goroutines produced must still parse as SENSE
	// grammar — a race that interleaved two writers' bytes would corrupt at
	// least one line into something Parse rejects.
	for _, raw := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if raw == "" {
			continue
		}
		if _, err := senselog.Parse(raw); err != nil {
			t.Fatalf("a concurrently-written line failed to parse: %v (%q)", err, raw)
		}
	}
}
