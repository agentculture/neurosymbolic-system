package mgmt_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
	"github.com/agentculture/neurosymbolic-system/internal/mgmt"
)

// smallBenchArgs mirrors t15's acceptance test config: small enough to run
// in well under a second, exercising the same synthetic-load path a full
// 200-rule/20-field/10,000-tick run does.
//
// Its 5ms period is real-clock and shared with whatever else is running on
// the box. Under `go test ./...` (every package's tests in parallel) it CAN
// and DOES overrun occasionally under contention — that is not a bug in
// bench, it is what a real-clock benchmark means. Every test below built on
// this config therefore asserts REPORT CONSISTENCY (all keys present, ok ==
// f(overruns, rss), exit code matches ok), never a specific pass/fail
// outcome. TestVerbBenchTinyBudgetFails is the one test in this file allowed
// to assert a specific overrun outcome, because a 1ns budget cannot be met
// regardless of what else is running.
var smallBenchArgs = []string{
	"--rules", "20", "--fields", "5", "--ticks", "200", "--period", "5ms",
}

// decodeBenchReport extracts the bench report's fields as a generic map,
// from wherever this Response actually put them: JSON stdout on success,
// the CliError's plain-text-table Message on failure (bench's failure
// message IS the table — see verbBench), and the plain-text table itself in
// both text-mode cases. Every value is normalized to float64/bool/string so
// callers can compare shapes without caring which of those four renderings
// they got.
func decodeBenchReport(t *testing.T, resp mgmt.Response) map[string]any {
	t.Helper()
	if resp.Stdout != "" {
		if report, ok := tryDecodeJSONObject(resp.Stdout); ok {
			return report
		}
		return parseBenchTable(t, resp.Stdout)
	}
	if resp.Stderr == "" {
		t.Fatal("Response carries neither Stdout nor Stderr")
	}
	var cliErr clifmt.CliError
	if err := json.Unmarshal([]byte(resp.Stderr), &cliErr); err == nil && cliErr.Message != "" {
		return parseBenchTable(t, cliErr.Message)
	}
	// Text-mode error: "error: <table>\nhint: <remediation>\n".
	firstLine := strings.SplitN(resp.Stderr, "\n", 2)[0]
	firstLine = strings.TrimPrefix(firstLine, "error: ")
	return parseBenchTable(t, firstLine)
}

func tryDecodeJSONObject(s string) (map[string]any, bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, false
	}
	return m, true
}

// parseBenchTable parses bench.Result.Table()'s "bench: key=value key=value
// ..." rendering into the same key set json.Marshal(Result) would produce:
// numeric strings become float64, "true"/"false" become bool, anything else
// (just "period", e.g. "5ms") stays a string.
func parseBenchTable(t *testing.T, s string) map[string]any {
	t.Helper()
	s = strings.TrimPrefix(strings.TrimSpace(s), "bench:")
	out := map[string]any{}
	for _, tok := range strings.Fields(s) {
		key, value, ok := strings.Cut(tok, "=")
		if !ok {
			continue
		}
		switch value {
		case "true":
			out[key] = true
		case "false":
			out[key] = false
		default:
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				out[key] = f
			} else {
				out[key] = value
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("parseBenchTable found no key=value pairs in %q", s)
	}
	return out
}

// assertBenchReportConsistent is the report-shape contract every bench
// invocation must satisfy REGARDLESS of whether this particular run's
// timing happened to pass or fail: every key present, ok is exactly the
// function of overruns/rss_mb/rss_ceiling_mb that bench.Result.OK computes,
// and the process exit code agrees with ok.
func assertBenchReportConsistent(t *testing.T, resp mgmt.Response) map[string]any {
	t.Helper()
	report := decodeBenchReport(t, resp)
	for _, key := range []string{
		"ticks", "period", "p50_us", "p99_us", "max_us", "overruns", "rss_mb", "rss_ceiling_mb", "ok",
	} {
		if _, present := report[key]; !present {
			t.Fatalf("report is missing key %q: %v", key, report)
		}
	}
	overruns, _ := report["overruns"].(float64)
	rssMB, _ := report["rss_mb"].(float64)
	rssCeilingMB, _ := report["rss_ceiling_mb"].(float64)
	ok, _ := report["ok"].(bool)

	wantOK := overruns == 0 && rssMB <= rssCeilingMB
	if ok != wantOK {
		t.Errorf("ok = %v, want %v (overruns=%v rss_mb=%v rss_ceiling_mb=%v): %v",
			ok, wantOK, overruns, rssMB, rssCeilingMB, report)
	}
	wantCode := clifmt.ExitUser
	if ok {
		wantCode = clifmt.ExitSuccess
	}
	if resp.Code != wantCode {
		t.Errorf("Code = %d, want %d (ok=%v): stdout=%q stderr=%q", resp.Code, wantCode, ok, resp.Stdout, resp.Stderr)
	}
	// Stdout and Stderr are never both non-empty for one Response (mgmt's own
	// contract — see TestHandleNeverMixesStdoutAndStderr).
	if resp.Stdout != "" && resp.Stderr != "" {
		t.Errorf("both Stdout and Stderr are non-empty: stdout=%q stderr=%q", resp.Stdout, resp.Stderr)
	}
	return report
}

// TestVerbBenchTextReportIsConsistent exercises text mode. It does NOT
// assert exit 0 — see the smallBenchArgs doc comment for why a 5ms
// real-clock budget cannot be promised under `go test ./...`'s CPU
// contention.
func TestVerbBenchTextReportIsConsistent(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "bench", Args: smallBenchArgs})
	report := assertBenchReportConsistent(t, resp)
	if ticks, _ := report["ticks"].(float64); ticks != 200 {
		t.Errorf("ticks = %v, want 200", report["ticks"])
	}
	if period, _ := report["period"].(string); period != "5ms" {
		t.Errorf("period = %v, want \"5ms\"", report["period"])
	}
}

// TestVerbBenchJSONReportIsConsistent exercises --json mode. Same
// consistency contract, not a pass/fail assumption.
func TestVerbBenchJSONReportIsConsistent(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "bench", Args: smallBenchArgs, JSON: true})
	report := assertBenchReportConsistent(t, resp)
	if ticks, _ := report["ticks"].(float64); ticks != 200 {
		t.Errorf("ticks = %v, want 200", report["ticks"])
	}
}

// TestVerbBenchTinyBudgetFails is acceptance criterion 1: a 1ns budget
// cannot be met by any tick no matter what else is running on the box, so
// this is the one test in this file allowed to assert Overruns > 0 and exit
// 1 unconditionally.
func TestVerbBenchTinyBudgetFails(t *testing.T) {
	h := testHandler()
	args := []string{"--rules", "20", "--fields", "5", "--ticks", "50", "--period", "1ns"}
	resp := h.Handle(mgmt.Request{Verb: "bench", Args: args})
	if resp.Code != clifmt.ExitUser {
		t.Fatalf("Code = %d, want %d (a tiny budget must overrun and exit non-zero); stdout=%q stderr=%q",
			resp.Code, clifmt.ExitUser, resp.Stdout, resp.Stderr)
	}
	if resp.Stdout != "" {
		t.Fatalf("Stdout = %q, want empty on failure", resp.Stdout)
	}
	report := assertBenchReportConsistent(t, resp)
	if overruns, _ := report["overruns"].(float64); overruns == 0 {
		t.Errorf("overruns = 0, want > 0 for a 1ns budget: %v", report)
	}
}

func TestVerbBenchUnknownFlagRefused(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "bench", Args: []string{"--bogus", "1"}})
	if resp.Code != clifmt.ExitUser {
		t.Fatalf("Code = %d, want %d", resp.Code, clifmt.ExitUser)
	}
}

func TestVerbBenchBadIntRefused(t *testing.T) {
	h := testHandler()
	resp := h.Handle(mgmt.Request{Verb: "bench", Args: []string{"--rules", "not-a-number"}})
	if resp.Code != clifmt.ExitUser {
		t.Fatalf("Code = %d, want %d", resp.Code, clifmt.ExitUser)
	}
}

// TestVerbBenchRSSCeilingFlagParses checks --rss-ceiling actually reaches
// the report, regardless of whether this run's timing passed or failed.
func TestVerbBenchRSSCeilingFlagParses(t *testing.T) {
	h := testHandler()
	args := append(append([]string{}, smallBenchArgs...), "--rss-ceiling", "32MB")
	resp := h.Handle(mgmt.Request{Verb: "bench", Args: args})
	report := assertBenchReportConsistent(t, resp)
	if ceiling, _ := report["rss_ceiling_mb"].(float64); ceiling != 32 {
		t.Errorf("rss_ceiling_mb = %v, want 32", report["rss_ceiling_mb"])
	}
}
