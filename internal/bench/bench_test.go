package bench_test

import (
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/bench"
)

// smallConfig mirrors the fast configuration t15 asks the acceptance test to
// use: small enough to run in well under a second, but the same shape as a
// real run — leaf and group predicates, an inhibit rule, a real ticker.
func smallConfig() bench.Config {
	cfg := bench.DefaultConfig()
	cfg.Rules = 20
	cfg.Fields = 5
	cfg.Ticks = 200
	cfg.Period = 5 * time.Millisecond
	return cfg
}

func TestRunReportsShapeAndPasses(t *testing.T) {
	cfg := smallConfig()
	result, err := bench.Run(cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Ticks != cfg.Ticks {
		t.Errorf("Ticks = %d, want %d", result.Ticks, cfg.Ticks)
	}
	if result.Period != cfg.Period.String() {
		t.Errorf("Period = %q, want %q", result.Period, cfg.Period.String())
	}
	if result.P50US <= 0 {
		t.Errorf("P50US = %v, want > 0", result.P50US)
	}
	if result.P99US < result.P50US {
		t.Errorf("P99US = %v < P50US = %v", result.P99US, result.P50US)
	}
	if result.MaxUS < result.P99US {
		t.Errorf("MaxUS = %v < P99US = %v", result.MaxUS, result.P99US)
	}
	if result.RSSMB <= 0 {
		t.Errorf("RSSMB = %v, want > 0 (this process is definitely resident)", result.RSSMB)
	}
	if result.RSSCeilingMB != float64(cfg.RSSCeiling)/(1024*1024) {
		t.Errorf("RSSCeilingMB = %v, want %v", result.RSSCeilingMB, float64(cfg.RSSCeiling)/(1024*1024))
	}
	if !result.OK {
		t.Errorf("OK = false, want true for a generous 5ms period and 32MB ceiling: %+v", result)
	}
	if result.Overruns != 0 {
		t.Errorf("Overruns = %d, want 0", result.Overruns)
	}
	if table := result.Table(); table == "" {
		t.Error("Table() is empty")
	}
}

func TestRunWithTinyBudgetOverruns(t *testing.T) {
	cfg := smallConfig()
	cfg.Ticks = 50
	cfg.Period = 1 * time.Nanosecond
	result, err := bench.Run(cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Overruns == 0 {
		t.Fatalf("Overruns = 0, want > 0 for a 1ns budget: %+v", result)
	}
	if result.OK {
		t.Errorf("OK = true, want false when Overruns > 0: %+v", result)
	}
}

func TestRunRefusesBadConfig(t *testing.T) {
	for name, mutate := range map[string]func(*bench.Config){
		"rules":       func(c *bench.Config) { c.Rules = 0 },
		"fields":      func(c *bench.Config) { c.Fields = 0 },
		"ticks":       func(c *bench.Config) { c.Ticks = 0 },
		"period":      func(c *bench.Config) { c.Period = 0 },
		"rss-ceiling": func(c *bench.Config) { c.RSSCeiling = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := smallConfig()
			mutate(&cfg)
			if _, err := bench.Run(cfg); err == nil {
				t.Fatalf("Run(%+v): want an error", cfg)
			}
		})
	}
}

func TestRunIsDeterministic(t *testing.T) {
	cfg := smallConfig()
	a, err := bench.Run(cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	b, err := bench.Run(cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Timing numbers vary run to run; the seeded sense feed and the resulting
	// tick count and overrun count must not.
	if a.Ticks != b.Ticks || a.Overruns != b.Overruns {
		t.Errorf("two runs of the same config disagree: %+v vs %+v", a, b)
	}
}
