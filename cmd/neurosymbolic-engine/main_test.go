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

// No command is a REFUSAL, and every refusal this CLI makes is the same two
// lines. It used to print the whole usage screen to stderr instead, which
// neither the Python front nor an agent parsing stderr can read.
func TestRunNoArgsEmitsTheTwoLineErrorContract(t *testing.T) {
	var code int
	stderr := capture(t, func(w *os.File) {
		code = run(nil, os.Stdout, w)
	})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stderr = %q, want exactly two lines", stderr)
	}
	if lines[0] != "error: no command given" {
		t.Errorf("first line = %q, want %q", lines[0], "error: no command given")
	}
	if !strings.HasPrefix(lines[1], "hint: ") {
		t.Errorf("second line = %q, want a hint: line", lines[1])
	}
	for _, want := range []string{"help", "run", "rules check"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("hint = %q, want it to name %q", lines[1], want)
		}
	}
	if strings.Contains(stderr, "Usage:") {
		t.Error("stderr still carries the usage screen; that is what `help` is for")
	}
}

func TestRunNoArgsJSON(t *testing.T) {
	var code int
	var stdout, stderr string
	stdout = capture(t, func(w *os.File) {
		stderr = capture(t, func(errW *os.File) {
			code = run([]string{"--json"}, w, errW)
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
	if decoded.Code != 1 || decoded.Message != "no command given" {
		t.Fatalf("decoded = %+v", decoded)
	}
	if decoded.Remediation == "" {
		t.Error("remediation is empty")
	}
}

// Asking for help is a SUCCESS: the usage screen goes to stdout with exit 0,
// which is the only way to see the command list without causing a failure.
func TestRunHelpPrintsUsageOnStdout(t *testing.T) {
	var code int
	var stdout, stderr string
	stdout = capture(t, func(w *os.File) {
		stderr = capture(t, func(errW *os.File) {
			code = run([]string{"help"}, w, errW)
		})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "Usage:") {
		t.Errorf("stdout = %q, want the usage screen", stdout)
	}
	for _, want := range []string{"run", "rules migrate", "help"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("usage does not list %q", want)
		}
	}
}

func TestRunHelpJSON(t *testing.T) {
	var code int
	stdout := capture(t, func(w *os.File) {
		code = run([]string{"help", "--json"}, w, os.Stderr)
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var decoded struct {
		Usage    string   `json:"usage"`
		Commands []string `json:"commands"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout)
	}
	if !strings.Contains(decoded.Usage, "Usage:") {
		t.Errorf("usage = %q", decoded.Usage)
	}
	var sawRun bool
	for _, name := range decoded.Commands {
		if name == "run" {
			sawRun = true
		}
	}
	if !sawRun {
		t.Errorf("commands = %v, want 'run' listed", decoded.Commands)
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

// A noun group with no sub-verb is the other shape that used to print a usage
// screen. Its hint lists only that group's sub-verbs — the whole verb list is
// noise when the caller already picked a noun.
func TestRunRulesNoSubverbEmitsTheTwoLineErrorContract(t *testing.T) {
	var code int
	stderr := capture(t, func(w *os.File) {
		code = run([]string{"rules"}, os.Stdout, w)
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stderr = %q, want exactly two lines", stderr)
	}
	if lines[0] != "error: no sub-command given for 'rules'" {
		t.Errorf("first line = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "hint: ") {
		t.Errorf("second line = %q, want a hint: line", lines[1])
	}
	for _, want := range []string{"rules check", "rules list", "rules migrate", "rules reload"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("hint = %q, want it to name %q", lines[1], want)
		}
	}
	if strings.Contains(lines[1], "version") {
		t.Errorf("hint = %q, want only the 'rules' group's sub-verbs", lines[1])
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
