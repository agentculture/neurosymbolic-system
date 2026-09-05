package mgmt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
	"github.com/agentculture/neurosymbolic-system/internal/rules"
)

// verbVersion reproduces exactly the "version" verb t1 shipped: one line,
// "neurosymbolic-engine <version> (<revision>)".
func verbVersion(h *Handler, _ []string) (verbResult, error) {
	text := fmt.Sprintf("neurosymbolic-engine %s (%s)", h.Version, h.Revision)
	return verbResult{
		Text: text,
		Value: map[string]string{
			"version":  h.Version,
			"revision": h.Revision,
		},
	}, nil
}

// verbWhoami prints "neurosymbolic-engine <version> (<revision>)" plus the
// module path, so an agent driving the binary can identify both the build
// and the source module it came from without a separate `go version -m`.
func verbWhoami(h *Handler, _ []string) (verbResult, error) {
	module := h.Module
	if module == "" {
		module = modulePath()
	}
	text := fmt.Sprintf("neurosymbolic-engine %s (%s)\nmodule: %s", h.Version, h.Revision, module)
	return verbResult{
		Text: text,
		Value: map[string]string{
			"version":  h.Version,
			"revision": h.Revision,
			"module":   module,
		},
	}, nil
}

// modulePath reads this binary's own module path from its embedded build
// info, so whoami never has to hard-code it.
func modulePath() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return info.Main.Path
}

// doctorReport is doctor's JSON shape and the source of its text rendering.
type doctorReport struct {
	Toolchain          string `json:"toolchain"`
	VendorPresent      bool   `json:"vendor_present"`
	RulesDefaultLoads  *bool  `json:"rules_default_loads,omitempty"`
	RulesDefaultDetail string `json:"rules_default_detail,omitempty"`
	Healthy            bool   `json:"healthy"`
}

// verbDoctor runs the environment checks doctor promises: the compiled-in Go
// toolchain version, whether this checkout vendors its dependencies (the
// runtime's offline-build requirement — see docs/go-dependencies.md), and,
// when --rules is given, whether that file actually loads through
// internal/rules with no vocabulary attached.
func verbDoctor(_ *Handler, args []string) (verbResult, error) {
	rulesPath, _, rest := extractStringFlag(args, "rules")
	if bad, found := rejectUnknownFlags(rest); found {
		return verbResult{}, clifmt.NewUserError(
			fmt.Sprintf("doctor does not recognize %q", bad), "run: doctor [--rules <path>]")
	}

	report := doctorReport{
		Toolchain:     runtime.Version(),
		VendorPresent: vendorPresent(),
		Healthy:       true,
	}
	var failures []string
	if !report.VendorPresent {
		failures = append(failures, "vendor_present: the vendor/ directory is missing "+
			"(go mod vendor) — a bare box builds offline only with it present")
	}

	if rulesPath != "" {
		_, err := rules.LoadFile(rulesPath, nil)
		ok := err == nil
		report.RulesDefaultLoads = &ok
		if err != nil {
			report.RulesDefaultDetail = err.Error()
			failures = append(failures, fmt.Sprintf("rules_default_loads: %s", err.Error()))
		}
	}

	if len(failures) > 0 {
		report.Healthy = false
		return verbResult{}, clifmt.NewEnvError(
			fmt.Sprintf("doctor found %d problem(s)", len(failures)),
			joinLines(failures),
		)
	}

	text := "neurosymbolic-engine doctor\n" +
		fmt.Sprintf("  toolchain: %s\n", report.Toolchain) +
		fmt.Sprintf("  vendor_present: %s\n", yesNo(report.VendorPresent))
	if report.RulesDefaultLoads != nil {
		text += fmt.Sprintf("  rules_default_loads: %s\n", yesNo(*report.RulesDefaultLoads))
	}
	text += "healthy"

	return verbResult{Text: text, Value: report}, nil
}

// verbStatus reports the live engine's state via an installed StatusSource.
// The one-off exec front never installs one — there is no live engine beside
// it — so this always answers noLiveEngine() there; a socket front (t8) that
// wires a real StatusSource gets a real report instead.
func verbStatus(h *Handler, _ []string) (verbResult, error) {
	if h.Status == nil {
		return verbResult{}, noLiveEngine()
	}
	status, err := h.Status.Status()
	if err != nil {
		return verbResult{}, asCliError(err, clifmt.ExitEnv, "")
	}
	return verbResult{Text: fmt.Sprintf("%v", status), Value: status}, nil
}

// vendorPresent reports whether a vendor/ directory sits beside the nearest
// go.mod found by walking up from the current working directory. This is a
// source-tree check (doctor is a development/CI tool, run from a checkout,
// not from wherever a built binary happens to be installed) — a build that
// embedded its own module cache would have no directory to point at here at
// all, so a missing go.mod simply reports "not present" rather than erroring.
func vendorPresent() bool {
	dir, err := os.Getwd()
	if err != nil {
		return false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			_, vendorErr := os.Stat(filepath.Join(dir, "vendor"))
			return vendorErr == nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func joinLines(lines []string) string {
	out := ""
	for i, line := range lines {
		if i > 0 {
			out += "; "
		}
		out += line
	}
	return out
}
