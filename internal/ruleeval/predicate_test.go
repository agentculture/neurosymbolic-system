package ruleeval_test

import (
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/ruleeval"
	"github.com/agentculture/neurosymbolic-system/internal/rules"
)

// Predicate evaluation is a pure function of a view plus one number per field:
// no clock, no snapshot, no engine. These pin its total, never-raising shape.

func TestOrderedAndEqualityComparators(t *testing.T) {
	view := map[string]any{fieldLux: 4.0, fieldTag: "b", fieldWarm: true}
	cases := []struct {
		field string
		op    string
		value any
		want  bool
	}{
		{fieldLux, "gt", 3.0, true},
		{fieldLux, "gt", 4.0, false},
		{fieldLux, "ge", 4.0, true},
		{fieldLux, "lt", 5.0, true},
		{fieldLux, "le", 4.0, true},
		{fieldLux, "eq", 4.0, true},
		{fieldLux, "ne", 4.0, false},
		{fieldTag, "eq", "b", true},
		{fieldTag, "lt", "c", true},
		{fieldTag, "eq", "c", false},
		{fieldWarm, "eq", true, true},
		{fieldWarm, "ne", true, false},
	}
	for _, tc := range cases {
		got := ruleeval.Eval(leaf(tc.field, tc.op, tc.value), view, nil)
		if got != tc.want {
			t.Errorf("%s %s %v = %v, want %v", tc.field, tc.op, tc.value, got, tc.want)
		}
	}
}

// A widened integer reading compares like the float it is: a transport that
// feeds an int must not silently stop matching a rule written against 3.0.
func TestNumericReadingsAreWidened(t *testing.T) {
	for _, value := range []any{4, int64(4), float32(4), uint8(4)} {
		if !ruleeval.Eval(leaf(fieldLux, "gt", 3.0), map[string]any{fieldLux: value}, nil) {
			t.Errorf("a %T reading did not compare as a number", value)
		}
	}
}

// No reading means an ordered or equality predicate CANNOT hold — never that
// the sensor read zero.
func TestAMissingReadingIsNeverZero(t *testing.T) {
	for _, view := range []map[string]any{{}, {fieldLux: nil}} {
		if ruleeval.Eval(leaf(fieldLux, "lt", 1.0), view, nil) {
			t.Errorf("a missing reading satisfied 'lt 1' in view %v — it must not", view)
		}
		if ruleeval.Eval(leaf(fieldLux, "eq", 0.0), view, nil) {
			t.Errorf("a missing reading satisfied 'eq 0' in view %v — it must not", view)
		}
	}
}

// A type mismatch is FALSE, not a panic: a rule that raised would take the tick
// with it.
func TestATypeMismatchIsFalseNotAPanic(t *testing.T) {
	view := map[string]any{fieldTag: "text", fieldLux: 1.0}
	if ruleeval.Eval(leaf(fieldTag, "gt", 3.0), view, nil) {
		t.Error("a string compared greater than a number")
	}
	if ruleeval.Eval(leaf(fieldLux, "eq", "text"), view, nil) {
		t.Error("a number compared equal to a string")
	}
}

// is_true is "the cue fired": a value is present, and a boolean value is true.
func TestIsTrueAndIsFalse(t *testing.T) {
	cases := []struct {
		view map[string]any
		want bool
	}{
		{map[string]any{fieldWarm: true}, true},
		{map[string]any{fieldWarm: false}, false},
		{map[string]any{fieldWarm: nil}, false},
		{map[string]any{}, false},
		{map[string]any{fieldWarm: 0.0}, true}, // a non-boolean READING is a cue
	}
	for _, tc := range cases {
		if got := ruleeval.Eval(leaf(fieldWarm, "is_true", nil), tc.view, nil); got != tc.want {
			t.Errorf("is_true over %v = %v, want %v", tc.view, got, tc.want)
		}
		if got := ruleeval.Eval(leaf(fieldWarm, "is_false", nil), tc.view, nil); got == tc.want {
			t.Errorf("is_false over %v = %v, want the negation", tc.view, got)
		}
	}
}

func TestAbsentForReadsTheInjectedClock(t *testing.T) {
	absent := func(field string) float64 {
		if field == fieldLux {
			return 0.5
		}
		return 0
	}
	if !ruleeval.Eval(leaf(fieldLux, "absent_for", 0.5), nil, absent) {
		t.Error("absent_for did not hold at exactly its threshold")
	}
	if ruleeval.Eval(leaf(fieldLux, "absent_for", 0.6), nil, absent) {
		t.Error("absent_for held below its threshold")
	}
	if ruleeval.Eval(leaf(fieldLux, "absent_for", 0.5), nil, nil) {
		t.Error("absent_for held with no clock wired — it must report no absence")
	}
}

func TestGroups(t *testing.T) {
	view := map[string]any{fieldLux: 4.0, fieldWarm: true}
	all := rules.Predicate{All: []rules.Predicate{
		leaf(fieldLux, "gt", 1.0),
		leaf(fieldWarm, "is_true", nil),
	}}
	if !ruleeval.Eval(all, view, nil) {
		t.Error("an all group with both children true did not hold")
	}
	allFails := rules.Predicate{All: []rules.Predicate{
		leaf(fieldLux, "gt", 1.0),
		leaf(fieldWarm, "is_false", nil),
	}}
	if ruleeval.Eval(allFails, view, nil) {
		t.Error("an all group held with a false child")
	}
	any := rules.Predicate{Any: []rules.Predicate{
		leaf(fieldWarm, "is_false", nil),
		leaf(fieldLux, "gt", 1.0),
	}}
	if !ruleeval.Eval(any, view, nil) {
		t.Error("an any group with one true child did not hold")
	}
	anyFails := rules.Predicate{Any: []rules.Predicate{
		leaf(fieldWarm, "is_false", nil),
		leaf(fieldLux, "lt", 1.0),
	}}
	if ruleeval.Eval(anyFails, view, nil) {
		t.Error("an any group held with no true child")
	}
	nested := rules.Predicate{Any: []rules.Predicate{
		{All: []rules.Predicate{leaf(fieldLux, "gt", 1.0), leaf(fieldWarm, "is_true", nil)}},
		leaf(fieldTag, "eq", "never"),
	}}
	if !ruleeval.Eval(nested, view, nil) {
		t.Error("a nested all-inside-any did not hold")
	}
}

func TestAnUnknownComparatorIsFalse(t *testing.T) {
	if ruleeval.Eval(leaf(fieldLux, "sideways", 1.0), map[string]any{fieldLux: 4.0}, nil) {
		t.Error("an unknown comparator held; a predicate nobody implements must be false")
	}
}
