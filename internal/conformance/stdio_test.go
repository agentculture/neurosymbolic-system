package conformance_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// protocolVersion is the wire version a client must announce. It is spelled
// here rather than imported because this test speaks the protocol as an
// OUTSIDE consumer does — over a pipe to a built binary, with no shared Go
// types — which is the only way to prove what a consumer actually receives.
const protocolVersion = 1

// wireTimeout bounds the whole spawned run. It is generous: this test is about
// what the two streams CARRY, never about how fast they carry it.
const wireTimeout = 30 * time.Second

// TestBuiltEngineKeepsStdoutPureProtocol is acceptance criterion 3's process
// half.
//
// The engine is built and run over stdio against a conformance fixture for at
// least 50 ticks, and then:
//
//   - every byte of stdout is accounted for by length-prefixed protocol frames,
//     each a JSON object carrying "v" and "kind" — nothing else, not one stray
//     log line;
//   - stderr carries the SENSE-grammar lines.
//
// This is not a stylistic preference. A consumer piping the engine's stdout
// into a JSONL reader gets a pure stream only if NOTHING else can ever reach
// it, and the cheapest way for that to break is a diagnostic printed by a
// package that did not think about which stream it was on. A unit test cannot
// catch that; a process reading the real file descriptors can.
func TestBuiltEngineKeepsStdoutPureProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture spawns a POSIX pipeline")
	}
	engine := buildEngine(t)

	fixture := filepath.Join("testdata", "reachy")
	cmd := exec.Command(engine, "run", // #nosec G204 - a binary this test just built
		"--adaptor", filepath.Join(fixture, "adaptor.toml"),
		"--rules", filepath.Join(fixture, "default_rules.v1.toml"),
		"--stdio",
		"--period", "5ms",
		"--heartbeat", "50ms",
		"--base-action", "feel-alive",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the engine: %v", err)
	}

	frames := make(chan map[string]any, 1024)
	readErr := make(chan error, 1)
	go func() { readErr <- readFrames(stdout, frames) }()

	send(t, stdin, map[string]any{
		"v": protocolVersion, "kind": "hello", "client": "conformance",
	})
	send(t, stdin, map[string]any{
		"v": protocolVersion, "kind": "sense", "fields": map[string]any{"pat": true},
	})

	// Collect until the engine has streamed at least 50 poses.
	const wantPoses = 50
	poses, kinds := 0, map[string]int{}
	deadline := time.After(wireTimeout)
	for poses < wantPoses {
		select {
		case frame := <-frames:
			kinds[stringField(frame, "kind")]++
			if stringField(frame, "kind") == "pose" {
				poses++
			}
		case err := <-readErr:
			t.Fatalf("stdout ended after %d poses: %v\nstderr:\n%s", poses, err, stderr.String())
		case <-deadline:
			t.Fatalf("only %d poses in %v\nstderr:\n%s", poses, wireTimeout, stderr.String())
		}
	}

	// SIGTERM, not a closed stdin: the engine's signal path is the one that
	// settles the robot and sends the peer an end frame, and it is what a
	// consumer supervising the process actually uses. (Closing stdin ends the
	// engine's loop but does not currently end the process — noted in
	// docs/verification/2026-09-05-donor-conformance.md.)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signalling the engine: %v", err)
	}
	for range frames { // drain whatever the shutdown still streams
	}
	if err := <-readErr; err != nil && !errors.Is(err, io.EOF) {
		t.Errorf("stdout did not end cleanly: %v", err)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Errorf("the engine exited with %v\nstderr:\n%s", err, stderr.String())
	}

	if kinds["hello"] == 0 {
		t.Errorf("no hello frame came back; kinds seen: %v", kinds)
	}
	// stderr is where every diagnostic goes, and it must be SENSE grammar.
	if !strings.Contains(stderr.String(), "[SENSE stage=") {
		t.Errorf("stderr carries no SENSE line:\n%s", stderr.String())
	}
	for i, line := range strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n") {
		if line != "" && !strings.HasPrefix(line, "[SENSE stage=") {
			t.Errorf("stderr line %d is not SENSE grammar: %q", i+1, line)
		}
	}
}

// readFrames decodes the length-prefixed stream and reports the error that
// ended it. A trailing byte that is not a whole frame is an error, which is the
// point: it means something that was not a frame reached stdout.
func readFrames(r io.Reader, out chan<- map[string]any) error {
	defer close(out)
	var header [4]byte
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		size := binary.BigEndian.Uint32(header[:])
		body := make([]byte, size)
		if _, err := io.ReadFull(r, body); err != nil {
			return err
		}
		var frame map[string]any
		if err := json.Unmarshal(body, &frame); err != nil {
			return err
		}
		if _, ok := frame["v"]; !ok {
			return errors.New("a stdout frame carries no \"v\"")
		}
		if stringField(frame, "kind") == "" {
			return errors.New("a stdout frame carries no \"kind\"")
		}
		out <- frame
	}
}

func send(t *testing.T, w io.Writer, frame map[string]any) {
	t.Helper()
	body, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshalling %v: %v", frame, err)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	if _, err := w.Write(append(header[:], body...)); err != nil {
		t.Fatalf("writing a frame: %v", err)
	}
}

func stringField(frame map[string]any, name string) string {
	value, _ := frame[name].(string)
	return value
}

// buildEngine builds cmd/neurosymbolic-engine into the test's temp directory.
func buildEngine(t *testing.T) string {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go toolchain on PATH: %v", err)
	}
	out := filepath.Join(t.TempDir(), "neurosymbolic-engine")
	cmd := exec.Command(goTool, "build", "-o", out, // #nosec G204 - the toolchain on PATH
		"github.com/agentculture/neurosymbolic-system/cmd/neurosymbolic-engine")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	return out
}
