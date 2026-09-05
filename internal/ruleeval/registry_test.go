package ruleeval_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/ruleeval"
	"github.com/agentculture/neurosymbolic-system/internal/rules"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func emptyCfg() *rules.Config {
	return &rules.Config{SchemaVersion: rules.SchemaVersion2}
}

// A rule-fired action and an injected intent enter the SAME registry and are
// arbitrated by the same class and recency rules. Both are stoppable, so the
// more recently admitted one owns the channel they share — which the streamed
// pose proves, since it is what the robot was actually told.
func TestRuleFireAndInjectedIntentShareArbitration(t *testing.T) {
	h := newHarness(t, reactCfg(rules.Rule{
		ID:        "r-share",
		When:      leaf(fieldLux, "gt", 1.0),
		Run:       actionRamp, // claims ch_a, rises from (0,0)
		DurationS: seconds(1.0),
	}), nil)
	h.start()
	defer h.stop()

	h.feed(map[string]any{fieldLux: 5.0})
	h.ticks(1)
	if h.fires("r-share") != 1 {
		t.Fatal("the react rule did not fire")
	}
	h.ticks(2)
	if value := h.sink.Written()[2]["ch_a"][0]; value <= 0 {
		t.Fatalf("ch_a = %v, want the rule's rising action to own it", value)
	}

	// Now an agent asks for a LATER behavior on the same channel. Same
	// registry, same class, later admission — so it takes the channel.
	if err := h.reg.RunOnce(actionHum, nil, 1.0); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	h.ticks(2)
	written := h.sink.Written()
	if value := written[len(written)-1]["ch_a"][0]; value >= 0 {
		t.Fatalf("ch_a = %v, want the later intent's action (which starts negative) to own it",
			value)
	}
}

// An inhibit blocks BOTH sources alike: the rule that would fire the action and
// the agent that asks for it by hand get the same named refusal, because both
// reach admission through the one registry.
func TestAnInhibitRuleBlocksARuleFireAndAnIntentAlike(t *testing.T) {
	cfg := &rules.Config{
		SchemaVersion: rules.SchemaVersion2,
		React: []rules.Rule{{
			ID: "r-go", Kind: rules.KindReact, When: leaf(fieldLux, "gt", 1.0),
			Run: actionRamp, DurationS: seconds(0.02),
		}},
		Inhibit: []rules.Rule{{
			ID: "r-block", Kind: rules.KindInhibit, When: leaf(fieldWarm, "is_true", nil),
			Disable: []string{actionRamp},
		}},
	}
	h := newHarness(t, cfg, nil)
	h.start()
	defer h.stop()

	h.feed(map[string]any{fieldLux: 5.0, fieldWarm: true})
	if err := h.reg.RunOnce(actionRamp, nil, 0.1); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	h.ticks(3)

	if got := h.fires("r-go"); got != 0 {
		t.Fatalf("the react rule fired %d times while inhibited, want 0", got)
	}
	if reasons := h.suppressions("r-go"); len(reasons) == 0 || reasons[0] != ruleeval.ReasonInhibited {
		t.Fatalf("react suppression reasons = %v, want %q first", reasons, ruleeval.ReasonInhibited)
	}
	blocked := h.eventsNamed(ruleeval.EventBlocked)
	if len(blocked) == 0 {
		t.Fatal("the injected intent was not blocked")
	}
	if reason := blocked[0].Data["reason"]; reason != ruleeval.ReasonInhibited {
		t.Fatalf("the intent was blocked for %q, want %q — an inhibit must name one reason "+
			"for both sources", reason, ruleeval.ReasonInhibited)
	}

	// Lift the inhibition and both paths come back to life with no further call
	// beyond the one the agent already made.
	h.feed(map[string]any{fieldWarm: false})
	h.ticks(3)
	if got := h.fires("r-go"); got == 0 {
		t.Fatal("the react rule never fired after the inhibition lifted")
	}
}

// The registry's own inhibited set blocks a rule fire exactly like an inhibit
// rule does — the same set, consulted on the same path.
func TestARegistryInhibitionBlocksARuleFire(t *testing.T) {
	h := newHarness(t, reactCfg(rules.Rule{
		ID: "r-go", When: leaf(fieldLux, "gt", 1.0), Run: actionRamp, DurationS: seconds(0.02),
	}), nil)
	h.start()
	defer h.stop()

	h.reg.Inhibit(actionRamp)
	h.feed(map[string]any{fieldLux: 5.0})
	h.ticks(3)
	if got := h.fires("r-go"); got != 0 {
		t.Fatalf("fires = %d while the registry inhibited the action, want 0", got)
	}
	h.reg.Uninhibit(actionRamp)
	h.ticks(2)
	if got := h.fires("r-go"); got == 0 {
		t.Fatal("the rule never fired after the inhibition was lifted")
	}
}

// A standing goal is re-admitted every tick it is missing, until it is
// withdrawn — declared once, in effect with no further call.
func TestAStandingGoalIsSustainedUntilWithdrawn(t *testing.T) {
	h := newHarness(t, emptyCfg(), nil)
	h.start()
	defer h.stop()

	if err := h.reg.StandingGoal("keeper", actionHum, nil); err != nil {
		t.Fatalf("StandingGoal: %v", err)
	}
	h.ticks(5)
	if got := goalAdmissions(h); got != 1 {
		t.Fatalf("a sustained goal was admitted %d times in 5 ticks, want 1 — a live goal "+
			"must not be re-admitted on top of itself", got)
	}

	// Something else evicts it. The very next tick puts it back.
	h.eng.Send(tick.EvictCmd{Name: "keeper"})
	h.ticks(2)
	if got := goalAdmissions(h); got != 2 {
		t.Fatalf("goal admissions = %d after an eviction, want 2 — a standing goal must "+
			"re-admit itself", got)
	}

	if !h.reg.Withdraw("keeper") {
		t.Fatal("Withdraw did not report the standing goal")
	}
	h.ticks(5)
	if got := goalAdmissions(h); got != 2 {
		t.Fatalf("goal admissions = %d after Withdraw, want 2 — a withdrawn goal is gone", got)
	}
	if goals := h.reg.Goals(); len(goals) != 0 {
		t.Fatalf("Goals() = %v after Withdraw, want none", goals)
	}
}

// A goal declared while its name is inhibited is RECORDED and withheld; the
// tick the inhibition clears admits it, with no further agent call.
func TestAGoalDeclaredWhileInhibitedIsWithheldThenAdmitted(t *testing.T) {
	h := newHarness(t, emptyCfg(), nil)
	h.start()
	defer h.stop()

	h.reg.Inhibit("keeper")
	if err := h.reg.StandingGoal("keeper", actionHum, nil); err != nil {
		t.Fatalf("StandingGoal: %v", err)
	}
	h.ticks(3)
	if got := goalAdmissions(h); got != 0 {
		t.Fatalf("an inhibited goal was admitted %d times, want 0", got)
	}
	if goals := h.reg.Goals(); len(goals) != 1 {
		t.Fatalf("Goals() = %v, want the goal to be recorded even while withheld", goals)
	}
	h.reg.Uninhibit("keeper")
	h.ticks(1)
	if got := goalAdmissions(h); got != 1 {
		t.Fatalf("goal admissions = %d after the inhibition lifted, want 1", got)
	}
}

// set mode swaps the active mode, and its params apply to SUBSEQUENTLY admitted
// behaviors — the resolved bag rides on the fire event, so what the robot was
// asked to do is visible without guessing.
func TestSetModeSwapsTheModeAndItsParamsApply(t *testing.T) {
	cfg := reactCfg(rules.Rule{
		ID: "r-go", When: leaf(fieldLux, "gt", 1.0), Run: actionRamp, DurationS: seconds(0.02),
	})
	cfg.Modes = map[string]map[string]float64{
		"soft": {"gain": 0.25},
		"firm": {"gain": 0.75},
	}
	h := newHarness(t, cfg, nil)
	h.start()
	defer h.stop()

	h.feed(map[string]any{fieldLux: 5.0})
	h.ticks(1)
	if params := fireParams(t, h, 0); params != nil {
		t.Fatalf("with no active mode the resolved params are %v, want none", params)
	}

	if err := h.reg.SetMode("soft"); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if got := h.eval.ActiveMode(); got != "soft" {
		t.Fatalf("ActiveMode() = %q, want %q", got, "soft")
	}
	h.ticks(1)
	if params := fireParams(t, h, 1); params["gain"] != 0.25 {
		t.Fatalf("resolved params = %v, want the active mode's gain", params)
	}

	if err := h.reg.SetMode("firm"); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	h.ticks(1)
	if params := fireParams(t, h, 2); params["gain"] != 0.75 {
		t.Fatalf("resolved params = %v, want the newly selected mode's gain", params)
	}
}

// A rule's own params win over the active mode's: a rules file is a
// version-controlled statement about this robot, a mode is a broad bag.
func TestARulesOwnParamsWinOverTheActiveMode(t *testing.T) {
	cfg := reactCfg(rules.Rule{
		ID: "r-go", When: leaf(fieldLux, "gt", 1.0), Run: actionRamp,
		DurationS: seconds(0.02), Params: map[string]float64{"gain": 0.9},
	})
	cfg.Modes = map[string]map[string]float64{"soft": {"gain": 0.25}}
	cfg.ActiveMode = "soft"
	h := newHarness(t, cfg, nil)
	h.start()
	defer h.stop()

	h.feed(map[string]any{fieldLux: 5.0})
	h.ticks(1)
	if params := fireParams(t, h, 0); params["gain"] != 0.9 {
		t.Fatalf("resolved params = %v, want the rule's own gain", params)
	}
}

func TestSetModeRefusesAnUndefinedMode(t *testing.T) {
	cfg := emptyCfg()
	cfg.Modes = map[string]map[string]float64{"soft": {"gain": 0.25}}
	h := newHarness(t, cfg, nil)

	err := h.reg.SetMode("brisk")
	if err == nil {
		t.Fatal("an undefined mode was accepted")
	}
	for _, want := range []string{"brisk", "soft"} {
		if !contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
	if got := h.eval.ActiveMode(); got != "" {
		t.Fatalf("ActiveMode() = %q after a refusal, want it unchanged", got)
	}
}

func TestSetModeWithNoEvaluatorIsNamedNotSilent(t *testing.T) {
	if err := ruleeval.NewRegistry().SetMode("soft"); err == nil {
		t.Fatal("SetMode on an unwired registry silently did nothing")
	}
}

// run-once refuses an UNBOUNDED lifetime: a looping action with no duration
// would hold its channels forever with no standing record to withdraw.
func TestRunOnceRefusesAnUnboundedLifetime(t *testing.T) {
	h := newHarness(t, emptyCfg(), nil)
	err := h.reg.RunOnce(actionHum, nil, 0)
	if err == nil {
		t.Fatal("an unbounded run-once was accepted")
	}
	if !contains(err.Error(), actionHum) || !contains(err.Error(), "duration") {
		t.Errorf("refusal %q does not name the action and the fix", err)
	}
	// The same action WITH a bound is fine.
	if err := h.reg.RunOnce(actionHum, nil, 0.5); err != nil {
		t.Fatalf("a bounded run-once was refused: %v", err)
	}
}

// Every mutator is safe from any goroutine while the loop is ticking. Run under
// -race.
func TestRegistryIsSafeFromManyGoroutines(t *testing.T) {
	h := newHarness(t, emptyCfg(), nil)
	h.start()
	defer h.stop()

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = h.reg.RunOnce(actionRamp, nil, 0.1)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = h.reg.StandingGoal("keeper", actionHum, nil)
			h.reg.Withdraw("keeper")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			h.reg.Inhibit(actionRamp)
			h.reg.Uninhibit(actionRamp)
			_ = h.reg.Inhibitions()
		}
	}()
	h.ticks(20)
	wg.Wait()
	h.ticks(5)
}

// goalAdmissions counts the standing-goal admissions the registry reported.
func goalAdmissions(h *harness) int {
	count := 0
	for _, ev := range h.eventsNamed(ruleeval.EventApplied) {
		if ev.Data["kind"] == ruleeval.KindStandingGoal {
			count++
		}
	}
	return count
}

// fireParams is the resolved parameter bag carried on the nth fire event.
func fireParams(t *testing.T, h *harness, n int) map[string]float64 {
	t.Helper()
	fires := h.eventsNamed(ruleeval.EventFire)
	if len(fires) <= n {
		t.Fatalf("only %d fire events, wanted at least %d", len(fires), n+1)
	}
	params, _ := fires[n].Data["params"].(map[string]float64)
	return params
}
