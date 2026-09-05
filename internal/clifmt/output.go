package clifmt

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// EmitResult writes a human-readable command result to w, ensuring exactly
// one trailing newline.
func EmitResult(w io.Writer, text string) {
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	fmt.Fprint(w, text)
}

// EmitResultJSON writes v to w as single-line JSON followed by a newline.
func EmitResultJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// Emit writes err to w. In text mode it renders the two-line agent-first
// shape:
//
//	error: <message>
//	hint: <remediation>
//
// (the hint line only when Remediation is non-empty). In JSON mode it writes
// {"code","message","remediation"} as single-line JSON. This is the one
// rendering both cmd/neurosymbolic-engine/main.go and (later) the socket
// front use for a failed verb, so the two transports can never drift apart
// on what an error looks like.
func Emit(w io.Writer, err *CliError, jsonMode bool) error {
	if jsonMode {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return enc.Encode(err)
	}
	if _, wErr := fmt.Fprintf(w, "error: %s\n", err.Message); wErr != nil {
		return wErr
	}
	if err.Remediation != "" {
		if _, wErr := fmt.Fprintf(w, "hint: %s\n", err.Remediation); wErr != nil {
			return wErr
		}
	}
	return nil
}

// EmitDiagnostic writes a plain-text human diagnostic (progress, a doctor
// check line) to w, ensuring exactly one trailing newline. Diagnostics are
// never JSON-rendered — they are for human eyes, not the machine-readable
// result/error channels.
func EmitDiagnostic(w io.Writer, message string) {
	if !strings.HasSuffix(message, "\n") {
		message += "\n"
	}
	fmt.Fprint(w, message)
}
