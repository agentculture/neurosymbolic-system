package adaptor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
)

// minimalConfig is the smallest adaptor config that validates: one channel,
// one sense, one action. Each case below appends something AFTER it.
const minimalConfig = `{
  "channels": [{"name": "arm", "arity": 1, "neutral": [0.0]}],
  "senses": [{"name": "tag", "type": "string"}],
  "actions": [{"name": "wave", "claims": ["arm"],
    "trajectories": {"arm": {"keyframes": [{"t": 0.0, "value": [0.0]}]}}}]
}`

// A json.Decoder stops at the end of the first value, so a second document —
// or any trailing garbage — decodes as if it were not there. An operator
// editing the second half of such a file would see no effect and no error,
// which is the same silent-no-op failure DisallowUnknownFields already exists
// to prevent. The decoder must be at EOF after the one document it read.
func TestParseRefusesTrailingData(t *testing.T) {
	cases := map[string]string{
		"second document":  minimalConfig + "\n" + minimalConfig,
		"trailing garbage": minimalConfig + "\nnot json at all\n",
		"trailing array":   minimalConfig + " []",
		"trailing scalar":  minimalConfig + " 7",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := adaptor.Parse([]byte(body))
			if err == nil {
				t.Fatal("Parse succeeded, want a refusal naming the trailing content")
			}
			if !strings.Contains(err.Error(), "trailing") {
				t.Errorf("error = %q, want it to name the trailing content", err)
			}
		})
	}
}

func TestLoadJSONRefusesTrailingDataNamingTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adaptor.json")
	if err := os.WriteFile(path, []byte(minimalConfig+"\n"+minimalConfig), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := adaptor.LoadJSON(path)
	if err == nil {
		t.Fatal("LoadJSON succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error = %q, want it to name the file %q", err, path)
	}
}

// Whitespace and a trailing newline are not trailing DATA.
func TestParseAcceptsTrailingWhitespace(t *testing.T) {
	if _, err := adaptor.Parse([]byte(minimalConfig + "\n\n  \n")); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}
