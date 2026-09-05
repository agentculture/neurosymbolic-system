package clifmt

import (
	"errors"
	"strings"
	"testing"
)

func TestGuardSuccessReturnsNil(t *testing.T) {
	if got := Guard(func() error { return nil }); got != nil {
		t.Fatalf("Guard() = %v, want nil", got)
	}
}

func TestGuardPassesThroughCliErrorUnchanged(t *testing.T) {
	original := &CliError{Code: ExitUser, Message: "bad path", Remediation: "known paths: whoami, version"}
	got := Guard(func() error { return original })
	if got != original {
		t.Fatalf("Guard() = %v, want the same *CliError instance %v", got, original)
	}
	if got.Code != ExitUser {
		t.Fatalf("Code = %d, want %d (a deliberate user error must not be reclassified)", got.Code, ExitUser)
	}
}

func TestGuardWrapsPlainError(t *testing.T) {
	got := Guard(func() error { return errors.New("disk full") })
	if got == nil {
		t.Fatal("Guard() = nil, want a wrapped CliError")
	}
	if got.Code != ExitEnv {
		t.Fatalf("Code = %d, want %d for an unrecognised error", got.Code, ExitEnv)
	}
	if !strings.Contains(got.Message, "disk full") {
		t.Fatalf("Message %q does not mention the underlying error", got.Message)
	}
}

func TestGuardRecoversPanic(t *testing.T) {
	got := Guard(func() error {
		panic("something exploded")
	})
	if got == nil {
		t.Fatal("Guard() = nil, want a CliError recovered from the panic")
	}
	if got.Code != ExitEnv {
		t.Fatalf("Code = %d, want %d", got.Code, ExitEnv)
	}
	if !strings.Contains(got.Message, "something exploded") {
		t.Fatalf("Message %q does not mention the panic value", got.Message)
	}
}

func TestGuardRecoversPanicWithNonStringValue(t *testing.T) {
	got := Guard(func() error {
		panic(errors.New("typed panic"))
	})
	if got == nil || got.Code != ExitEnv {
		t.Fatalf("Guard() = %v, want an ExitEnv CliError", got)
	}
}
