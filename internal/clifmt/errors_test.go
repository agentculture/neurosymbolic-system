package clifmt

import (
	"errors"
	"strings"
	"testing"
)

func TestCliErrorImplementsError(t *testing.T) {
	err := &CliError{Code: ExitUser, Message: "bad flag", Remediation: "try again"}

	var asError error = err
	if asError.Error() != "bad flag" {
		t.Fatalf("Error() = %q, want %q", asError.Error(), "bad flag")
	}

	var target *CliError
	if !errors.As(asError, &target) {
		t.Fatalf("errors.As failed to unwrap *CliError")
	}
	if target.Code != ExitUser {
		t.Fatalf("unwrapped Code = %d, want %d", target.Code, ExitUser)
	}
}

func TestNewUserError(t *testing.T) {
	err := NewUserError("bad input", "fix it")
	if err.Code != ExitUser {
		t.Fatalf("Code = %d, want %d", err.Code, ExitUser)
	}
	if err.Message != "bad input" || err.Remediation != "fix it" {
		t.Fatalf("err = %+v", err)
	}
}

func TestNewEnvError(t *testing.T) {
	err := NewEnvError("no live engine", "restart it")
	if err.Code != ExitEnv {
		t.Fatalf("Code = %d, want %d", err.Code, ExitEnv)
	}
	if err.Message != "no live engine" || err.Remediation != "restart it" {
		t.Fatalf("err = %+v", err)
	}
}

func TestNewPanicErrorWrapsCause(t *testing.T) {
	err := newPanicError("boom")
	if err.Code != ExitEnv {
		t.Fatalf("Code = %d, want %d", err.Code, ExitEnv)
	}
	if !strings.Contains(err.Message, "boom") {
		t.Fatalf("Message %q does not mention the cause", err.Message)
	}
}

func TestNewPanicErrorWrapsArbitraryValue(t *testing.T) {
	// panic() accepts any value, not just errors/strings; newPanicError must
	// not blow up formatting one.
	err := newPanicError(42)
	if !strings.Contains(err.Message, "42") {
		t.Fatalf("Message %q does not mention the panic value", err.Message)
	}
}

func TestExitCodePolicyValues(t *testing.T) {
	// Locks the exit-code policy the Python CLI's cli/_errors.py documents —
	// a stable contract the two languages must not disagree about.
	if ExitSuccess != 0 {
		t.Fatalf("ExitSuccess = %d, want 0", ExitSuccess)
	}
	if ExitUser != 1 {
		t.Fatalf("ExitUser = %d, want 1", ExitUser)
	}
	if ExitEnv != 2 {
		t.Fatalf("ExitEnv = %d, want 2", ExitEnv)
	}
}
