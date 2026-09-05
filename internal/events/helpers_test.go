package events_test

import (
	"os"
	"path/filepath"
	"testing"
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
