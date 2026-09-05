// Package senselog is the neurosymbolic-system engine's per-stage sensory
// logging grammar.
//
// Cited (cite-don't-import) from reachy-mini-cli's reachy/senselog.py, which
// itself cites reachy_nova's reachy_nova/sensory_log.py — see
// docs/skill-sources.md. It gives every stage of a sense pipeline (capture,
// gate, inject, reaction, ...) one grep-able, parseable stderr line, so "a
// sense was heard/handled correctly" — or deliberately dropped, and why — is
// verifiable from the log alone. stdout is never written by this package.
//
// Line shape (fixed, parseable):
//
//	[SENSE stage=<stage> source=<source> event=<event>] <detail>
//
// Example:
//
//	[SENSE stage=vad source=speech event=3f2a9c1e] utterance detected
//
// A dropped sense uses the same shape via Drop, whose detail always names
// the reason so it stays greppable:
//
//	[SENSE stage=engagement source=speech event=3f2a9c1e] dropped reason=self-mute
//
// Suppressions are logged per EPISODE, not per tick — the donor's #99 fix,
// implemented here by Streak. A gated streak of N ticks under one reason
// emits exactly one entry line, one line per reason change, and one summary
// line naming every reason seen and the streak length when it ends.
//
// This package is intentionally tiny and pure: it only formats and writes
// lines to the io.Writer it was given. It installs no handlers and owns no
// process-wide logging configuration — that is the composition root's job.
package senselog

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

const lineFormat = "[SENSE stage=%s source=%s event=%s] %s\n"

// Logger writes SENSE-grammar lines to an io.Writer.
//
// A Logger is safe for concurrent use: mu serializes every write so two
// goroutines logging at once (t10 worked around the lack of this with a
// provider-local mutex) can never interleave one line's bytes with another's,
// which would otherwise corrupt the fixed "[SENSE ...] " grammar a consumer
// parses line by line.
type Logger struct {
	mu sync.Mutex
	w  io.Writer
}

// New returns a Logger that writes to w.
func New(w io.Writer) *Logger {
	return &Logger{w: w}
}

// Default returns a Logger writing to os.Stderr — the process-wide default.
// stdout must never carry a SENSE line, so every drop/stage site should use
// this (or an explicit New(os.Stderr)) rather than a stdout-attached writer.
func Default() *Logger {
	return New(os.Stderr)
}

// sanitize replaces spaces and ']' in a grammar value with '_', since those
// characters would break the fixed "[SENSE stage=... ] detail" shape.
func sanitize(value string) string {
	value = strings.ReplaceAll(value, " ", "_")
	value = strings.ReplaceAll(value, "]", "_")
	return value
}

// sanitizeDetail replaces every control character in a detail with '_'.
//
// detail is the one field of the grammar this package does not control: it
// carries error strings, peer-supplied names and rule ids from callers that
// got them off a wire. A '\n' in there ends the record early and the bytes
// after it become, to a consumer reading line by line, a SECOND record — one
// a peer wrote. That is a forged log entry, so the newline is removed rather
// than trusted. Spaces and ']' stay: the detail is the free-text tail, and
// sanitize's stricter rule is only right for the three keyed fields.
func sanitizeDetail(detail string) string {
	if strings.IndexFunc(detail, isControl) < 0 {
		return detail
	}
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return '_'
		}
		return r
	}, detail)
}

// isControl reports whether r would break the one-record-per-line grammar.
func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// Stage emits one SENSE-grammar line for a pipeline stage that ran normally.
func (l *Logger) Stage(stage, source, event, detail string) {
	l.write(stage, source, event, detail)
}

// Drop emits one SENSE-grammar line for a dropped event, naming why. detail
// is optional extra context appended after the "dropped reason=<reason>"
// token; pass "" when the reason alone says enough.
func (l *Logger) Drop(stage, source, event, reason, detail string) {
	msg := "dropped reason=" + sanitize(reason)
	if detail != "" {
		msg += " " + detail
	}
	l.write(stage, source, event, msg)
}

func (l *Logger) write(stage, source, event, detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, lineFormat,
		sanitize(stage), sanitize(source), sanitize(event), sanitizeDetail(detail))
}
