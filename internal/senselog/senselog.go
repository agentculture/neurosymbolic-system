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
)

const lineFormat = "[SENSE stage=%s source=%s event=%s] %s\n"

// Logger writes SENSE-grammar lines to an io.Writer.
type Logger struct {
	w io.Writer
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
	fmt.Fprintf(l.w, lineFormat, sanitize(stage), sanitize(source), sanitize(event), detail)
}
