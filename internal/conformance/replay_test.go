package conformance_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/conformance"
	"github.com/agentculture/neurosymbolic-system/internal/ruleeval"
)

// -update regenerates every case's expected.jsonl.
//
// A REGENERATED TRACE IS NOT EVIDENCE. It records what the engine did, which is
// the thing under test; it becomes evidence only once a human has re-checked it
// against the donor test the case came from. Every case's check is written down
// in docs/verification/2026-09-05-donor-conformance.md, and that document is
// where a regenerated trace has to be re-argued before it is committed.
var update = flag.Bool("update", false, "regenerate every case's expected.jsonl")

// cases enumerates testdata/<donor>/<case>/ — every directory carrying a
// senses.jsonl. A new fixture directory is picked up with no code change, so
// nobody can add a case that silently is not run.
func cases(t *testing.T) []string {
	t.Helper()
	var out []string
	root := "testdata"
	donors, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading %s: %v", root, err)
	}
	for _, donor := range donors {
		if !donor.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, donor.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", donor.Name(), err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(root, donor.Name(), entry.Name())
			if _, err := os.Stat(filepath.Join(dir, conformance.SensesFile)); err != nil {
				continue
			}
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatal("no conformance cases found under testdata/")
	}
	return out
}

func caseName(dir string) string {
	return filepath.ToSlash(strings.TrimPrefix(filepath.ToSlash(dir), "testdata/"))
}

// TestDonorConformance replays every fixture and diffs it against the recorded
// trace, tick by tick.
func TestDonorConformance(t *testing.T) {
	for _, dir := range cases(t) {
		name := caseName(dir)
		t.Run(name, func(t *testing.T) {
			trace, err := conformance.Replay(dir)
			if err != nil {
				t.Fatalf("case %s: replay: %v", name, err)
			}
			got, err := conformance.MarshalTrace(trace.Frames)
			if err != nil {
				t.Fatalf("case %s: marshal: %v", name, err)
			}

			expectedPath := filepath.Join(dir, conformance.ExpectedFile)
			if *update {
				if err := os.WriteFile(expectedPath, []byte(got), 0o644); err != nil {
					t.Fatalf("case %s: writing %s: %v", name, expectedPath, err)
				}
				t.Logf("case %s: regenerated %s (%d ticks) — re-check it against the "+
					"donor test before committing", name, expectedPath, len(trace.Frames))
				return
			}

			want, err := conformance.ParseTrace(expectedPath)
			if err != nil {
				t.Fatalf("case %s: %v", name, err)
			}
			diffTrace(t, name, want, trace.Frames)

			// The pose the sink was handed and the pose the seam saw are the
			// same object; one settling neutral pose follows the last tick.
			if len(trace.Poses) != len(trace.Frames)+1 {
				t.Errorf("case %s: the sink received %d poses, want %d ticks plus one "+
					"settling pose", name, len(trace.Poses), len(trace.Frames))
			}
		})
	}
}

// diffTrace compares two traces line by line, naming the first tick and field
// that disagree — and the rule id whenever the disagreement is about an event.
func diffTrace(t *testing.T, name string, want, got []conformance.Frame) {
	t.Helper()
	for i := 0; i < len(want) && i < len(got); i++ {
		diffFrame(t, name, want[i], got[i])
		if t.Failed() {
			return
		}
	}
	if len(want) != len(got) {
		t.Fatalf("case %s: tick count: want %d, got %d", name, len(want), len(got))
	}
}

func diffFrame(t *testing.T, name string, want, got conformance.Frame) {
	t.Helper()
	if want.Tick != got.Tick {
		t.Fatalf("case %s: tick %d: tick: want %d, got %d",
			name, got.Tick, want.Tick, got.Tick)
	}
	tick := got.Tick

	wantLine, err := conformance.Marshal(want)
	if err != nil {
		t.Fatalf("case %s: tick %d: marshal expected: %v", name, tick, err)
	}
	gotLine, err := conformance.Marshal(got)
	if err != nil {
		t.Fatalf("case %s: tick %d: marshal recorded: %v", name, tick, err)
	}
	if wantLine == gotLine {
		return
	}

	if !sameJSON(want.Events, got.Events) {
		t.Fatalf("case %s: tick %d: events%s: want %s, got %s",
			name, tick, rulesNamed(want.Events, got.Events),
			mustJSON(t, want.Events), mustJSON(t, got.Events))
	}
	if !sameJSON(want.Ownership, got.Ownership) {
		t.Fatalf("case %s: tick %d: ownership: want %s, got %s",
			name, tick, mustJSON(t, want.Ownership), mustJSON(t, got.Ownership))
	}
	if !sameJSON(want.PoseChannels, got.PoseChannels) {
		t.Fatalf("case %s: tick %d: pose_channels: want %s, got %s",
			name, tick, mustJSON(t, want.PoseChannels), mustJSON(t, got.PoseChannels))
	}
	t.Fatalf("case %s: tick %d: frame: want %s, got %s", name, tick, wantLine, gotLine)
}

// rulesNamed is the " (rule ...)" suffix a mismatched-events failure carries.
// An operator overrides, tunes and tombstones a rule BY ID, so a conformance
// failure that does not name the id makes them go looking for it.
func rulesNamed(want, got []conformance.EventFrame) string {
	seen := map[string]bool{}
	var ids []string
	for _, frame := range append(append([]conformance.EventFrame{}, want...), got...) {
		id, _ := frame.Data["rule"].(string)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	return " (rule " + strings.Join(ids, ", ") + ")"
}

func sameJSON(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(left) == string(right)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// TestEveryTransitionIsExactlyOneFrame is acceptance criterion 3's structural
// half: a decision appears ONCE, and a held suppression is an EPISODE rather
// than a frame per tick.
//
// The flood this guards against is measured, not hypothetical: the donor's #99
// emitted a drop line every tick a gated predicate held, writing 6722 lines
// into a 3 h journal against 42 genuine fires. An export feed following the
// event stream would drown the same way, so the events follow the same
// transition cadence the log does.
func TestEveryTransitionIsExactlyOneFrame(t *testing.T) {
	for _, dir := range cases(t) {
		name := caseName(dir)
		t.Run(name, func(t *testing.T) {
			frames, err := conformance.ParseTrace(
				filepath.Join(dir, conformance.ExpectedFile))
			if err != nil {
				t.Fatalf("case %s: %v", name, err)
			}
			open := map[string]string{} // rule -> the reason its episode is under
			for _, frame := range frames {
				seen := map[string]bool{}
				for _, event := range frame.Events {
					key := eventKey(event)
					if seen[key] {
						t.Errorf("case %s: tick %d: %s appears twice in one tick",
							name, frame.Tick, key)
					}
					seen[key] = true
				}
				checkEpisodes(t, name, frame, open)
			}
		})
	}
}

// checkEpisodes fails a suppression frame that merely repeats the reason its
// rule is already suppressed under.
func checkEpisodes(t *testing.T, name string, frame conformance.Frame, open map[string]string) {
	t.Helper()
	for _, event := range frame.Events {
		rule, _ := event.Data["rule"].(string)
		reason, _ := event.Data["reason"].(string)
		switch event.Name {
		case ruleeval.EventSuppress:
			if _, isSummary := event.Data["ticks"]; isSummary {
				delete(open, rule)
				continue
			}
			if open[rule] == reason {
				t.Errorf("case %s: tick %d: rule %s: a second %q suppression frame "+
					"inside one episode", name, frame.Tick, rule, reason)
			}
			open[rule] = reason
		case ruleeval.EventFire:
			delete(open, rule)
		}
	}
}

func eventKey(event conformance.EventFrame) string {
	parts := []string{event.Name}
	for _, field := range []string{"rule", "kind", "name", "reason", "id"} {
		if value, ok := event.Data[field].(string); ok && value != "" {
			parts = append(parts, field+"="+value)
		}
	}
	return strings.Join(parts, " ")
}

// TestFixturesUseDonorRuleFilesVerbatim keeps the copies of the two donors'
// shipped rule files identical to the copies internal/rules already pins.
//
// The rule ids in those files are a PUBLIC INTERFACE — an operator overrides or
// tombstones a shipped rule by id — so a divergence between the loader's copy
// and the conformance copy would mean the two packages were conforming to
// different robots.
func TestFixturesUseDonorRuleFilesVerbatim(t *testing.T) {
	for _, pair := range []struct{ here, there string }{
		{
			here:  filepath.Join("testdata", "reachy", "default_rules.v1.toml"),
			there: filepath.Join("..", "rules", "testdata", "reachy", "default_rules.v1.toml"),
		},
		{
			here:  filepath.Join("testdata", "microduck", "default_rules.toml"),
			there: filepath.Join("..", "rules", "testdata", "microduck", "default_rules.toml"),
		},
	} {
		here, err := os.ReadFile(pair.here)
		if err != nil {
			t.Fatalf("reading %s: %v", pair.here, err)
		}
		there, err := os.ReadFile(pair.there)
		if err != nil {
			t.Fatalf("reading %s: %v", pair.there, err)
		}
		if string(here) != string(there) {
			t.Errorf("%s differs from %s — re-sync the copy rather than editing one",
				pair.here, pair.there)
		}
	}
}

// TestSenseLogIsTheOnlyDiagnosticSurface asserts the in-process half of the
// observability contract: a replay that fires anything writes SENSE-grammar
// lines, and every line it writes is one.
func TestSenseLogIsTheOnlyDiagnosticSurface(t *testing.T) {
	for _, dir := range cases(t) {
		name := caseName(dir)
		t.Run(name, func(t *testing.T) {
			trace, err := conformance.Replay(dir)
			if err != nil {
				t.Fatalf("case %s: replay: %v", name, err)
			}
			for i, line := range strings.Split(strings.TrimRight(trace.SenseLog, "\n"), "\n") {
				if line == "" {
					continue
				}
				if !strings.HasPrefix(line, "[SENSE stage=") {
					t.Fatalf("case %s: senselog line %d is not SENSE grammar: %q",
						name, i+1, line)
				}
			}
		})
	}
}
