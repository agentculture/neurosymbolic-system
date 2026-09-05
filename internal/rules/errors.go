package rules

import (
	"fmt"
	"sort"
	"strings"
)

// Error is every refusal this package produces. It renders as
//
//	rules: <path>: rule '<id>': <what> — <fix>
//
// or, for a failure with no rule to name (a bad schema_version, an unknown
// top-level field, a mode), as
//
//	rules: <path>: <what> — <fix>
//
// The fix half is not decoration: it names the accepted values, versions or
// fields, so a refusal tells an operator what to write instead of only that
// what they wrote is wrong.
type Error struct {
	// Path is the file the refusal came from.
	Path string
	// RuleID names the offending rule or event where one is identifiable —
	// the rule's own id, an event's "source/type" identity, or a positional
	// label (react[0], event[0]) when the id is what is wrong.
	RuleID string
	// Label is the noun the message uses for RuleID — "rule" or "event".
	// Empty behaves as "rule", which keeps every pre-existing rule refusal's
	// wording unchanged.
	Label string
	// What went wrong.
	What string
	// Fix names the accepted values.
	Fix string
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("rules: ")
	b.WriteString(e.Path)
	b.WriteString(": ")
	if e.RuleID != "" {
		label := e.Label
		if label == "" {
			label = "rule"
		}
		fmt.Fprintf(&b, "%s '%s': ", label, e.RuleID)
	}
	b.WriteString(e.What)
	if e.Fix != "" {
		b.WriteString(" — ")
		b.WriteString(e.Fix)
	}
	return b.String()
}

// ctx carries the file path and the current rule/event label through
// validation, so no validator has to be handed both separately.
type ctx struct {
	path   string
	ruleID string
	label  string
}

func (c ctx) rule(id string) ctx { c.ruleID = id; c.label = "rule"; return c }

// event returns a ctx labeling id as an event's "source/type" identity, so
// its refusals render as "event '<id>': ..." rather than "rule '<id>': ...".
func (c ctx) event(id string) ctx { c.ruleID = id; c.label = "event"; return c }

func (c ctx) errf(fix string, format string, args ...any) *Error {
	return &Error{
		Path: c.path, RuleID: c.ruleID, Label: c.label,
		What: fmt.Sprintf(format, args...), Fix: fix,
	}
}

// sortedKeys renders a name set for a remediation half, deterministically.
func sortedKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
