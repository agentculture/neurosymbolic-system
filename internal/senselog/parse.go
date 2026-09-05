package senselog

import (
	"fmt"
	"strings"
)

// Line is a parsed SENSE-grammar log line.
type Line struct {
	Stage   string
	Source  string
	Event   string
	Dropped bool
	Reason  string
	Detail  string
}

const (
	prefix     = "[SENSE stage="
	sourceMark = " source="
	eventMark  = " event="
	closeMark  = "] "
	dropPrefix = "dropped reason="
)

// Parse parses one SENSE-grammar line (with or without a trailing newline)
// into a Line. It returns an error if line does not match the fixed
// "[SENSE stage=<stage> source=<source> event=<event>] <detail>" grammar.
func Parse(line string) (Line, error) {
	line = strings.TrimRight(line, "\n")

	if !strings.HasPrefix(line, prefix) {
		return Line{}, fmt.Errorf("senselog: line does not start with %q: %q", prefix, line)
	}
	rest := line[len(prefix):]

	sourceIdx := strings.Index(rest, sourceMark)
	if sourceIdx < 0 {
		return Line{}, fmt.Errorf("senselog: missing %q: %q", sourceMark, line)
	}
	stage := rest[:sourceIdx]
	rest = rest[sourceIdx+len(sourceMark):]

	eventIdx := strings.Index(rest, eventMark)
	if eventIdx < 0 {
		return Line{}, fmt.Errorf("senselog: missing %q: %q", eventMark, line)
	}
	source := rest[:eventIdx]
	rest = rest[eventIdx+len(eventMark):]

	closeIdx := strings.Index(rest, closeMark)
	if closeIdx < 0 {
		return Line{}, fmt.Errorf("senselog: missing closing %q: %q", closeMark, line)
	}
	event := rest[:closeIdx]
	detail := rest[closeIdx+len(closeMark):]

	if stage == "" || source == "" || event == "" {
		return Line{}, fmt.Errorf("senselog: empty stage/source/event field: %q", line)
	}

	result := Line{Stage: stage, Source: source, Event: event, Detail: detail}

	if strings.HasPrefix(detail, dropPrefix) {
		remainder := detail[len(dropPrefix):]
		result.Dropped = true
		if spaceIdx := strings.Index(remainder, " "); spaceIdx >= 0 {
			result.Reason = remainder[:spaceIdx]
			result.Detail = remainder[spaceIdx+1:]
		} else {
			result.Reason = remainder
			result.Detail = ""
		}
	}

	return result, nil
}
