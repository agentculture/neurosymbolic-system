package compose

import (
	"errors"
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
)

// providerBase is a minimally valid provider config; each case below appends
// exactly one knob to it, so the refusal under test is the only variable.
const providerBaseTOML = `name = "m"
kind = "completion"
base_url = "http://127.0.0.1:1"
inputs = ["tag"]
output = "mood"
`

func providerJSONWith(extra string) string {
	return `{"name":"m","kind":"completion","base_url":"http://127.0.0.1:1",` +
		`"inputs":["tag"],"output":"mood"` + extra + `}`
}

// An explicitly written non-positive or non-finite bound is REFUSED, not
// quietly swapped for the default. Silently defaulting makes a knob an
// operator can see in their own file and cannot affect — the same failure
// shape as an unknown key, which this loader already refuses.
func TestProviderNonPositiveBoundsAreRefused(t *testing.T) {
	cases := []struct {
		name  string
		file  string
		body  string
		field string
	}{
		{"timeout_s zero", "p.toml", providerBaseTOML + "timeout_s = 0.0\n", "timeout_s"},
		{"timeout_s negative", "p.toml", providerBaseTOML + "timeout_s = -0.5\n", "timeout_s"},
		{"timeout_s nan", "p.toml", providerBaseTOML + "timeout_s = nan\n", "timeout_s"},
		{"timeout_s inf", "p.toml", providerBaseTOML + "timeout_s = inf\n", "timeout_s"},
		{"queue_depth zero", "p.toml", providerBaseTOML + "queue_depth = 0\n", "queue_depth"},
		{"queue_depth negative", "p.toml", providerBaseTOML + "queue_depth = -2\n", "queue_depth"},
		{"cadence zero", "p.toml", providerBaseTOML + "cadence = 0\n", "cadence"},
		{"cadence negative", "p.toml", providerBaseTOML + "cadence = -1\n", "cadence"},
		{"json timeout_s zero", "p.json", providerJSONWith(`,"timeout_s":0`), "timeout_s"},
		{"json queue_depth zero", "p.json", providerJSONWith(`,"queue_depth":0`), "queue_depth"},
		{"json cadence negative", "p.json", providerJSONWith(`,"cadence":-3`), "cadence"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeProviderConfig(t, tc.file, tc.body)
			_, err := loadProviderConfig(path)
			if err == nil {
				t.Fatal("want a refusal, got none")
			}
			var cliErr *clifmt.CliError
			if !errors.As(err, &cliErr) {
				t.Fatalf("err = %T, want *clifmt.CliError", err)
			}
			if !strings.Contains(cliErr.Message, tc.field) {
				t.Errorf("Message = %q, want it to name %q", cliErr.Message, tc.field)
			}
			if !strings.Contains(cliErr.Message, path) {
				t.Errorf("Message = %q, want it to name the file %q", cliErr.Message, path)
			}
			if cliErr.Remediation == "" {
				t.Error("Remediation is empty")
			}
		})
	}
}

// OMITTED is not the same as present-and-zero: an omitted knob is left zero
// for provider.Config.validate to fill in, which is the one place a default
// is decided.
func TestProviderOmittedBoundsStillTakeTheDefault(t *testing.T) {
	for _, tc := range []struct{ name, file, body string }{
		{"toml", "p.toml", providerBaseTOML},
		{"json", "p.json", providerJSONWith("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadProviderConfig(writeProviderConfig(t, tc.file, tc.body))
			if err != nil {
				t.Fatalf("loadProviderConfig: %v", err)
			}
			if cfg.Timeout != 0 || cfg.QueueDepth != 0 || cfg.Cadence != 0 {
				t.Fatalf("cfg = %+v, want the omitted knobs left zero", cfg)
			}
		})
	}
}

func TestProviderPositiveBoundsAreCarriedThrough(t *testing.T) {
	cfg, err := loadProviderConfig(writeProviderConfig(t, "p.toml",
		providerBaseTOML+"timeout_s = 0.25\nqueue_depth = 3\ncadence = 5\n"))
	if err != nil {
		t.Fatalf("loadProviderConfig: %v", err)
	}
	if cfg.Timeout.Milliseconds() != 250 || cfg.QueueDepth != 3 || cfg.Cadence != 5 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

// A JSON decoder that stops at the first value silently accepts a second
// document, or trailing garbage, as if it were not there — so an operator
// editing the wrong half of a two-document file would see no effect and no
// error. The loader must be at EOF after the one document it decoded.
func TestProviderJSONWithTrailingDataIsRefused(t *testing.T) {
	cases := map[string]string{
		"second document":  providerJSONWith("") + "\n" + providerJSONWith(`,"cadence":9`),
		"trailing garbage": providerJSONWith("") + "\nthis is not JSON\n",
		"trailing array":   providerJSONWith("") + " []",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeProviderConfig(t, "p.json", body)
			_, err := loadProviderConfig(path)
			if err == nil {
				t.Fatal("want a refusal, got none")
			}
			var cliErr *clifmt.CliError
			if !errors.As(err, &cliErr) {
				t.Fatalf("err = %T, want *clifmt.CliError", err)
			}
			if !strings.Contains(cliErr.Message, path) {
				t.Errorf("Message = %q, want it to name the file", cliErr.Message)
			}
			if !strings.Contains(cliErr.Message, "trailing") {
				t.Errorf("Message = %q, want it to name the trailing content", cliErr.Message)
			}
		})
	}
}
