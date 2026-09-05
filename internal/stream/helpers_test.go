package stream

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// The toy vocabulary every test in this package runs against.
//
// Like internal/tick's, it names NOTHING either donor robot names: a fixture
// that borrowed a real robot's words would quietly make the wire protocol look
// plant-specific. Channel arities differ (2, 1) so a framing bug that assumes a
// uniform width cannot hide.
const toyVocabulary = `{
  "channels": [
    {"name": "ch_a", "arity": 2, "neutral": [0.0, 0.0]},
    {"name": "ch_b", "arity": 1, "neutral": [5.0]}
  ],
  "senses": [
    {"name": "lux", "type": "float"}
  ],
  "actions": [
    {
      "name": "ramp",
      "claims": ["ch_a"],
      "params": [{"name": "gain", "min": 0.0, "max": 1.0}],
      "trajectories": {
        "ch_a": {"easing": {"kind": "linear", "from": [0.0, 0.0],
                            "to": [10.0, 20.0], "duration_s": 10.0}}
      }
    }
  ]
}`

func toyVoc(t *testing.T) *adaptor.Vocabulary {
	t.Helper()
	v, err := adaptor.Parse([]byte(toyVocabulary))
	if err != nil {
		t.Fatalf("parsing the toy vocabulary: %v", err)
	}
	return v
}

// recordingSense is the SenseSink double: it records every update and signals
// one on a channel, so a test can wait for a sense frame to have LANDED rather
// than sleeping and hoping.
type recordingSense struct {
	mu      sync.Mutex
	updates []map[string]any
	times   []time.Time
	seen    chan struct{}
}

func newRecordingSense() *recordingSense {
	return &recordingSense{seen: make(chan struct{}, 64)}
}

func (r *recordingSense) Update(fields map[string]any, now time.Time) {
	r.mu.Lock()
	r.updates = append(r.updates, fields)
	r.times = append(r.times, now)
	r.mu.Unlock()
	select {
	case r.seen <- struct{}{}:
	default:
	}
}

func (r *recordingSense) await(t *testing.T) {
	t.Helper()
	select {
	case <-r.seen:
	case <-time.After(2 * time.Second):
		t.Fatal("no sense update arrived within 2s")
	}
}

func (r *recordingSense) all() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]map[string]any, len(r.updates))
	copy(out, r.updates)
	return out
}

// funcMgmt is the MgmtHandler double.
type funcMgmt func(json.RawMessage) json.RawMessage

func (f funcMgmt) HandleJSON(raw json.RawMessage) json.RawMessage { return f(raw) }

// syncBuffer is an io.Writer safe to read from a test goroutine while the
// session's writer goroutine appends to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf.Bytes()...)
}

// blockingWriter is the "slow reader": every Write parks until release is
// closed. It is how the backpressure test makes the outbound queue fill
// deterministically, with no sleeping and no reliance on a socket buffer size.
type blockingWriter struct {
	release chan struct{}
	mu      sync.Mutex
	n       int
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{release: make(chan struct{})}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	<-b.release
	b.mu.Lock()
	b.n += len(p)
	b.mu.Unlock()
	return len(p), nil
}

// frameConn reads and writes protocol frames over a net.Conn or a pipe.
type frameConn struct {
	rw io.ReadWriter
	t  *testing.T
}

func (f *frameConn) send(payload any) {
	f.t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatalf("marshalling %+v: %v", payload, err)
	}
	prefix := make([]byte, 4)
	binary.BigEndian.PutUint32(prefix, uint32(len(body)))
	if _, err := f.rw.Write(append(prefix, body...)); err != nil {
		f.t.Fatalf("writing a frame: %v", err)
	}
}

// sendRaw writes an arbitrary length prefix and body, for the malformed-frame
// tests that must not go through the well-behaved encoder.
func (f *frameConn) sendRaw(length uint32, body []byte) {
	f.t.Helper()
	prefix := make([]byte, 4)
	binary.BigEndian.PutUint32(prefix, length)
	if _, err := f.rw.Write(append(prefix, body...)); err != nil {
		f.t.Fatalf("writing a raw frame: %v", err)
	}
}

// recv reads one frame and decodes it into a generic map.
func (f *frameConn) recv() map[string]any {
	f.t.Helper()
	var prefix [4]byte
	if _, err := io.ReadFull(f.rw, prefix[:]); err != nil {
		f.t.Fatalf("reading a frame prefix: %v", err)
	}
	body := make([]byte, binary.BigEndian.Uint32(prefix[:]))
	if _, err := io.ReadFull(f.rw, body); err != nil {
		f.t.Fatalf("reading a frame body: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		f.t.Fatalf("decoding a frame body %q: %v", body, err)
	}
	return out
}

// recvKind reads frames until one of the wanted kind arrives, failing after a
// bounded number of frames so a wrong-kind stream cannot hang the suite.
func (f *frameConn) recvKind(kind string) map[string]any {
	f.t.Helper()
	for i := 0; i < 4096; i++ {
		frame := f.recv()
		if frame["kind"] == kind {
			return frame
		}
	}
	f.t.Fatalf("no %q frame in the first 4096 frames", kind)
	return nil
}

// handshake performs the mandatory hello exchange and returns the reply.
func (f *frameConn) handshake() map[string]any {
	f.t.Helper()
	f.send(map[string]any{"v": Version, "kind": KindHello, "client": "test"})
	reply := f.recv()
	if reply["kind"] != KindHello {
		f.t.Fatalf("first reply is %v, want %q", reply["kind"], KindHello)
	}
	return reply
}

// rig is a running engine plus a stream server wired as its sink, with the
// deterministic clock the whole suite drives by hand.
type rig struct {
	t      *testing.T
	voc    *adaptor.Vocabulary
	clock  *tick.FakeClock
	eng    *tick.Engine
	srv    *Server
	sense  *recordingSense
	logBuf *syncBuffer
	period time.Duration

	runDone chan error
	seamMu  sync.Mutex
	extra   func(tick.TickContext)
}

const testPeriod = 20 * time.Millisecond

// newRig builds an engine + server pair. cfgFn may adjust the stream Config
// before the server is constructed.
func newRig(t *testing.T, cfgFn func(*Config), mgmt MgmtHandler) *rig {
	t.Helper()
	voc := toyVoc(t)
	clock := tick.NewFakeClock(testPeriod)
	logBuf := &syncBuffer{}
	sense := newRecordingSense()

	r := &rig{
		t: t, voc: voc, clock: clock, sense: sense,
		logBuf: logBuf, period: testPeriod, runDone: make(chan error, 1),
	}

	cfg := Config{
		Dir:            t.TempDir(),
		Vocabulary:     voc,
		Clock:          clock,
		HeartbeatEvery: -1, // off unless a test asks for it
		// Backpressure has its own test (over stdio, with a wedged writer).
		// Every other test drives the clock in bursts while the client is not
		// reading, so a production-sized queue would drop the very frames
		// under assertion and make an unrelated test flaky.
		OutboundQueue: 4096,
	}
	if cfgFn != nil {
		cfgFn(&cfg)
	}

	// The server is the engine's sink, so it must exist before the engine.
	srv, err := New(cfg, nil, sense, mgmt, senselog.New(logBuf))
	if err != nil {
		t.Fatalf("stream.New: %v", err)
	}
	eng, err := tick.New(voc, tick.Config{
		Period: testPeriod,
		Clock:  clock,
		Ticker: clock,
		Log:    senselog.New(logBuf),
		Settle: tick.Bool(false),
	}, srv)
	if err != nil {
		t.Fatalf("tick.New: %v", err)
	}
	srv.Attach(eng)
	r.eng, r.srv = eng, srv
	return r
}

// start listens, serves and runs the engine, installing the server's own seam
// (plus any per-test extra) so heartbeats are driven by the injected clock.
func (r *rig) start(ctx context.Context) {
	r.t.Helper()
	if err := r.srv.Listen(); err != nil {
		r.t.Fatalf("Listen: %v", err)
	}
	go r.srv.Serve(ctx)
	if !r.eng.Send(tick.SetSeamCmd{Seam: r.seam}) {
		r.t.Fatal("the engine refused the seam command")
	}
	go func() { r.runDone <- r.eng.Run(ctx) }()
	r.t.Cleanup(func() {
		r.clock.Stop()
		select {
		case <-r.runDone:
		case <-time.After(2 * time.Second):
			r.t.Error("the engine's Run did not return within 2s")
		}
		r.srv.Close()
	})
}

func (r *rig) setExtraSeam(fn func(tick.TickContext)) {
	r.seamMu.Lock()
	r.extra = fn
	r.seamMu.Unlock()
}

func (r *rig) seam(c tick.TickContext) {
	r.srv.Seam(c)
	r.seamMu.Lock()
	extra := r.extra
	r.seamMu.Unlock()
	if extra != nil {
		extra(c)
	}
}

// dial connects a client to the rig's unix socket.
func (r *rig) dial() (*frameConn, net.Conn) {
	r.t.Helper()
	c, err := net.Dial("unix", r.srv.Addr())
	if err != nil {
		r.t.Fatalf("dialling %s: %v", r.srv.Addr(), err)
	}
	r.t.Cleanup(func() { _ = c.Close() })
	return &frameConn{rw: c, t: r.t}, c
}

func (r *rig) logText() string { return string(r.logBuf.Bytes()) }
