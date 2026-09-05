package compose

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/stream"
)

// stdinStopBudget is how long Run may take to return after a stdio peer closes
// the pipe.
//
// The number is generous on purpose: what is being asserted is that the run
// ends AT ALL, since before d3 it ended only on a signal and a parent that had
// already exited could never send one. The tightness of the stop is asserted
// separately, and in the unit that actually means something — how many more
// ticks the loop ran (see maxTicksAfterStdinClose). A wall-clock bound of two
// periods would be a test that fails on a loaded CI box while measuring nothing
// the robot cares about.
const stdinStopBudget = 2 * time.Second

// maxTicksAfterStdinClose is "within two periods", counted in ticks.
//
// The loop must notice the closed pipe on its next tick. The slack is for the
// sampling race alone — the tick count is read from another goroutine just
// before the pipe is closed, so one or two ticks can land in between.
const maxTicksAfterStdinClose = 4

// syncWriter is a log sink safe to write from several goroutines and read from
// the test's own.
//
// It is needed, and strings.Builder is not enough, because a senselog.Logger
// does NOT synchronize its writes: every package that logs from more than one
// goroutine wraps it with its own mutex (internal/tick's dropLog, the stream's
// logMu, the provider's logMu). A running engine has the tick goroutine, a
// reader goroutine and the composition root all writing lines to one Logger, so
// a test that hands it a bare Builder is racing on the Builder — not on
// anything the engine does. In production the writer is os.Stderr, where each
// line is one write(2) and the kernel serializes them.
type syncWriter struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// frameSink collects complete protocol frames written to it, in order.
//
// The stream writes each frame with exactly one Write (encodeFrame builds the
// prefix and the body together, so a half-frame can never reach a peer), which
// is what lets this decode per-write instead of reassembling a stream.
type frameSink struct {
	t  *testing.T
	mu sync.Mutex
	// raw keeps the bytes rather than decoded maps so Write stays trivial and
	// nothing in the engine's write path can be slowed by a test's decoder.
	raw [][]byte
}

func newFrameSink(t *testing.T) *frameSink { return &frameSink{t: t} }

func (f *frameSink) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.raw = append(f.raw, append([]byte(nil), p...))
	f.mu.Unlock()
	return len(p), nil
}

func (f *frameSink) frames() []map[string]any {
	f.mu.Lock()
	raw := append([][]byte(nil), f.raw...)
	f.mu.Unlock()

	out := make([]map[string]any, 0, len(raw))
	for _, body := range raw {
		if len(body) < 4 {
			continue
		}
		size := binary.BigEndian.Uint32(body[:4])
		if int(size) != len(body)-4 {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal(body[4:], &frame); err != nil {
			f.t.Fatalf("decoding a frame body %q: %v", body[4:], err)
		}
		out = append(out, frame)
	}
	return out
}

func (f *frameSink) countOf(kind string) int {
	n := 0
	for _, frame := range f.frames() {
		if frame["kind"] == kind {
			n++
		}
	}
	return n
}

func (f *frameSink) waitFor(kind string, count int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if f.countOf(kind) >= count {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// writeFrame writes one length-prefixed JSON frame to w.
func writeFrame(t *testing.T, w io.Writer, payload any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshalling %+v: %v", payload, err)
	}
	prefix := make([]byte, 4)
	binary.BigEndian.PutUint32(prefix, uint32(len(body)))
	if _, err := w.Write(append(prefix, body...)); err != nil {
		t.Fatalf("writing a frame: %v", err)
	}
}

// TestClosingStdinStopsTheEngineCleanly is the acceptance test for d3.
//
// In --stdio mode the parent owns the engine's lifetime: it spawned it, it
// holds both pipes, and when it closes them there is nobody to stream to and
// nobody to take an intent from. Before this, the endpoint's reader hit EOF, the
// session was torn down, and the tick loop went on composing poses into a closed
// pipe FOREVER — every one a silent drop — until somebody signalled it. A parent
// that had already exited could not, so the engine was an orphan.
//
// The stop must be an ordinary graceful one, with all of its guarantees intact:
// the loop ends, the settling neutral pose reaches the peer, the end frame names
// "stdin-closed", and Run returns nil so the process exits 0.
func TestClosingStdinStopsTheEngineCleanly(t *testing.T) {
	opts := toyOptions(t)
	opts.SocketDir = ""
	opts.Stdio = true

	stdinR, stdinW := io.Pipe()
	out := newFrameSink(t)
	stderr := &syncWriter{}

	runtime, err := New(opts, Build{Version: "0.0.0-test"}, stdinR, out, stderr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer runtime.Close()

	done := make(chan error, 1)
	go func() { done <- runtime.Run(context.Background()) }()

	writeFrame(t, stdinW, map[string]any{"v": stream.Version, "kind": "hello", "client": "test"})
	if !out.waitFor("hello", 1, 5*time.Second) {
		t.Fatalf("the engine never greeted back:\n%s", stderr.String())
	}
	// Drive something that moves the robot off neutral, so "the last pose is
	// neutral" is a real assertion rather than one every pose satisfies.
	writeFrame(t, stdinW, map[string]any{
		"v": stream.Version, "kind": "sense",
		"fields": map[string]any{"bumper": true, "light_level": 0.2, "tag": "a"},
	})
	if !out.waitFor("pose", 5, 5*time.Second) {
		t.Fatalf("no poses streamed:\n%s", stderr.String())
	}

	ticksBefore := runtime.Engine.Stats().Ticks
	closedAt := time.Now()
	if err := stdinW.Close(); err != nil {
		t.Fatalf("closing the peer's write end: %v", err)
	}

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("Run returned %v, want nil — a closed pipe is a clean stop", runErr)
		}
	case <-time.After(stdinStopBudget):
		t.Fatalf("Run did not return within %v of the pipe closing; the engine is an orphan",
			stdinStopBudget)
	}
	t.Logf("the run ended %v after the pipe closed", time.Since(closedAt))

	// "Within two periods", in the unit that matters: the loop noticed on its
	// next tick rather than drifting on.
	if grew := runtime.Engine.Stats().Ticks - ticksBefore; grew > maxTicksAfterStdinClose {
		t.Errorf("the engine ran %d more ticks after the pipe closed, want at most %d",
			grew, maxTicksAfterStdinClose)
	}

	// The peer's last two frames: the settling neutral pose, then the end frame
	// naming the cause. A consumer still reading stdout gets both.
	frames := out.frames()
	if len(frames) < 2 {
		t.Fatalf("only %d frames were written", len(frames))
	}
	last, secondLast := frames[len(frames)-1], frames[len(frames)-2]

	if last["kind"] != stream.KindEnd {
		t.Fatalf("the last frame is %v, want %q", last["kind"], stream.KindEnd)
	}
	if last["reason"] != "stdin-closed" {
		t.Errorf("the end frame's reason is %v, want %q — the CAUSE, not the consequence",
			last["reason"], "stdin-closed")
	}
	if secondLast["kind"] != stream.KindPose {
		t.Fatalf("the frame before the end is %v, want %q", secondLast["kind"], stream.KindPose)
	}
	channels, ok := secondLast["channels"].(map[string]any)
	if !ok {
		t.Fatalf("the settle pose carries no channels: %v", secondLast)
	}
	for name, want := range runtime.Vocabulary.Neutral() {
		got, present := channels[name].([]any)
		if !present || len(got) != len(want) {
			t.Fatalf("the settle pose omits or mis-shapes channel %q: %v", name, channels)
		}
		for i, value := range want {
			number, isNumber := got[i].(float64)
			if !isNumber || number != value {
				t.Errorf("the settle pose is not neutral: %s[%d] = %v, want %v",
					name, i, got[i], value)
			}
		}
	}

	// And the stop is NAMED, on stderr, where an operator greps for it.
	log := stderr.String()
	if !strings.Contains(log, "the stdio peer closed the pipe") {
		t.Errorf("the stop was not named on stderr:\n%s", log)
	}
}

// TestASocketPeerLeavingDoesNotStopTheEngine is d3's other half, and the reason
// the fix could not simply be "any reader EOF stops the run".
//
// A socket peer disconnecting means one client went away, not that the robot
// should stop. The engine keeps ticking and the socket accepts the next owner —
// an engine that stopped every time somebody's debugger disconnected would be
// unusable, and a robot's tick loop does not belong to whoever last connected.
func TestASocketPeerLeavingDoesNotStopTheEngine(t *testing.T) {
	opts := toyOptions(t)

	stderr := &syncWriter{}
	runtime, err := New(opts, Build{Version: "0.0.0-test"}, nil, nil, stderr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer runtime.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()

	// A socket server offers no stdin-closed channel at all: the distinction
	// between the transports is in the type, not in a caution somebody reads.
	if gone := runtime.Server.StdinClosed(); gone != nil {
		t.Error("a socket server exposes a stdin-closed channel; a socket peer " +
			"leaving must not be able to stop the engine")
	}

	addr := waitForAddr(t, runtime)
	first := dialAndGreet(t, addr)
	ticksWithPeer := runtime.Engine.Stats().Ticks

	// The peer leaves, abruptly.
	_ = first.Close()

	// The engine keeps ticking...
	if !waitForTicks(runtime, ticksWithPeer+5, 5*time.Second) {
		t.Fatalf("the engine stopped ticking after its peer left:\n%s", stderr.String())
	}
	select {
	case runErr := <-done:
		t.Fatalf("Run returned (%v) when a socket peer disconnected", runErr)
	default:
	}

	// ...and the socket takes the next owner, who gets a live stream.
	second := dialAndGreet(t, addr)
	defer func() { _ = second.Close() }()
	if err := second.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if kind := readFrameKind(t, second); kind == "" {
		t.Fatal("the second owner received no frames from the still-running engine")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not return within 5s of cancellation")
	}
}

func waitForAddr(t *testing.T, runtime *Runtime) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if addr := runtime.Server.Addr(); addr != "" {
			return addr
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the server never reported a socket address")
	return ""
}

func waitForTicks(runtime *Runtime, want uint64, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if runtime.Engine.Stats().Ticks >= want {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// dialAndGreet connects to the socket and performs the mandatory handshake.
func dialAndGreet(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", addr)
	if err != nil {
		t.Fatalf("dialling %s: %v", addr, err)
	}
	writeFrame(t, conn, map[string]any{"v": stream.Version, "kind": "hello", "client": "test"})
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if kind := readFrameKind(t, conn); kind != "hello" {
		t.Fatalf("the first reply is %q, want %q", kind, "hello")
	}
	return conn
}

// readFrameKind reads one frame and returns its kind, or "" if none arrives.
func readFrameKind(t *testing.T, r io.Reader) string {
	t.Helper()
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return ""
	}
	body := make([]byte, binary.BigEndian.Uint32(prefix[:]))
	if _, err := io.ReadFull(r, body); err != nil {
		return ""
	}
	var frame map[string]any
	if err := json.Unmarshal(body, &frame); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}
	kind, _ := frame["kind"].(string)
	return kind
}
