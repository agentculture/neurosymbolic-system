package bench_test

import (
	"os"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/bench"
)

// smallConfig mirrors the fast configuration t15 asks the acceptance test to
// use: small enough to run in well under a second, but the same shape as a
// real run — leaf and group predicates, an inhibit rule, a real ticker.
//
// Its 5ms period is generous against this package's own synthetic load on a
// quiet box, but NOT generous against a noisy one: `go test ./...` runs every
// package's tests in parallel, and a real-clock bench sharing the machine
// with everything else can and does overrun occasionally. Tests below must
// never assert a specific pass/fail outcome for this config — only that
// whatever outcome happened is internally consistent. See
// TestRunWithTinyBudgetOverruns for the one deterministic overrun assertion,
// and TestRunDefaultConfigHasNoOverrunsStrict for a genuine "this box is
// fast enough" assertion gated behind an env var.
func smallConfig() bench.Config {
	cfg := bench.DefaultConfig()
	cfg.Rules = 20
	cfg.Fields = 5
	cfg.Ticks = 200
	cfg.Period = 5 * time.Millisecond
	return cfg
}

func TestRunReportsShapeAndConsistency(t *testing.T) {
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
	wantCeiling := float64(cfg.RSSCeiling) / (1024 * 1024)
	if result.RSSCeilingMB != wantCeiling {
		t.Errorf("RSSCeilingMB = %v, want %v", result.RSSCeilingMB, wantCeiling)
	}

	// This is the load-bearing assertion, and it must hold under ANY timing
	// outcome — including a tick that overran under contention from a
	// parallel `go test ./...` run: OK is a pure function of Overruns and
	// RSSMB against the ceiling, never an assumption that this run passed.
	wantOK := result.Overruns == 0 && result.RSSMB <= result.RSSCeilingMB
	if result.OK != wantOK {
		t.Errorf("OK = %v, want %v (overruns=%d rss_mb=%v rss_ceiling_mb=%v)",
			result.OK, wantOK, result.Overruns, result.RSSMB, result.RSSCeilingMB)
	}
	if table := result.Table(); table == "" {
		t.Error("Table() is empty")
	}
}

// TestRunWithTinyBudgetOverruns is acceptance criterion 1's deterministic
// exit-1 case: a 1ns budget cannot be met by any tick regardless of what
// else is running on the box, so Overruns > 0 and OK == false are asserted
// unconditionally here — this is the ONE test in this package allowed to
// assert a specific overrun outcome.
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

// TestRunTickCountIsDeterministic checks the one number a real-clock run can
// actually promise across two runs under load: the tick COUNT, which the
// ticker drives regardless of timing outcome. Overrun counts and raw timing
// numbers are deliberately not compared here — they vary under contention by
// construction, and asserting their equality is exactly the flake this
// rewrite removes.
func TestRunTickCountIsDeterministic(t *testing.T) {
	cfg := smallConfig()
	a, err := bench.Run(cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	b, err := bench.Run(cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if a.Ticks != cfg.Ticks || b.Ticks != cfg.Ticks {
		t.Fatalf("Ticks = %d, %d, want both == %d", a.Ticks, b.Ticks, cfg.Ticks)
	}
}

// TestRunDefaultConfigHasNoOverrunsStrict is the genuine "this box meets the
// spec's default 200-rule/20-field/10,000-tick/20ms budget with zero
// overruns" assertion — the one this package's other tests deliberately do
// NOT make, because it is only meaningful on a quiet box (this is exactly
// what docs/verification/*.md records by hand for the arm64 box). It is
// skipped unless NEUROSYMBOLIC_BENCH_STRICT=1 is set, which the verification
// record run sets explicitly; a shared CI runner running `go test ./...`
// alongside every other package's tests does not, and must not.
func TestRunDefaultConfigHasNoOverrunsStrict(t *testing.T) {
	if os.Getenv("NEUROSYMBOLIC_BENCH_STRICT") != "1" {
		t.Skip("set NEUROSYMBOLIC_BENCH_STRICT=1 to run the genuine zero-overrun assertion " +
			"under the full default config (200 rules/20 fields/10,000 ticks/20ms — " +
			"~200s on a real clock, and only meaningful on a quiet box)")
	}
	result, err := bench.Run(bench.DefaultConfig())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Overruns != 0 {
		t.Errorf("Overruns = %d, want 0 under the default config with NEUROSYMBOLIC_BENCH_STRICT=1: %+v",
			result.Overruns, result)
	}
	if !result.OK {
		t.Errorf("OK = false, want true: %+v", result)
	}
}
