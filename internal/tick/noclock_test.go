package tick

import (
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clockFile is the ONE file in this package allowed to read the wall clock.
const clockFile = "realclock.go"

// wallClockReads are the four wall-clock entry points the loop must never use.
var wallClockReads = map[string]bool{"Now": true, "Since": true, "Sleep": true, "Tick": true}

// scanWallClock reports every `time.Now` / `time.Since` / `time.Sleep` /
// `time.Tick` in src, as "line:expression".
//
// It works on TOKENS, not on text, for two reasons. Comments are skipped, so a
// doc comment may name the thing it forbids (this file's own does). And
// `time.Tick` is matched as a whole selector, so `time.Ticker` — the type
// realclock.go legitimately wraps — is not a hit.
func scanWallClock(src []byte) []string {
	var (
		found   []string
		fset    = token.NewFileSet()
		file    = fset.AddFile("", fset.Base(), len(src))
		s       scanner.Scanner
		prev    [2]token.Token
		prevLit string
		prevPos token.Pos
	)
	s.Init(file, src, nil, 0) // mode 0: comments are not emitted
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.IDENT && prev[0] == token.IDENT && prev[1] == token.PERIOD &&
			prevLit == "time" && wallClockReads[lit] {
			found = append(found, fmt.Sprintf("%d:time.%s", file.Line(prevPos), lit))
		}
		if tok == token.IDENT {
			prevLit, prevPos = lit, pos
		}
		prev[0], prev[1] = prev[1], tok
	}
	return found
}

// TestNoWallClockInLoop is acceptance criterion 3's mechanical half: the same
// timing tests pass at 20, 50 and 100 Hz because the loop reads no wall clock
// at all.
//
// A cadence-dependent tuning is a bug class of its own — the donor respecified
// a per-tick epsilon in deg/s and had to rename the variable so the old one
// could be ignored with a named log line rather than silently reinterpreted.
// The defence here is structural: time enters this package through an injected
// Clock and Ticker, and every wall-clock read lives behind them in
// realclock.go. A stray time.Now in the loop would make the engine untestable
// at any rate but the one the developer happened to run.
func TestNoWallClockInLoop(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || name == clockFile {
			continue
		}
		scanned++
		source, readErr := os.ReadFile(filepath.Clean(name))
		if readErr != nil {
			t.Fatalf("reading %s: %v", name, readErr)
		}
		for _, hit := range scanWallClock(source) {
			t.Errorf("%s:%s is a wall-clock read outside %s — the loop must go through "+
				"the injected Clock/Ticker so it stays deterministic and rate-independent",
				name, hit, clockFile)
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no sources; the walk is wrong")
	}
}

// The guard must match what it claims to, in both directions: a real
// wall-clock read is a hit, and time.Ticker, time.Duration, time.Time and a
// comment merely naming time.Now are not.
func TestWallClockScanMatchesTheRightThings(t *testing.T) {
	hits := []string{
		"package p\nimport \"time\"\nfunc f() { _ = time.Now() }",
		"package p\nimport \"time\"\nfunc f(s time.Time) { _ = time.Since(s) }",
		"package p\nimport \"time\"\nfunc f(d time.Duration) { time.Sleep(d) }",
		"package p\nimport \"time\"\nfunc f(d time.Duration) { <-time.Tick(d) }",
	}
	for _, src := range hits {
		if got := scanWallClock([]byte(src)); len(got) == 0 {
			t.Errorf("%q should be flagged as a wall-clock read", src)
		}
	}
	clean := []string{
		"package p\nimport \"time\"\nvar t *time.Ticker",
		"package p\nimport \"time\"\nfunc f(d time.Duration) *time.Ticker { return time.NewTicker(d) }",
		"package p\n// This loop never calls time.Now, time.Since, time.Sleep or time.Tick.\nvar x = 1",
	}
	for _, src := range clean {
		if got := scanWallClock([]byte(src)); len(got) != 0 {
			t.Errorf("%q must not be flagged, got %v", src, got)
		}
	}
}

// realclock.go is the file the guard exempts, so it had better be the file
// that actually holds the wall-clock reads. An exemption pointing at a file
// with nothing in it is a hole, not a rule.
func TestClockFileHoldsTheWallClockReads(t *testing.T) {
	source, err := os.ReadFile(clockFile)
	if err != nil {
		t.Fatalf("reading %s: %v", clockFile, err)
	}
	if len(scanWallClock(source)) == 0 {
		t.Fatalf("%s contains no wall-clock read; either it moved (update clockFile) "+
			"or the exemption is now dead weight", clockFile)
	}
}
