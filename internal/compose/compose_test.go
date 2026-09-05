package compose

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
)

// --- flag parsing: fail-closed, and every refusal names the fix -------------

func TestParseArgsAcceptsAFullInvocation(t *testing.T) {
	opts, err := ParseArgs([]string{
		"--adaptor", "robot.toml",
		"--rules", "shipped.toml",
		"--rules", "local.toml",
		"--socket-dir", "/run/robot",
		"--period", "10ms",
		"--heartbeat", "250ms",
		"--base-action", "idle-layer",
	})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if opts.AdaptorPath != "robot.toml" || opts.SocketDir != "/run/robot" {
		t.Errorf("opts = %+v", opts)
	}
	if opts.Period != 10*time.Millisecond || opts.Heartbeat != 250*time.Millisecond {
		t.Errorf("period=%v heartbeat=%v", opts.Period, opts.Heartbeat)
	}
	if opts.BaseAction != "idle-layer" {
		t.Errorf("BaseAction = %q", opts.BaseAction)
	}
	// The whole point of the repeatable flag: ORDER, one layer per occurrence.
	want := [][]string{{"shipped.toml"}, {"local.toml"}}
	got := opts.layers()
	if len(got) != len(want) || got[0][0] != want[0][0] || got[1][0] != want[1][0] {
		t.Errorf("layers() = %v, want %v", got, want)
	}
}

func TestParseArgsDefaultsThePeriodToFiftyHertz(t *testing.T) {
	opts, err := ParseArgs([]string{"--adaptor", "robot.toml", "--stdio"})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if opts.Period != DefaultPeriod {
		t.Errorf("Period = %v, want %v", opts.Period, DefaultPeriod)
	}
}

func TestParseArgsRefusals(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		match string
	}{
		{
			"no adaptor", []string{"--stdio"},
			"nothing declares this robot's channels",
		},
		{
			"no transport", []string{"--adaptor", "robot.toml"},
			"no transport was chosen",
		},
		{
			"two transports",
			[]string{"--adaptor", "robot.toml", "--stdio", "--socket-dir", "/run"},
			"exactly one transport",
		},
		{
			"non-positive period",
			[]string{"--adaptor", "robot.toml", "--stdio", "--period", "0s"},
			"must be greater than zero",
		},
		{
			"unknown flag",
			[]string{"--adaptor", "robot.toml", "--stdio", "--turbo"},
			"turbo",
		},
		{
			"positional argument",
			[]string{"--adaptor", "robot.toml", "--stdio", "extra"},
			"takes no positional arguments",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseArgs(tc.args)
			if err == nil {
				t.Fatal("want a refusal, got none")
			}
			var cliErr *clifmt.CliError
			if !errors.As(err, &cliErr) {
				t.Fatalf("err = %T, want *clifmt.CliError", err)
			}
			if cliErr.Code != clifmt.ExitUser {
				t.Errorf("Code = %d, want %d", cliErr.Code, clifmt.ExitUser)
			}
			if !strings.Contains(cliErr.Message, tc.match) {
				t.Errorf("Message = %q, want it to mention %q", cliErr.Message, tc.match)
			}
			// A refusal an operator cannot act on is a refusal they will work
			// around by disabling the check.
			if cliErr.Remediation == "" {
				t.Error("Remediation is empty")
			}
		})
	}
}

// TestTCPIsOnlySpelledInsecurely pins that the only way to ask for a TCP
// listener is a flag that says what it costs. There is no plain --tcp, so a
// listener that lets anything routable to the box admit intents cannot be
// switched on by an operator who never read the warning.
func TestTCPIsOnlySpelledInsecurely(t *testing.T) {
	if _, err := ParseArgs([]string{"--adaptor", "r.toml", "--tcp", "127.0.0.1:0"}); err == nil {
		t.Fatal("--tcp was accepted; TCP must only be spellable as --insecure-tcp")
	}
	opts, err := ParseArgs([]string{"--adaptor", "r.toml", "--insecure-tcp", "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	cfg := opts.streamConfig("v")
	if cfg.TCPAddr != "127.0.0.1:0" || !cfg.InsecureTCP {
		t.Errorf("streamConfig = %+v, want the address AND the acknowledgement", cfg)
	}
}

func TestUnrecognizedAdaptorExtensionIsRefusedByName(t *testing.T) {
	_, err := isTOMLConfig("robot.yaml")
	if err == nil {
		t.Fatal("want a refusal for a .yaml adaptor config")
	}
	if !strings.Contains(err.Error(), "unrecognized extension") {
		t.Errorf("err = %v", err)
	}
}

// --- building the runtime ---------------------------------------------------

// fixtureDir is the toy robot the end-to-end test drives, reused here so the Go
// and Python sides cannot come to disagree about what a valid config looks like.
func fixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		candidate := filepath.Join(dir, "tests", "toy_robot")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no tests/toy_robot fixture found above the test's working directory")
		}
		dir = parent
	}
}

func toyOptions(t *testing.T) Options {
	t.Helper()
	fixtures := fixtureDir(t)
	return Options{
		AdaptorPath: filepath.Join(fixtures, "adaptor.toml"),
		RuleLayers:  []string{filepath.Join(fixtures, "rules.toml")},
		SocketDir:   t.TempDir(),
		Period:      DefaultPeriod,
		Heartbeat:   100 * time.Millisecond,
		BaseAction:  "hum",
	}
}

// TestNewWiresTheWholeRuntime is the composition root's own acceptance test:
// an adaptor config and a rules file, and every part exists and is connected.
func TestNewWiresTheWholeRuntime(t *testing.T) {
	var stderr strings.Builder
	runtime, err := New(toyOptions(t), Build{Version: "0.0.0-test"}, nil, nil, &stderr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if runtime.Vocabulary == nil || runtime.Snapshot == nil || runtime.Engine == nil ||
		runtime.Server == nil || runtime.Rules == nil || runtime.Handler == nil {
		t.Fatalf("runtime has a nil part: %+v", runtime)
	}
	if runtime.Rules.Layers() != 1 {
		t.Errorf("Layers() = %d, want 1", runtime.Rules.Layers())
	}
	// The handler a management frame reaches is the SAME one the argv front
	// uses, with the live engine's reload and status wired in — that is what
	// makes `status` over a socket and `status` from a shell one verb.
	if runtime.Handler.Reloader == nil || runtime.Handler.Status == nil {
		t.Error("the handler has no live-engine sources installed")
	}
}

func TestNewRefusesAnUndeclaredBaseAction(t *testing.T) {
	opts := toyOptions(t)
	opts.BaseAction = "not-an-action"
	var stderr strings.Builder
	_, err := New(opts, Build{}, nil, nil, &stderr)
	if err == nil {
		t.Fatal("want a refusal for a base action the vocabulary does not declare")
	}
	if !strings.Contains(err.Error(), "not-an-action") {
		t.Errorf("err = %v, want it to name the offender", err)
	}
}

func TestNewRefusesARulesFileTheRobotCannotSatisfy(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "rules.toml")
	// A field this robot does not declare: refused at load, never "a rule that
	// simply never fires", which is a rule the operator believes is working.
	body := "schema_version = 2\n\n[[react]]\nid = \"x\"\n" +
		"when = { field = \"no_such_sense\", op = \"is_true\" }\nrun = \"wave\"\n"
	if err := os.WriteFile(bad, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	opts := toyOptions(t)
	opts.RuleLayers = []string{bad}

	var stderr strings.Builder
	_, err := New(opts, Build{}, nil, nil, &stderr)
	if err == nil {
		t.Fatal("want a refusal for a rule keyed on an undeclared sense")
	}
	if !strings.Contains(err.Error(), "no_such_sense") {
		t.Errorf("err = %v, want it to name the offender", err)
	}
}
