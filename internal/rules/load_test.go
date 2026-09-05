package rules_test

import (
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/rules"
)

const shippedLayer = `schema_version = 1
[[react]]
id = "pat-acknowledge"
when = { field = "pat", op = "is_true" }
run = "nod"
cooldown_s = 5.0
[[react]]
id = "look-toward-sound"
when = { field = "rms_ratio", op = "ge", value = 5.0 }
run = "orient"
duration_s = 12.0
[[inhibit]]
id = "quiet-hours"
when = { field = "battery_frac", op = "lt", value = 0.15 }
disable = ["orient"]
`

func TestLayerOverrideKeepsPositionAndReplacesWholesale(t *testing.T) {
	base := writeTOML(t, "shipped.toml", shippedLayer)
	overlay := writeTOML(t, "overlay.toml", `schema_version = 1
[[react]]
id = "pat-acknowledge"
when = { field = "pat", op = "is_true" }
run = "orient"
cooldown_s = 30.0
`)
	cfg, err := rules.Load([][]string{{base}, {overlay}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := ids(cfg.React), []string{"pat-acknowledge", "look-toward-sound"}; !equalStrings(got, want) {
		t.Fatalf("react ids = %v, want %v (override keeps the base's position)", got, want)
	}
	got := cfg.React[0]
	if got.Run != "orient" || got.CooldownS != 30.0 {
		t.Errorf("override did not win wholesale: %+v", got)
	}
	if got.Source != overlay {
		t.Errorf("source = %q, want the overlay %q", got.Source, overlay)
	}
	if cfg.React[1].Source != base {
		t.Errorf("base-only rule source = %q, want %q", cfg.React[1].Source, base)
	}
	if len(cfg.Inhibit) != 1 || cfg.Inhibit[0].ID != "quiet-hours" {
		t.Errorf("base-only inhibit rule lost: %v", ids(cfg.Inhibit))
	}
}

func TestLaterLayerOnlyIDIsAppended(t *testing.T) {
	base := writeTOML(t, "shipped.toml", shippedLayer)
	overlay := writeTOML(t, "overlay.toml", `schema_version = 1
[[react]]
id = "local-extra"
when = { field = "transcript", op = "is_true" }
run = "nod"
`)
	cfg, err := rules.Load([][]string{{base}, {overlay}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []string{"pat-acknowledge", "look-toward-sound", "local-extra"}
	if got := ids(cfg.React); !equalStrings(got, want) {
		t.Fatalf("react ids = %v, want %v", got, want)
	}
}

func TestTombstoneInLaterLayerDisablesEarlierRule(t *testing.T) {
	base := writeTOML(t, "shipped.toml", shippedLayer)
	overlay := writeTOML(t, "overlay.toml", `schema_version = 1
[[react]]
id = "look-toward-sound"
enabled = false
[[inhibit]]
id = "quiet-hours"
enabled = false
`)
	cfg, err := rules.Load([][]string{{base}, {overlay}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := ids(cfg.React), []string{"pat-acknowledge"}; !equalStrings(got, want) {
		t.Fatalf("react ids = %v, want %v", got, want)
	}
	if len(cfg.Inhibit) != 0 {
		t.Fatalf("inhibit ids = %v, want none", ids(cfg.Inhibit))
	}
}

func TestTombstoneForAnUnknownIDIsInert(t *testing.T) {
	base := writeTOML(t, "shipped.toml", shippedLayer)
	overlay := writeTOML(t, "overlay.toml", `schema_version = 1
[[react]]
id = "removed-upstream"
enabled = false
`)
	cfg, err := rules.Load([][]string{{base}, {overlay}}, nil)
	if err != nil {
		t.Fatalf("a tombstone naming an id no layer defines must be inert, got: %v", err)
	}
	if len(cfg.React) != 2 {
		t.Errorf("react ids = %v, want the two shipped ones", ids(cfg.React))
	}
}

func TestATombstonedIDCanBeRevivedByAStillLaterLayer(t *testing.T) {
	base := writeTOML(t, "shipped.toml", shippedLayer)
	mid := writeTOML(t, "mid.toml", `schema_version = 1
[[react]]
id = "pat-acknowledge"
enabled = false
`)
	top := writeTOML(t, "top.toml", `schema_version = 1
[[react]]
id = "pat-acknowledge"
when = { field = "pat", op = "is_true" }
run = "nod"
cooldown_s = 1.0
`)
	cfg, err := rules.Load([][]string{{base}, {mid}, {top}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.React) != 2 {
		t.Fatalf("react = %v, want the tombstoned rule back", ids(cfg.React))
	}
	var revived *rules.Rule
	for i := range cfg.React {
		if cfg.React[i].ID == "pat-acknowledge" {
			revived = &cfg.React[i]
		}
	}
	if revived == nil {
		t.Fatalf("react = %v, want pat-acknowledge revived", ids(cfg.React))
	}
	if revived.CooldownS != 1.0 || revived.Source != top {
		t.Errorf("revived = %+v, want the top layer's copy", *revived)
	}
}

func TestFilesWithinOneLayerMerge(t *testing.T) {
	a := writeTOML(t, "a.toml", shippedLayer)
	b := writeTOML(t, "b.toml", `schema_version = 1
[[react]]
id = "extra"
when = { field = "pat", op = "is_true" }
run = "nod"
`)
	cfg, err := rules.Load([][]string{{a, b}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []string{"pat-acknowledge", "look-toward-sound", "extra"}
	if got := ids(cfg.React); !equalStrings(got, want) {
		t.Fatalf("react ids = %v, want %v", got, want)
	}
}

func TestSchemaOneFileLoadsUnderASchemaTwoOverlay(t *testing.T) {
	base := writeTOML(t, "shipped.toml", shippedLayer)
	overlay := writeTOML(t, "overlay.toml", `schema_version = 2
[[react]]
id = "pat-acknowledge"
run = "nod"
when = { all = [
  { field = "pat", op = "is_true" },
  { field = "rms_ratio", op = "lt", value = 2.0 },
] }
`)
	cfg, err := rules.Load([][]string{{base}, {overlay}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SchemaVersion != 2 {
		t.Errorf("SchemaVersion = %d, want the highest layer's 2", cfg.SchemaVersion)
	}
	when := cfg.React[0].When
	if when.IsLeaf() {
		t.Fatalf("expected a group predicate, got leaf %+v", when)
	}
	if len(when.All) != 2 || len(when.Any) != 0 {
		t.Fatalf("when = %+v", when)
	}
	if when.All[0].Field != "pat" || when.All[1].Field != "rms_ratio" {
		t.Errorf("children = %+v", when.All)
	}
	if v, ok := when.All[1].Value.(float64); !ok || v != 2.0 {
		t.Errorf("child value = %#v", when.All[1].Value)
	}
}

func TestSchemaTwoNestingOneLevel(t *testing.T) {
	path := writeTOML(t, "rules.toml", `schema_version = 2
[[react]]
id = "r1"
run = "nod"
when = { any = [
  { field = "pat", op = "is_true" },
  { all = [ { field = "rms_ratio", op = "ge", value = 5.0 }, { field = "transcript", op = "is_true" } ] },
] }
`)
	cfg, err := rules.Load([][]string{{path}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	when := cfg.React[0].When
	if len(when.Any) != 2 {
		t.Fatalf("when = %+v", when)
	}
	if !when.Any[0].IsLeaf() {
		t.Errorf("first child should be a leaf: %+v", when.Any[0])
	}
	if len(when.Any[1].All) != 2 {
		t.Errorf("second child should be an all-group of 2: %+v", when.Any[1])
	}
}

func TestModes(t *testing.T) {
	path := writeTOML(t, "rules.toml", `schema_version = 1
active_mode = "calm"

[modes.calm]
gain = 0.5
rate_hz = 2

[modes.lively]
gain = 1.5
`)
	cfg, err := rules.Load([][]string{{path}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ActiveMode != "calm" {
		t.Errorf("ActiveMode = %q, want calm", cfg.ActiveMode)
	}
	if len(cfg.Modes) != 2 {
		t.Fatalf("Modes = %v", cfg.Modes)
	}
	params, ok := cfg.ActiveModeParams()
	if !ok {
		t.Fatal("ActiveModeParams() reported no active mode")
	}
	if params["gain"] != 0.5 || params["rate_hz"] != 2.0 {
		t.Errorf("active mode params = %v", params)
	}
	if cfg.Modes["lively"]["gain"] != 1.5 {
		t.Errorf("lively = %v", cfg.Modes["lively"])
	}
}

func TestModesMergeAcrossLayers(t *testing.T) {
	base := writeTOML(t, "shipped.toml", `schema_version = 1
active_mode = "calm"
[modes.calm]
gain = 0.5
[modes.lively]
gain = 1.5
`)
	overlay := writeTOML(t, "overlay.toml", `schema_version = 1
active_mode = "lively"
[modes.lively]
gain = 2.5
`)
	cfg, err := rules.Load([][]string{{base}, {overlay}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ActiveMode != "lively" {
		t.Errorf("ActiveMode = %q, want the overlay's lively", cfg.ActiveMode)
	}
	if cfg.Modes["lively"]["gain"] != 2.5 {
		t.Errorf("lively gain = %v, want the overlay's 2.5", cfg.Modes["lively"]["gain"])
	}
	if cfg.Modes["calm"]["gain"] != 0.5 {
		t.Errorf("base-only mode lost: %v", cfg.Modes)
	}
}

func TestNoModesNoActiveMode(t *testing.T) {
	path := writeTOML(t, "rules.toml", `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
`)
	cfg, err := rules.Load([][]string{{path}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ActiveMode != "" {
		t.Errorf("ActiveMode = %q, want empty", cfg.ActiveMode)
	}
	if _, ok := cfg.ActiveModeParams(); ok {
		t.Error("ActiveModeParams() reported an active mode where none is selected")
	}
}

func TestEmptyLoadIsInert(t *testing.T) {
	cfg, err := rules.Load(nil, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.React) != 0 || len(cfg.Inhibit) != 0 || cfg.ActiveMode != "" {
		t.Errorf("empty load produced %+v", cfg)
	}
}

func TestSameIDInDifferentLayersIsNotADuplicate(t *testing.T) {
	base := writeTOML(t, "a.toml", shippedLayer)
	overlay := writeTOML(t, "b.toml", shippedLayer)
	if _, err := rules.Load([][]string{{base}, {overlay}}, nil); err != nil {
		t.Fatalf("cross-layer ids are overrides, not duplicates: %v", err)
	}
}

func TestPredicateValueTypes(t *testing.T) {
	path := writeTOML(t, "rules.toml", `schema_version = 1
[[react]]
id = "str-eq"
when = { field = "transcript", op = "eq", value = "hello" }
run = "nod"
[[react]]
id = "bool-eq"
when = { field = "pat", op = "ne", value = true }
run = "nod"
[[react]]
id = "absent"
when = { field = "transcript", op = "absent_for", value = 30 }
run = "nod"
`)
	cfg, err := rules.Load([][]string{{path}}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.React[0].When.Value != "hello" {
		t.Errorf("str value = %#v", cfg.React[0].When.Value)
	}
	if cfg.React[1].When.Value != true {
		t.Errorf("bool value = %#v", cfg.React[1].When.Value)
	}
	if v, ok := cfg.React[2].When.Value.(float64); !ok || v != 30 {
		t.Errorf("absent_for value = %#v, want float64(30)", cfg.React[2].When.Value)
	}
}
