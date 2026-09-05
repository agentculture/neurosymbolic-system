package mgmt_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
	"github.com/agentculture/neurosymbolic-system/internal/mgmt"
	"github.com/agentculture/neurosymbolic-system/internal/stream"
)

func TestVerbVersionText(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "version"})
	want := "neurosymbolic-engine 1.2.3 (abc1234)\n"
	if resp.Stdout != want {
		t.Fatalf("Stdout = %q, want %q", resp.Stdout, want)
	}
}

func TestVerbVersionJSON(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "version", JSON: true})
	var decoded map[string]any
	if err := json.Unmarshal([]byte(resp.Stdout), &decoded); err != nil {
		t.Fatalf("invalid JSON %q: %v", resp.Stdout, err)
	}
	if decoded["version"] != "1.2.3" || decoded["revision"] != "abc1234" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

// TestVerbVersionJSONCarriesTheProtocolVersion pins the field
// neurosymbolic_system/engine_client.py's check_protocol reads (spec h35): a
// client speaking a different wire version must be able to learn that BY NAME
// from a one-shot exec, rather than by connecting and discovering that every
// frame it sends comes back refused.
func TestVerbVersionJSONCarriesTheProtocolVersion(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "version", JSON: true})
	var decoded map[string]any
	if err := json.Unmarshal([]byte(resp.Stdout), &decoded); err != nil {
		t.Fatalf("invalid JSON %q: %v", resp.Stdout, err)
	}
	protocol, ok := decoded["protocol"].(float64)
	if !ok {
		t.Fatalf("decoded = %+v, want a numeric \"protocol\" field", decoded)
	}
	if int(protocol) != stream.Version {
		t.Errorf("protocol = %v, want stream.Version (%d)", protocol, stream.Version)
	}
}

func TestVerbWhoamiTextIncludesModule(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "whoami"})
	if !strings.Contains(resp.Stdout, "neurosymbolic-engine 1.2.3 (abc1234)") {
		t.Errorf("Stdout = %q, missing version/revision line", resp.Stdout)
	}
	if !strings.Contains(resp.Stdout, "example.test/module") {
		t.Errorf("Stdout = %q, missing module path", resp.Stdout)
	}
}

func TestVerbWhoamiJSON(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "whoami", JSON: true})
	var decoded map[string]string
	if err := json.Unmarshal([]byte(resp.Stdout), &decoded); err != nil {
		t.Fatalf("invalid JSON %q: %v", resp.Stdout, err)
	}
	if decoded["module"] != "example.test/module" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestVerbStatusWithNoStatusSourceIsEnvError(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "status"})
	if resp.Code != clifmt.ExitEnv {
		t.Fatalf("Code = %d, want %d", resp.Code, clifmt.ExitEnv)
	}
	if !strings.Contains(resp.Stderr, "no live engine") {
		t.Errorf("Stderr = %q, want it to mention 'no live engine'", resp.Stderr)
	}
}

type fakeStatusSource struct {
	value any
	err   error
}

func (f *fakeStatusSource) Status() (any, error) { return f.value, f.err }

func TestVerbStatusWithInstalledSource(t *testing.T) {
	h := testHandler()
	h.Status = &fakeStatusSource{value: map[string]any{"active": []string{"feel-alive"}}}
	resp := h.Handle(mgmt.Request{Verb: "status", JSON: true})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	if !strings.Contains(resp.Stdout, "feel-alive") {
		t.Errorf("Stdout = %q, want it to carry the installed status", resp.Stdout)
	}
}

func TestVerbDoctorHealthy(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "doctor"})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	if !strings.Contains(resp.Stdout, "healthy") {
		t.Errorf("Stdout = %q, want it to report healthy", resp.Stdout)
	}
	if !strings.Contains(resp.Stdout, "toolchain:") {
		t.Errorf("Stdout = %q, want a toolchain line", resp.Stdout)
	}
}

func TestVerbDoctorWithRulesFlag(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{
		Verb: "doctor",
		Args: []string{"--rules", "../rules/testdata/reachy/default_rules.v1.toml"},
		JSON: true,
	})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(resp.Stdout), &decoded); err != nil {
		t.Fatalf("invalid JSON %q: %v", resp.Stdout, err)
	}
	if decoded["rules_default_loads"] != true {
		t.Errorf("decoded = %+v, want rules_default_loads = true", decoded)
	}
}

func TestVerbDoctorWithBrokenRulesFlagIsEnvError(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{
		Verb: "doctor",
		Args: []string{"--rules", "../rules/testdata/reachy/default_rules.toml"}, // missing schema_version
	})
	if resp.Code != clifmt.ExitEnv {
		t.Fatalf("Code = %d, want %d", resp.Code, clifmt.ExitEnv)
	}
}
