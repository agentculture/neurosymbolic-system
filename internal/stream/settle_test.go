package stream

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// gatedWriter is the "peer that is not reading yet": every Write parks until
// release is called, and each frame is recorded IN THE ORDER it was written.
//
// It is what makes the settle-pose ordering test deterministic rather than a
// race the fast path usually wins. With a real socket the kernel's send buffer
// absorbs a handful of small frames instantly, so a queued pose and a directly
// written end frame would almost always come out in the right order by luck —
// and a test that passes by luck would not have caught the bug this file pins.
type gatedWriter struct {
	release chan struct{}
	once    sync.Once

	mu     sync.Mutex
	frames [][]byte
}

func newGatedWriter() *gatedWriter {
	return &gatedWriter{release: make(chan struct{})}
}

func (g *gatedWriter) Write(p []byte) (int, error) {
	<-g.release
	g.mu.Lock()
	g.frames = append(g.frames, append([]byte(nil), p...))
	g.mu.Unlock()
	return len(p), nil
}

func (g *gatedWriter) open() { g.once.Do(func() { close(g.release) }) }

// decoded returns every frame written so far, in write order.
func (g *gatedWriter) decoded(t *testing.T) []map[string]any {
	t.Helper()
	g.mu.Lock()
	raw := append([][]byte(nil), g.frames...)
	g.mu.Unlock()

	// Each Write is one complete framed payload (encodeFrame builds the prefix
	// and the body together precisely so a half-frame can never reach a peer),
	// so decoding is per-write rather than a stream reassembly.
	out := make([]map[string]any, 0, len(raw))
	for _, body := range raw {
		if len(body) < lengthPrefixBytes {
			t.Fatalf("a write of %d bytes is shorter than the length prefix", len(body))
		}
		size := binary.BigEndian.Uint32(body[:lengthPrefixBytes])
		if int(size) != len(body)-lengthPrefixBytes {
			t.Fatalf("a write declared %d body bytes but carried %d",
				size, len(body)-lengthPrefixBytes)
		}
		var frame map[string]any
		if err := json.Unmarshal(body[lengthPrefixBytes:], &frame); err != nil {
			t.Fatalf("decoding %q: %v", body[lengthPrefixBytes:], err)
		}
		out = append(out, frame)
	}
	return out
}

// blockedReader is a reader that never returns, standing in for a peer that
// has sent its hello and then gone quiet. The session needs one; nothing in
// this test reads from the client side.
type blockedReader struct{ done chan struct{} }

func (b blockedReader) Read([]byte) (int, error) {
	<-b.done
	return 0, context.Canceled
}

// TestTheSettlingNeutralPoseReachesThePeerBeforeTheEndFrame is the acceptance
// test for the shutdown's ordering contract.
//
// On the way out, the tick loop writes ONE settling neutral pose so a robot is
// not left holding whatever the last tick happened to compose. That pose rides
// the ordinary bounded queue; the end-of-stream frame takes the direct write
// path. Unless the queue is drained first, the end frame overtakes the settle
// pose (it only has to win wmu once) and the session then closes with the pose
// still queued — so the consumer's last word from the robot would be a pose
// from BEFORE the stop, which is exactly the thing a settle exists to prevent.
//
// The assertion is deliberately about ORDER and CONTENT together: the last pose
// frame before the end frame must be the vocabulary's own neutral.
func TestTheSettlingNeutralPoseReachesThePeerBeforeTheEndFrame(t *testing.T) {
	voc := toyVoc(t)
	clock := tick.NewFakeClock(testPeriod)
	logBuf := &syncBuffer{}
	writer := newGatedWriter()
	reader := blockedReader{done: make(chan struct{})}
	defer close(reader.done)

	srv, err := NewStdio(Config{
		Vocabulary:     voc,
		Clock:          clock,
		HeartbeatEvery: -1, // one kind of frame under test is enough
		OutboundQueue:  DefaultOutboundQueue,
	}, nil, newRecordingSense(), nil, senselog.New(logBuf), reader, writer)
	if err != nil {
		t.Fatalf("NewStdio: %v", err)
	}

	// These mechanics tests never send a hello; the wire rule is hello-first, so
	// mark the session subscribed the way a completed handshake would.
	srv.session().subscribed.Store(true)
	// Settle is left at its default (nil), which is the whole point: the engine
	// writes one neutral pose as the loop exits.
	eng, err := tick.New(voc, tick.Config{
		Period: testPeriod,
		Clock:  clock,
		Ticker: clock,
		Log:    senselog.New(logBuf),
	}, srv)
	if err != nil {
		t.Fatalf("tick.New: %v", err)
	}
	srv.Attach(eng)

	// Something that moves ch_a off neutral, so "the last pose is neutral" is a
	// real assertion rather than one every pose would satisfy anyway.
	hold := 10.0
	if !eng.Send(tick.AdmitCmd{Behavior: tick.Behavior{
		Action:   "ramp",
		Class:    tick.ClassStoppable,
		Lifetime: tick.Lifetime{DurationS: &hold},
	}}) {
		t.Fatal("the engine refused the admit command")
	}

	runCtx, cancel := context.WithCancel(context.Background())
	// Serve gets its own context, cancelled only after the run returns — the
	// arrangement internal/compose uses in production, and the one that lets
	// the settling pose be composed before the endpoint tears the session down.
	serveCtx, stopServing := context.WithCancel(context.Background())
	defer stopServing()
	go srv.Serve(serveCtx)

	done := make(chan error, 1)
	go func() { done <- srv.RunWith(runCtx, eng.Run) }()

	// Three ticks with the peer NOT reading: the poses queue up behind a writer
	// that is parked, which is the state the ordering bug needs to show itself.
	clock.Advance(3 * testPeriod)

	cancel()
	// Let the drain proceed well inside flushGrace, but only once the shutdown
	// is already under way.
	go func() {
		time.Sleep(20 * time.Millisecond)
		writer.open()
	}()

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("RunWith: %v", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not return within 5s")
	}

	frames := writer.decoded(t)
	endAt := -1
	for i, frame := range frames {
		if frame["kind"] == KindEnd {
			endAt = i
			break
		}
	}
	if endAt < 0 {
		t.Fatalf("no %q frame was written; frames = %v", KindEnd, kindsOf(frames))
	}

	lastPose := -1
	for i := 0; i < endAt; i++ {
		if frames[i]["kind"] == KindPose {
			lastPose = i
		}
	}
	if lastPose < 0 {
		t.Fatalf("no pose frame preceded the end frame; frames = %v", kindsOf(frames))
	}

	// The settle pose is the vocabulary's neutral, verbatim.
	channels, ok := frames[lastPose]["channels"].(map[string]any)
	if !ok {
		t.Fatalf("the last pose frame carries no channels: %v", frames[lastPose])
	}
	for name, want := range voc.Neutral() {
		got, present := channels[name].([]any)
		if !present {
			t.Fatalf("the settle pose omits channel %q: %v", name, channels)
		}
		if len(got) != len(want) {
			t.Fatalf("channel %q carried %d values, want %d", name, len(got), len(want))
		}
		for i, value := range want {
			number, isNumber := got[i].(float64)
			if !isNumber || number != value {
				t.Errorf("the settle pose is not neutral: %s[%d] = %v, want %v",
					name, i, got[i], value)
			}
		}
	}

	// And it really was the LAST thing before the end frame — nothing about the
	// robot's state may follow the announcement that the stream is over.
	for i := lastPose + 1; i < endAt; i++ {
		if frames[i]["kind"] == KindPose {
			t.Errorf("a pose frame at %d follows the settle pose", i)
		}
	}
}

// TestAPeerThatStoppedReadingStillCannotWedgeTheShutdown pins the other half of
// the flush's contract: the drain is BOUNDED. A consumer that wedged must not
// be able to wedge the engine's own shutdown — the exact inversion this package
// exists to prevent — so Close returns on the grace whether or not the queue
// ever drains.
func TestAPeerThatStoppedReadingStillCannotWedgeTheShutdown(t *testing.T) {
	voc := toyVoc(t)
	clock := tick.NewFakeClock(testPeriod)
	writer := newGatedWriter() // never opened: this peer never reads again
	reader := blockedReader{done: make(chan struct{})}
	defer close(reader.done)

	srv, err := NewStdio(Config{
		Vocabulary: voc, Clock: clock, HeartbeatEvery: -1,
		OutboundQueue: DefaultOutboundQueue,
	}, nil, newRecordingSense(), nil, senselog.New(&syncBuffer{}), reader, writer)
	if err != nil {
		t.Fatalf("NewStdio: %v", err)
	}
	go srv.Serve(context.Background())
	for i := 0; i < 4; i++ {
		_ = srv.Write(voc.Neutral())
	}

	closed := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(closed)
		srv.Close()
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close never returned against a peer that stopped reading")
	}
	// flushGrace + endFrameGrace twice, with generous headroom for a loaded CI
	// box: the point is that it is bounded at all, not the exact number.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Close took %v against a wedged peer", elapsed)
	}
	writer.open()
}

func kindsOf(frames []map[string]any) []any {
	out := make([]any, 0, len(frames))
	for _, frame := range frames {
		out = append(out, frame["kind"])
	}
	return out
}

// enteredWriter signals when a Write has BEGUN and then blocks inside it.
//
// It exists to reproduce the one window "is the queue empty?" cannot see: the
// writer goroutine has received a frame from the channel — so the channel is
// empty — but has not finished writing it.
type enteredWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newEnteredWriter() *enteredWriter {
	return &enteredWriter{entered: make(chan struct{}, 8), release: make(chan struct{})}
}

func (e *enteredWriter) Write(p []byte) (int, error) {
	e.entered <- struct{}{}
	<-e.release
	return len(p), nil
}

func (e *enteredWriter) open() { e.once.Do(func() { close(e.release) }) }

// TestFlushWaitsForTheWriteNotJustTheDequeue pins the flush's actual condition.
//
// An earlier version of flushWithin waited for the outbound CHANNEL to empty.
// That is a different question, and the difference is a real race the toy
// robot's stdio end-to-end test caught one run in three: the writer receives a
// frame (channel now empty) and only then locks wmu, so a flush that stopped
// there would let the end frame win that lock and overtake the very settle pose
// it was supposed to be waiting for.
//
// The condition is "every frame accepted onto the queue has been WRITTEN", and
// this test holds the writer inside its Write to prove the flush is waiting on
// that and not on the channel.
func TestFlushWaitsForTheWriteNotJustTheDequeue(t *testing.T) {
	voc := toyVoc(t)
	writer := newEnteredWriter()
	reader := blockedReader{done: make(chan struct{})}
	defer close(reader.done)

	srv, err := NewStdio(Config{
		Vocabulary: voc, Clock: tick.RealClock{}, HeartbeatEvery: -1,
		OutboundQueue: DefaultOutboundQueue,
	}, nil, newRecordingSense(), nil, senselog.New(&syncBuffer{}), reader, writer)
	if err != nil {
		t.Fatalf("NewStdio: %v", err)
	}
	// These mechanics tests never send a hello; the wire rule is hello-first, so
	// mark the session subscribed the way a completed handshake would.
	srv.session().subscribed.Store(true)
	go srv.Serve(context.Background())

	if err := srv.Write(voc.Neutral()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Wait until the writer is INSIDE the Write. The channel is empty now, so
	// a flush keyed on emptiness would return immediately.
	select {
	case <-writer.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the writer never began its write")
	}
	sess := srv.session()
	if sess == nil {
		t.Fatal("the stdio session is gone")
	}
	if len(sess.out) != 0 {
		t.Fatalf("the outbound channel holds %d frames, want 0 for this test to mean anything",
			len(sess.out))
	}

	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		sess.flushWithin(2 * time.Second)
	}()

	select {
	case <-flushed:
		t.Fatal("the flush returned while a frame was still being written")
	case <-time.After(50 * time.Millisecond):
	}

	writer.open()
	select {
	case <-flushed:
	case <-time.After(3 * time.Second):
		t.Fatal("the flush did not return after the write completed")
	}
	srv.Close()
}
