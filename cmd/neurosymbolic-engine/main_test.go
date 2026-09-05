package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
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
	if !strings.Contains(stderr, "hint:") {
		t.Fatalf("stderr = %q, want it to contain %q", stderr, "hint:")
	}
}

func TestRunUnknownCommandJSON(t *testing.T) {
	var code int
	var stdout, stderr string
	stdout = capture(t, func(w *os.File) {
		stderr = capture(t, func(errW *os.File) {
			code = run([]string{"bogus", "--json"}, w, errW)
		})
	})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	var decoded struct {
		Code        int    `json:"code"`
		Message     string `json:"message"`
		Remediation string `json:"remediation"`
	}
	if err := json.Unmarshal([]byte(stderr), &decoded); err != nil {
		t.Fatalf("stderr is not valid JSON: %v (%q)", err, stderr)
	}
	if decoded.Code != 1 {
		t.Fatalf("decoded code = %d, want 1", decoded.Code)
	}
}

func TestRunRulesNoSubverbExitsOneWithUsage(t *testing.T) {
	var code int
	stderr := capture(t, func(w *os.File) {
		code = run([]string{"rules"}, os.Stdout, w)
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Fatalf("stderr = %q, want it to contain %q", stderr, "Usage:")
	}
}

func TestRunWhoamiIncludesModulePath(t *testing.T) {
	var code int
	stdout := capture(t, func(w *os.File) {
		code = run([]string{"whoami"}, w, os.Stderr)
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "neurosymbolic-engine") {
		t.Fatalf("stdout = %q, want it to contain the tool name", stdout)
	}
	if !strings.Contains(stdout, "module:") {
		t.Fatalf("stdout = %q, want a module: line", stdout)
	}
}

func TestRunDoctorReportsHealthy(t *testing.T) {
	var code int
	stdout := capture(t, func(w *os.File) {
		code = run([]string{"doctor"}, w, os.Stderr)
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stdout=%q)", code, stdout)
	}
	if !strings.Contains(stdout, "healthy") {
		t.Fatalf("stdout = %q, want it to report healthy", stdout)
	}
}

func TestRunRulesCheckSuccess(t *testing.T) {
	rulesPath := filepath.Join("..", "..", "internal", "rules", "testdata", "reachy", "default_rules.v1.toml")
	var code int
	stdout := capture(t, func(w *os.File) {
		code = run([]string{"rules", "check", rulesPath}, w, os.Stderr)
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stdout=%q)", code, stdout)
	}
	if !strings.Contains(stdout, "rules:") {
		t.Fatalf("stdout = %q, want a rules: summary", stdout)
	}
}

func TestRunRulesCheckFailureExitsOneWithHint(t *testing.T) {
	rulesPath := filepath.Join("..", "..", "internal", "rules", "testdata", "reachy", "default_rules.toml")
	var code int
	stderr := capture(t, func(w *os.File) {
		code = run([]string{"rules", "check", rulesPath}, os.Stdout, w)
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr=%q)", code, stderr)
	}
	if !strings.HasPrefix(stderr, "error:") {
		t.Fatalf("stderr = %q, want it to start with 'error:'", stderr)
	}
	if !strings.Contains(stderr, "hint:") {
		t.Fatalf("stderr = %q, want it to contain 'hint:'", stderr)
	}
}

func TestRunStatusWithNoLiveEngineExitsTwo(t *testing.T) {
	var code int
	stderr := capture(t, func(w *os.File) {
		code = run([]string{"status"}, os.Stdout, w)
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (stderr=%q)", code, stderr)
	}
}
