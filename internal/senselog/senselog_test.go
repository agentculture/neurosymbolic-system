package senselog

import (
	"bytes"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

// splitLines returns the non-empty lines written to buf.
func splitLines(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	out := buf.String()
	if out == "" {
		return nil
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return lines
}

func mustParseAll(t *testing.T, lines []string) []Line {
	t.Helper()
	parsed := make([]Line, 0, len(lines))
	for _, ln := range lines {
		l, err := Parse(ln)
		if err != nil {
			t.Fatalf("Parse(%q): %v", ln, err)
		}
		parsed = append(parsed, l)
	}
	return parsed
}

func TestStageLineGrammar(t *testing.T) {
	buf := &bytes.Buffer{}
	l := New(buf)
	l.Stage("vad", "speech", "evt-1", "utterance detected")

	lines := splitLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %q", len(lines), lines)
	}
	want := "[SENSE stage=vad source=speech event=evt-1] utterance detected"
	if lines[0] != want {
		t.Fatalf("line = %q, want %q", lines[0], want)
	}

	parsed := mustParseAll(t, lines)[0]
	if parsed.Stage != "vad" || parsed.Source != "speech" || parsed.Event != "evt-1" {
		t.Fatalf("parsed fields = %+v", parsed)
	}
	if parsed.Dropped {
		t.Fatalf("parsed.Dropped = true, want false")
	}
	if parsed.Detail != "utterance detected" {
		t.Fatalf("parsed.Detail = %q", parsed.Detail)
	}
}

func TestDropLineGrammar(t *testing.T) {
	buf := &bytes.Buffer{}
	l := New(buf)
	l.Drop("engagement", "speech", "evt-2", "self-mute", "")

	lines := splitLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %q", len(lines), lines)
	}
	want := "[SENSE stage=engagement source=speech event=evt-2] dropped reason=self-mute"
	if lines[0] != want {
		t.Fatalf("line = %q, want %q", lines[0], want)
	}

	parsed := mustParseAll(t, lines)[0]
	if !parsed.Dropped {
		t.Fatalf("parsed.Dropped = false, want true")
	}
	if parsed.Reason != "self-mute" {
		t.Fatalf("parsed.Reason = %q", parsed.Reason)
	}
	if parsed.Detail != "" {
		t.Fatalf("parsed.Detail = %q, want empty", parsed.Detail)
	}
}

func TestSanitizeStripsSpacesAndBrackets(t *testing.T) {
	buf := &bytes.Buffer{}
	l := New(buf)
	l.Stage("my stage", "my]source", "evt 3", "detail here")

	lines := splitLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("got %d lines: %q", len(lines), lines)
	}
	want := "[SENSE stage=my_stage source=my_source event=evt_3] detail here"
	if lines[0] != want {
		t.Fatalf("line = %q, want %q", lines[0], want)
	}
	if _, err := Parse(lines[0]); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

// TestGatedStreak200TicksOneReason is acceptance criterion 1: a gated
// streak of 200 ticks under a single reason emits exactly one entry line
// and one summary line naming the tick count.
func TestGatedStreak200TicksOneReason(t *testing.T) {
	buf := &bytes.Buffer{}
	l := New(buf)
	s := l.NewStreak("engagement", "speech", "evt-4")

	for tick := 1; tick <= 200; tick++ {
		s.Enter(tick, "cooldown")
	}
	s.End(201)

	lines := splitLines(t, buf)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (entry + summary): %q", len(lines), lines)
	}

	wantEntry := "[SENSE stage=engagement source=speech event=evt-4] dropped reason=cooldown"
	if lines[0] != wantEntry {
		t.Fatalf("entry line = %q, want %q", lines[0], wantEntry)
	}

	wantSummary := "[SENSE stage=engagement source=speech event=evt-4] dropped reason=cooldown suppressed 200 ticks"
	if lines[1] != wantSummary {
		t.Fatalf("summary line = %q, want %q", lines[1], wantSummary)
	}

	mustParseAll(t, lines)
}

// TestStreakReasonChangeMidStreak asserts one new line is emitted only when
// the reason changes mid-streak, and the summary names every reason seen in
// first-seen order.
func TestStreakReasonChangeMidStreak(t *testing.T) {
	buf := &bytes.Buffer{}
	l := New(buf)
	s := l.NewStreak("engagement", "speech", "evt-5")

	for tick := 1; tick <= 50; tick++ {
		s.Enter(tick, "cooldown")
	}
	for tick := 51; tick <= 80; tick++ {
		s.Enter(tick, "self-mute")
	}
	// Reason reverts; still a mid-streak change, so it logs again even
	// though "cooldown" was already seen once.
	for tick := 81; tick <= 100; tick++ {
		s.Enter(tick, "cooldown")
	}
	s.End(101)

	lines := splitLines(t, buf)
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4 (entry + 2 reason changes + summary): %q", len(lines), lines)
	}

	wantLines := []string{
		"[SENSE stage=engagement source=speech event=evt-5] dropped reason=cooldown",
		"[SENSE stage=engagement source=speech event=evt-5] dropped reason=self-mute",
		"[SENSE stage=engagement source=speech event=evt-5] dropped reason=cooldown",
		"[SENSE stage=engagement source=speech event=evt-5] dropped reason=cooldown,self-mute suppressed 100 ticks",
	}
	for i, want := range wantLines {
		if lines[i] != want {
			t.Fatalf("line[%d] = %q, want %q", i, lines[i], want)
		}
	}

	mustParseAll(t, lines)
}

// TestStreakSameReasonNoRepeat asserts a run of Enter calls under the SAME
// reason logs nothing beyond the entry line, however long the streak.
func TestStreakSameReasonNoRepeat(t *testing.T) {
	buf := &bytes.Buffer{}
	l := New(buf)
	s := l.NewStreak("gate", "touch", "evt-6")

	for tick := 1; tick <= 1000; tick++ {
		s.Enter(tick, "throttle")
	}

	lines := splitLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %q", len(lines), lines)
	}
}

// TestStreakEndWithNoOpenStreakIsNoOp: End on a fresh Streak (nothing
// entered) writes nothing.
func TestStreakEndWithNoOpenStreakIsNoOp(t *testing.T) {
	buf := &bytes.Buffer{}
	l := New(buf)
	s := l.NewStreak("gate", "touch", "evt-7")
	s.End(1)

	if buf.Len() != 0 {
		t.Fatalf("End on unopened streak wrote %q, want nothing", buf.String())
	}
}

// TestStreakStillOpenAtProcessStopEmitsNoSummary: a streak that never gets
// End'd (the process just stops) never writes a summary line — only its
// entry (and any reason-change) lines are the record.
func TestStreakStillOpenAtProcessStopEmitsNoSummary(t *testing.T) {
	buf := &bytes.Buffer{}
	l := New(buf)
	s := l.NewStreak("gate", "touch", "evt-8")

	for tick := 1; tick <= 5; tick++ {
		s.Enter(tick, "cooldown")
	}
	// No End call — simulates the process stopping mid-streak.

	lines := splitLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (entry only, no summary): %q", len(lines), lines)
	}
}

// TestFireEndsOpenStreakSummaryBeforeFireLine is acceptance criterion 3's
// Fire semantics: an open streak's summary line appears BEFORE the fire
// line, and the fire line uses the Stage grammar (no "dropped" token).
func TestFireEndsOpenStreakSummaryBeforeFireLine(t *testing.T) {
	buf := &bytes.Buffer{}
	l := New(buf)
	s := l.NewStreak("engagement", "speech", "evt-9")

	for tick := 1; tick <= 10; tick++ {
		s.Enter(tick, "cooldown")
	}
	s.Fire(11, "utterance detected")

	lines := splitLines(t, buf)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3 (entry + summary + fire): %q", len(lines), lines)
	}

	wantSummary := "[SENSE stage=engagement source=speech event=evt-9] dropped reason=cooldown suppressed 10 ticks"
	if lines[1] != wantSummary {
		t.Fatalf("summary line = %q, want %q", lines[1], wantSummary)
	}
	wantFire := "[SENSE stage=engagement source=speech event=evt-9] utterance detected"
	if lines[2] != wantFire {
		t.Fatalf("fire line = %q, want %q", lines[2], wantFire)
	}

	parsedFire, err := Parse(lines[2])
	if err != nil {
		t.Fatalf("Parse fire line: %v", err)
	}
	if parsedFire.Dropped {
		t.Fatalf("fire line parsed as dropped, want a plain stage line")
	}

	mustParseAll(t, lines)
}

// TestFireWithNoOpenStreakOnlyLogsFire: Fire with nothing gated logs just
// the fire line.
func TestFireWithNoOpenStreakOnlyLogsFire(t *testing.T) {
	buf := &bytes.Buffer{}
	l := New(buf)
	s := l.NewStreak("engagement", "speech", "evt-10")
	s.Fire(1, "utterance detected")

	lines := splitLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %q", len(lines), lines)
	}
}

// TestEveryDropSiteNamesItsReason walks a mixed sequence of many
// stage/streak/fire calls across several sources and events, and asserts
// every emitted line parses against the SENSE grammar and every dropped
// line names a non-empty reason (acceptance criterion 2).
func TestEveryDropSiteNamesItsReason(t *testing.T) {
	buf := &bytes.Buffer{}
	l := New(buf)

	speechStreak := l.NewStreak("engagement", "speech", "evt-a")
	visionStreak := l.NewStreak("gate", "vision", "evt-b")

	for tick := 1; tick <= 30; tick++ {
		speechStreak.Enter(tick, "cooldown")
	}
	for tick := 1; tick <= 15; tick++ {
		visionStreak.Enter(tick, "throttle")
	}
	for tick := 31; tick <= 45; tick++ {
		speechStreak.Enter(tick, "self-mute")
	}
	speechStreak.Fire(46, "utterance detected")
	visionStreak.End(16)
	l.Drop("inject", "touch", "evt-c", "gate-reject", "extra context")
	l.Stage("capture", "speech", "evt-d", "frame captured")

	lines := splitLines(t, buf)
	if len(lines) == 0 {
		t.Fatalf("expected at least one line")
	}
	parsed := mustParseAll(t, lines)
	for i, p := range parsed {
		if p.Stage == "" || p.Source == "" || p.Event == "" {
			t.Fatalf("line[%d] missing stage/source/event: %+v", i, p)
		}
		if p.Dropped && p.Reason == "" {
			t.Fatalf("line[%d] is a drop with no reason: %q", i, lines[i])
		}
	}
}

// TestStdoutNeverWritten redirects os.Stdout to a pipe around a full
// streak-plus-fire sequence run through Default() (which targets
// os.Stderr) and asserts nothing lands on stdout.
func TestStdoutNeverWritten(t *testing.T) {
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	func() {
		defer func() {
			os.Stdout = origStdout
			w.Close()
		}()

		l := Default()
		s := l.NewStreak("engagement", "speech", "evt-stdout")
		for tick := 1; tick <= 200; tick++ {
			s.Enter(tick, "cooldown")
		}
		s.Fire(201, "utterance detected")
	}()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("stdout got %d bytes, want 0: %q", len(got), string(got))
	}
}

func TestParseRejectsMalformedLine(t *testing.T) {
	cases := []string{
		"",
		"not a sense line",
		"[SENSE stage=vad] missing source and event",
		"[SENSE stage=vad source=speech event=e1] ",
	}
	for _, c := range cases {
		if _, err := Parse(c); err != nil {
			// Malformed cases are allowed to error; the last case is
			// actually well-formed with empty detail, so only assert an
			// error for the genuinely malformed ones.
			if strings.HasPrefix(c, "[SENSE stage=vad source=speech event=e1]") {
				t.Fatalf("Parse(%q) unexpectedly errored: %v", c, err)
			}
		} else if !strings.HasPrefix(c, "[SENSE stage=vad source=speech event=e1]") {
			t.Fatalf("Parse(%q) unexpectedly succeeded", c)
		}
	}
}

func TestStreakTickCountMatchesEnterCalls(t *testing.T) {
	buf := &bytes.Buffer{}
	l := New(buf)
	s := l.NewStreak("gate", "touch", "evt-count")

	const n = 37
	for tick := 0; tick < n; tick++ {
		s.Enter(tick, "throttle")
	}
	s.End(n)

	lines := splitLines(t, buf)
	summary := lines[len(lines)-1]
	if !strings.Contains(summary, "suppressed "+strconv.Itoa(n)+" ticks") {
		t.Fatalf("summary = %q, want it to contain suppressed %d ticks", summary, n)
	}
}

// TestDetailCannotForgeAnExtraRecord pins that one call is one line.
//
// detail reaches this package from error strings, peer-supplied client names
// and rule ids — none of which this package controls. A detail carrying a
// newline plus a plausible "[SENSE ...]" prefix would otherwise appear to a
// line-by-line consumer as a SECOND, fabricated record: exactly the failure
// the one-record-per-line grammar exists to prevent.
func TestDetailCannotForgeAnExtraRecord(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	l.Stage("gate", "speech", "abc",
		"first\n[SENSE stage=forged source=evil event=deadbeef] injected")

	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("one Stage call wrote %d lines, want 1: %q", strings.Count(out, "\n"), out)
	}
	line, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(%q): %v", out, err)
	}
	if line.Stage != "gate" {
		t.Errorf("stage = %q, want %q", line.Stage, "gate")
	}
	if strings.Contains(line.Detail, "\n") {
		t.Errorf("detail kept a newline: %q", line.Detail)
	}
	if !strings.Contains(line.Detail, "injected") {
		t.Errorf("detail lost its content: %q", line.Detail)
	}
}

// TestDropDetailIsSanitizedToo — Drop takes the same untrusted detail.
func TestDropDetailIsSanitizedToo(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	l.Drop("gate", "speech", "abc", "self-mute", "a\rb\nc\x00d")
	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("one Drop call wrote %d lines, want 1: %q", strings.Count(out, "\n"), out)
	}
	line, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(%q): %v", out, err)
	}
	if line.Detail != "a_b_c_d" {
		t.Errorf("detail = %q, want %q", line.Detail, "a_b_c_d")
	}
}

// TestParseRefusesAControlCharacter is the reader-side half: a line that
// somehow carries a control character did not come out of this Logger, and
// parsing it as a well-formed record would launder a forgery.
func TestParseRefusesAControlCharacter(t *testing.T) {
	forged := "[SENSE stage=gate source=speech event=abc] first\rsecond"
	if _, err := Parse(forged); err == nil {
		t.Fatal("Parse accepted a line with an embedded control character")
	}
}
