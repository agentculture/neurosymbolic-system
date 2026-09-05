package adaptor_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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
// directory. Only string literals are inspected — identifiers, comments and
// this test's own fixtures are exempt by construction.
func TestNoDonorLiteralsInEngineSources(t *testing.T) {
	root := repoRoot(t)
	names := loadDonorNames(t, filepath.Join("testdata", "donor_names.txt"))
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
				if d.Name() == "testdata" {
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

func checkFileLiterals(t *testing.T, root, path string, names []string) {
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
		text := lit.Value
		for _, name := range names {
			if containsToken(text, name) {
				pos := fset.Position(lit.Pos())
				t.Errorf(
					"%s:%d: donor name %q appears as a string literal (%s); "+
						"the engine must learn it from an adaptor config instead",
					rel, pos.Line, name, text,
				)
			}
		}
		return true
	})
}

// containsToken reports whether name occurs in text bounded by non-alphanumeric
// characters, so "do" does not match inside "does" while "head" still matches
// inside "head_pose" (an underscore is a separator, not part of a name).
func containsToken(text, name string) bool {
	for i := 0; i+len(name) <= len(text); i++ {
		if text[i:i+len(name)] != name {
			continue
		}
		if i > 0 && isNameByte(text[i-1]) {
			continue
		}
		if j := i + len(name); j < len(text) && isNameByte(text[j]) {
			continue
		}
		return true
	}
	return false
}

func isNameByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func loadDonorNames(t *testing.T, path string) []string {
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

// TestContainsTokenBoundaries pins the boundary rule the scan depends on, so a
// future tightening of the matcher cannot silently start ignoring real leaks.
func TestContainsTokenBoundaries(t *testing.T) {
	cases := []struct {
		text, name string
		want       bool
	}{
		{`"head"`, "head", true},
		{`"the head channel"`, "head", true},
		{`"overhead"`, "head", false},
		{`"head_pose"`, "head", true},
		{`"loop_hz"`, "loops", false},
		{`"does not"`, "do", false},
		{`"feel-alive"`, "feel-alive", true},
		{`"a feel-alive base layer"`, "feel-alive", true},
		{`"unfeel-alive"`, "feel-alive", false},
	}
	for _, c := range cases {
		if got := containsToken(c.text, c.name); got != c.want {
			t.Errorf("containsToken(%q, %q) = %v, want %v", c.text, c.name, got, c.want)
		}
	}
}
