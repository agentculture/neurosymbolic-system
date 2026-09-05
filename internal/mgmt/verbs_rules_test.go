package mgmt_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
	"github.com/agentculture/neurosymbolic-system/internal/mgmt"
)

const (
	reachyRulesV1     = "../rules/testdata/reachy/default_rules.v1.toml"
	reachyRulesBroken = "../rules/testdata/reachy/default_rules.toml" // missing schema_version
	reachyVocabJSON   = "../adaptor/testdata/reachy_vocabulary.json"
	reachyVocabTOML   = "../adaptor/testdata/reachy_vocabulary.toml"
	rulesWithEvents   = "testdata/with_events.toml"
)

func TestVerbRulesCheckSuccessSummary(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.check", Args: []string{reachyRulesV1}})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	want := "rules: 3 react, 0 inhibit, 0 event entries, schema_version 1\n"
	if resp.Stdout != want {
		t.Fatalf("Stdout = %q, want %q", resp.Stdout, want)
	}
}

func TestVerbRulesCheckCountsEventEntries(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.check", Args: []string{rulesWithEvents}})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	want := "rules: 1 react, 0 inhibit, 2 event entries, schema_version 1\n"
	if resp.Stdout != want {
		t.Fatalf("Stdout = %q, want %q", resp.Stdout, want)
	}
}

func TestVerbRulesListShowsEventEntries(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.list", Args: []string{rulesWithEvents}})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	for _, want := range []string{
		"sample-react",
		"event tracking/face_seen priority=NORMAL urgency=DEFERRABLE",
		"event rule/fire priority=HIGH urgency=NOW",
	} {
		if !strings.Contains(resp.Stdout, want) {
			t.Errorf("Stdout = %q, missing %q", resp.Stdout, want)
		}
	}
}

func TestVerbRulesListJSONIncludesEventEntries(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.list", Args: []string{rulesWithEvents}, JSON: true})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	var decoded []map[string]string
	if err := json.Unmarshal([]byte(resp.Stdout), &decoded); err != nil {
		t.Fatalf("invalid JSON %q: %v", resp.Stdout, err)
	}
	if len(decoded) != 3 {
		t.Fatalf("decoded = %+v, want 3 entries (1 rule + 2 events)", decoded)
	}
	event := decoded[1]
	if event["id"] != "tracking/face_seen" || event["kind"] != "event" ||
		event["priority"] != "NORMAL" || event["urgency"] != "DEFERRABLE" {
		t.Errorf("decoded[1] = %+v", event)
	}
}

func TestVerbRulesCheckWithAdaptorJSON(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{
		Verb: "rules.check",
		Args: []string{reachyRulesV1, "--adaptor", reachyVocabJSON},
		JSON: true,
	})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(resp.Stdout), &decoded); err != nil {
		t.Fatalf("invalid JSON %q: %v", resp.Stdout, err)
	}
	if decoded["react"] != float64(3) {
		t.Errorf("decoded = %+v, want react = 3", decoded)
	}
}

func TestVerbRulesCheckWithAdaptorTOML(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{
		Verb: "rules.check",
		Args: []string{reachyRulesV1, "--adaptor", reachyVocabTOML},
	})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
}

func TestVerbRulesCheckNoFilesIsUserError(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.check"})
	if resp.Code != clifmt.ExitUser {
		t.Fatalf("Code = %d, want %d", resp.Code, clifmt.ExitUser)
	}
}

func TestVerbRulesCheckFailureIsTwoLineTextError(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.check", Args: []string{reachyRulesBroken}})
	if resp.Code != clifmt.ExitUser {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitUser, resp.Stderr)
	}
	lines := strings.Split(strings.TrimSuffix(resp.Stderr, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stderr lines = %v, want exactly 2", lines)
	}
	if !strings.HasPrefix(lines[0], "error:") || !strings.Contains(lines[0], "schema_version") {
		t.Errorf("error line = %q, want it to mention schema_version", lines[0])
	}
	if !strings.HasPrefix(lines[1], "hint:") {
		t.Errorf("hint line = %q", lines[1])
	}
}

func TestVerbRulesCheckWithUnknownAdaptorExtension(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.check", Args: []string{reachyRulesV1, "--adaptor", "vocab.yaml"}})
	if resp.Code != clifmt.ExitUser {
		t.Fatalf("Code = %d, want %d", resp.Code, clifmt.ExitUser)
	}
}

func TestVerbRulesListPrintsEachRule(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.list", Args: []string{reachyRulesV1}})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	for _, want := range []string{"pat-acknowledge", "look-toward-sound", "greet-when-addressed", "react"} {
		if !strings.Contains(resp.Stdout, want) {
			t.Errorf("Stdout = %q, missing %q", resp.Stdout, want)
		}
	}
}

func TestVerbRulesListJSONShape(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.list", Args: []string{reachyRulesV1}, JSON: true})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	var decoded []map[string]string
	if err := json.Unmarshal([]byte(resp.Stdout), &decoded); err != nil {
		t.Fatalf("invalid JSON %q: %v", resp.Stdout, err)
	}
	if len(decoded) != 3 {
		t.Fatalf("decoded = %+v, want 3 entries", decoded)
	}
	if decoded[0]["id"] != "pat-acknowledge" || decoded[0]["kind"] != "react" {
		t.Errorf("decoded[0] = %+v", decoded[0])
	}
}

func TestVerbRulesReloadWithNoReloaderIsEnvError(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.reload", Args: []string{reachyRulesV1}})
	if resp.Code != clifmt.ExitEnv {
		t.Fatalf("Code = %d, want %d", resp.Code, clifmt.ExitEnv)
	}
	if !strings.Contains(resp.Stderr, "no live engine") {
		t.Errorf("Stderr = %q, want it to mention 'no live engine'", resp.Stderr)
	}
	if !strings.Contains(resp.Stderr, "restart") || !strings.Contains(resp.Stderr, "stream endpoint") {
		t.Errorf("Stderr = %q, want the documented remediation", resp.Stderr)
	}
}

type fakeReloader struct {
	calledWith []string
	err        error
}

func (f *fakeReloader) Reload(paths []string) error {
	f.calledWith = paths
	return f.err
}

func TestVerbRulesReloadWithInstalledReloader(t *testing.T) {
	h := testHandler()
	reloader := &fakeReloader{}
	h.Reloader = reloader
	resp := h.Handle(mgmt.Request{Verb: "rules.reload", Args: []string{reachyRulesV1}})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	if len(reloader.calledWith) != 1 || reloader.calledWith[0] != reachyRulesV1 {
		t.Errorf("Reload called with %v, want [%s]", reloader.calledWith, reachyRulesV1)
	}
}

func TestVerbRulesReloadRefusalKeepsCodeUser(t *testing.T) {
	h := testHandler()
	h.Reloader = &fakeReloader{err: os.ErrInvalid}
	resp := h.Handle(mgmt.Request{Verb: "rules.reload", Args: []string{reachyRulesV1}})
	if resp.Code != clifmt.ExitUser {
		t.Fatalf("Code = %d, want %d (a bad new rule set is a user error, not the engine's fault)", resp.Code, clifmt.ExitUser)
	}
}

// --- rules migrate ----------------------------------------------------------

func TestVerbRulesMigrateWritesV2AndLeavesInputUntouched(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "rules.toml")
	original := readFixture(t, reachyRulesV1)
	if err := os.WriteFile(inPath, original, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.migrate", Args: []string{inPath}})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}

	afterInput, err := os.ReadFile(inPath)
	if err != nil {
		t.Fatalf("ReadFile input: %v", err)
	}
	if string(afterInput) != string(original) {
		t.Fatal("migrate modified its input file; it must leave it byte-identical")
	}

	wantOut := filepath.Join(dir, "rules.v2.toml")
	migrated, err := os.ReadFile(wantOut)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", wantOut, err)
	}
	if !strings.Contains(string(migrated), "schema_version = 2") {
		t.Errorf("migrated file does not declare schema_version = 2:\n%s", migrated)
	}

	// Rule content must be identical apart from the schema_version bump.
	for _, want := range []string{"pat-acknowledge", "look-toward-sound", "greet-when-addressed"} {
		if !strings.Contains(string(migrated), want) {
			t.Errorf("migrated file is missing rule id %q", want)
		}
	}
}

func TestVerbRulesMigrateRefusesExistingOutputWithoutForce(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "rules.toml")
	writeCopy(t, reachyRulesV1, inPath)
	outPath := filepath.Join(dir, "rules.v2.toml")
	if err := os.WriteFile(outPath, []byte("placeholder"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.migrate", Args: []string{inPath}})
	if resp.Code != clifmt.ExitUser {
		t.Fatalf("Code = %d, want %d", resp.Code, clifmt.ExitUser)
	}

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(content) != "placeholder" {
		t.Error("migrate overwrote the existing output without --force")
	}
}

func TestVerbRulesMigrateForceOverwritesExistingOutput(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "rules.toml")
	writeCopy(t, reachyRulesV1, inPath)
	outPath := filepath.Join(dir, "rules.v2.toml")
	if err := os.WriteFile(outPath, []byte("placeholder"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.migrate", Args: []string{inPath, "--force"}})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(content), "placeholder") {
		t.Error("--force did not overwrite the existing output")
	}
}

func TestVerbRulesMigrateRefusesWritingToItsInput(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "rules.toml")
	writeCopy(t, reachyRulesV1, inPath)

	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.migrate", Args: []string{inPath, "--out", inPath}})
	if resp.Code != clifmt.ExitUser {
		t.Fatalf("Code = %d, want %d", resp.Code, clifmt.ExitUser)
	}
	content, err := os.ReadFile(inPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(content), "schema_version = 1") {
		t.Error("migrate must not have touched its input file")
	}
}

func TestVerbRulesMigrateExplicitOutPath(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "rules.toml")
	writeCopy(t, reachyRulesV1, inPath)
	outPath := filepath.Join(dir, "custom-name.toml")

	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.migrate", Args: []string{inPath, "--out", outPath}})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("expected %s to exist: %v", outPath, err)
	}
}

func TestVerbRulesMigrateOfBrokenFileFails(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "rules.toml")
	writeCopy(t, reachyRulesBroken, inPath) // no schema_version at all

	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "rules.migrate", Args: []string{inPath}})
	if resp.Code != clifmt.ExitUser {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitUser, resp.Stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "rules.v2.toml")); err == nil {
		t.Error("migrate must not write an output file when the input fails validation")
	}
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return data
}

func writeCopy(t *testing.T, src, dst string) {
	t.Helper()
	data := readFixture(t, src)
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", dst, err)
	}
}
