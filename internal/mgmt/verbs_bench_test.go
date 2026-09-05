package mgmt_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
	"github.com/agentculture/neurosymbolic-system/internal/mgmt"
)

// smallBenchArgs mirrors t15's acceptance test config: small enough to run
// in well under a second, exercising the same synthetic-load path a full
// 200-rule/20-field/10,000-tick run does.
var smallBenchArgs = []string{
	"--rules", "20", "--fields", "5", "--ticks", "200", "--period", "5ms",
}

func TestVerbBenchSuccessText(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "bench", Args: smallBenchArgs})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	if resp.Stderr != "" {
		t.Fatalf("Stderr = %q, want empty on success", resp.Stderr)
	}
	for _, want := range []string{"ticks=200", "period=5ms", "overruns=0", "ok=true"} {
		if !strings.Contains(resp.Stdout, want) {
			t.Errorf("Stdout %q does not contain %q", resp.Stdout, want)
		}
	}
}

func TestVerbBenchSuccessJSON(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "bench", Args: smallBenchArgs, JSON: true})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(resp.Stdout), &decoded); err != nil {
		t.Fatalf("Stdout is not valid JSON: %v (%q)", err, resp.Stdout)
	}
	for _, key := range []string{
		"ticks", "period", "p50_us", "p99_us", "max_us", "overruns", "rss_mb", "rss_ceiling_mb", "ok",
	} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("decoded result is missing key %q: %v", key, decoded)
		}
	}
	if ok, _ := decoded["ok"].(bool); !ok {
		t.Errorf("decoded ok = %v, want true", decoded["ok"])
	}
}

func TestVerbBenchTinyBudgetFails(t *testing.T) {
	h := testHandler()
	args := []string{"--rules", "20", "--fields", "5", "--ticks", "50", "--period", "1ns"}
	resp := h.Handle(mgmt.Request{Verb: "bench", Args: args})
	if resp.Code != clifmt.ExitUser {
		t.Fatalf("Code = %d, want %d (a tiny budget must overrun and exit non-zero); stdout=%q stderr=%q",
			resp.Code, clifmt.ExitUser, resp.Stdout, resp.Stderr)
	}
	if resp.Stdout != "" {
		t.Fatalf("Stdout = %q, want empty on failure", resp.Stdout)
	}
	if !strings.Contains(resp.Stderr, "overruns=") {
		t.Errorf("Stderr = %q, want it to name the overrun count", resp.Stderr)
	}
}

func TestVerbBenchUnknownFlagRefused(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "bench", Args: []string{"--bogus", "1"}})
	if resp.Code != clifmt.ExitUser {
		t.Fatalf("Code = %d, want %d", resp.Code, clifmt.ExitUser)
	}
}

func TestVerbBenchBadIntRefused(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "bench", Args: []string{"--rules", "not-a-number"}})
	if resp.Code != clifmt.ExitUser {
		t.Fatalf("Code = %d, want %d", resp.Code, clifmt.ExitUser)
	}
}

func TestVerbBenchDefaultRSSCeilingParses(t *testing.T) {
	h := testHandler()
	args := append(append([]string{}, smallBenchArgs...), "--rss-ceiling", "32MB")
	resp := h.Handle(mgmt.Request{Verb: "bench", Args: args})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	if !strings.Contains(resp.Stdout, "rss_ceiling_mb=32.00") {
		t.Errorf("Stdout = %q, want rss_ceiling_mb=32.00", resp.Stdout)
	}
}
