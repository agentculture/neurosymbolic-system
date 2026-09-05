package allowlist

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// goModPath finds the go.mod file by walking up from the current directory.
func goModPath(t *testing.T) string {
	t.Helper()

	// Get the current working directory.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}

	// Walk up until we find go.mod.
	for {
		candidate := filepath.Join(cwd, "go.mod")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}

		parent := filepath.Dir(cwd)
		if parent == cwd {
			t.Fatal("go.mod not found in any parent directory")
		}
		cwd = parent
	}
}

// moduleRoot returns the directory containing the go.mod file.
func moduleRoot(t *testing.T) string {
	t.Helper()
	return filepath.Dir(goModPath(t))
}

// parseAllowlist extracts allowed modules from go.mod.
// A require line whose trailing comment starts with "// allow:" is allowed.
// The comment text (minus the "// allow: " prefix) is returned as documentation.
func parseAllowlist(t *testing.T, goModText string) map[string]string {
	t.Helper()

	allowlist := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(goModText))
	inRequireBlock := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Track require block boundaries.
		if strings.HasPrefix(line, "require (") || line == "require (" {
			inRequireBlock = true
			continue
		}
		if line == ")" && inRequireBlock {
			inRequireBlock = false
			continue
		}

		// Skip lines that don't look like require statements.
		if !inRequireBlock && !strings.HasPrefix(line, "require ") {
			continue
		}

		// Skip empty lines and closing parens.
		if line == "" || line == ")" {
			continue
		}

		// Remove the "require " prefix if it's a single-line require.
		if strings.HasPrefix(line, "require ") {
			line = strings.TrimPrefix(line, "require ")
		}

		// Extract module name and comment.
		parts := strings.SplitN(line, "//", 2)
		if len(parts) != 2 {
			continue // No comment.
		}

		comment := strings.TrimSpace(parts[1])
		if !strings.HasPrefix(comment, "allow:") {
			continue // Not an allow comment.
		}

		// Extract module name from the line.
		// Format: "module/path version"
		requirePart := strings.TrimSpace(parts[0])
		fields := strings.Fields(requirePart)
		if len(fields) < 1 {
			continue
		}

		modulePath := fields[0]
		reason := strings.TrimSpace(strings.TrimPrefix(comment, "allow:"))
		allowlist[modulePath] = reason
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}

	return allowlist
}

// denylist is the set of standard packages that are forbidden.
var denylist = map[string]bool{
	"os/exec":         true,
	"plugin":          true,
	"database/sql":    true,
	"net/rpc":         true,
	"net/rpc/jsonrpc": true,
	"syscall/js":      true,
	"runtime/cgo":     true,
}

// denyFamilies are top-level families that are entirely forbidden.
var denyFamilies = map[string]bool{
	"image": true,
	"debug": true,
}

// allowedFamilies are top-level families known to be safe.
var allowedFamilies = map[string]bool{
	"bufio":     true,
	"bytes":     true,
	"cmp":       true,
	"context":   true,
	"encoding":  true,
	"errors":    true,
	"flag":      true,
	"fmt":       true,
	"io":        true,
	"iter":      true,
	"log":       true,
	"maps":      true,
	"math":      true,
	"net":       true,
	"os":        true,
	"path":      true,
	"reflect":   true,
	"regexp":    true,
	"runtime":   true,
	"slices":    true,
	"sort":      true,
	"strconv":   true,
	"strings":   true,
	"sync":      true,
	"syscall":   true,
	"text":      true,
	"time":      true,
	"unicode":   true,
	"unique":    true,
	"weak":      true,
	"unsafe":    true,
	"internal":  true,
	"vendor":    true,
	"embed":     true,
	"hash":      true,
	"crypto":    true,
	"container": true,
	"go":        true,
}

// TestImportAllowlist validates that the engine's dependency graph
// contains no forbidden packages and that all third-party packages are allowlisted.
func TestImportAllowlist(t *testing.T) {
	// Read go.mod.
	goModFile := goModPath(t)
	goModText, err := os.ReadFile(goModFile)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", goModFile, err)
	}

	allowlist := parseAllowlist(t, string(goModText))

	// Run go list -deps, WITH CGO_ENABLED=0 — the way this binary is actually
	// built, in CI and on a bare box alike. That is not a detail: with cgo on,
	// `net` (which the stream endpoint needs, and which entered the cmd graph
	// when `version` started reporting the wire protocol version) drags in
	// runtime/cgo, which this test denies precisely because a cgo-linked engine
	// is not the statically linked artifact a robot gets deployed. Listing the
	// graph the way the graph is built is what makes the denial mean what it
	// says.
	root := moduleRoot(t)
	cmd := exec.Command(
		"go",
		"list",
		"-deps",
		"-f", "{{.ImportPath}}|{{.Standard}}|{{if .Module}}{{.Module.Path}}{{end}}",
		"./cmd/neurosymbolic-engine",
	)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list -deps: %v\nstdout:\n%s", err, stdout.String())
	}

	// Parse output and validate each package.
	scanner := bufio.NewScanner(&stdout)
	unknownFamilies := make(map[string]bool)

	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			t.Fatalf("unexpected go list output format: %q", line)
		}

		importPath := parts[0]
		isStandard := parts[1] == "true"
		modulePath := parts[2]

		// Check standard packages.
		if isStandard {
			// Check explicit denylist.
			if denylist[importPath] {
				t.Errorf("forbidden standard package in graph: %s", importPath)
			}

			// Check family denylist.
			family := strings.Split(importPath, "/")[0]
			if denyFamilies[family] {
				t.Errorf("forbidden standard package family in graph: %s (family %s)", importPath, family)
			}

			// Report unknown families (don't fail on them).
			if !allowedFamilies[family] {
				unknownFamilies[family] = true
			}

			continue
		}

		// Non-standard packages must be in this module or allowlisted.
		if modulePath == "github.com/agentculture/neurosymbolic-system" {
			continue
		}

		if _, allowed := allowlist[modulePath]; !allowed {
			t.Errorf(
				"unapproved third-party package in graph: %s\n"+
					"hint: add `// allow: <argument>` next to the require line in go.mod or remove the dependency",
				modulePath,
			)
		}
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}

	// Report unknown families.
	if len(unknownFamilies) > 0 {
		var families []string
		for f := range unknownFamilies {
			families = append(families, f)
		}
		t.Logf("new standard package families in graph: %v", families)
	}
}

// TestParseAllowlistWithoutComment rejects requires without allow comments.
func TestParseAllowlistWithoutComment(t *testing.T) {
	goModText := `module test

require (
	example.com/pkg v1.2.3
)
`

	allowlist := parseAllowlist(t, goModText)
	if len(allowlist) > 0 {
		t.Errorf("expected empty allowlist, got %v", allowlist)
	}
}

// TestParseAllowlistWithComment accepts requires with allow comments.
func TestParseAllowlistWithComment(t *testing.T) {
	goModText := `module test

require (
	example.com/pkg v1.2.3 // allow: needed for feature X
	other.com/lib v0.1.0 // allow: temporal compatibility
)
`

	allowlist := parseAllowlist(t, goModText)

	wantCount := 2
	if len(allowlist) != wantCount {
		t.Fatalf("expected %d entries in allowlist, got %d: %v", wantCount, len(allowlist), allowlist)
	}

	if reason, ok := allowlist["example.com/pkg"]; !ok {
		t.Error("example.com/pkg not in allowlist")
	} else if reason != "needed for feature X" {
		t.Errorf("example.com/pkg reason = %q, want %q", reason, "needed for feature X")
	}

	if reason, ok := allowlist["other.com/lib"]; !ok {
		t.Error("other.com/lib not in allowlist")
	} else if reason != "temporal compatibility" {
		t.Errorf("other.com/lib reason = %q, want %q", reason, "temporal compatibility")
	}
}

// TestDenylistContainsExpectedPackages verifies the denylist is complete.
func TestDenylistContainsExpectedPackages(t *testing.T) {
	expected := []string{
		"os/exec",
		"plugin",
		"database/sql",
		"net/rpc",
		"net/rpc/jsonrpc",
		"syscall/js",
		"runtime/cgo",
	}

	for _, pkg := range expected {
		if !denylist[pkg] {
			t.Errorf("denylist missing %s", pkg)
		}
	}
}

// TestDenyFamiliesContainsExpectedFamilies verifies the deny families are complete.
func TestDenyFamiliesContainsExpectedFamilies(t *testing.T) {
	expected := []string{
		"image",
		"debug",
	}

	for _, family := range expected {
		if !denyFamilies[family] {
			t.Errorf("denyFamilies missing %s", family)
		}
	}
}

// TestRejectRPCInGraph demonstrates how the test fails on a denied package.
// This is a negative test: it fails as expected when rpc is in the graph.
// We skip it by default since the real engine doesn't import rpc.
func TestRejectRPCInGraph(t *testing.T) {
	// This test is for documentation; it would fail if net/rpc were in the graph.
	// If you see this test skipped, that's correct — net/rpc is not supposed to
	// be a dependency of the engine.
	t.Skip("negative test — only run when debugging the allowlist checker")

	denylist := map[string]bool{
		"net/rpc": true,
	}

	packages := []string{"net/rpc", "os"}
	failed := false
	for _, pkg := range packages {
		if denylist[pkg] {
			t.Logf("forbidden package would be rejected: %s", pkg)
			failed = true
		}
	}

	if !failed {
		t.Error("expected net/rpc to be rejected")
	}
}
