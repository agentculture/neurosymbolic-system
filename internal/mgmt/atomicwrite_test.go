package mgmt

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplaceFileAtomicallyReplacesOnSuccess(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "rules.v2.toml")
	if err := os.WriteFile(dest, []byte("previous"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := replaceFileAtomically(dest, []byte("next"), func(string) error { return nil }); err != nil {
		t.Fatalf("replaceFileAtomically: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "next" {
		t.Fatalf("dest = %q, want %q", got, "next")
	}
	leftovers := tempLeftovers(t, dir)
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

// The regression this function exists for: a failed verification must leave a
// PRE-EXISTING destination byte-identical. The naive shape (write dest, load
// dest, delete dest on failure) destroyed a file this process did not create.
func TestReplaceFileAtomicallyLeavesExistingDestinationUntouchedOnVerifyFailure(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "rules.v2.toml")
	const previous = "schema_version = 2\n# the operator's own previous migration\n"
	if err := os.WriteFile(dest, []byte(previous), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	refusal := errors.New("nope")
	err := replaceFileAtomically(dest, []byte("candidate"), func(string) error { return refusal })
	if err == nil {
		t.Fatal("replaceFileAtomically succeeded, want the verify refusal")
	}
	var verifyErr *verificationError
	if !errors.As(err, &verifyErr) {
		t.Fatalf("error = %T (%v), want a *verificationError", err, err)
	}
	if !errors.Is(err, refusal) {
		t.Errorf("error does not unwrap to the verify refusal: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != previous {
		t.Fatalf("dest = %q, want it byte-identical to %q", got, previous)
	}
	if leftovers := tempLeftovers(t, dir); len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

// A verify failure must also not CREATE a destination that was not there.
func TestReplaceFileAtomicallyCreatesNothingOnVerifyFailure(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "rules.v2.toml")

	err := replaceFileAtomically(dest, []byte("candidate"), func(string) error {
		return errors.New("nope")
	})
	if err == nil {
		t.Fatal("replaceFileAtomically succeeded, want the verify refusal")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("os.Stat(dest) = %v, want the destination not to exist", statErr)
	}
}

// The candidate handed to verify is a DIFFERENT path than dest — verifying
// the destination itself is what forced the destructive shape in the first
// place.
func TestReplaceFileAtomicallyVerifiesATemporaryPath(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "rules.v2.toml")

	var seen string
	if err := replaceFileAtomically(dest, []byte("body"), func(tmpPath string) error {
		seen = tmpPath
		body, readErr := os.ReadFile(tmpPath) // #nosec G304 -- a path this test just made
		if readErr != nil {
			return readErr
		}
		if string(body) != "body" {
			t.Errorf("verify saw %q, want the candidate content", body)
		}
		return nil
	}); err != nil {
		t.Fatalf("replaceFileAtomically: %v", err)
	}
	if seen == dest {
		t.Fatal("verify was handed the destination itself, not a temporary path")
	}
	if filepath.Dir(seen) != dir {
		t.Fatalf("temp path %q is not in the destination's own directory %q", seen, dir)
	}
}

func tempLeftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			out = append(out, e.Name())
		}
	}
	return out
}
