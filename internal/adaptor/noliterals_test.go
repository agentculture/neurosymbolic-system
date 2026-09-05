package adaptor_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoDonorLiteralsInEngineSources is the acceptance test for "the engine
// holds no robot literal".
//
// Every channel, sense field and action name either donor robot uses lives in
// testdata/donor_names.txt and NOWHERE else in this repository's Go sources.
// The engine learns those names at startup from an adaptor config; if one shows
// up hard-coded in cmd/ or internal/, a robot has leaked into the runtime and
// the second robot (MicroDuck) can no longer be described by the same engine.
//
// Scope: non-test .go files under cmd/ and internal/, excluding any testdata
// directory and any vendored tree.
//
// What counts as a leak: a string literal whose WHOLE value, after unquoting,
// equals a donor name. That is deliberately narrow. An earlier version scanned
// for donor names as tokens INSIDE literals and drowned in false positives the
// moment the rules loader landed beside it — 17 of them, every one either a
// rules-file schema keyword ("active_mode", "enabled") or ordinary English in
// an error message ("...or move one into a higher layer...", "...the words to
// speak..."). Those are not robot names baked into the engine; they are the
// engine's own vocabulary, which happens to collide with a donor's word list.
// The guard's intent is "no channel/field/action name is compiled in as an
// identifier", not "no English word", so identifier-shaped matching is the
// honest test and prose is out of scope by construction.
//
// The residual collisions — a schema key spelled exactly like a donor name —
// are listed in testdata/schema_keywords.txt with their justification, so an
// exemption is a visible, reviewed edit rather than a quietly loosened matcher.
func TestNoDonorLiteralsInEngineSources(t *testing.T) {
	root := repoRoot(t)
	names := donorNames(t)
	if len(names) == 0 {
		t.Fatal("testdata/donor_names.txt yielded no names")
	}

	scanned := 0
	for _, dir := range []string{"cmd", "internal"} {
		base := filepath.Join(root, dir)
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			scanned++
			checkFileLiterals(t, root, path, names)
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", base, err)
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no Go sources; the walk roots are wrong")
	}
}

// donorNames is the donor name list minus the exempted schema keywords. It is
// built by subtraction rather than by editing the donor list, so the donor list
// stays a faithful transcription of what the two robots actually call things.
func donorNames(t *testing.T) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, name := range loadNameFile(t, filepath.Join("testdata", "donor_names.txt")) {
		names[name] = true
	}
	for _, exempt := range loadNameFile(t, filepath.Join("testdata", "schema_keywords.txt")) {
		delete(names, exempt)
	}
	return names
}

func checkFileLiterals(t *testing.T, root, path string, names map[string]bool) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	rel, relErr := filepath.Rel(root, path)
	if relErr != nil {
		rel = path
	}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, unquoteErr := strconv.Unquote(lit.Value)
		if unquoteErr != nil {
			// A literal Go itself accepted but strconv cannot unquote is not
			// something to guess at; skip it rather than invent a value.
			return true
		}
		if names[value] {
			pos := fset.Position(lit.Pos())
			t.Errorf(
				"%s:%d: donor name %q appears as a string literal; the engine must "+
					"learn it from an adaptor config instead (if this is a rules-file "+
					"schema key rather than a robot name, justify it in "+
					"internal/adaptor/testdata/schema_keywords.txt)",
				rel, pos.Line, value,
			)
		}
		return true
	})
}

func loadNameFile(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var names []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		names = append(names, line)
	}
	return names
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found above the test's working directory")
		}
		dir = parent
	}
}

// TestDonorNamesAreExactMatchesOnly pins the matching rule the scan depends on,
// in both directions: an exact literal is a leak, and the same name appearing
// inside a longer identifier or a sentence is not. A future tightening that
// silently stopped matching the first case would leave the guard passing while
// the leak it exists to catch went through; a loosening that started matching
// the second would resurrect the 17 false positives that motivated this shape.
func TestDonorNamesAreExactMatchesOnly(t *testing.T) {
	names := donorNames(t)
	for _, name := range []string{"body_yaw", "antennas", "feel-alive", "orient-to-sound"} {
		if !names[name] {
			t.Errorf("%q should be a live donor name", name)
		}
	}
	notLeaks := []string{
		"active_mode", // a rules-file schema key, not a channel
		"head_pose",   // an identifier that merely contains one
		"overhead",
		"a mode is a flat bag of numbers",
		"give say the words to speak, or remove the field entirely",
		"rename one of them, or move one into a higher layer to override the other",
	}
	for _, text := range notLeaks {
		if names[text] {
			t.Errorf("%q must not be treated as a donor name", text)
		}
	}
}

// TestSchemaKeywordsAreExempted pins the exemption list itself: the rules
// loader spells these as literals legitimately, and each one must actually be
// subtracted from the enforced set (an entry that changes nothing is dead
// weight that reads like a hole in the guard).
func TestSchemaKeywordsAreExempted(t *testing.T) {
	exempt := loadNameFile(t, filepath.Join("testdata", "schema_keywords.txt"))
	want := map[string]bool{"enabled": false, "mode": false, "active_mode": false}
	for _, name := range exempt {
		if _, known := want[name]; known {
			want[name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("schema_keywords.txt is missing %q", name)
		}
	}
	names := donorNames(t)
	for _, name := range exempt {
		if names[name] {
			t.Errorf("%q is exempted but still enforced", name)
		}
	}
}
