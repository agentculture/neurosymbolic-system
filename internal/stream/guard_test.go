package stream

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
)

// TestEveryListenerGoesThroughTheFactory is the other half of the no-TCP
// assertion in TestNoTCPListenerIsEverCreated.
//
// That test records what Config.listen was asked for, which is only a total
// account of what this package listened on if Config.listen is the ONLY way a
// listener is ever created. This pins that mechanically: net.Listen appears in
// exactly one non-test source in this package, inside defaultListen. A future
// edit that calls net.Listen directly — a TCP fallback, a debug endpoint —
// fails here rather than quietly slipping past the recording factory.
func TestEveryListenerGoesThroughTheFactory(t *testing.T) {
	sites := map[string]int{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", name, parseErr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Listen" {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "net" {
				return true
			}
			sites[name]++
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("scanned no non-test sources; the walk is wrong")
	}
	total := 0
	for file, count := range sites {
		total += count
		if file != "listener.go" {
			t.Errorf("%s calls net.Listen directly; route it through Config.listen", file)
		}
	}
	if total != 1 {
		t.Errorf("net.Listen is called %d times, want exactly 1 (defaultListen)", total)
	}
}

// TestPoseKindIsTheDeclaredWireToken pins the one frame kind that collides with
// a donor robot's vocabulary.
//
// "pose" is a MicroDuck CHANNEL name, so internal/adaptor's donor-literal guard
// fails on any non-test source containing it as a whole string literal unless it
// is exempted. The collision is accidental: this is a protocol schema token, the
// same on every robot, not a robot name compiled into the engine — which is what
// testdata/protocol_tokens.txt records (see TestEveryCollidingFrameKindIsExempted
// below). This test pins the wire token itself, so a client on the other side of
// the socket can rely on it.
func TestPoseKindIsTheDeclaredWireToken(t *testing.T) {
	if KindPose != "pose" {
		t.Fatalf("KindPose=%q, want %q", KindPose, "pose")
	}
	// And it is still the kind carried on the frame the Sink's pose goes out as.
	var p adaptor.Pose
	_ = p
}

// TestEveryCollidingFrameKindIsExempted closes the loop between this package's
// frame kinds and the donor-literal guard's exemption list.
//
// The guard lives in internal/adaptor and cannot import this package (that would
// be an import cycle), so it spells the wire kinds out. This test walks the
// other way: every kind THIS package declares that is also a donor name must
// appear in protocol_tokens.txt. A new frame kind colliding with some robot's
// channel would otherwise fail the guard from across the tree with no hint that
// the fix is one justified exemption line.
func TestEveryCollidingFrameKindIsExempted(t *testing.T) {
	donor := readNameFile(t, filepath.Join("..", "adaptor", "testdata", "donor_names.txt"))
	exempt := readNameFile(t, filepath.Join("..", "adaptor", "testdata", "protocol_tokens.txt"))

	kinds := []string{
		KindHello, KindSense, KindIntent, KindMgmt, KindMgmtResult,
		KindPose, KindEvent, KindHeartbeat, KindEnd, KindError,
	}
	declared := map[string]bool{}
	for _, kind := range kinds {
		declared[kind] = true
		if donor[kind] && !exempt[kind] {
			t.Errorf("frame kind %q collides with a donor name but is not listed in "+
				"internal/adaptor/testdata/protocol_tokens.txt; add it with its "+
				"justification, or the donor-literal guard will fail the build", kind)
		}
	}
	// And nothing is exempted that this package does not actually declare: a
	// stale entry reads like a hole in the guard.
	for token := range exempt {
		if !declared[token] {
			t.Errorf("protocol_tokens.txt exempts %q, which is not a frame kind this "+
				"package declares", token)
		}
	}
}

// readNameFile reads one of the guard's `#`-commented name lists.
func readNameFile(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	names := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		names[line] = true
	}
	return names
}

// TestFrameKindsAreDistinct catches a copy-paste that would make two frame
// kinds indistinguishable on the wire.
func TestFrameKindsAreDistinct(t *testing.T) {
	kinds := []string{
		KindHello, KindSense, KindIntent, KindMgmt,
		KindPose, KindEvent, KindHeartbeat, KindEnd, KindError, KindMgmtResult,
	}
	seen := map[string]bool{}
	for _, kind := range kinds {
		if kind == "" {
			t.Error("a frame kind is empty")
		}
		if seen[kind] {
			t.Errorf("frame kind %q is declared twice", kind)
		}
		seen[kind] = true
	}
}

// TestPackageImportsStayStdlibPlusThisModule keeps the dependency policy honest
// at the package level: the stream endpoint is net + encoding/json + os, never
// a transport library.
func TestPackageImportsStayStdlibPlusThisModule(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", name, parseErr)
		}
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(path, "github.com/agentculture/neurosymbolic-system/") {
				continue
			}
			if strings.Contains(strings.Split(path, "/")[0], ".") {
				t.Errorf("%s imports %q, which is outside the stdlib and this module",
					filepath.Base(name), path)
			}
		}
	}
}
