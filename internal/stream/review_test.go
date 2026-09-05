package stream

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/senselog"
)

// --- finding 1: no encoding on the tick goroutine ---------------------------

// TestEnqueueDoesNotEncodeOnTheTickGoroutine is the structural half.
//
// enqueue is called from Write, Seam and onEvent — all three on the TICK
// goroutine, inside a 20 ms budget. CLAUDE.md's rule for an export leg is a
// timestamp and a bounded append, O(1), no encoding: json.Marshal over a pose
// allocates and its cost scales with the channel count, so it belongs on the
// writer goroutine, which is allowed to be slow. A future edit that moves it
// back fails here rather than showing up as an overrun on a robot.
func TestEnqueueDoesNotEncodeOnTheTickGoroutine(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "session.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing session.go: %v", err)
	}
	var found bool
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "enqueue" {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if ok && ident.Name == "encodeFrame" {
				t.Errorf("enqueue calls encodeFrame at %s; encoding belongs in writeLoop",
					fset.Position(ident.Pos()))
			}
			return true
		})
	}
	if !found {
		t.Fatal("session.go declares no enqueue method; this guard is watching nothing")
	}
}

// TestAFrameThatWillNotEncodeIsANamedDropInTheWriter is the behavioural half.
//
// Deferring the encode moves the failure with it: a payload json cannot
// marshal is no longer refused on the tick goroutine, so the writer must name
// that drop itself — and must keep serving the frames behind it, since one bad
// payload is not a reason to stop a robot's telemetry.
func TestAFrameThatWillNotEncodeIsANamedDropInTheWriter(t *testing.T) {
	voc := toyVoc(t)
	logBuf := &syncBuffer{}
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	out := &syncBuffer{}

	srv, err := NewStdio(Config{Vocabulary: voc, HeartbeatEvery: -1},
		nil, newRecordingSense(), nil, senselog.New(logBuf), reader, out)
	if err != nil {
		t.Fatalf("NewStdio: %v", err)
	}
	sess := srv.session()
	sess.subscribed.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// +Inf is a float64 json.Marshal refuses, so this pose cannot be encoded.
	sess.enqueue(KindPose, poseOut{
		V: Version, Kind: KindPose, Tick: 1,
		Channels: map[string][]float64{"ch_a": {math.Inf(1), 0}},
	})
	// A well-formed frame behind it must still reach the peer.
	if err := srv.Write(voc.Neutral()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		if strings.Contains(string(logBuf.Bytes()), "reason=frame-too-large") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("no encode-failure drop was logged:\n%s", logBuf.Bytes())
		case <-time.After(5 * time.Millisecond):
		}
	}
	deadline = time.After(2 * time.Second)
	for {
		if strings.Contains(string(out.Bytes()), "\"tick\":") {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the frame behind the unencodable one never reached the peer")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if srv.Stats().Drops == 0 {
		t.Error("the encode failure was not counted as a drop")
	}
}

// --- finding 5: accept-and-count must be one step ---------------------------

// framesIn decodes every complete frame in a recorded outbound byte stream and
// returns their kinds, in order.
func framesInOrder(t *testing.T, raw []byte) []string {
	t.Helper()
	var kinds []string
	for len(raw) >= lengthPrefixBytes {
		size := int(binary.BigEndian.Uint32(raw[:lengthPrefixBytes]))
		if len(raw) < lengthPrefixBytes+size {
			t.Fatalf("a truncated frame is on the wire: %d bytes left, %d promised",
				len(raw)-lengthPrefixBytes, size)
		}
		var frame struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw[lengthPrefixBytes:lengthPrefixBytes+size], &frame); err != nil {
			t.Fatalf("decoding a frame: %v", err)
		}
		kinds = append(kinds, frame.Kind)
		raw = raw[lengthPrefixBytes+size:]
	}
	if len(raw) != 0 {
		t.Fatalf("%d trailing bytes are not a frame", len(raw))
	}
	return kinds
}

// TestTheEndFrameNeverOvertakesAnAcceptedPose pins the shutdown ordering
// against the accept/count race.
//
// flushWithin's condition is "every frame ACCEPTED onto the queue has been
// WRITTEN". When enqueue published to the channel and only afterwards bumped
// queued, there was a window in which the writer had already taken the frame
// while the counters still read 0 and 0 — so a Close landing in that window saw
// written >= queued, skipped the drain, and let the DIRECTLY written end frame
// win wmu ahead of the pose it was supposed to be waiting for. The consumer's
// last word from the robot then arrives after the stream has been declared
// over.
//
// The window is a few instructions wide, so this runs the whole shutdown many
// times; under -race the instrumentation widens it further.
func TestTheEndFrameNeverOvertakesAnAcceptedPose(t *testing.T) {
	voc := toyVoc(t)
	const rounds = 400
	for i := 0; i < rounds; i++ {
		out := &syncBuffer{}
		reader := blockedReader{done: make(chan struct{})}
		srv, err := NewStdio(Config{
			Vocabulary: voc, HeartbeatEvery: -1, OutboundQueue: DefaultOutboundQueue,
		}, nil, newRecordingSense(), nil, senselog.New(&syncBuffer{}), reader, out)
		if err != nil {
			t.Fatalf("NewStdio: %v", err)
		}
		srv.session().subscribed.Store(true)
		ctx, cancel := context.WithCancel(context.Background())
		go srv.Serve(ctx)

		// Warm up: Serve starts the writer goroutine, and until it is running
		// the queue cannot be raced at all. Waiting for one frame to land makes
		// every round below exercise the window rather than a cold session.
		if err := srv.Write(voc.Neutral()); err != nil {
			t.Fatalf("Write: %v", err)
		}
		waitForFrames(t, out, 1)

		// The pose under test, and a shutdown racing straight for it.
		if err := srv.Write(voc.Neutral()); err != nil {
			t.Fatalf("Write: %v", err)
		}
		srv.Close()
		cancel()
		close(reader.done)

		kinds := framesInOrder(t, out.Bytes())
		poses := 0
		for _, k := range kinds {
			if k == KindPose {
				poses++
			}
		}
		if poses != 2 {
			t.Fatalf("round %d wrote %v; both accepted poses must reach the peer", i, kinds)
		}
		if kinds[len(kinds)-1] != KindEnd {
			t.Fatalf("round %d wrote %v; the end frame must be last, never ahead of an "+
				"accepted pose", i, kinds)
		}
	}
}

// waitForFrames blocks until at least n complete frames have been written.
func waitForFrames(t *testing.T, out *syncBuffer, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if len(framesInOrder(t, out.Bytes())) >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("only %d frames were written within 2s, want %d",
				len(framesInOrder(t, out.Bytes())), n)
		case <-time.After(time.Millisecond):
		}
	}
}

// TestADroppedFrameIsNotCountedAsQueued is the rollback half, and it is
// deterministic: counting BEFORE the send means the full-queue path has to undo
// its own increment, or flushWithin would wait out its whole grace on every
// shutdown that followed a burst of drops.
func TestADroppedFrameIsNotCountedAsQueued(t *testing.T) {
	voc := toyVoc(t)
	blocked := newBlockingWriter()
	reader := blockedReader{done: make(chan struct{})}
	defer close(reader.done)

	srv, err := NewStdio(Config{
		Vocabulary: voc, HeartbeatEvery: -1, OutboundQueue: DefaultOutboundQueue,
	}, nil, newRecordingSense(), nil, senselog.New(&syncBuffer{}), reader, blocked)
	if err != nil {
		t.Fatalf("NewStdio: %v", err)
	}
	sess := srv.session()
	sess.subscribed.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer func() { close(blocked.release); srv.Close() }()

	const writes = 100
	for i := 0; i < writes; i++ {
		if err := srv.Write(voc.Neutral()); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	drops := srv.Stats().Drops
	if drops == 0 {
		t.Fatalf("no frames were dropped after %d writes into a wedged writer", writes)
	}
	if got := sess.queued.Load() + drops; got != writes {
		t.Errorf("queued(%d) + drops(%d) = %d, want %d — a dropped frame was still "+
			"counted as accepted", sess.queued.Load(), drops, got, writes)
	}
}

// TestAFrameIsNeverVisibleOnTheQueueBeforeItIsCounted is the invariant behind
// the shutdown ordering, tested directly rather than through the race it
// causes.
//
// flushWithin's whole correctness is "queued counts what the writer may still
// have to write". Publishing to the channel BEFORE bumping queued breaks that
// for a few instructions each time: during that window a frame is on the queue
// and uncounted, so a Close on another goroutine reads written >= queued,
// skips the drain and writes the end frame straight past a pose the writer
// still holds. Counting first makes queued an over-estimate at worst, which
// only ever makes the flush wait one poll longer.
//
// The check runs in the RECEIVER, because a channel receive is the one
// observation with a happens-before edge to the send. Counting before the send
// therefore GUARANTEES the receiver of the nth frame sees queued >= n — no
// false positive is possible, and no reordering can hide one. Serve is not
// started: this test is the writer, and the queue is deep enough that nothing
// is dropped, so exactly as many frames are received as were enqueued.
func TestAFrameIsNeverVisibleOnTheQueueBeforeItIsCounted(t *testing.T) {
	voc := toyVoc(t)
	const frames = 100000
	reader := blockedReader{done: make(chan struct{})}
	defer close(reader.done)
	srv, err := NewStdio(Config{
		Vocabulary: voc, HeartbeatEvery: -1, OutboundQueue: 2 * frames,
	}, nil, newRecordingSense(), nil, senselog.New(&syncBuffer{}), reader, &syncBuffer{})
	if err != nil {
		t.Fatalf("NewStdio: %v", err)
	}
	sess := srv.session()
	sess.subscribed.Store(true)

	violation := make(chan string, 1)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for received := uint64(1); received <= frames; received++ {
			<-sess.out
			if counted := sess.queued.Load(); counted < received {
				violation <- fmt.Sprintf(
					"the writer holds frame %d while queued reads %d", received, counted)
				return
			}
		}
	}()

	for i := 0; i < frames; i++ {
		sess.enqueue(KindHeartbeat, heartbeatOut{
			V: Version, Kind: KindHeartbeat, Tick: uint64(i), Now: "t",
		})
	}
	select {
	case <-drained:
	case msg := <-violation:
		t.Fatalf("a frame reached the queue before it was counted: %s", msg)
	case <-time.After(30 * time.Second):
		t.Fatal("the drain did not finish within 30s")
	}
	select {
	case msg := <-violation:
		t.Fatalf("a frame reached the queue before it was counted: %s", msg)
	default:
	}
	if drops := srv.Stats().Drops; drops != 0 {
		t.Fatalf("%d frames were dropped; the queue must be deep enough that every "+
			"enqueue is accepted or the receiver count means nothing", drops)
	}
	if got := sess.queued.Load(); got != frames {
		t.Errorf("queued = %d after %d accepted frames, want %d", got, frames, frames)
	}
}

// --- finding 2: an unknown field is refused, never ignored ------------------

// TestAnUnknownFieldIsRefusedOnEveryInboundKind.
//
// json.Unmarshal ignores a field the target struct does not declare, so a
// client sending "durations" for "duration_s", or a v2 field to a v1 engine,
// got silence and an intent that ran with a default it never asked for. That is
// the same class of failure as clamping an out-of-range param: the engine
// quietly reinterpreting a command it did not understand. This runtime's rule
// is fail-closed — an unknown field is refused, and the refusal names it so the
// client can see which one.
func TestAnUnknownFieldIsRefusedOnEveryInboundKind(t *testing.T) {
	cases := []struct {
		name  string
		frame map[string]any
		// fatal is true when the violation ends the connection: a handshake
		// this engine did not fully understand is not a handshake.
		fatal bool
	}{
		{name: KindHello, fatal: true, frame: map[string]any{
			"v": Version, "kind": KindHello, "client": "test", "bogus_field": 1}},
		{name: KindSense, frame: map[string]any{
			"v": Version, "kind": KindSense, "fields": map[string]any{"lux": 1.0},
			"bogus_field": 1}},
		{name: KindIntent, frame: map[string]any{
			"v": Version, "kind": KindIntent, "op": OpAdmit, "action": "ramp",
			"bogus_field": 1}},
		{name: KindMgmt, frame: map[string]any{
			"v": Version, "kind": KindMgmt, "id": "m1", "verb": "version",
			"bogus_field": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			handler := funcMgmt(func(json.RawMessage) json.RawMessage {
				t.Error("the management handler ran for a frame with an unknown field")
				return json.RawMessage(`null`)
			})
			r := newRig(t, nil, handler)
			r.start(ctx)

			client, _ := r.dial()
			if !tc.fatal {
				client.handshake()
			}
			client.send(tc.frame)

			frame := client.recvKind(KindError)
			message, _ := frame["message"].(string)
			if !strings.Contains(message, "bogus_field") {
				t.Errorf("the refusal %q does not name the unknown field", message)
			}
			if code, _ := frame["code"].(float64); int(code) != CodeUser {
				t.Errorf("the refusal carries code %v, want %d", frame["code"], CodeUser)
			}
			if remediation, _ := frame["remediation"].(string); remediation == "" {
				t.Error("the refusal carries no remediation")
			}
			if tc.name == KindSense && len(r.sense.all()) != 0 {
				t.Errorf("the refused sense frame still reached the sink: %v", r.sense.all())
			}
		})
	}
}

// --- finding 3: an over-long client name is refused, never truncated --------

// TestAnOverLongClientNameIsRefusedNotTruncated.
//
// sanitizeClient used to cut a hello's client name to 64 bytes and carry on, so
// the name in the log — the operator's only record of WHICH client attached —
// was a value the client never sent. Two clients whose names share a 64-byte
// prefix became indistinguishable. This runtime refuses rather than
// reinterprets everywhere else (an out-of-range axis, a `say` over MaxSayChars,
// an unknown field); a name is no different, and a peer that learns its name is
// too long can send a shorter one.
func TestAnOverLongClientNameIsRefusedNotTruncated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.start(ctx)

	client, conn := r.dial()
	long := strings.Repeat("n", maxClientNameBytes+1)
	client.send(map[string]any{"v": Version, "kind": KindHello, "client": long})

	frame := client.recvKind(KindError)
	if code, _ := frame["code"].(float64); int(code) != CodeUser {
		t.Errorf("the refusal carries code %v, want %d", frame["code"], CodeUser)
	}
	remediation, _ := frame["remediation"].(string)
	if !strings.Contains(remediation, strconv.Itoa(maxClientNameBytes)) {
		t.Errorf("the remediation %q does not name the %d-byte limit",
			remediation, maxClientNameBytes)
	}
	// The connection is closed: the handshake was refused, so there is no
	// session to send anything else on.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Error("the connection stayed open after a refused handshake")
	}
	// And no truncated name reached the log.
	if strings.Contains(r.logText(), strings.Repeat("n", maxClientNameBytes)) {
		t.Errorf("a truncated client name was logged:\n%s", r.logText())
	}
}

// A name AT the limit is still accepted, verbatim.
func TestAClientNameAtTheLimitIsAcceptedVerbatim(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.start(ctx)

	client, _ := r.dial()
	name := strings.Repeat("n", maxClientNameBytes)
	client.send(map[string]any{"v": Version, "kind": KindHello, "client": name})
	if reply := client.recv(); reply["kind"] != KindHello {
		t.Fatalf("a name at the limit was refused: %+v", reply)
	}
	deadline := time.After(2 * time.Second)
	for !strings.Contains(r.logText(), name) {
		select {
		case <-deadline:
			t.Fatalf("the client name was not logged verbatim:\n%s", r.logText())
		case <-time.After(5 * time.Millisecond):
		}
	}
}
