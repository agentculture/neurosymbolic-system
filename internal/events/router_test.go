package events_test

import (
	"strings"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/events"
	"github.com/agentculture/neurosymbolic-system/internal/rules"
)

func loadConfig(t *testing.T, body string) *rules.Config {
	t.Helper()
	path := writeTOML(t, "rules.toml", body)
	cfg, err := rules.Load([][]string{{path}}, nil)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}
	return cfg
}

func TestNovaRulesLoadAndUnmatchedEventResolvesToDefault(t *testing.T) {
	cfg, err := rules.Load([][]string{{"testdata/nova_rules.toml"}}, nil)
	if err != nil {
		t.Fatalf("rules.Load(nova_rules.toml): %v", err)
	}
	if cfg.EventDefault == nil {
		t.Fatal("EventDefault is nil, want the transcribed [event_default]")
	}
	if len(cfg.Events) == 0 {
		t.Fatal("Events is empty, want the transcribed [[event]] entries")
	}

	router := events.New(cfg)
	routed, ok := router.Route(time.Unix(0, 0), events.Event{Source: "nothing", Type: "matches-no-rule"})
	if !ok {
		t.Fatal("Route on an unmatched event was deduped/dropped, want ok=true")
	}
	if routed.Priority != cfg.EventDefault.Priority || routed.Urgency != cfg.EventDefault.Urgency {
		t.Fatalf("routed = %+v, want default priority/urgency %s/%s",
			routed, cfg.EventDefault.Priority, cfg.EventDefault.Urgency)
	}

	// A known entry resolves its OWN priority/urgency, not the default's.
	routed, ok = router.Route(time.Unix(1, 0), events.Event{
		Source: "tracking", Type: "snap_detected",
	})
	if !ok {
		t.Fatal("Route on tracking/snap_detected was dropped")
	}
	if routed.Priority != "HIGH" || routed.Urgency != "IMMEDIATE" {
		t.Fatalf("routed = %+v, want HIGH/IMMEDIATE", routed)
	}
	if routed.Inject != "You heard a sharp snap nearby." {
		t.Fatalf("Inject = %q", routed.Inject)
	}
}

func TestDedupeCollapsesSameKeyWithinWindow(t *testing.T) {
	cfg := loadConfig(t, `schema_version = 1
[[event]]
source = "pat"
type = "level1"
priority = "NORMAL"
urgency = "NOW"
`)
	router := events.New(cfg, events.WithWindow(10*time.Second))
	base := time.Unix(1000, 0)

	if _, ok := router.Route(base, events.Event{Source: "pat", Type: "level1"}); !ok {
		t.Fatal("first event should route")
	}
	if _, ok := router.Route(base.Add(2*time.Second), events.Event{Source: "pat", Type: "level1"}); ok {
		t.Fatal("second event within the window should be deduped (ok=false)")
	}
	if _, ok := router.Route(base.Add(11*time.Second), events.Event{Source: "pat", Type: "level1"}); !ok {
		t.Fatal("a third event outside the window should route again")
	}
}

func TestDistinctKeysDoNotCollapse(t *testing.T) {
	cfg := loadConfig(t, `schema_version = 1
[[event]]
source = "pat"
type = "level1"
priority = "NORMAL"
urgency = "NOW"

[[event]]
source = "pat"
type = "level2"
priority = "NORMAL"
urgency = "NOW"
`)
	router := events.New(cfg, events.WithWindow(10*time.Second))
	base := time.Unix(1000, 0)

	if _, ok := router.Route(base, events.Event{Source: "pat", Type: "level1"}); !ok {
		t.Fatal("pat/level1 should route")
	}
	if _, ok := router.Route(base.Add(time.Second), events.Event{Source: "pat", Type: "level2"}); !ok {
		t.Fatal("pat/level2 should route independently of pat/level1")
	}
}

func TestExplicitDedupeKeyCollapsesDistinctSourceType(t *testing.T) {
	// Mirrors reachy_nova's two pat rule/fire overrides sharing dedupe =
	// "pat-touch": two DIFFERENT source/type identities collapse into one
	// routed record when they share an explicit dedupe key.
	cfg := loadConfig(t, `schema_version = 1
[[event]]
source = "rule"
type = "fire-a"
priority = "NORMAL"
urgency = "NOW"
dedupe = "pat-touch"

[[event]]
source = "rule"
type = "fire-b"
priority = "NORMAL"
urgency = "NOW"
dedupe = "pat-touch"
`)
	router := events.New(cfg, events.WithWindow(10*time.Second))
	base := time.Unix(1000, 0)

	if _, ok := router.Route(base, events.Event{Source: "rule", Type: "fire-a"}); !ok {
		t.Fatal("rule/fire-a should route")
	}
	if _, ok := router.Route(base.Add(time.Second), events.Event{Source: "rule", Type: "fire-b"}); ok {
		t.Fatal("rule/fire-b shares dedupe=pat-touch with fire-a within the window; want ok=false")
	}
}

func TestVoiceNoneIsRoutedButNotDelivered(t *testing.T) {
	cfg := loadConfig(t, `schema_version = 1
[[event]]
source = "intent"
type = "applied"
priority = "NORMAL"
urgency = "DEFERRABLE"
voice = "none"
inject_template = "Your standing intention '{name}' is now in effect."
`)
	router := events.New(cfg)
	routed, ok := router.Route(time.Unix(0, 0), events.Event{
		Source: "intent", Type: "applied", Payload: map[string]any{"name": "set_inhibition"},
	})
	if !ok {
		t.Fatal("a voice: none event must still be routed (ok=true)")
	}
	if routed.Deliver {
		t.Error("Deliver = true, want false for voice: none")
	}
	if !strings.Contains(routed.Inject, "set_inhibition") {
		t.Errorf("Inject = %q, want the rendered template", routed.Inject)
	}
}

func TestMissingPayloadKeyStaysLiteral(t *testing.T) {
	cfg := loadConfig(t, `schema_version = 1
[[event]]
source = "slack"
type = "slack_message"
priority = "NORMAL"
urgency = "NOW"
inject_template = "New Slack message from {user}: {text}"
`)
	router := events.New(cfg)
	routed, ok := router.Route(time.Unix(0, 0), events.Event{
		Source: "slack", Type: "slack_message",
		Payload: map[string]any{"user": "kiro"},
	})
	if !ok {
		t.Fatal("route failed")
	}
	want := "New Slack message from kiro: {text}"
	if routed.Inject != want {
		t.Errorf("Inject = %q, want %q (a missing key stays literal)", routed.Inject, want)
	}
}

func TestTickFieldsSurfacesAndClearsRoutedEvents(t *testing.T) {
	cfg := loadConfig(t, `schema_version = 1
[[event]]
source = "pat"
type = "level1"
priority = "NORMAL"
urgency = "NOW"
`)
	router := events.New(cfg, events.WithWindow(time.Millisecond))
	now := time.Unix(1000, 0)

	if _, ok := router.Route(now, events.Event{Source: "pat", Type: "level1"}); !ok {
		t.Fatal("route failed")
	}
	fields := router.TickFields(now)
	if v, ok := fields["pat/level1"]; !ok || v != true {
		t.Fatalf("TickFields = %v, want pat/level1: true", fields)
	}
	if fields2 := router.TickFields(now); len(fields2) != 0 {
		t.Fatalf("TickFields after a read = %v, want empty (it clears on read)", fields2)
	}
}

func TestNoEntryNoDefaultStillRoutesWithFreeVoice(t *testing.T) {
	cfg := loadConfig(t, `schema_version = 1
[[event]]
source = "a"
type = "b"
priority = "LOW"
urgency = "BACKGROUND"
`)
	router := events.New(cfg)
	routed, ok := router.Route(time.Unix(0, 0), events.Event{Source: "unknown", Type: "thing"})
	if !ok {
		t.Fatal("route failed")
	}
	if routed.Voice != "free" || !routed.Deliver {
		t.Errorf("routed = %+v, want default free/deliverable when there is no entry and no default", routed)
	}
}
