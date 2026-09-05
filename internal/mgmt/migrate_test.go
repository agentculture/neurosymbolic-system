package mgmt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
	"github.com/agentculture/neurosymbolic-system/internal/rules"
)

// sayBodyRules carries a say body whose own line is EXACTLY the text the
// whole-document regexp used to match. The migrated twin must keep that body
// byte for byte and change only the document's own key.
const sayBodyRules = `schema_version = 1

[[react]]
id = "explain-the-schema"
when = { field = "pat", op = "is_true" }
run = "nod"
say = """
schema_version = 1
"""
`

func writeRules(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func migrateOK(t *testing.T, inPath string) string {
	t.Helper()
	h := &Handler{Version: "0.0.0-test", Revision: "test"}
	resp := h.Handle(Request{Verb: "rules.migrate", Args: []string{inPath}})
	if resp.Code != clifmt.ExitSuccess {
		t.Fatalf("Code = %d, want %d (stderr=%q)", resp.Code, clifmt.ExitSuccess, resp.Stderr)
	}
	out := strings.TrimSuffix(inPath, ".toml") + ".v2.toml"
	migrated, err := os.ReadFile(out) // #nosec G304 -- a path this test just made
	if err != nil {
		t.Fatalf("ReadFile %s: %v", out, err)
	}
	return string(migrated)
}

// The regression: a `say` body's own line must survive verbatim.
func TestMigrateLeavesAMultilineSayBodyVerbatim(t *testing.T) {
	migrated := migrateOK(t, writeRules(t, "rules.toml", sayBodyRules))

	want := strings.Replace(sayBodyRules, "schema_version = 1\n\n", "schema_version = 2\n\n", 1)
	if migrated != want {
		t.Fatalf("migrated twin differs from the expected one-line change:\n--- got ---\n%s\n--- want ---\n%s",
			migrated, want)
	}
	if !strings.Contains(migrated, "say = \"\"\"\nschema_version = 1\n\"\"\"") {
		t.Error("the say body was rewritten; only the document's own key may change")
	}

	// The loaded rule set is the real contract, not just the bytes.
	before, err := rules.LoadFile(writeRules(t, "again.toml", sayBodyRules), nil)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	after, err := rules.LoadFile(writeRules(t, "after.toml", migrated), nil)
	if err != nil {
		t.Fatalf("LoadFile migrated: %v", err)
	}
	if after.SchemaVersion != rules.SchemaVersion2 {
		t.Errorf("SchemaVersion = %d, want %d", after.SchemaVersion, rules.SchemaVersion2)
	}
	if before.React[0].Say != after.React[0].Say {
		t.Errorf("Say = %q, want %q", after.React[0].Say, before.React[0].Say)
	}
}

// The same failure one table down: `schema_version = 1` under [modes.<name>]
// is that mode's parameter, and a top-level key cannot appear after a table
// header at all, so this line is never the document's version.
func TestMigrateLeavesAModeParameterAlone(t *testing.T) {
	body := `schema_version = 1
active_mode = "calm"

[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"

[modes.calm]
schema_version = 1
`
	migrated := migrateOK(t, writeRules(t, "rules.toml", body))
	if !strings.HasPrefix(migrated, "schema_version = 2\n") {
		t.Errorf("the document's own key was not bumped:\n%s", migrated)
	}
	if !strings.HasSuffix(migrated, "[modes.calm]\nschema_version = 1\n") {
		t.Errorf("the mode parameter was rewritten:\n%s", migrated)
	}
}

// Everything else about the file — comments, blank lines, indentation, the
// trailing comment on the key's own line — survives.
func TestMigrateRewritesOnlyTheKeyOnItsLine(t *testing.T) {
	body := `# a leading comment
schema_version = 1  # the version this file speaks

[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
say = 'schema_version = 1'
`
	migrated := migrateOK(t, writeRules(t, "rules.toml", body))
	if !strings.Contains(migrated, "schema_version = 2  # the version this file speaks") {
		t.Errorf("the key's line lost its comment or its spacing:\n%s", migrated)
	}
	if !strings.Contains(migrated, "say = 'schema_version = 1'") {
		t.Errorf("a single-line literal string was rewritten:\n%s", migrated)
	}
}

// --- the scanner, directly ---------------------------------------------------

func TestRewriteSchemaVersion(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		rewrote bool
	}{
		{
			"plain", "schema_version = 1\n", "schema_version = 2\n", true,
		},
		{
			"no trailing newline", "schema_version = 1", "schema_version = 2", true,
		},
		{
			"crlf", "schema_version = 1\r\n", "schema_version = 2\r\n", true,
		},
		{
			"only the first", "schema_version = 1\n[modes.a]\nschema_version = 1\n",
			"schema_version = 2\n[modes.a]\nschema_version = 1\n", true,
		},
		{
			"inside a basic multi-line string",
			"schema_version = 1\nsay = \"\"\"\nschema_version = 1\n\"\"\"\n",
			"schema_version = 2\nsay = \"\"\"\nschema_version = 1\n\"\"\"\n", true,
		},
		{
			"inside a literal multi-line string",
			"schema_version = 1\nsay = '''\nschema_version = 1\n'''\n",
			"schema_version = 2\nsay = '''\nschema_version = 1\n'''\n", true,
		},
		{
			"a lone quote inside a literal multi-line string does not close it",
			"schema_version = 1\nsay = '''\n\"\"\"\nschema_version = 1\n'''\n",
			"schema_version = 2\nsay = '''\n\"\"\"\nschema_version = 1\n'''\n", true,
		},
		{
			"commented out", "# schema_version = 1\n", "# schema_version = 1\n", false,
		},
		{
			"already v2", "schema_version = 2\n", "schema_version = 2\n", false,
		},
		{
			"another version", "schema_version = 3\n", "schema_version = 3\n", false,
		},
		{
			"nothing to rewrite", "", "", false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rewrote := rewriteSchemaVersion(tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if rewrote != tc.rewrote {
				t.Errorf("rewrote = %v, want %v", rewrote, tc.rewrote)
			}
		})
	}
}

// --- sameRules ---------------------------------------------------------------

// loadTwo loads two rules bodies as Configs, for a sameRules comparison.
func loadTwo(t *testing.T, before, after string) (*rules.Config, *rules.Config) {
	t.Helper()
	b, err := rules.LoadFile(writeRules(t, "before.toml", before), nil)
	if err != nil {
		t.Fatalf("LoadFile before: %v", err)
	}
	a, err := rules.LoadFile(writeRules(t, "after.toml", after), nil)
	if err != nil {
		t.Fatalf("LoadFile after: %v", err)
	}
	return b, a
}

// The field that let a corrupted twin through: sameRules compared ids, kinds,
// run names and predicates, and never looked at say.
func TestSameRulesDetectsADifferingSay(t *testing.T) {
	const base = `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
say = "%s"
`
	before, after := loadTwo(t,
		strings.Replace(base, "%s", "schema_version = 1", 1),
		strings.Replace(base, "%s", "schema_version = 2", 1))
	if sameRules(before, after) {
		t.Fatal("sameRules called two rule sets identical although their say text differs")
	}
}

func TestSameRulesDetectsEveryOtherPreservedField(t *testing.T) {
	const base = `schema_version = 1
active_mode = "calm"

[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
params = { amplitude = 10.0 }
cooldown_s = 5.0
duration_s = 2.0

[[inhibit]]
id = "i1"
when = { field = "pat", op = "is_false" }
disable = ["nod"]

[modes.calm]
amplitude = 3.0

[modes.loud]
amplitude = 9.0

[[event]]
source = "tracking"
type = "snap_detected"
priority = "HIGH"
urgency = "IMMEDIATE"
`
	cases := map[string]struct{ from, to string }{
		"params":      {"amplitude = 10.0", "amplitude = 11.0"},
		"cooldown_s":  {"cooldown_s = 5.0", "cooldown_s = 6.0"},
		"duration_s":  {"duration_s = 2.0", "duration_s = 3.0"},
		"modes":       {"[modes.calm]\namplitude = 3.0", "[modes.calm]\namplitude = 4.0"},
		"active_mode": {`active_mode = "calm"`, `active_mode = "loud"`},
		"event":       {`priority = "HIGH"`, `priority = "LOW"`},
		"predicate":   {`op = "is_true"`, `op = "is_false"`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			changed := strings.Replace(base, tc.from, tc.to, 1)
			if changed == base {
				t.Fatalf("the fixture does not contain %q", tc.from)
			}
			before, after := loadTwo(t, base, changed)
			if sameRules(before, after) {
				t.Errorf("sameRules missed a change to %s", name)
			}
		})
	}
}

// Two loads of the SAME text, from two different paths, are the same rule set
// — the per-rule Source and per-event Path differ by construction and must
// not count as a difference.
func TestSameRulesIgnoresTheSourcePath(t *testing.T) {
	const base = `schema_version = 1
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
say = "hello"

[[event]]
source = "tracking"
type = "snap_detected"
priority = "HIGH"
urgency = "IMMEDIATE"
`
	before, after := loadTwo(t, base, base)
	if before.React[0].Source == after.React[0].Source {
		t.Fatal("the fixture loaded both sides from one path; the test proves nothing")
	}
	if !sameRules(before, after) {
		t.Fatal("sameRules called one rule set different from itself")
	}
}

// And the schema version itself is the one difference migrate is allowed to
// make, so it must not be compared.
func TestSameRulesIgnoresTheSchemaVersion(t *testing.T) {
	const base = `schema_version = %d
[[react]]
id = "r1"
when = { field = "pat", op = "is_true" }
run = "nod"
`
	before, after := loadTwo(t,
		strings.Replace(base, "%d", "1", 1),
		strings.Replace(base, "%d", "2", 1))
	if !sameRules(before, after) {
		t.Fatal("sameRules compared the schema version, which is what migrate changes")
	}
}
