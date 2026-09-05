// Package bench is the load internal/mgmt's `bench` verb drives: a synthetic
// 200-rule / 20-field vocabulary and rules file, assembled the way
// internal/compose wires a real robot but with a null sink and no transport,
// ticked at a real --period for --ticks ticks, measuring per-tick evaluation
// time and steady-state RSS.
//
// It is the acceptance test for spec h17/h26: per-tick evaluation stays
// inside its budget with zero overruns over 10,000 ticks, and steady-state
// RSS under a 200-rule, 20-field load stays under a published ceiling.
//
// Every name this package mints — f0..fN, ch0..ch3, act0..act7 — is
// synthetic. This package holds no robot literal and no hardware: it is a
// load generator over the same seams a real composition root uses
// (adaptor.Vocabulary, rules.Config, ruleeval.Evaluator, tick.Engine), so a
// number this package reports is a number the real engine would show under
// the identical load.
package bench

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/ruleeval"
	"github.com/agentculture/neurosymbolic-system/internal/rules"
	"github.com/agentculture/neurosymbolic-system/internal/sense"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// Defaults, spelled once so the verb's flag parsing and this package's zero
// value cannot drift.
const (
	DefaultRules       = 200
	DefaultFields      = 20
	DefaultTicks       = 10000
	DefaultPeriod      = 20 * time.Millisecond
	DefaultRSSCeilingB = 32 * 1024 * 1024

	// warmupCap is the tick count an RSS sample is taken at, per the design
	// ("after a warm-up of 1000 ticks"). A run shorter than that samples at
	// its own midpoint instead, so a small test config still exercises the
	// same code path.
	warmupCap = 1000

	// seed is fixed, not configurable: a benchmark whose sense feed differs
	// between two runs of the same --rules/--fields/--ticks cannot be compared
	// against its own history, and this package has no flag for it because
	// nothing downstream needs one.
	seed = int64(1)
)

// Config is one bench run's tunables. The zero value is not directly usable;
// build one with DefaultConfig and override only what a caller's flags named.
type Config struct {
	Rules      int
	Fields     int
	Ticks      int
	Period     time.Duration
	RSSCeiling int64 // bytes
}

// DefaultConfig returns the spec's defaults: 200 rules, 20 fields, 10,000
// ticks at 20ms (50Hz), a 32MB RSS ceiling.
func DefaultConfig() Config {
	return Config{
		Rules: DefaultRules, Fields: DefaultFields, Ticks: DefaultTicks,
		Period: DefaultPeriod, RSSCeiling: DefaultRSSCeilingB,
	}
}

// validate refuses the same way the rest of this runtime does: fail-closed,
// naming the offending value.
func (c Config) validate() error {
	if c.Rules < 1 {
		return fmt.Errorf("bench: --rules is %d — it must be at least 1", c.Rules)
	}
	if c.Fields < 1 {
		return fmt.Errorf("bench: --fields is %d — it must be at least 1", c.Fields)
	}
	if c.Ticks < 1 {
		return fmt.Errorf("bench: --ticks is %d — it must be at least 1", c.Ticks)
	}
	if c.Period <= 0 {
		return fmt.Errorf("bench: --period is %v — it must be greater than zero", c.Period)
	}
	if c.RSSCeiling <= 0 {
		return fmt.Errorf("bench: --rss-ceiling is %d bytes — it must be greater than zero", c.RSSCeiling)
	}
	return nil
}

// Result is one bench run's report — the exact shape both the text table and
// --json render.
type Result struct {
	Ticks        int     `json:"ticks"`
	Period       string  `json:"period"`
	P50US        float64 `json:"p50_us"`
	P99US        float64 `json:"p99_us"`
	MaxUS        float64 `json:"max_us"`
	Overruns     uint64  `json:"overruns"`
	RSSMB        float64 `json:"rss_mb"`
	RSSCeilingMB float64 `json:"rss_ceiling_mb"`
	OK           bool    `json:"ok"`
}

// Table renders Result as the one-line human-text summary the verb prints.
func (r Result) Table() string {
	return fmt.Sprintf(
		"bench: ticks=%d period=%s p50_us=%.1f p99_us=%.1f max_us=%.1f "+
			"overruns=%d rss_mb=%.2f rss_ceiling_mb=%.2f ok=%v",
		r.Ticks, r.Period, r.P50US, r.P99US, r.MaxUS, r.Overruns, r.RSSMB, r.RSSCeilingMB, r.OK)
}

// warmupMark is the tick count RSS is first sampled at.
func warmupMark(total int) int {
	if total > warmupCap {
		return warmupCap
	}
	if total < 2 {
		return total
	}
	return total / 2
}

// Run assembles the synthetic load, drives it for cfg.Ticks real ticks at
// cfg.Period, and reports the timing and memory numbers acceptance criterion
// 3 asks for.
//
// It builds the vocabulary and rules exactly the way internal/compose does —
// adaptor.LoadJSON, rules.Load, ruleeval.New — over a scratch directory
// removed before Run returns, so a failure here is the identical failure a
// real composition root would report for the same (synthetic) documents.
func Run(cfg Config) (Result, error) {
	if err := cfg.validate(); err != nil {
		return Result{}, err
	}

	voc, rulesCfg, err := loadSynthetic(cfg)
	if err != nil {
		return Result{}, err
	}

	logger := senselog.New(io.Discard)
	snapshot := sense.New()
	evaluator, err := ruleeval.New(ruleeval.Config{
		Rules:      rulesCfg,
		Vocabulary: voc,
		Snapshot:   snapshot,
		Registry:   ruleeval.NewRegistry(),
		Logger:     logger,
	})
	if err != nil {
		return Result{}, fmt.Errorf("bench: the synthetic rules did not compile: %w", err)
	}

	feedRNG := rand.New(rand.NewSource(seed + 1)) // #nosec G404 -- a deterministic synthetic load, not a security context
	seam := ruleeval.NewBus(logger).
		Add("sense", senseSeam(cfg.Fields, snapshot, feedRNG)).
		Add(ruleeval.StageRule, evaluator.Tick).
		Compose()

	durations := make([]time.Duration, 0, cfg.Ticks)
	var rssHigh float64
	warmAt := warmupMark(cfg.Ticks)
	n := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine, err := tick.New(voc, tick.Config{
		Period: cfg.Period,
		Log:    logger,
		Settle: tick.Bool(false),
		OnTickDone: func(d time.Duration) {
			n++
			durations = append(durations, d)
			if n == warmAt {
				if rss := readRSSMB(); rss > rssHigh {
					rssHigh = rss
				}
			}
			if n >= cfg.Ticks {
				if rss := readRSSMB(); rss > rssHigh {
					rssHigh = rss
				}
				cancel()
			}
		},
	}, nullSink{})
	if err != nil {
		return Result{}, fmt.Errorf("bench: could not build the engine: %w", err)
	}
	if !engine.Send(tick.SetSeamCmd{Seam: seam}) {
		return Result{}, fmt.Errorf("bench: the engine refused the tick seam before the first tick")
	}

	if err := engine.Run(ctx); err != nil {
		return Result{}, fmt.Errorf("bench: the run failed: %w", err)
	}

	stats := engine.Stats()
	p50, p99, max := percentilesUS(durations)
	ceilingMB := float64(cfg.RSSCeiling) / (1024 * 1024)
	result := Result{
		Ticks: len(durations), Period: cfg.Period.String(),
		P50US: p50, P99US: p99, MaxUS: max,
		Overruns: stats.Overruns, RSSMB: rssHigh, RSSCeilingMB: ceilingMB,
	}
	result.OK = stats.Overruns == 0 && rssHigh <= ceilingMB
	return result, nil
}

// loadSynthetic writes the generated vocabulary and rules file to a scratch
// directory, loads them through the real loaders, and cleans up.
func loadSynthetic(cfg Config) (*adaptor.Vocabulary, *rules.Config, error) {
	dir, err := os.MkdirTemp("", "neurosymbolic-bench-*")
	if err != nil {
		return nil, nil, fmt.Errorf("bench: could not create a scratch directory: %w", err)
	}
	defer os.RemoveAll(dir) // #nosec G104 -- best-effort cleanup of our own scratch dir

	vocData, err := generateVocabulary(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("bench: could not build the synthetic vocabulary: %w", err)
	}
	vocPath := filepath.Join(dir, "vocabulary.json")
	if err := os.WriteFile(vocPath, vocData, 0o600); err != nil {
		return nil, nil, fmt.Errorf("bench: could not write the synthetic vocabulary: %w", err)
	}
	voc, err := adaptor.LoadJSON(vocPath)
	if err != nil {
		return nil, nil, fmt.Errorf("bench: the synthetic vocabulary did not load: %w", err)
	}

	genRNG := rand.New(rand.NewSource(seed)) // #nosec G404 -- a deterministic synthetic load, not a security context
	rulesPath := filepath.Join(dir, "rules.toml")
	if err := os.WriteFile(rulesPath, []byte(generateRulesTOML(cfg, genRNG)), 0o600); err != nil {
		return nil, nil, fmt.Errorf("bench: could not write the synthetic rules file: %w", err)
	}
	rulesCfg, err := rules.Load([][]string{{rulesPath}}, voc)
	if err != nil {
		return nil, nil, fmt.Errorf("bench: the synthetic rules file did not load: %w", err)
	}
	return voc, rulesCfg, nil
}

// senseSeam feeds a fresh random reading for every declared field, once per
// tick, seeded so a run is exactly reproducible. It runs FIRST in the bus, so
// the rule evaluator that runs after it sees this tick's frame rather than the
// previous one's.
func senseSeam(fieldCount int, snapshot *sense.Snapshot, rng *rand.Rand) tick.TickSeam {
	names := make([]string, fieldCount)
	isBool := make([]bool, fieldCount)
	for i := 0; i < fieldCount; i++ {
		names[i] = fieldName(i)
		isBool[i] = fieldIsBool(i)
	}
	return func(ctx tick.TickContext) {
		frame := make(map[string]any, fieldCount)
		for i, name := range names {
			if isBool[i] {
				frame[name] = rng.Float64() < 0.5
			} else {
				frame[name] = rng.Float64() * 10.0
			}
		}
		snapshot.Update(frame, ctx.Now)
	}
}

// percentilesUS reduces a run's per-tick elapsed times to p50/p99/max in
// microseconds. An empty input reports all zeros rather than panicking.
func percentilesUS(durations []time.Duration) (p50, p99, max float64) {
	if len(durations) == 0 {
		return 0, 0, 0
	}
	us := make([]float64, len(durations))
	for i, d := range durations {
		us[i] = float64(d) / float64(time.Microsecond)
	}
	sort.Float64s(us)
	return percentileOf(us, 0.50), percentileOf(us, 0.99), us[len(us)-1]
}

// percentileOf is nearest-rank on an already-sorted slice: index
// round(p*(n-1)), which for p50/p99 on a few thousand samples is well within
// the noise these numbers already carry.
func percentileOf(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p*float64(len(sorted)-1) + 0.5)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
