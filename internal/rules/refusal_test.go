package rules_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/rules"
)

// writeTOML drops body into a temp file and returns its path.
func writeTOML(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// testVocab is the vocabulary used by the refusal cases that need names checked.
var testVocab = donorVocab{
	fields:  set("pat", "rms_ratio", "transcript", "battery_frac"),
	actions: set("nod", "orient", "idle"),
	loops:   set("idle"),
	params: map[string]map[string][2]float64{
		"nod":    {"amplitude": {0, 30}},
		"orient": {"rms_ratio": {0, 100}},
		"idle":   {},
	},
}

func TestRefusals(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		vocab rules.Vocabulary
		// every fragment must appear in the error message
		want []string
	}{
		{
			name: "missing schema_version",
			body: `
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
`,
			want: []string{"schema_version", "1", "2"},
		},
		{
			name: "unknown schema_version",
			body: `schema_version = 7
`,
			want: []string{"schema_version", "7", "1", "2"},
		},
		{
			name: "unknown top-level field",
			body: `schema_version = 1
exec = "rm -rf /"
`,
			want: []string{"exec", "react", "inhibit", "modes", "active_mode"},
		},
		{
			name: "unknown rule field",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
source = "evil.py"
`,
			want: []string{"r1", "source"},
		},
		{
			name: "unknown predicate field",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true", nonsense = 1 }
run = "nod"
`,
			want: []string{"r1", "nonsense", "field", "op", "value"},
		},
		{
			name: "unknown mode field",
			body: `schema_version = 1
active_mode = "calm"
[modes.calm]
gain = 0.5
[modes.calm.nested]
oops = 1
`,
			want: []string{"calm", "nested"},
		},
		{
			name: "unknown predicate field name",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "telepathy", op = "is_true" }
run = "nod"
`,
			vocab: testVocab,
			want:  []string{"r1", "telepathy", "sense field"},
		},
		{
			name: "unknown op",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "approximately", value = 1 }
run = "nod"
`,
			want: []string{"r1", "approximately", "is_true", "ge"},
		},
		{
			name: "nan cooldown_s",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
cooldown_s = nan
`,
			want: []string{"r1", "cooldown_s", "finite"},
		},
		{
			name: "inf hysteresis",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
hysteresis = inf
`,
			want: []string{"r1", "hysteresis", "finite"},
		},
		{
			name: "inf duration_s",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
duration_s = inf
`,
			want: []string{"r1", "duration_s", "finite"},
		},
		{
			name: "nan predicate value",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "rms_ratio", op = "ge", value = nan }
run = "nod"
`,
			want: []string{"r1", "value", "finite"},
		},
		{
			name: "nan param",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
params = { amplitude = nan }
`,
			want: []string{"r1", "amplitude", "finite"},
		},
		{
			name: "zero duration_s",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
duration_s = 0
`,
			want: []string{"r1", "duration_s", "> 0"},
		},
		{
			name: "negative duration_s",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
duration_s = -2.0
`,
			want: []string{"r1", "duration_s", "> 0"},
		},
		{
			name: "negative cooldown_s",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
cooldown_s = -1.0
`,
			want: []string{"r1", "cooldown_s", ">= 0"},
		},
		{
			name: "negative hysteresis",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
hysteresis = -0.5
`,
			want: []string{"r1", "hysteresis", ">= 0"},
		},
		{
			name: "boolean op carrying a value",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true", value = 3 }
run = "nod"
`,
			want: []string{"r1", "is_true", "value"},
		},
		{
			name: "numeric op missing a value",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "rms_ratio", op = "ge" }
run = "nod"
`,
			want: []string{"r1", "ge", "value"},
		},
		{
			name: "equality op missing a value",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "rms_ratio", op = "eq" }
run = "nod"
`,
			want: []string{"r1", "eq", "value"},
		},
		{
			name: "looping action with no duration_s",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "idle"
`,
			vocab: testVocab,
			want:  []string{"r1", "idle", "duration_s", "loop"},
		},
		{
			name: "unknown action",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "explode"
`,
			vocab: testVocab,
			want:  []string{"r1", "explode", "action"},
		},
		{
			name: "duplicate id within one file",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
[[inhibit]]
id = "r1"
when = { field = "pat", op = "is_true" }
disable = ["nod"]
`,
			want: []string{"r1", "duplicate"},
		},
		{
			name: "empty say",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
say = "   "
`,
			want: []string{"r1", "say", "non-empty"},
		},
		{
			name: "over-long say",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
say = "` + strings.Repeat("x", rules.MaxSayChars+1) + `"
`,
			want: []string{"r1", "say", "501", "500"},
		},
		{
			name: "param outside the action's domain",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
params = { velocity = 3.0 }
`,
			vocab: testVocab,
			want:  []string{"r1", "velocity", "nod"},
		},
		{
			name: "param out of range",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
params = { amplitude = 900.0 }
`,
			vocab: testVocab,
			want:  []string{"r1", "amplitude", "900", "30"},
		},
		{
			name: "active_mode names an undefined mode",
			body: `schema_version = 1
active_mode = "frenzy"
[modes.calm]
gain = 0.5
`,
			want: []string{"active_mode", "frenzy", "calm"},
		},
		{
			name: "modes with no active_mode",
			body: `schema_version = 1
[modes.calm]
gain = 0.5
`,
			want: []string{"active_mode", "calm"},
		},
		{
			name: "v1 file using all",
			body: `schema_version = 1
[[react]]
id = "r1"
run = "nod"
when = { all = [ { field = "pat", op = "is_true" } ] }
`,
			want: []string{"r1", "all", "schema_version", "2"},
		},
		{
			name: "v1 file using any",
			body: `schema_version = 1
[[react]]
id = "r1"
run = "nod"
when = { any = [ { field = "pat", op = "is_true" } ] }
`,
			want: []string{"r1", "any", "schema_version", "2"},
		},
		{
			name: "v2 empty all",
			body: `schema_version = 2
[[react]]
id = "r1"
run = "nod"
when = { all = [] }
`,
			want: []string{"r1", "all", "empty"},
		},
		{
			name: "v2 empty any",
			body: `schema_version = 2
[[react]]
id = "r1"
run = "nod"
when = { any = [] }
`,
			want: []string{"r1", "any", "empty"},
		},
		{
			name: "v2 unknown key beside all",
			body: `schema_version = 2
[[react]]
id = "r1"
run = "nod"
when = { all = [ { field = "pat", op = "is_true" } ], field = "pat" }
`,
			want: []string{"r1", "all", "field"},
		},
		{
			name: "v2 all and any together",
			body: `schema_version = 2
[[react]]
id = "r1"
run = "nod"
when = { all = [ { field = "pat", op = "is_true" } ], any = [ { field = "pat", op = "is_true" } ] }
`,
			want: []string{"r1", "all", "any"},
		},
		{
			name: "v2 nesting deeper than one level",
			body: `schema_version = 2
[[react]]
id = "r1"
run = "nod"
when = { all = [ { any = [ { all = [ { field = "pat", op = "is_true" } ] } ] } ] }
`,
			want: []string{"r1", "nest", "one level"},
		},
		{
			name: "missing required field",
			body: `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
`,
			want: []string{"r1", "run"},
		},
		{
			name: "inhibit with empty disable",
			body: `schema_version = 1
[[inhibit]]
id = "r1"
when = { field = "pat", op = "is_true" }
disable = []
`,
			want: []string{"r1", "disable"},
		},
		{
			name: "inhibit disabling an unknown action",
			body: `schema_version = 1
[[inhibit]]
id = "r1"
when = { field = "pat", op = "is_true" }
disable = ["fly"]
`,
			vocab: testVocab,
			want:  []string{"r1", "fly", "action"},
		},
		{
			name: "enabled is not a boolean",
			body: `schema_version = 1
[[react]]
id = "r1"
enabled = "no"
when = { field = "pat", op = "is_true" }
run = "nod"
`,
			want: []string{"r1", "enabled", "boolean"},
		},
		{
			name: "tombstone with an unknown field is still refused",
			body: `schema_version = 1
[[react]]
id = "r1"
enabled = false
sorce = "typo"
`,
			want: []string{"r1", "sorce"},
		},
		{
			name: "not valid TOML",
			body: "schema_version = 1\n[[react\n",
			want: []string{"TOML"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTOML(t, "rules.toml", tc.body)
			_, err := rules.Load([][]string{{path}}, tc.vocab)
			if err == nil {
				t.Fatalf("expected refusal, got nil")
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, "rules: ") {
				t.Errorf("message %q does not start with the %q prefix", msg, "rules: ")
			}
			if !strings.Contains(msg, path) {
				t.Errorf("message %q does not name the source path %q", msg, path)
			}
			if !strings.Contains(msg, " — ") {
				t.Errorf("message %q carries no ' — <fix>' remediation half", msg)
			}
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Errorf("message %q does not mention %q", msg, want)
				}
			}
		})
	}
}

func TestDuplicateIDAcrossFilesOfTheSameLayerNamesBothPaths(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.toml")
	b := filepath.Join(dir, "b.toml")
	body := `schema_version = 1
[[react]]
id = "shared"
when = { field = "pat", op = "is_true" }
run = "nod"
`
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := rules.Load([][]string{{a, b}}, nil)
	if err == nil {
		t.Fatal("expected a duplicate-id refusal")
	}
	msg := err.Error()
	for _, want := range []string{"shared", "duplicate", a, b} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}

func TestMissingFileIsRefusedNamingThePath(t *testing.T) {
	_, err := rules.Load([][]string{{"testdata/nope.toml"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "testdata/nope.toml") {
		t.Fatalf("err = %v, want one naming the missing path", err)
	}
}
