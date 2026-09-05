package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// capture runs fn with a pipe wired up as an *os.File, and returns everything
// written to it as a string. run() takes *os.File (not io.Writer) so main()
// can hand it os.Stdout/os.Stderr directly without an adapter; tests use a
// pipe to get a real *os.File back.
func capture(t *testing.T, fn func(w *os.File)) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	fn(w)
	w.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return string(out)
}

func TestRunVersionDefault(t *testing.T) {
	origVersion, origRevision := version, revision
	defer func() { version, revision = origVersion, origRevision }()
	version, revision = "0.0.0-dev", "unknown"

	var code int
	stdout := capture(t, func(w *os.File) {
		code = run([]string{"version"}, w, os.Stderr)
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	want := "neurosymbolic-engine 0.0.0-dev (unknown)\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestRunVersionLdflagsOverride(t *testing.T) {
	origVersion, origRevision := version, revision
	defer func() { version, revision = origVersion, origRevision }()
	version, revision = "1.2.3", "abc1234"

	var code int
	stdout := capture(t, func(w *os.File) {
		code = run([]string{"version"}, w, os.Stderr)
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	want := "neurosymbolic-engine 1.2.3 (abc1234)\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestRunNoArgsExitsOneWithUsageOnStderr(t *testing.T) {
	var code int
	stderr := capture(t, func(w *os.File) {
		code = run(nil, os.Stdout, w)
	})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Fatalf("stderr = %q, want it to contain %q", stderr, "Usage:")
	}
}

func TestRunUnknownCommandExitsOneWithErrorOnStderr(t *testing.T) {
	var code int
	stderr := capture(t, func(w *os.File) {
		code = run([]string{"bogus"}, os.Stdout, w)
	})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Fatalf("stderr = %q, want it to contain %q", stderr, "unknown command")
	}
}
