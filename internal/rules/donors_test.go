package rules_test

import (
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/rules"
)

// The two donors' shipped rule files are copied VERBATIM into testdata (see
// testdata/README.md). They are the contract: whatever this loader does, it
// must keep loading the rules two real robots ship today.

func TestReachyVerbatimIsRefusedForMissingSchemaVersion(t *testing.T) {
	// The reachy donor predates the schema_version field entirely. Missing is
	// REFUSED fail-closed, and the message names the versions we accept.
	_, err := rules.Load([][]string{{"testdata/reachy/default_rules.toml"}}, nil)
	if err == nil {
		t.Fatal("expected the verbatim reachy file to be refused, got nil error")
	}
	msg := err.Error()
	for _, want := range []string{
		"testdata/reachy/default_rules.toml",
		"schema_version",
		"1",
		"2",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

func TestReachyV1Loads(t *testing.T) {
	cfg, err := rules.Load([][]string{{"testdata/reachy/default_rules.v1.toml"}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", cfg.SchemaVersion)
	}
	if len(cfg.Inhibit) != 0 {
		t.Errorf("Inhibit = %v, want none", cfg.Inhibit)
	}
	gotIDs := ids(cfg.React)
	wantIDs := []string{"pat-acknowledge", "look-toward-sound", "greet-when-addressed"}
	if !equalStrings(gotIDs, wantIDs) {
		t.Fatalf("react ids = %v, want %v", gotIDs, wantIDs)
	}

	pat := cfg.React[0]
	if pat.Kind != rules.KindReact {
		t.Errorf("pat kind = %q, want %q", pat.Kind, rules.KindReact)
	}
	if pat.Run != "pet-reaction" {
		t.Errorf("pat run = %q", pat.Run)
	}
	if pat.When.Field != "pat" || pat.When.Op != "is_true" || pat.When.Value != nil {
		t.Errorf("pat when = %+v", pat.When)
	}
	if pat.CooldownS != 5.0 {
		t.Errorf("pat cooldown_s = %v, want 5", pat.CooldownS)
	}
	if pat.Hysteresis != rules.DefaultHysteresis {
		t.Errorf("pat hysteresis = %v, want default", pat.Hysteresis)
	}
	if pat.DurationS != nil {
		t.Errorf("pat duration_s = %v, want nil", *pat.DurationS)
	}
	if pat.Say != "" {
		t.Errorf("pat say = %q, want empty", pat.Say)
	}
	if pat.Source != "testdata/reachy/default_rules.v1.toml" {
		t.Errorf("pat source = %q", pat.Source)
	}

	sound := cfg.React[1]
	if sound.When.Field != "rms_ratio" || sound.When.Op != "ge" {
		t.Errorf("sound when = %+v", sound.When)
	}
	if v, ok := sound.When.Value.(float64); !ok || v != 5.0 {
		t.Errorf("sound when value = %#v, want 5.0", sound.When.Value)
	}
	if sound.Run != "orient-to-sound" {
		t.Errorf("sound run = %q", sound.Run)
	}
	if sound.DurationS == nil || *sound.DurationS != 12.0 {
		t.Errorf("sound duration_s = %v, want 12", sound.DurationS)
	}
	if sound.CooldownS != 2.0 {
		t.Errorf("sound cooldown_s = %v, want 2", sound.CooldownS)
	}

	greet := cfg.React[2]
	if greet.When.Field != "transcript" || greet.When.Op != "is_true" {
		t.Errorf("greet when = %+v", greet.When)
	}
	if greet.Run != "speak" {
		t.Errorf("greet run = %q", greet.Run)
	}
	if greet.DurationS == nil || *greet.DurationS != 1.6 {
		t.Errorf("greet duration_s = %v, want 1.6", greet.DurationS)
	}
	if greet.CooldownS != 12.0 {
		t.Errorf("greet cooldown_s = %v, want 12", greet.CooldownS)
	}
	if greet.Say != "I'm here." {
		t.Errorf("greet say = %q", greet.Say)
	}
}

func TestMicroduckLoads(t *testing.T) {
	cfg, err := rules.Load([][]string{{"testdata/microduck/default_rules.toml"}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", cfg.SchemaVersion)
	}
	if got, want := ids(cfg.Inhibit), []string{"fallen-inhibit", "low-battery-inhibit"}; !equalStrings(got, want) {
		t.Fatalf("inhibit ids = %v, want %v", got, want)
	}
	if got, want := ids(cfg.React), []string{"stop-when-limp"}; !equalStrings(got, want) {
		t.Fatalf("react ids = %v, want %v", got, want)
	}

	fallen := cfg.Inhibit[0]
	if fallen.Kind != rules.KindInhibit {
		t.Errorf("fallen kind = %q", fallen.Kind)
	}
	if fallen.When.Field != "fallen" || fallen.When.Op != "is_true" {
		t.Errorf("fallen when = %+v", fallen.When)
	}
	wantDisable := []string{"do", "idle", "look", "mode", "move", "sound"}
	if !equalStrings(fallen.Disable, wantDisable) {
		t.Errorf("fallen disable = %v, want %v", fallen.Disable, wantDisable)
	}
	if fallen.CooldownS != rules.DefaultCooldownS {
		t.Errorf("fallen cooldown_s = %v, want default %v", fallen.CooldownS, rules.DefaultCooldownS)
	}

	battery := cfg.Inhibit[1]
	if battery.When.Field != "battery_frac" || battery.When.Op != "lt" {
		t.Errorf("battery when = %+v", battery.When)
	}
	if v, ok := battery.When.Value.(float64); !ok || v != 0.15 {
		t.Errorf("battery when value = %#v, want 0.15", battery.When.Value)
	}
	if !equalStrings(battery.Disable, []string{"do", "idle", "mode", "move"}) {
		t.Errorf("battery disable = %v", battery.Disable)
	}

	limp := cfg.React[0]
	if limp.Run != "stop" || limp.When.Field != "limp" || limp.When.Op != "is_true" {
		t.Errorf("limp = %+v", limp)
	}
	if limp.CooldownS != 5.0 {
		t.Errorf("limp cooldown_s = %v", limp.CooldownS)
	}
	if limp.DurationS != nil {
		t.Errorf("limp duration_s = %v, want nil", *limp.DurationS)
	}
}

// The donors' vocabularies, so the fixtures also load with names CHECKED.

type donorVocab struct {
	fields  map[string]bool
	actions map[string]bool
	loops   map[string]bool
	params  map[string]map[string][2]float64
}

func (v donorVocab) HasField(name string) bool  { return v.fields[name] }
func (v donorVocab) HasAction(name string) bool { return v.actions[name] }
func (v donorVocab) ActionLoops(name string) bool {
	return v.loops[name]
}
func (v donorVocab) ActionParam(action, param string) (float64, float64, bool) {
	p, ok := v.params[action][param]
	if !ok {
		return 0, 0, false
	}
	return p[0], p[1], true
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func TestDonorFixturesLoadWithVocabulary(t *testing.T) {
	reachy := donorVocab{
		fields: set("doa", "speech", "rms", "rms_ratio", "pat", "face",
			"frame_available", "transcript", "self_moving"),
		actions: set("pet-reaction", "orient-to-sound", "speak", "feel-alive"),
		params: map[string]map[string][2]float64{
			"orient-to-sound": {"rms_ratio": {0, 100}},
		},
	}
	if _, err := rules.Load([][]string{{"testdata/reachy/default_rules.v1.toml"}}, reachy); err != nil {
		t.Errorf("reachy with vocabulary: %v", err)
	}

	microduck := donorVocab{
		fields:  set("fallen", "limp", "battery_frac"),
		actions: set("do", "look", "move", "sound", "stop", "mode", "idle"),
		loops:   set("idle"),
	}
	if _, err := rules.Load([][]string{{"testdata/microduck/default_rules.toml"}}, microduck); err != nil {
		t.Errorf("microduck with vocabulary: %v", err)
	}
}

func ids(rs []rules.Rule) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
