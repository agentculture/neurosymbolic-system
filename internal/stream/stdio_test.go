package stream

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// pipeRig is the stdio equivalent of rig: the same engine and server, talking
// the same framing over an io.Pipe pair instead of a socket.
type pipeRig struct {
	srv    *Server
	eng    *tick.Engine
	clock  *tick.FakeClock
	client *frameConn
	sense  *recordingSense
	logBuf *syncBuffer
	period time.Duration
}

func newPipeRig(t *testing.T, mgmt MgmtHandler, cfgFn func(*Config)) *pipeRig {
	t.Helper()
	voc := toyVoc(t)
	clock := tick.NewFakeClock(testPeriod)
	logBuf := &syncBuffer{}
	sense := newRecordingSense()

	// toEngine carries the client's frames in; fromEngine carries the
	// engine's frames back out.
	toEngineR, toEngineW := io.Pipe()
	fromEngineR, fromEngineW := io.Pipe()

	cfg := Config{Vocabulary: voc, Clock: clock, HeartbeatEvery: -1}
	if cfgFn != nil {
		cfgFn(&cfg)
	}
	srv, err := NewStdio(cfg, nil, sense, mgmt, senselog.New(logBuf), toEngineR, fromEngineW)
	if err != nil {
		t.Fatalf("NewStdio: %v", err)
	}
	eng, err := tick.New(voc, tick.Config{
		Period: testPeriod, Clock: clock, Ticker: clock,
		Log: senselog.New(logBuf), Settle: tick.Bool(false),
	}, srv)
	if err != nil {
		t.Fatalf("tick.New: %v", err)
	}
	srv.Attach(eng)

	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	if !eng.Send(tick.SetSeamCmd{Seam: srv.Seam}) {
		t.Fatal("the engine refused the seam command")
	}
	runDone := make(chan error, 1)
	go func() { runDone <- eng.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		clock.Stop()
		<-runDone
		srv.Close()
		_ = toEngineW.Close()
		_ = fromEngineR.Close()
	})

	return &pipeRig{
		srv: srv, eng: eng, clock: clock, sense: sense, logBuf: logBuf,
		period: testPeriod,
		client: &frameConn{t: t, rw: struct {
			io.Reader
			io.Writer
		}{fromEngineR, toEngineW}},
	}
}

func TestStdioSpeaksTheSameFraming(t *testing.T) {
	r := newPipeRig(t, nil, nil)
	reply := r.client.handshake()
	if reply["v"] != float64(Version) {
		t.Errorf("the hello reply carries v=%v", reply["v"])
	}

	r.client.send(map[string]any{
		"v": Version, "kind": KindSense, "fields": map[string]any{"lux": 2.5},
	})
	r.sense.await(t)

	r.clock.Advance(r.period)
	frame := r.client.recvKind(KindPose)
	if frame["tick"] != float64(1) {
		t.Errorf("pose frame carries tick=%v, want 1", frame["tick"])
	}
	channels := frame["channels"].(map[string]any)
	if got := channels["ch_b"].([]any); len(got) != 1 || got[0] != float64(5) {
		t.Errorf("ch_b is %v, want [5]", got)
	}
}

func TestStdioCarriesMgmtAndEndFrames(t *testing.T) {
	handler := funcMgmt(func(raw json.RawMessage) json.RawMessage {
		return json.RawMessage(`{"echo":` + string(raw) + `}`)
	})
	r := newPipeRig(t, handler, nil)
	r.client.handshake()
	r.client.send(map[string]any{
		"v": Version, "kind": KindMgmt, "id": "m7", "verb": "whoami",
		"args": []string{"--json"},
	})

	frame := r.client.recvKind(KindMgmtResult)
	if frame["id"] != "m7" {
		t.Errorf("the result carries id=%v, want m7", frame["id"])
	}
	body, err := json.Marshal(frame["result"])
	if err != nil {
		t.Fatalf("re-marshalling the result: %v", err)
	}
	if !strings.Contains(string(body), "whoami") {
		t.Errorf("the handler did not receive the request verbatim: %s", body)
	}

	r.srv.Close()
	end := r.client.recvKind(KindEnd)
	if reason, _ := end["reason"].(string); reason == "" {
		t.Errorf("the end frame names no reason: %+v", end)
	}
}

func TestStdioRefusesAVersionMismatch(t *testing.T) {
	r := newPipeRig(t, nil, nil)
	r.client.send(map[string]any{"v": 99, "kind": KindHello, "client": "test"})
	frame := r.client.recv()
	if frame["kind"] != KindError {
		t.Fatalf("a mismatched version got a %v frame", frame["kind"])
	}
	message, _ := frame["message"].(string)
	if !strings.Contains(message, "99") || !strings.Contains(message, "1") {
		t.Errorf("the refusal %q does not name both versions", message)
	}
}

// The hello reply must be the first frame a peer ever receives. Telemetry is
// enqueued from the tick goroutine from the moment a session exists, so a tick
// that lands between accept and the hello reply used to put a pose on the wire
// first — seen on the arm64 CI leg under QEMU, where the window is wide. A
// session that has not completed hello is not a subscriber yet: frames before
// that are skipped, not dropped.
func TestTheHelloReplyPrecedesAnyTelemetry(t *testing.T) {
	r := newPipeRig(t, nil, nil)
	neutral := r.srv.cfg.Vocabulary.Neutral()
	for i := 0; i < 3; i++ {
		if err := r.srv.Write(neutral); err != nil {
			t.Fatalf("Write before hello: %v", err)
		}
	}
	// handshake sends hello and reads the reply; a pose arriving first fails it.
	r.client.handshake()
	// Once the peer has read its hello it is subscribed: the very next write is
	// owed to it, with no window in which a pose could still be skipped.
	if err := r.srv.Write(neutral); err != nil {
		t.Fatalf("Write after hello: %v", err)
	}
	if got := r.client.recvKind(KindPose); got == nil {
		t.Fatal("no pose frame after the handshake")
	}
	if drops := r.srv.Stats().Drops; drops != 0 {
		t.Errorf("pre-hello frames were counted as drops (%d); they are skipped, not owed", drops)
	}
}
