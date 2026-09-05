package compose

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
	"github.com/agentculture/neurosymbolic-system/internal/provider"
)

// unreachableBaseURL is a port nothing listens on, so a warm-up fails
// IMMEDIATELY with a connection refusal rather than waiting out a timeout.
// Port 1 is privileged and unused; a test that had to wait for a real timeout
// would be a test nobody runs.
const unreachableBaseURL = "http://127.0.0.1:1"

// writeProviderConfig writes one provider config and returns its path.
func writeProviderConfig(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// toyProviderTOML is a config against the toy robot's own senses: it reads the
// tag it sees and writes a decision back as `mood`, which adaptor.toml declares
// as a string sense precisely so a rule could key on it.
const toyProviderTOML = `
name = "mood-reader"
kind = "completion"
base_url = "%s"
model = "toy-model"
inputs = ["tag", "light_level"]
output = "mood"
timeout_s = 0.25
queue_depth = 2
cadence = 5
system_prompt = "Answer in one short word."
max_tokens = 4
`

func toyProviderPath(t *testing.T) string {
	t.Helper()
	return writeProviderConfig(t, "mood.toml",
		strings.Replace(toyProviderTOML, "%s", unreachableBaseURL, 1))
}

// --- loading ----------------------------------------------------------------

func TestLoadProviderConfigTOML(t *testing.T) {
	cfg, err := loadProviderConfig(toyProviderPath(t))
	if err != nil {
		t.Fatalf("loadProviderConfig: %v", err)
	}
	if cfg.Name != "mood-reader" || cfg.Kind != provider.KindCompletion {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.Output != "mood" || len(cfg.Inputs) != 2 {
		t.Errorf("cfg = %+v", cfg)
	}
	// timeout_s is SECONDS on disk and a Duration in the struct — the unit
	// lives in the name, so a number on disk is never ambiguous.
	if cfg.Timeout != 250*time.Millisecond {
		t.Errorf("Timeout = %v, want 250ms", cfg.Timeout)
	}
	if cfg.Cadence != 5 || cfg.MaxTokens != 4 {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadProviderConfigJSON(t *testing.T) {
	path := writeProviderConfig(t, "mood.json", `{
  "name": "mood-reader",
  "kind": "completion",
  "base_url": "`+unreachableBaseURL+`",
  "model": "toy-model",
  "inputs": ["tag"],
  "output": "mood"
}`)
	cfg, err := loadProviderConfig(path)
	if err != nil {
		t.Fatalf("loadProviderConfig: %v", err)
	}
	if cfg.Name != "mood-reader" || cfg.Output != "mood" {
		t.Errorf("cfg = %+v", cfg)
	}
	// Left zero on disk, filled by provider.Config.validate — one place decides
	// a default, and it is not this package.
	if cfg.Timeout != 0 || cfg.QueueDepth != 0 {
		t.Errorf("cfg = %+v, want the unset knobs left zero", cfg)
	}
}

func TestProviderConfigRefusals(t *testing.T) {
	cases := []struct {
		name  string
		file  string
		body  string
		match string
	}{
		{
			"unknown key", "p.toml",
			"name = \"m\"\nkind = \"completion\"\nbase_url = \"http://x\"\n" +
				"inputs = [\"tag\"]\noutput = \"mood\"\nturbo = true\n",
			"unknown key",
		},
		{
			"malformed toml", "p.toml", "name = \n", "not valid TOML",
		},
		{
			"unknown json key", "p.json",
			`{"name":"m","kind":"completion","base_url":"http://x","inputs":["tag"],` +
				`"output":"mood","turbo":true}`,
			"turbo",
		},
		{
			"unrecognized extension", "p.yaml", "name: m\n", "unrecognized extension",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadProviderConfig(writeProviderConfig(t, tc.file, tc.body))
			if err == nil {
				t.Fatal("want a refusal, got none")
			}
			var cliErr *clifmt.CliError
			if !errors.As(err, &cliErr) {
				t.Fatalf("err = %T, want *clifmt.CliError", err)
			}
			if !strings.Contains(cliErr.Message, tc.match) {
				t.Errorf("Message = %q, want it to mention %q", cliErr.Message, tc.match)
			}
			if cliErr.Remediation == "" {
				t.Error("Remediation is empty")
			}
		})
	}
}

func TestAProviderReadingOrWritingAnUndeclaredFieldIsRefused(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		match string
	}{
		{
			"undeclared input",
			"name = \"m\"\nkind = \"completion\"\nbase_url = \"" + unreachableBaseURL +
				"\"\ninputs = [\"no_such_sense\"]\noutput = \"mood\"\n",
			"no_such_sense",
		},
		{
			"undeclared output",
			"name = \"m\"\nkind = \"completion\"\nbase_url = \"" + unreachableBaseURL +
				"\"\ninputs = [\"tag\"]\noutput = \"no_such_output\"\n",
			"no_such_output",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := toyOptions(t)
			opts.ProviderPaths = []string{writeProviderConfig(t, "p.toml", tc.body)}

			var stderr strings.Builder
			runtime, err := New(opts, Build{}, nil, nil, &stderr)
			if runtime != nil {
				runtime.Close()
			}
			if err == nil {
				t.Fatal("want a refusal for a field the robot does not declare")
			}
			if !strings.Contains(err.Error(), tc.match) {
				t.Errorf("err = %v, want it to name the offender", err)
			}
		})
	}
}

// --- wiring -----------------------------------------------------------------

// TestAProviderIsWiredOntoTheSeamAndIntoStatus is the composition-root half of
// the provider seam: the config becomes a live provider, riding the same bus as
// rules, with its counters answerable through `status`.
func TestAProviderIsWiredOntoTheSeamAndIntoStatus(t *testing.T) {
	opts := toyOptions(t)
	opts.ProviderPaths = []string{toyProviderPath(t)}

	var stderr strings.Builder
	runtime, err := New(opts, Build{Version: "0.0.0-test"}, nil, nil, &stderr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer runtime.Close()

	if len(runtime.Providers) != 1 {
		t.Fatalf("Providers = %d, want 1", len(runtime.Providers))
	}
	if runtime.Providers[0].Name != "mood-reader" {
		t.Errorf("Name = %q", runtime.Providers[0].Name)
	}

	status, err := runtime.Handler.Status.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	report, ok := status.(Status)
	if !ok {
		t.Fatalf("Status returned %T, want compose.Status", status)
	}
	// The counters are the ONLY way to tell "the model said no" from "the model
	// was never reached": every provider failure is an abstention, so an
	// unreachable gateway looks exactly like a predicate that never held.
	stats, present := report.Providers["mood-reader"]
	if !present {
		t.Fatalf("status carries no entry for the configured provider: %+v", report.Providers)
	}
	if stats.Requests != 0 || stats.Results != 0 {
		t.Errorf("a provider that has not ticked reports %+v", stats)
	}
}

// TestAnUnreachableProviderDoesNotStopTheEngineStarting is internal/provider's
// contract, verified from the composition root that has to honour it.
//
// A warm-up failure marks the provider unconfigured and logs one named drop;
// it does not fail New. A robot that refused to boot because a side-car model
// was down would be a robot taken out by its least important dependency — and
// the rule bound to that provider's output simply abstains forever, which is
// the same thing it does when the model has nothing to say.
func TestAnUnreachableProviderDoesNotStopTheEngineStarting(t *testing.T) {
	// An EMBEDDING provider, because that is the kind that actually performs
	// I/O at warm-up: it fetches every label's reference vector before the
	// first tick. A completion provider has nothing to warm.
	body := "name = \"tag-classifier\"\nkind = \"embedding\"\nbase_url = \"" +
		unreachableBaseURL + "\"\nmodel = \"toy-embed\"\n" +
		"inputs = [\"tag\"]\noutput = \"mood\"\nlabels = [\"calm\", \"busy\"]\n" +
		"timeout_s = 0.25\n"

	opts := toyOptions(t)
	opts.ProviderPaths = []string{writeProviderConfig(t, "classifier.toml", body)}

	var stderr strings.Builder
	started := time.Now()
	runtime, err := New(opts, Build{}, nil, nil, &stderr)
	if err != nil {
		t.Fatalf("New refused to build a runtime because a provider was unreachable: %v", err)
	}
	defer runtime.Close()

	// The refusal is a connection refusal, not a timeout: warming must not
	// spend the startup budget waiting on a gateway that is not there.
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("New took %v against an unreachable provider", elapsed)
	}
	if len(runtime.Providers) != 1 {
		t.Fatalf("Providers = %d, want the unconfigured provider still wired", len(runtime.Providers))
	}
	if runtime.Engine == nil || runtime.Server == nil {
		t.Fatal("the engine was not built")
	}

	// And the failure is NAMED. A layer whose drops are invisible is
	// indistinguishable from a layer that silently no-ops.
	log := stderr.String()
	if !strings.Contains(log, "[SENSE stage=provider") {
		t.Errorf("no provider SENSE line on stderr:\n%s", log)
	}
	if !strings.Contains(log, "unconfigured") {
		t.Errorf("the warm-up failure was not named 'unconfigured':\n%s", log)
	}
	if !strings.Contains(log, "tag-classifier") {
		t.Errorf("the drop does not name which provider failed:\n%s", log)
	}
}

func TestProviderFlagIsRepeatableAndOrdered(t *testing.T) {
	opts, err := ParseArgs([]string{
		"--adaptor", "robot.toml", "--stdio",
		"--provider", "first.toml",
		"--provider", "second.json",
	})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	want := []string{"first.toml", "second.json"}
	if len(opts.ProviderPaths) != len(want) {
		t.Fatalf("ProviderPaths = %v, want %v", opts.ProviderPaths, want)
	}
	for i, path := range want {
		if opts.ProviderPaths[i] != path {
			t.Errorf("ProviderPaths[%d] = %q, want %q", i, opts.ProviderPaths[i], path)
		}
	}
}

// TestAProviderRidesTheTickAndItsAnswerBecomesASense is the seam end to end,
// against a real (if tiny) OpenAI-compatible gateway.
//
// It is the claim the whole provider seam rests on: a model's answer arrives as
// an ORDINARY SENSE FIELD. Nothing in the rules layer, the engine, or the
// vocabulary is aware that a model produced it — the composition root wired a
// driver onto the same bus the rules ride, and the answer landed in the same
// snapshot a sensor reading would have.
func TestAProviderRidesTheTickAndItsAnswerBecomesASense(t *testing.T) {
	var calls atomic.Uint64
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w,
			`{"choices":[{"message":{"role":"assistant","content":"calm"}}]}`)
	}))
	defer gateway.Close()

	body := "name = \"mood-reader\"\nkind = \"completion\"\nbase_url = \"" + gateway.URL +
		"\"\nmodel = \"toy-model\"\ninputs = [\"tag\"]\noutput = \"mood\"\ntimeout_s = 2.0\n"

	opts := toyOptions(t)
	opts.ProviderPaths = []string{writeProviderConfig(t, "mood.toml", body)}

	var stderr strings.Builder
	runtime, err := New(opts, Build{}, nil, nil, &stderr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer runtime.Close()

	// Feed the input the provider reads, then run the loop briefly. The answer
	// necessarily lands on a LATER tick than the request: the worker writes it
	// back from its own goroutine, which is what keeps the HTTP call off the
	// tick thread entirely.
	runtime.Snapshot.Update(map[string]any{"tag": "quiet-room"}, time.Now())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	var landed bool
	for time.Now().Before(deadline) {
		if value, ok := runtime.Snapshot.Get("mood"); ok && value == "calm" {
			landed = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not return within 5s")
	}

	if !landed {
		t.Fatalf("the provider's answer never reached the snapshot (calls=%d)\n%s",
			calls.Load(), stderr.String())
	}
	// And it shows up in status as a request that produced a result, which is
	// how an operator tells a working provider from a silently abstaining one.
	status, statusErr := runtime.Handler.Status.Status()
	if statusErr != nil {
		t.Fatalf("Status: %v", statusErr)
	}
	stats := status.(Status).Providers["mood-reader"]
	if stats.Requests == 0 || stats.Results == 0 {
		t.Errorf("status reports %+v, want both a request and a result", stats)
	}
}
