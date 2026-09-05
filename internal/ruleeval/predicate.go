package ruleeval

import (
	"math"

	"github.com/agentculture/neurosymbolic-system/internal/rules"
)

// AbsentFunc answers "how many seconds has this field been absent, continuously,
// as of now". It is injected rather than computed here so predicate evaluation
// stays a pure function of a view plus one number per field — which is what
// makes it testable with no clock, no snapshot and no engine.
type AbsentFunc func(field string) float64

// Eval evaluates one predicate against one tick's view.
//
// It is TOTAL and never panics: a missing field, a nil reading, a comparator
// applied to a type that cannot answer it — every one of those is simply false.
// A predicate that raised would take a rule, and then the tick, with it; a
// predicate that is false is a rule that did not fire, which is the honest
// reading of "there is nothing here to compare".
//
// absent may be nil, in which case absent_for reports no absence at all.
func Eval(p rules.Predicate, view map[string]any, absent AbsentFunc) bool {
	if !p.IsLeaf() {
		return evalGroup(p, view, absent)
	}
	switch p.Op {
	case "absent_for":
		if absent == nil {
			return false
		}
		want, ok := toFloat(p.Value)
		if !ok {
			return false
		}
		return absent(p.Field) >= want
	case "is_true":
		return Present(view, p.Field)
	case "is_false":
		return !Present(view, p.Field)
	}
	left, ok := view[p.Field]
	if !ok || left == nil {
		// No reading: an ordered or equality predicate cannot hold. This is the
		// donor's rule verbatim, and it is why "the sensor is missing" never
		// reads as "the sensor says zero".
		return false
	}
	return compare(p.Op, left, p.Value)
}

// evalGroup resolves an all/any group. An empty all is vacuously true and an
// empty any is false — the standard readings, and unreachable through the
// loader, which refuses a group with no children.
func evalGroup(p rules.Predicate, view map[string]any, absent AbsentFunc) bool {
	for _, child := range p.All {
		if !Eval(child, view, absent) {
			return false
		}
	}
	if len(p.Any) == 0 {
		// Every all-child held (or there were none): an all group is true.
		return true
	}
	for _, child := range p.Any {
		if Eval(child, view, absent) {
			return true
		}
	}
	return false
}

// Present is the is_true reading of a field: it has a value this tick, and if
// that value is a boolean, the boolean is true.
//
// The donor spelled this as two branches — its two always-populated boolean
// CONDITION fields used their flag directly, every other field was "not None".
// One rule covers both, and it generalises to a plant whose conditions are
// declared as bool senses without the library having to know which those are.
func Present(view map[string]any, field string) bool {
	value, ok := view[field]
	if !ok || value == nil {
		return false
	}
	if flag, isBool := value.(bool); isBool {
		return flag
	}
	return true
}

// compare is a total comparison: a type mismatch answers false rather than
// raising, mirroring the donor's TypeError guard.
func compare(op string, left, right any) bool {
	if l, lok := toFloat(left); lok {
		if r, rok := toFloat(right); rok {
			return compareFloat(op, l, r)
		}
		return op == "ne"
	}
	if l, lok := left.(string); lok {
		if r, rok := right.(string); rok {
			return compareString(op, l, r)
		}
		return op == "ne"
	}
	if l, lok := left.(bool); lok {
		if r, rok := right.(bool); rok {
			switch op {
			case "eq":
				return l == r
			case "ne":
				return l != r
			}
			return false
		}
		return op == "ne"
	}
	return op == "ne" && left != right
}

func compareFloat(op string, l, r float64) bool {
	if math.IsNaN(l) || math.IsNaN(r) {
		// NaN compares false to everything, including itself — except ne,
		// which is true for exactly that reason.
		return op == "ne"
	}
	switch op {
	case "gt":
		return l > r
	case "lt":
		return l < r
	case "ge":
		return l >= r
	case "le":
		return l <= r
	case "eq":
		return l == r
	case "ne":
		return l != r
	}
	return false
}

func compareString(op string, l, r string) bool {
	switch op {
	case "gt":
		return l > r
	case "lt":
		return l < r
	case "ge":
		return l >= r
	case "le":
		return l <= r
	case "eq":
		return l == r
	case "ne":
		return l != r
	}
	return false
}

// toFloat widens any numeric reading a transport might feed. A bool is
// deliberately NOT a number: a plant that means "true" must say so.
func toFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	}
	return 0, false
}
