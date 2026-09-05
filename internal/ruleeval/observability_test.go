package ruleeval_test

import (
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/ruleeval"
	"github.com/agentculture/neurosymbolic-system/internal/rules"
)

// spec c15: a suppression is logged per EPISODE, not per tick. The donor's #99:
// one deployed rule wrote 6722 drop lines into a 3 h journal against 42 genuine
// fires. One line at entry, one more only when the reason CHANGES, one summary
// naming every reason and the length when it ends.
func TestSuppressionIsLoggedOncePerEpisode(t *testing.T) {
	h := newHarness(t, reactCfg(rules.Rule{
		ID:        "r-cool",
		When:      leaf(fieldLux, "gt", 1.0),
		Run:       actionBlip,
		CooldownS: 1.0,
		DurationS: seconds(0.02),
	}), nil)
	h.start()
	defer h.stop()

	h.feed(map[string]any{fieldLux: 5.0})
	h.ticks(51) // fire, 49 gated ticks, fire

	lines := h.ruleLines("r-cool")
	if len(lines) != 4 {
		t.Fatalf("a 49-tick gated streak wrote %d lines, want 4 "+
			"(fire, entry, summary, fire):\n%s", len(lines), h.log.String())
	}
	if lines[0].Dropped || !strings.HasPrefix(lines[0].Detail, "fired") {
		t.Fatalf("line 0 = %+v, want the first fire", lines[0])
	}
	if !lines[1].Dropped || lines[1].Reason != ruleeval.ReasonCooldown {
		t.Fatalf("line 1 = %+v, want one entry drop naming the reason", lines[1])
	}
	if !lines[2].Dropped || lines[2].Detail != "suppressed 49 ticks" {
		t.Fatalf("line 2 = %+v, want the episode summary with its tick count", lines[2])
	}
	if lines[3].Dropped || !strings.HasPrefix(lines[3].Detail, "fired") {
		t.Fatalf("line 3 = %+v, want the summary to precede the second fire", lines[3])
	}
	if lines[1].Source != fieldLux || lines[1].Event != "r-cool" {
		t.Fatalf("line 1 keys on source=%q event=%q, want the field and the rule id",
			lines[1].Source, lines[1].Event)
	}
}

// A reason CHANGE mid-streak writes exactly one more line, and the summary names
// every reason the episode passed through, in order.
func TestAMidStreakReasonChangeWritesOneMoreLine(t *testing.T) {
	h := newHarness(t, reactCfg(rules.Rule{
		ID:        "r-two",
		When:      leaf(fieldLux, "gt", 1.0),
		Run:       actionBlip,
		CooldownS: 0.1, // 5 ticks
		DurationS: seconds(0.2),
	}), nil)
	h.start()
	defer h.stop()

	h.feed(map[string]any{fieldLux: 5.0})
	h.ticks(11)

	lines := h.ruleLines("r-two")
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5 (fire, cooldown, already-active, summary, fire):\n%s",
			len(lines), h.log.String())
	}
	if lines[1].Reason != ruleeval.ReasonCooldown {
		t.Fatalf("line 1 reason = %q, want %q", lines[1].Reason, ruleeval.ReasonCooldown)
	}
	if lines[2].Reason != ruleeval.ReasonAlreadyActive {
		t.Fatalf("line 2 reason = %q, want %q", lines[2].Reason, ruleeval.ReasonAlreadyActive)
	}
	wantSummary := ruleeval.ReasonCooldown + "," + ruleeval.ReasonAlreadyActive
	if lines[3].Reason != wantSummary || lines[3].Detail != "suppressed 9 ticks" {
		t.Fatalf("summary = %+v, want reason %q and 9 ticks", lines[3], wantSummary)
	}

	// The event stream follows the SAME transition cadence, so an export feed
	// is spared the flood too — and the summary event carries the count.
	reasons := h.suppressions("r-two")
	if len(reasons) != 3 {
		t.Fatalf("suppression events = %v, want three transitions", reasons)
	}
	summary := h.eventsNamed(ruleeval.EventSuppress)[2]
	if summary.Data["ticks"] != 9 {
		t.Fatalf("the summary event carries ticks=%v, want 9", summary.Data["ticks"])
	}
}

// A tick where nothing matches logs NOTHING: no per-tick no-match noise, so a
// log capture reconstructs every non-trivial decision and nothing else.
func TestANoMatchTickIsSilent(t *testing.T) {
	h := newHarness(t, reactCfg(rules.Rule{
		ID: "r-quiet", When: leaf(fieldLux, "gt", 1.0), Run: actionBlip, DurationS: seconds(0.02),
	}), nil)
	h.start()
	defer h.stop()

	h.feed(map[string]any{fieldLux: 0.0})
	h.ticks(20)
	if lines := h.ruleLines("r-quiet"); len(lines) != 0 {
		t.Fatalf("a rule that never matched wrote %d lines:\n%s", len(lines), h.log.String())
	}
}

// A fire line names what fired, and forwards say as data — this layer owns no
// actuator and must never render speech on the tick thread.
func TestAFireNamesWhatItRanAndForwardsSay(t *testing.T) {
	h := newHarness(t, reactCfg(rules.Rule{
		ID: "r-talk", When: leaf(fieldLux, "gt", 1.0), Run: actionBlip,
		DurationS: seconds(0.02), Say: "hello there",
	}), nil)
	h.start()
	defer h.stop()

	h.feed(map[string]any{fieldLux: 5.0})
	h.ticks(1)

	lines := h.ruleLines("r-talk")
	if len(lines) != 1 || lines[0].Dropped {
		t.Fatalf("lines = %+v, want one fire line", lines)
	}
	if !strings.Contains(lines[0].Detail, "run="+actionBlip) {
		t.Fatalf("fire detail %q does not name what it ran", lines[0].Detail)
	}
	fire := h.eventsNamed(ruleeval.EventFire)[0]
	if fire.Data["say"] != "hello there" {
		t.Fatalf("the fire event forwards say=%v, want the rule's text", fire.Data["say"])
	}
	if fire.Data["kind"] != rules.KindReact || fire.Data["field"] != fieldLux {
		t.Fatalf("the fire event = %v, want the donor's kind/field keys", fire.Data)
	}
}

// An inhibit rule evicts what it names, and says so on the log.
func TestAnInhibitRuleEvictsAndSaysSo(t *testing.T) {
	cfg := &rules.Config{
		SchemaVersion: rules.SchemaVersion2,
		Inhibit: []rules.Rule{{
			ID: "r-stop", Kind: rules.KindInhibit, When: leaf(fieldWarm, "is_true", nil),
			Disable: []string{actionHum},
		}},
	}
	h := newHarness(t, cfg, nil)
	h.start()
	defer h.stop()

	if err := h.reg.StandingGoal(actionHum, actionHum, nil); err != nil {
		t.Fatalf("StandingGoal: %v", err)
	}
	h.ticks(2)
	if goalAdmissions(h) != 1 {
		t.Fatal("the goal was never admitted")
	}
	h.feed(map[string]any{fieldWarm: true})
	h.ticks(2)

	lines := h.ruleLines("r-stop")
	if len(lines) == 0 || lines[0].Dropped {
		t.Fatalf("lines = %+v, want an inhibit fire line", lines)
	}
	if !strings.Contains(lines[0].Detail, "disable=") {
		t.Fatalf("inhibit fire detail %q does not name what it disabled", lines[0].Detail)
	}
	if got := goalAdmissions(h); got != 1 {
		t.Fatalf("the inhibited goal was re-admitted (%d admissions), want it withheld", got)
	}
}

// A group predicate still writes a parseable line: the SENSE grammar needs a
// non-empty source, and naming one of the group's fields would claim the rule
// keys on a signal it does not.
func TestAGroupPredicateLogsAParseableSource(t *testing.T) {
	h := newHarness(t, reactCfg(rules.Rule{
		ID: "r-group",
		When: rules.Predicate{All: []rules.Predicate{
			leaf(fieldLux, "gt", 1.0),
			leaf(fieldWarm, "is_true", nil),
		}},
		Run: actionBlip, DurationS: seconds(0.02),
	}), nil)
	h.start()
	defer h.stop()

	h.feed(map[string]any{fieldLux: 5.0, fieldWarm: true})
	h.ticks(1)
	lines := h.ruleLines("r-group")
	if len(lines) != 1 {
		t.Fatalf("lines = %+v, want one fire line", lines)
	}
	if lines[0].Source == "" {
		t.Fatal("a group rule wrote an empty source, which breaks the SENSE grammar")
	}
}
