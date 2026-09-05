package mgmt_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
	"github.com/agentculture/neurosymbolic-system/internal/mgmt"
)

func testHandler() *mgmt.Handler {
	return &mgmt.Handler{Version: "1.2.3", Revision: "abc1234", Module: "example.test/module"}
}

func TestHandleUnknownVerbTextIsTwoLines(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "bogus"})

	if resp.Code != clifmt.ExitUser {
		t.Fatalf("Code = %d, want %d", resp.Code, clifmt.ExitUser)
	}
	if resp.Stdout != "" {
		t.Fatalf("Stdout = %q, want empty", resp.Stdout)
	}
	lines := strings.Split(strings.TrimSuffix(resp.Stderr, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stderr lines = %v, want exactly 2", lines)
	}
	if !strings.HasPrefix(lines[0], "error:") {
		t.Errorf("first line %q does not start with 'error:'", lines[0])
	}
	if !strings.HasPrefix(lines[1], "hint:") {
		t.Errorf("second line %q does not start with 'hint:'", lines[1])
	}
}

func TestHandleUnknownVerbJSON(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "bogus", JSON: true})

	if resp.Code != clifmt.ExitUser {
		t.Fatalf("Code = %d, want %d", resp.Code, clifmt.ExitUser)
	}
	if resp.Stdout != "" {
		t.Fatalf("Stdout = %q, want empty", resp.Stdout)
	}
	var decoded clifmt.CliError
	if err := json.Unmarshal([]byte(resp.Stderr), &decoded); err != nil {
		t.Fatalf("stderr is not valid JSON: %v (%q)", err, resp.Stderr)
	}
	if decoded.Code != clifmt.ExitUser {
		t.Errorf("decoded code = %d, want %d", decoded.Code, clifmt.ExitUser)
	}
}

func TestHandleNeverMixesStdoutAndStderr(t *testing.T) {
	h := testHandler()
	for _, req := range []mgmt.Request{
		{Verb: "version"},
		{Verb: "bogus"},
		{Verb: "status"},
		{Verb: "rules.check"}, // no files: a user error
	} {
		resp := h.Handle(req)
		if resp.Stdout != "" && resp.Stderr != "" {
			t.Errorf("Handle(%+v): both Stdout and Stderr are non-empty: %q / %q", req, resp.Stdout, resp.Stderr)
		}
	}
}

func TestHandleSuccessExitsZero(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "version"})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d", resp.Code, clifmt.ExitSuccess)
	}
	if resp.Stderr != "" {
		t.Fatalf("Stderr = %q, want empty on success", resp.Stderr)
	}
}
