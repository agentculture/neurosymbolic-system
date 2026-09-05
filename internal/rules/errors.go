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
	// RuleID names the offending rule where one is identifiable — the rule's
	// own id, or its positional label (react[0]) when the id is what is wrong.
	RuleID string
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
		fmt.Fprintf(&b, "rule '%s': ", e.RuleID)
	}
	b.WriteString(e.What)
	if e.Fix != "" {
		b.WriteString(" — ")
		b.WriteString(e.Fix)
	}
	return b.String()
}

// ctx carries the file path and the current rule label through validation, so
// no validator has to be handed both separately.
type ctx struct {
	path   string
	ruleID string
}

func (c ctx) rule(id string) ctx { c.ruleID = id; return c }

func (c ctx) errf(fix string, format string, args ...any) *Error {
	return &Error{Path: c.path, RuleID: c.ruleID, What: fmt.Sprintf(format, args...), Fix: fix}
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
