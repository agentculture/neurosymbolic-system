package rules_test

import (
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/rules"
)

// eventFieldVocab declares two senses and one action, and deliberately does
// NOT declare any "source/type" field: an event field's validity must come
// from the loaded config's own [[event]] entries, never from the robot's
// sense vocabulary — internal/events.Router publishes those fields itself.
var eventFieldVocab = donorVocab{
	fields:  set("pat", "rms_ratio"),
	actions: set("nod"),
	params:  map[string]map[string][2]float64{"nod": {}},
}

const eventFieldRules = `schema_version = 1

[[event]]
source = "tracking"
type = "snap_detected"
priority = "HIGH"
urgency = "IMMEDIATE"

[[react]]
id = "snap-acknowledge"
when = { field = "tracking/snap_detected", op = "is_true" }
run = "nod"
`

func TestReactOnDeclaredEventFieldLoadsWithAndWithoutVocabulary(t *testing.T) {
	path := writeTOML(t, "event_field.toml", eventFieldRules)

	for _, tc := range []struct {
		name  string
		vocab rules.Vocabulary
	}{
		{"no vocabulary", nil},
		{"with vocabulary", eventFieldVocab},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := rules.Load([][]string{{path}}, tc.vocab)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if len(cfg.React) != 1 {
				t.Fatalf("React = %v, want the one event-keyed rule", cfg.React)
			}
			if cfg.React[0].When.Field != "tracking/snap_detected" {
				t.Fatalf("predicate field = %q", cfg.React[0].When.Field)
			}
		})
	}
}

// The [[event]] entry may live in a DIFFERENT layer than the rule keyed on
// it: a shipped layer declaring the event and a box-local overlay reacting to
// it is exactly the layering this schema exists to support.
func TestEventFieldDeclaredInAnotherLayerIsValid(t *testing.T) {
	shipped := writeTOML(t, "shipped.toml", `schema_version = 1

[[event]]
source = "tracking"
type = "snap_detected"
priority = "HIGH"
urgency = "IMMEDIATE"
`)
	overlay := writeTOML(t, "overlay.toml", `schema_version = 1

[[react]]
id = "snap-acknowledge"
when = { field = "tracking/snap_detected", op = "is_true" }
run = "nod"
`)
	if _, err := rules.Load([][]string{{shipped}, {overlay}}, eventFieldVocab); err != nil {
		t.Fatalf("load across layers: %v", err)
	}
}

// A tombstoned event still DECLARES its identity: an overlay disabling an
// event must not brick the rule keyed on it — the rule simply abstains
// forever, exactly as it does before the event ever fires.
func TestTombstonedEventStillDeclaresItsField(t *testing.T) {
	shipped := writeTOML(t, "shipped.toml", eventFieldRules)
	overlay := writeTOML(t, "overlay.toml", `schema_version = 1

[[event]]
source = "tracking"
type = "snap_detected"
enabled = false
`)
	cfg, err := rules.Load([][]string{{shipped}, {overlay}}, eventFieldVocab)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Events) != 0 {
		t.Fatalf("Events = %v, want the tombstone to have removed it", cfg.Events)
	}
	if len(cfg.React) != 1 {
		t.Fatalf("React = %v, want the rule kept", cfg.React)
	}
}

func TestEventFieldWithNoMatchingEntryIsStillRefused(t *testing.T) {
	path := writeTOML(t, "orphan_event_field.toml", `schema_version = 1

[[event]]
source = "tracking"
type = "snap_detected"
priority = "HIGH"
urgency = "IMMEDIATE"

[[react]]
id = "orphan"
when = { field = "tracking/never_declared", op = "is_true" }
run = "nod"
`)
	_, err := rules.Load([][]string{{path}}, eventFieldVocab)
	if err == nil {
		t.Fatal("load succeeded, want a refusal naming the undeclared event field")
	}
	for _, want := range []string{"orphan", "tracking/never_declared"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q, want it to contain %q", err, want)
		}
	}
}

// An event field is BOOLEAN: it is present-and-true for one tick or absent.
// An ordered or equality comparison over it is a rule that can never mean
// what its author thought, so it is refused rather than left to never fire.
func TestEventFieldWithNonBooleanOpIsRefused(t *testing.T) {
	path := writeTOML(t, "event_field_op.toml", `schema_version = 1

[[event]]
source = "tracking"
type = "snap_detected"
priority = "HIGH"
urgency = "IMMEDIATE"

[[react]]
id = "bad-op"
when = { field = "tracking/snap_detected", op = "gt", value = 0.5 }
run = "nod"
`)
	_, err := rules.Load([][]string{{path}}, eventFieldVocab)
	if err == nil {
		t.Fatal("load succeeded, want a refusal naming the op")
	}
	for _, want := range []string{"bad-op", "tracking/snap_detected", "gt", "is_true"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q, want it to contain %q", err, want)
		}
	}
}
