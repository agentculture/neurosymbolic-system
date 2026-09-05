package clifmt

import "fmt"

// Exit-code policy, load-bearing across both languages: the Python CLI's
// cli/_errors.py documents the identical mapping, and a Go verb driven from
// there must not disagree about what an exit code means.
//
//	0   success
//	1   user error   (bad argument, invalid input, an unknown verb)
//	2   environment error (missing config, no live engine, an unexpected panic)
//	3+  reserved
const (
	ExitSuccess = 0
	ExitUser    = 1
	ExitEnv     = 2
)

// CliError is the structured shape every verb failure surfaces as. JSON field
// order matches declaration order below, since encoding/json preserves it.
type CliError struct {
	Code        int    `json:"code"`
	Message     string `json:"message"`
	Remediation string `json:"remediation"`
}

// Error implements the error interface so a CliError travels through normal
// Go error-handling paths (errors.As, %w) without a caller needing to know
// its concrete type up front.
func (e *CliError) Error() string {
	return e.Message
}

// NewUserError builds a CliError in the ExitUser bucket: the caller gave a
// bad verb, a bad flag, or a value this engine refuses.
func NewUserError(message, remediation string) *CliError {
	return &CliError{Code: ExitUser, Message: message, Remediation: remediation}
}

// NewEnvError builds a CliError in the ExitEnv bucket: something about the
// environment this process is running in — a missing file, no live engine to
// answer a request, an unexpected panic — is why the verb could not complete.
func NewEnvError(message, remediation string) *CliError {
	return &CliError{Code: ExitEnv, Message: message, Remediation: remediation}
}

// newPanicError builds the CliError Guard recovers a panic into. It is
// unexported: a caller should never manufacture one of these by hand, only
// Guard should, since its message shape (wrapping an arbitrary recovered
// value) is a recovery detail, not a public constructor.
func newPanicError(cause any) *CliError {
	return &CliError{
		Code:    ExitEnv,
		Message: fmt.Sprintf("unexpected: %v", cause),
		Remediation: "this is a bug in the engine, not a mistake in the request — " +
			"file an issue against neurosymbolic-system",
	}
}
