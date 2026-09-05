package ruleeval_test

import (
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/ruleeval"
	"github.com/agentculture/neurosymbolic-system/internal/rules"
)

// These are the donor's "Timing semantics (asserted exactly by the tests)" —
// reachy/behavior/rule_engine.py's module docstring — re-asserted against the
// injected clock (tick.FakeClock). At 20 ms a tick, one second is 50 ticks; the
// numbers below are all derived from ctx.Now deltas, never from a tick count,
// so the same rules behave the same at another cadence.

// reactCfg is one react rule and nothing else.
func reactCfg(rule rules.Rule) *rules.Config {
	rule.Kind = rules.KindReact
	return &rules.Config{SchemaVersion: rules.SchemaVersion2, React: []rules.Rule{rule}}
}

// cooldown_s is the minimum seconds between two fires of the SAME rule. With a
// predicate that never stops holding, the rule fires on the tick it is first
// true and then exactly once per cooldown, forever.
func TestCooldownIsTheMinimumSecondsBetweenFires(t *testing.T) {
	h := newHarness(t, reactCfg(rules.Rule{
		ID:        "r-cool",
		When:      leaf(fieldLux, "gt", 1.0),
		Run:       actionBlip,
		CooldownS: 1.0,
	}), nil)
	h.start()
	defer h.stop()

	h.feed(map[string]any{fieldLux: 5.0})

	// Tick 1 fires. The next fire is due a full second later — tick 51, since
	// tick n happens at n*20 ms.
	h.ticks(50)
	if got := h.fires("r-cool"); got != 1 {
		t.Fatalf("after 50 ticks (0.98 s past the first fire) fires = %d, want 1", got)
	}
	h.ticks(1)
	if got := h.fires("r-cool"); got != 2 {
		t.Fatalf("at exactly cooldown_s past the first fire, fires = %d, want 2", got)
	}
	h.ticks(50)
	if got := h.fires("r-cool"); got != 3 {
		t.Fatalf("after a second full cooldown, fires = %d, want 3", got)
	}
	if reasons := h.suppressions("r-cool"); len(reasons) == 0 || reasons[0] != ruleeval.ReasonCooldown {
		t.Fatalf("suppression reasons = %v, want the first to be %q",
			reasons, ruleeval.ReasonCooldown)
	}
}

// hysteresis == 0 means cooldown ALONE governs: with no cooldown either, a rule
// whose predicate holds fires every tick its behavior is not already active.
func TestZeroHysteresisMeansCooldownAlone(t *testing.T) {
	h := newHarness(t, reactCfg(rules.Rule{
		ID:        "r-free",
		When:      leaf(fieldLux, "gt", 1.0),
		Run:       actionBlip,
		DurationS: seconds(0.02), // one tick: expired before the next seam call
	}), nil)
	h.start()
	defer h.stop()

	h.feed(map[string]any{fieldLux: 5.0})
	h.ticks(10)
	if got := h.fires("r-free"); got != 10 {
		t.Fatalf("fires = %d, want one per tick with no cooldown and no hysteresis", got)
	}
	if reasons := h.suppressions("r-free"); len(reasons) != 0 {
		t.Fatalf("an ungated rule suppressed nothing, got %v", reasons)
	}
}

// After a fire, hysteresis requires the predicate to be false CONTINUOUSLY for
// at least that long before the rule may fire again — and a single true tick
// mid-streak restarts the count.
func TestHysteresisRequiresContinuousFalseToReArm(t *testing.T) {
	h := newHarness(t, reactCfg(rules.Rule{
		ID:         "r-hyst",
		When:       leaf(fieldLux, "gt", 1.0),
		Run:        actionBlip,
		Hysteresis: 0.2, // 10 ticks
		DurationS:  seconds(0.02),
	}), nil)
	h.start()
	defer h.stop()

	h.feed(map[string]any{fieldLux: 5.0})
	h.ticks(1)
	if got := h.fires("r-hyst"); got != 1 {
		t.Fatalf("first fire did not happen: fires = %d", got)
	}
	h.ticks(20)
	if got := h.fires("r-hyst"); got != 1 {
		t.Fatalf("a rule held true never re-arms: fires = %d, want 1", got)
	}
	if reasons := h.suppressions("r-hyst"); len(reasons) == 0 || reasons[0] != ruleeval.ReasonRearming {
		t.Fatalf("suppression reasons = %v, want the first to be %q",
			reasons, ruleeval.ReasonRearming)
	}

	// Five false ticks, one true tick (which BREAKS continuity), five more
	// false ticks: still short of a continuous 0.2 s, so no re-arm.
	h.feed(map[string]any{fieldLux: 0.0})
	h.ticks(5)
	h.feed(map[string]any{fieldLux: 5.0})
	h.ticks(1)
	if got := h.fires("r-hyst"); got != 1 {
		t.Fatalf("an interrupted false streak re-armed the rule: fires = %d, want 1", got)
	}
	h.feed(map[string]any{fieldLux: 0.0})
	h.ticks(5)
	h.feed(map[string]any{fieldLux: 5.0})
	h.ticks(1)
	if got := h.fires("r-hyst"); got != 1 {
		t.Fatalf("two half-length false streaks re-armed the rule: fires = %d, want 1", got)
	}

	// Now a genuinely continuous false streak: the first false tick starts the
	// clock, and 0.2 s later (ten more ticks) the rule is armed again.
	h.feed(map[string]any{fieldLux: 0.0})
	h.ticks(11)
	h.feed(map[string]any{fieldLux: 5.0})
	h.ticks(1)
	if got := h.fires("r-hyst"); got != 2 {
		t.Fatalf("after a continuous false streak of hysteresis seconds, fires = %d, want 2", got)
	}
}

// absent_for holds once the named field has been absent continuously for at
// least value seconds, on a per-field last-seen clock seeded at the first tick.
func TestAbsentForMeasuresContinuousAbsenceFromTheFirstTick(t *testing.T) {
	h := newHarness(t, reactCfg(rules.Rule{
		ID:        "r-gone",
		When:      leaf(fieldLux, "absent_for", 0.2),
		Run:       actionBlip,
		DurationS: seconds(0.02),
	}), nil)
	h.start()
	defer h.stop()

	// Nothing is ever fed. Absence is measured from the FIRST tick, not from
	// minus infinity, so the rule stays quiet for exactly 0.2 s.
	h.ticks(10)
	if got := h.fires("r-gone"); got != 0 {
		t.Fatalf("fires = %d after 0.18 s of absence, want 0", got)
	}
	h.ticks(1)
	if got := h.fires("r-gone"); got == 0 {
		t.Fatal("the rule did not fire at exactly absent_for seconds of absence")
	}
}

func TestAReadingResetsTheAbsenceClock(t *testing.T) {
	h := newHarness(t, reactCfg(rules.Rule{
		ID:        "r-gone",
		When:      leaf(fieldLux, "absent_for", 0.2),
		Run:       actionBlip,
		DurationS: seconds(0.02),
	}), nil)
	h.start()
	defer h.stop()

	h.ticks(5)
	h.feed(map[string]any{fieldLux: 1.0}) // seen: the clock restarts here
	h.ticks(1)
	h.feed(map[string]any{fieldLux: nil}) // and gone again
	h.ticks(9)
	if got := h.fires("r-gone"); got != 0 {
		t.Fatalf("fires = %d, want 0 — the reading must have restarted the clock", got)
	}
	h.ticks(1)
	if got := h.fires("r-gone"); got != 1 {
		t.Fatalf("fires = %d, want 1 once 0.2 s has passed since the reading", got)
	}
}

// A boolean condition fed FALSE is absent, not "seen": the is_true reading is
// what the absence clock tracks, exactly as the donor's _field_present does.
func TestAFalseBooleanCountsAsAbsent(t *testing.T) {
	h := newHarness(t, reactCfg(rules.Rule{
		ID:        "r-cold",
		When:      leaf(fieldWarm, "absent_for", 0.1),
		Run:       actionBlip,
		DurationS: seconds(0.02),
	}), nil)
	h.start()
	defer h.stop()

	h.feed(map[string]any{fieldWarm: false})
	h.ticks(5)
	if got := h.fires("r-cold"); got != 0 {
		t.Fatalf("fires = %d before 0.1 s, want 0", got)
	}
	h.ticks(1)
	if got := h.fires("r-cold"); got != 1 {
		t.Fatalf("fires = %d, want 1 — a false flag is an absent cue", got)
	}
}

// duration_s caps the admitted behavior's lifetime, overriding the action's own
// bound. With no cooldown, the rule re-fires the moment its behavior expires,
// so the gap between fires MEASURES the cap.
func TestDurationSCapsTheAdmittedLifetime(t *testing.T) {
	for _, tc := range []struct {
		name      string
		duration  float64
		wantFires int
	}{
		{"a fifth of a second", 0.2, 3}, // ticks 1, 11, 21 within 30 ticks
		{"a tenth of a second", 0.1, 6}, // ticks 1, 6, 11, 16, 21, 26
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, reactCfg(rules.Rule{
				ID:        "r-len",
				When:      leaf(fieldLux, "gt", 1.0),
				Run:       actionHum, // a LOOPING action: only duration_s bounds it
				DurationS: seconds(tc.duration),
			}), nil)
			h.start()
			defer h.stop()

			h.feed(map[string]any{fieldLux: 5.0})
			h.ticks(30)
			if got := h.fires("r-len"); got != tc.wantFires {
				t.Fatalf("fires = %d, want %d — duration_s must cap the lifetime",
					got, tc.wantFires)
			}
		})
	}
}

// A looping action admitted with no bound would hold its channels forever, so a
// react rule that asks for one is refused at COMPILE time, not at admission.
func TestALoopingActionWithNoDurationIsRefused(t *testing.T) {
	_, err := ruleeval.New(ruleeval.Config{
		Rules:      reactCfg(rules.Rule{ID: "r-forever", When: leaf(fieldLux, "gt", 1.0), Run: actionHum}),
		Vocabulary: toyVoc(t),
	})
	if err == nil {
		t.Fatal("an unbounded looping react rule was accepted")
	}
	for _, want := range []string{"r-forever", actionHum, "duration_s"} {
		if !contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
}
