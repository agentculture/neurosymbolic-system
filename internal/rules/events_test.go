package rules_test

import (
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/rules"
)

func TestEventLoadsAlongsideTickRules(t *testing.T) {
	path := writeTOML(t, "with_events.toml", `schema_version = 1
[[react]]
id = "pat-acknowledge"
when = { field = "pat", op = "is_true" }
run = "nod"

[event_default]
priority = "NORMAL"
urgency = "DEFERRABLE"

[[event]]
source = "tracking"
type = "snap_detected"
priority = "HIGH"
urgency = "IMMEDIATE"
llm_evaluate = false
inject_template = "You heard a sharp snap nearby."

[[event]]
source = "rule"
type = "fire"
priority = "NORMAL"
urgency = "NOW"
voice = "silent"
sense = "body"
dedupe = "reflex"
`)
	cfg, err := rules.Load([][]string{{path}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.React) != 1 {
		t.Fatalf("react = %v, want 1 rule (events must not disturb tick rules)", cfg.React)
	}
	if cfg.EventDefault == nil || cfg.EventDefault.Priority != "NORMAL" || cfg.EventDefault.Urgency != "DEFERRABLE" {
		t.Fatalf("EventDefault = %+v", cfg.EventDefault)
	}
	if !cfg.EventDefault.LLMEvaluate {
		t.Errorf("EventDefault.LLMEvaluate = false, want true (the declared default)")
	}
	if cfg.EventDefault.Voice != "free" {
		t.Errorf("EventDefault.Voice = %q, want %q (the declared default)", cfg.EventDefault.Voice, "free")
	}
	if len(cfg.Events) != 2 {
		t.Fatalf("Events = %v, want 2", cfg.Events)
	}
	snap := cfg.Events[0]
	if snap.Source != "tracking" || snap.Type != "snap_detected" {
		t.Errorf("snap event = %+v", snap)
	}
	if snap.Priority != "HIGH" || snap.Urgency != "IMMEDIATE" || snap.LLMEvaluate {
		t.Errorf("snap event fields = %+v", snap)
	}
	if snap.Voice != "free" {
		t.Errorf("snap.Voice = %q, want default %q", snap.Voice, "free")
	}
	fire := cfg.Events[1]
	if fire.Voice != "silent" || fire.Sense != "body" || fire.Dedupe != "reflex" {
		t.Errorf("fire event = %+v", fire)
	}
}

func TestEventBothSchemaVersionsAccepted(t *testing.T) {
	for _, version := range []int{1, 2} {
		body := strings.ReplaceAll(`schema_version = V
[[event]]
source = "a"
type = "b"
priority = "LOW"
urgency = "BACKGROUND"
`, "V", itoa(version))
		path := writeTOML(t, "v.toml", body)
		cfg, err := rules.Load([][]string{{path}}, nil)
		if err != nil {
			t.Fatalf("schema_version %d: load: %v", version, err)
		}
		if len(cfg.Events) != 1 {
			t.Fatalf("schema_version %d: Events = %v, want 1", version, cfg.Events)
		}
	}
}

func TestEventTombstoneOverlaySemantics(t *testing.T) {
	base := writeTOML(t, "base.toml", `schema_version = 1
[[event]]
source = "a"
type = "b"
priority = "LOW"
urgency = "BACKGROUND"

[[event]]
source = "c"
type = "d"
priority = "LOW"
urgency = "BACKGROUND"
`)
	overlay := writeTOML(t, "overlay.toml", `schema_version = 1
[[event]]
source = "a"
type = "b"
enabled = false
`)
	cfg, err := rules.Load([][]string{{base}, {overlay}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Events) != 1 || cfg.Events[0].Source != "c" || cfg.Events[0].Type != "d" {
		t.Fatalf("Events = %v, want only c/d (a/b tombstoned)", cfg.Events)
	}
}

func TestEventLaterLayerOverridesWholesale(t *testing.T) {
	base := writeTOML(t, "base.toml", `schema_version = 1
[[event]]
source = "a"
type = "b"
priority = "LOW"
urgency = "BACKGROUND"
`)
	overlay := writeTOML(t, "overlay.toml", `schema_version = 1
[[event]]
source = "a"
type = "b"
priority = "CRITICAL"
urgency = "IMMEDIATE"
`)
	cfg, err := rules.Load([][]string{{base}, {overlay}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Events) != 1 || cfg.Events[0].Priority != "CRITICAL" || cfg.Events[0].Urgency != "IMMEDIATE" {
		t.Fatalf("Events = %v, want the overlay's CRITICAL/IMMEDIATE to win wholesale", cfg.Events)
	}
}

func TestEventSameLayerDuplicateRefused(t *testing.T) {
	a := writeTOML(t, "a.toml", `schema_version = 1
[[event]]
source = "x"
type = "y"
priority = "LOW"
urgency = "BACKGROUND"
`)
	b := writeTOML(t, "b.toml", `schema_version = 1
[[event]]
source = "x"
type = "y"
priority = "HIGH"
urgency = "NOW"
`)
	_, err := rules.Load([][]string{{a, b}}, nil)
	if err == nil {
		t.Fatal("expected a same-layer duplicate event to be refused")
	}
	msg := err.Error()
	for _, want := range []string{"x/y", a, b} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

func TestEventRefusals(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "missing required fields",
			body: `schema_version = 1
[[event]]
source = "a"
type = "b"
`,
			want: []string{"a/b", "priority", "urgency"},
		},
		{
			name: "unknown priority",
			body: `schema_version = 1
[[event]]
source = "a"
type = "b"
priority = "URGENT"
urgency = "NOW"
`,
			want: []string{"a/b", "priority", "URGENT"},
		},
		{
			name: "unknown urgency",
			body: `schema_version = 1
[[event]]
source = "a"
type = "b"
priority = "LOW"
urgency = "SOON"
`,
			want: []string{"a/b", "urgency", "SOON"},
		},
		{
			name: "unknown voice",
			body: `schema_version = 1
[[event]]
source = "a"
type = "b"
priority = "LOW"
urgency = "BACKGROUND"
voice = "loud"
`,
			want: []string{"a/b", "voice", "loud"},
		},
		{
			name: "unknown field",
			body: `schema_version = 1
[[event]]
source = "a"
type = "b"
priority = "LOW"
urgency = "BACKGROUND"
exec = "rm -rf /"
`,
			want: []string{"a/b", "exec"},
		},
		{
			name: "event_default missing priority",
			body: `schema_version = 1
[event_default]
urgency = "NOW"
`,
			want: []string{"event_default", "priority"},
		},
		{
			name: "event_default missing urgency",
			body: `schema_version = 1
[event_default]
priority = "NORMAL"
`,
			want: []string{"event_default", "urgency"},
		},
		{
			name: "event_default unknown field",
			body: `schema_version = 1
[event_default]
priority = "NORMAL"
urgency = "NOW"
exec = "rm -rf /"
`,
			want: []string{"event_default", "exec"},
		},
		{
			name: "event missing source",
			body: `schema_version = 1
[[event]]
type = "b"
priority = "LOW"
urgency = "BACKGROUND"
`,
			want: []string{"source"},
		},
		{
			name: "event missing type",
			body: `schema_version = 1
[[event]]
source = "a"
priority = "LOW"
urgency = "BACKGROUND"
`,
			want: []string{"type"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTOML(t, "f.toml", tc.body)
			_, err := rules.Load([][]string{{path}}, nil)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, "rules: ") {
				t.Fatalf("error %q does not start with the rules: prefix", msg)
			}
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q does not mention %q", msg, want)
				}
			}
		})
	}
}

func itoa(n int) string {
	if n == 1 {
		return "1"
	}
	return "2"
}
