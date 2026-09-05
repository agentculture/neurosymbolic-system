package adaptor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
)

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// reachy_vocabulary.toml is a TOML twin of reachy_vocabulary.json — same
// channels, senses and actions, generated once from the JSON fixture with
// BurntSushi/toml's own encoder so the two documents describe the identical
// vocabulary (see docs/skill-sources.md-style provenance: it is not
// hand-maintained prose, it is a mechanical transcription).
const reachyTOMLFixture = "reachy_vocabulary.toml"

func loadTOML(t *testing.T, fixture string) *adaptor.Vocabulary {
	t.Helper()
	path := filepath.Join("testdata", fixture)
	v, err := adaptor.LoadTOML(path)
	if err != nil {
		t.Fatalf("LoadTOML(%s): %v", path, err)
	}
	return v
}

func TestLoadTOMLAcceptsTheDonorShape(t *testing.T) {
	fromTOML := loadTOML(t, reachyTOMLFixture)
	fromJSON := load(t, reachyFixture)

	if len(fromTOML.Channels()) != len(fromJSON.Channels()) {
		t.Fatalf("channels: TOML has %d, JSON has %d", len(fromTOML.Channels()), len(fromJSON.Channels()))
	}
	if len(fromTOML.Senses()) != len(fromJSON.Senses()) {
		t.Fatalf("senses: TOML has %d, JSON has %d", len(fromTOML.Senses()), len(fromJSON.Senses()))
	}
	if len(fromTOML.Actions()) != len(fromJSON.Actions()) {
		t.Fatalf("actions: TOML has %d, JSON has %d", len(fromTOML.Actions()), len(fromJSON.Actions()))
	}

	if !fromTOML.HasField("rms_ratio") || !fromTOML.HasAction("orient-to-sound") {
		t.Error("TOML vocabulary is missing a name its own rules use")
	}

	// Freshness links decode correctly (age_field is exactly the field that
	// needed a toml struct tag distinct from its Go field name).
	for _, s := range fromTOML.Senses() {
		if s.Name == "doa" && s.AgeField != "doa_age_s" {
			t.Errorf("doa.AgeField = %q, want %q", s.AgeField, "doa_age_s")
		}
	}

	min, max, ok := fromTOML.ActionParam("orient-to-sound", "gain")
	if !ok || min != 0.0 || max != 2.0 {
		t.Errorf("ActionParam(orient-to-sound, gain) = (%v, %v, %v), want (0, 2, true)", min, max, ok)
	}
}

func TestLoadTOMLOriginIsTheConfigPath(t *testing.T) {
	v := loadTOML(t, reachyTOMLFixture)
	want := filepath.Join("testdata", reachyTOMLFixture)
	if v.Origin() != want {
		t.Errorf("Origin() = %q, want %q", v.Origin(), want)
	}
}

func TestLoadTOMLMissingFile(t *testing.T) {
	_, err := adaptor.LoadTOML(filepath.Join("testdata", "does-not-exist.toml"))
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestLoadTOMLRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	writeFile(t, path, `
bogus = true

[[channels]]
name = "head"
arity = 1
neutral = [0.0]

[[actions]]
name = "noop"
claims = ["head"]
loops = false

[actions.trajectories.head]
[actions.trajectories.head.easing]
kind = "linear"
from = [0.0]
to = [0.0]
duration_s = 1.0
`)
	_, err := adaptor.LoadTOML(path)
	if err == nil {
		t.Fatal("expected an error for an unknown top-level key")
	}
}

func TestLoadTOMLRejectsInvalidSyntax(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	writeFile(t, path, "this is not [ valid toml")
	_, err := adaptor.LoadTOML(path)
	if err == nil {
		t.Fatal("expected an error for invalid TOML syntax")
	}
}

func TestLoadTOMLAndLoadJSONRefuseTheSameBrokenReference(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	writeFile(t, path, `
[[channels]]
name = "head"
arity = 1
neutral = [0.0]

[[actions]]
name = "noop"
claims = ["missing-channel"]
loops = false
`)
	_, err := adaptor.LoadTOML(path)
	if err == nil {
		t.Fatal("expected an error for an action claiming an undeclared channel")
	}
}
