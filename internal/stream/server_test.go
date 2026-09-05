package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// --- acceptance 1: a sense frame in yields a pose frame within one tick ------

func TestSenseFrameYieldsAPoseFrameWithinOneTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.start(ctx)

	client, _ := r.dial()
	client.handshake()

	client.send(map[string]any{
		"v":    Version,
		"kind": KindSense,
		"fields": map[string]any{
			"lux": 0.5,
		},
	})
	r.sense.await(t)

	// Exactly one tick period of the injected clock.
	r.clock.Advance(r.period)

	frame := client.recvKind(KindPose)
	if frame["v"] != float64(Version) {
		t.Errorf("pose frame carries v=%v, want %d", frame["v"], Version)
	}
	if frame["tick"] != float64(1) {
		t.Errorf("pose frame carries tick=%v, want 1", frame["tick"])
	}
	channels, ok := frame["channels"].(map[string]any)
	if !ok {
		t.Fatalf("pose frame carries no channels object: %+v", frame)
	}
	if len(channels) != 2 {
		t.Errorf("pose frame carries %d channels, want 2: %+v", len(channels), channels)
	}
	// No behavior was admitted, so every channel sits at its declared neutral.
	got := channels["ch_b"].([]any)
	if len(got) != 1 || got[0] != float64(5) {
		t.Errorf("ch_b is %v, want [5]", got)
	}

	updates := r.sense.all()
	if len(updates) != 1 {
		t.Fatalf("the sense sink saw %d updates, want 1", len(updates))
	}
	if updates[0]["lux"] != float64(0.5) {
		t.Errorf("the sense sink saw lux=%v, want 0.5", updates[0]["lux"])
	}
}

// --- acceptance 2: a slow reader drops, never blocks ------------------------

func TestSlowReaderDropsPoseFramesAndNeverBlocksTheTick(t *testing.T) {
	voc := toyVoc(t)
	logBuf := &syncBuffer{}
	blocked := newBlockingWriter()
	// Nobody ever writes into the pipe, so the session's reader goroutine
	// parks: this test is only about the outbound half.
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()

	srv, err := NewStdio(Config{Vocabulary: voc, HeartbeatEvery: -1},
		nil, newRecordingSense(), nil, senselog.New(logBuf), reader, blocked)
	if err != nil {
		t.Fatalf("NewStdio: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// The writer goroutine parks in the first Write, so the outbound queue
	// fills after DefaultOutboundQueue frames and every later one is dropped.
	// A blocked Write here would hang the test rather than fail it, which is
	// exactly the failure this asserts against.
	const writes = 200
	done := make(chan struct{})
	go func() {
		for i := 0; i < writes; i++ {
			if err := srv.Write(voc.Neutral()); err != nil {
				t.Errorf("Write returned %v; a stream consumer must never fail a tick", err)
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Write blocked: the tick was pushed into backpressure")
	}
	close(blocked.release)

	stats := srv.Stats()
	if stats.Drops == 0 {
		t.Fatalf("no frames were dropped after %d writes into a wedged writer", writes)
	}
	if stats.FramesOut >= writes {
		t.Errorf("FramesOut=%d of %d writes; the queue never bounded anything",
			stats.FramesOut, writes)
	}
	line := logBuf.Bytes()
	for _, want := range []string{"stage=stream", "source=" + KindPose, "reason=backpressure"} {
		if !strings.Contains(string(line), want) {
			t.Errorf("the drop log does not name %q:\n%s", want, line)
		}
	}
}

// --- acceptance 3: one owner per socket -------------------------------------

func TestSecondConnectionIsRefusedWithAStructuredErrorFrame(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.start(ctx)

	first, _ := r.dial()
	first.handshake()

	second, raw := r.dial()
	frame := second.recv()
	if frame["kind"] != KindError {
		t.Fatalf("the second connection got a %v frame, want %q", frame["kind"], KindError)
	}
	if frame["code"] != float64(CodeUser) {
		t.Errorf("the refusal carries code=%v, want %d", frame["code"], CodeUser)
	}
	for _, key := range []string{"message", "remediation"} {
		if text, _ := frame[key].(string); text == "" {
			t.Errorf("the refusal carries no %s: %+v", key, frame)
		}
	}
	// And the connection is closed, not left hanging.
	_ = raw.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := raw.Read(buf); err == nil {
		t.Error("the refused connection stayed open")
	}

	// The first connection is untouched.
	r.clock.Advance(r.period)
	first.recvKind(KindPose)
	if refused := r.srv.Stats().Refused; refused != 1 {
		t.Errorf("Stats().Refused=%d, want 1", refused)
	}
}

// --- acceptance 4: heartbeat, end-of-stream, events, protocol version --------

func TestHeartbeatFramesFollowTheInjectedClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, func(c *Config) { c.HeartbeatEvery = 100 * time.Millisecond }, nil)
	r.start(ctx)

	client, _ := r.dial()
	client.handshake()

	// 50 ticks of 20 ms. The first tick arms the interval, so beats land at
	// +100 ms .. +900 ms: nine of them, with no sleeping anywhere.
	r.clock.Advance(50 * r.period)

	beats := 0
	for i := 0; i < 50+9; i++ {
		frame := client.recv()
		if frame["kind"] != KindHeartbeat {
			continue
		}
		beats++
		if frame["v"] != float64(Version) {
			t.Errorf("heartbeat carries v=%v", frame["v"])
		}
		if _, ok := frame["now"].(string); !ok {
			t.Errorf("heartbeat carries no now: %+v", frame)
		}
		if beats == 9 {
			break
		}
	}
	if beats != 9 {
		t.Errorf("saw %d heartbeats over 1 s at 100 ms, want 9", beats)
	}
}

func TestCloseEmitsAnEndOfStreamFrame(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.start(ctx)

	client, _ := r.dial()
	client.handshake()
	r.srv.Close()

	frame := client.recvKind(KindEnd)
	if reason, _ := frame["reason"].(string); reason == "" {
		t.Errorf("the end frame names no reason: %+v", frame)
	}
}

func TestRunWithEmitsAnEndOfStreamFrameWhenRunReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	voc := toyVoc(t)
	clock := tick.NewFakeClock(testPeriod)
	logBuf := &syncBuffer{}

	srv, err := New(Config{Dir: t.TempDir(), Vocabulary: voc, Clock: clock, HeartbeatEvery: -1},
		nil, newRecordingSense(), nil, senselog.New(logBuf))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	eng, err := tick.New(voc, tick.Config{
		Period: testPeriod, Clock: clock, Ticker: clock,
		Log: senselog.New(logBuf), Settle: tick.Bool(false),
	}, srv)
	if err != nil {
		t.Fatalf("tick.New: %v", err)
	}
	srv.Attach(eng)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go srv.Serve(ctx)

	c, err := net.Dial("unix", srv.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	client := &frameConn{rw: c, t: t}
	client.handshake()

	runDone := make(chan error, 1)
	go func() { runDone <- srv.RunWith(ctx, eng.Run) }()
	clock.Advance(2 * testPeriod)
	clock.Stop()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWith did not return")
	}

	frame := client.recvKind(KindEnd)
	if reason, _ := frame["reason"].(string); !strings.Contains(reason, "engine") {
		t.Errorf("the end frame reason is %q, want it to name the engine", reason)
	}
}

func TestEventFramesRideBesidePoseFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.setExtraSeam(func(c tick.TickContext) {
		if c.Tick == 1 {
			c.Emit(tick.Event{Name: "rule_fire", Data: map[string]any{"id": "r1", "n": 3}})
		}
	})
	r.start(ctx)

	client, _ := r.dial()
	client.handshake()
	r.clock.Advance(2 * r.period)

	frame := client.recvKind(KindEvent)
	if frame["name"] != "rule_fire" {
		t.Errorf("event frame carries name=%v, want rule_fire", frame["name"])
	}
	data, ok := frame["data"].(map[string]any)
	if !ok {
		t.Fatalf("event frame carries no data object: %+v", frame)
	}
	if data["id"] != "r1" || data["n"] != float64(3) {
		t.Errorf("event data was not passed through verbatim: %+v", data)
	}
}

func TestEveryOutboundFrameCarriesTheProtocolVersionAndAKind(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, func(c *Config) { c.HeartbeatEvery = 40 * time.Millisecond }, nil)
	r.setExtraSeam(func(c tick.TickContext) {
		if c.Tick == 1 {
			c.Emit(tick.Event{Name: "probe"})
		}
	})
	r.start(ctx)

	client, _ := r.dial()
	reply := client.handshake()
	if reply["v"] != float64(Version) {
		t.Errorf("the hello reply carries v=%v", reply["v"])
	}
	r.clock.Advance(6 * r.period)

	kinds := map[string]bool{}
	for i := 0; i < 10; i++ {
		frame := client.recv()
		if frame["v"] != float64(Version) {
			t.Errorf("a %v frame carries v=%v, want %d", frame["kind"], frame["v"], Version)
		}
		kind, _ := frame["kind"].(string)
		if kind == "" {
			t.Errorf("a frame carries no kind: %+v", frame)
		}
		kinds[kind] = true
		if kinds[KindPose] && kinds[KindEvent] && kinds[KindHeartbeat] {
			break
		}
	}
	for _, want := range []string{KindPose, KindEvent, KindHeartbeat} {
		if !kinds[want] {
			t.Errorf("no %q frame arrived; saw %v", want, kinds)
		}
	}
}

func TestAVersionMismatchIsRefusedNamingBothVersions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.start(ctx)

	client, raw := r.dial()
	client.send(map[string]any{"v": Version + 1, "kind": KindHello, "client": "test"})

	frame := client.recv()
	if frame["kind"] != KindError {
		t.Fatalf("a mismatched version got a %v frame, want %q", frame["kind"], KindError)
	}
	message, _ := frame["message"].(string)
	for _, want := range []string{fmt.Sprint(Version), fmt.Sprint(Version + 1)} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal %q does not name version %s", message, want)
		}
	}
	_ = raw.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := raw.Read(buf); err == nil {
		t.Error("the connection stayed open after a version mismatch")
	}
}

func TestAFrameWithNoVersionIsRefused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.start(ctx)

	client, _ := r.dial()
	client.send(map[string]any{"kind": KindHello, "client": "test"})
	frame := client.recv()
	if frame["kind"] != KindError {
		t.Fatalf("a frame with no version got %v, want an error frame", frame["kind"])
	}
}

// --- acceptance 5: 0600 unix socket, no TCP ---------------------------------

func TestTheSocketIsCreated0600InsideTheConfiguredDirectory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	r := newRig(t, func(c *Config) { c.Dir = dir }, nil)
	r.start(ctx)

	path := r.srv.Addr()
	if filepath.Dir(path) != dir {
		t.Errorf("the socket is at %s, outside the configured %s", path, dir)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode()&fs.ModeSocket == 0 {
		t.Errorf("%s is not a socket (mode %v)", path, info.Mode())
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the socket is mode %#o, want 0600", perm)
	}
}

func TestAMissingOrNonDirectoryDirIsRefused(t *testing.T) {
	voc := toyVoc(t)
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	for name, dir := range map[string]string{"empty": "", "a file": file} {
		if _, err := New(Config{Dir: dir, Vocabulary: voc}, nil,
			newRecordingSense(), nil, nil); err == nil {
			t.Errorf("New accepted %s as Dir", name)
		}
	}
}

// TestNoTCPListenerIsEverCreated is the no-TCP assertion.
//
// Strategy: a WRAPPED LISTENER FACTORY, not a /proc scan. Every listener this
// package creates goes through Config.listen (defaulting to net.Listen), and
// TestEveryListenerGoesThroughTheFactory pins mechanically that net.Listen is
// named in exactly one place in the package's non-test sources. So recording
// what the factory was asked for is a total account of what was listened on —
// no sampling, no race with a listener that opened and closed between the
// startup and the scan, and no dependence on /proc being mounted or on inode
// bookkeeping that differs between kernels and containers.
func TestNoTCPListenerIsEverCreated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var asked []string
	r := newRig(t, func(c *Config) {
		c.listen = func(network, addr string) (net.Listener, error) {
			asked = append(asked, network)
			return net.Listen(network, addr)
		}
	}, nil)
	r.start(ctx)

	client, _ := r.dial()
	client.handshake()
	r.clock.Advance(3 * r.period)
	client.recvKind(KindPose)

	if len(asked) == 0 {
		t.Fatal("no listener was created at all; the factory is not the only path")
	}
	for _, network := range asked {
		if strings.HasPrefix(network, "tcp") {
			t.Errorf("a %q listener was created; the engine is unix-socket-only", network)
		}
		if network != "unix" {
			t.Errorf("an unexpected %q listener was created", network)
		}
	}
}

func TestATCPAddressIsRefusedWithoutInsecureTCP(t *testing.T) {
	voc := toyVoc(t)
	called := 0
	factory := func(network, addr string) (net.Listener, error) {
		called++
		return net.Listen(network, addr)
	}
	cfg := Config{Dir: t.TempDir(), Vocabulary: voc, TCPAddr: "127.0.0.1:0", listen: factory}

	srv, err := New(cfg, nil, newRecordingSense(), nil, nil)
	if err == nil {
		srv.Close()
		t.Fatal("New accepted a TCP address without InsecureTCP")
	}
	if !strings.Contains(err.Error(), "InsecureTCP") {
		t.Errorf("the refusal %q does not name the flag that would allow it", err)
	}
	if called != 0 {
		t.Errorf("the listener factory ran %d times while refusing a TCP address", called)
	}
}

func TestAnExplicitlyInsecureTCPAddressIsHonoured(t *testing.T) {
	voc := toyVoc(t)
	cfg := Config{
		Vocabulary: voc, TCPAddr: "127.0.0.1:0", InsecureTCP: true, HeartbeatEvery: -1,
	}
	srv, err := New(cfg, nil, newRecordingSense(), nil, nil)
	if err != nil {
		t.Fatalf("New refused an explicitly insecure TCP address: %v", err)
	}
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer srv.Close()
	if !strings.Contains(srv.Addr(), "127.0.0.1:") {
		t.Errorf("Addr()=%q, want a loopback TCP address", srv.Addr())
	}
}

// --- intents, management, framing limits ------------------------------------

func TestAnAdmitIntentReachesTheEngine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.start(ctx)

	client, _ := r.dial()
	client.handshake()
	client.send(map[string]any{
		"v": Version, "kind": KindIntent, "op": OpAdmit,
		"action": "ramp", "class": string(tick.ClassStoppable), "duration_s": 5.0,
		"params": map[string]any{"gain": 0.5},
	})

	// The intent goes through the engine inbox, so it lands on a later tick.
	waitForChA(t, r, client, func(v float64) bool { return v != 0 })
}

func TestAnEvictIntentReachesTheEngine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.start(ctx)

	client, _ := r.dial()
	client.handshake()
	client.send(map[string]any{
		"v": Version, "kind": KindIntent, "op": OpAdmit,
		"action": "ramp", "class": string(tick.ClassStoppable), "duration_s": 5.0,
	})
	// Wait for the admit to take effect, so the eviction is observable.
	waitForChA(t, r, client, func(v float64) bool { return v != 0 })

	client.send(map[string]any{"v": Version, "kind": KindIntent, "op": OpEvict, "name": "ramp"})
	waitForChA(t, r, client, func(v float64) bool { return v == 0 })
}

// waitForChA advances the clock one tick at a time until a pose frame's ch_a
// satisfies want, failing rather than hanging if it never does.
func waitForChA(t *testing.T, r *rig, client *frameConn, want func(float64) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		r.clock.Advance(r.period)
		frame := client.recvKind(KindPose)
		channels := frame["channels"].(map[string]any)
		if want(channels["ch_a"].([]any)[0].(float64)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ch_a never reached the wanted value; last frame %+v", frame)
		}
	}
}

func TestAnUndeclaredIntentIsRefusedWithAStructuredError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.start(ctx)

	client, _ := r.dial()
	client.handshake()
	client.send(map[string]any{
		"v": Version, "kind": KindIntent, "op": OpAdmit,
		"action": "no-such-action", "class": string(tick.ClassStoppable), "duration_s": 1.0,
	})
	frame := client.recvKind(KindError)
	if frame["code"] != float64(CodeUser) {
		t.Errorf("the refusal carries code=%v, want %d", frame["code"], CodeUser)
	}
	// The stream survives a refused intent.
	r.clock.Advance(r.period)
	client.recvKind(KindPose)
}

func TestAManagementRequestIsAnsweredWithoutPausingTheStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	handler := funcMgmt(func(raw json.RawMessage) json.RawMessage {
		<-release
		return json.RawMessage(`{"ok":true}`)
	})
	r := newRig(t, nil, handler)
	r.start(ctx)

	client, _ := r.dial()
	client.handshake()
	client.send(map[string]any{
		"v": Version, "kind": KindMgmt, "id": "m1", "verb": "version", "json": true,
	})

	// The handler is parked, and pose frames keep flowing.
	r.clock.Advance(5 * r.period)
	for i := 0; i < 3; i++ {
		client.recvKind(KindPose)
	}
	close(release)

	frame := client.recvKind(KindMgmtResult)
	if frame["id"] != "m1" {
		t.Errorf("the result carries id=%v, want m1", frame["id"])
	}
	result, ok := frame["result"].(map[string]any)
	if !ok || result["ok"] != true {
		t.Errorf("the handler's body did not come back verbatim: %+v", frame)
	}
	if r.eng.Stats().Overruns != 0 {
		t.Errorf("the management request cost %d overrun ticks", r.eng.Stats().Overruns)
	}
}

func TestAManagementRequestWithNoHandlerIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.start(ctx)

	client, _ := r.dial()
	client.handshake()
	client.send(map[string]any{"v": Version, "kind": KindMgmt, "id": "m1", "verb": "version"})

	frame := client.recvKind(KindError)
	if message, _ := frame["message"].(string); !strings.Contains(message, "management handler") {
		t.Errorf("the refusal %q does not name the missing handler", message)
	}
	if frame["id"] != "m1" {
		t.Errorf("the refusal does not correlate with the request: %+v", frame)
	}
}

func TestTheFirstFrameMustBeHello(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.start(ctx)

	client, _ := r.dial()
	client.send(map[string]any{"v": Version, "kind": KindSense, "fields": map[string]any{}})
	frame := client.recv()
	if frame["kind"] != KindError {
		t.Fatalf("a leading %q frame was accepted", KindSense)
	}
	if message, _ := frame["message"].(string); !strings.Contains(message, KindHello) {
		t.Errorf("the refusal %q does not name the frame that must come first", message)
	}
}

func TestAnOversizeFrameIsRefused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.start(ctx)

	client, raw := r.dial()
	client.handshake()
	client.sendRaw(MaxFrameBytes+1, nil)

	frame := client.recvKind(KindError)
	if message, _ := frame["message"].(string); !strings.Contains(message, fmt.Sprint(MaxFrameBytes)) {
		t.Errorf("the refusal %q does not name the maximum frame size", message)
	}
	_ = raw.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := raw.Read(buf); err == nil {
		t.Error("the connection stayed open after an oversize frame")
	}
}

func TestAClientThatLeavesFreesTheSocketForTheNextOwner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.start(ctx)

	first, raw := r.dial()
	first.handshake()
	_ = raw.Close()

	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.Dial("unix", r.srv.Addr())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		client := &frameConn{rw: c, t: t}
		frame := client.recvOrHandshake()
		if frame == nil {
			_ = c.Close()
			return // the second owner was accepted
		}
		_ = c.Close()
		if time.Now().After(deadline) {
			t.Fatal("the socket never freed up after its owner left")
		}
	}
}

// recvOrHandshake sends a hello and returns the error frame if the server
// refused, or nil once the handshake succeeded.
func (f *frameConn) recvOrHandshake() map[string]any {
	f.t.Helper()
	f.send(map[string]any{"v": Version, "kind": KindHello, "client": "test"})
	frame := f.recv()
	if frame["kind"] == KindHello {
		return nil
	}
	return frame
}

func TestStatsCountFramesInAndOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRig(t, nil, nil)
	r.start(ctx)

	client, _ := r.dial()
	client.handshake()
	client.send(map[string]any{
		"v": Version, "kind": KindSense, "fields": map[string]any{"lux": 1.0},
	})
	r.sense.await(t)
	r.clock.Advance(r.period)
	client.recvKind(KindPose)

	stats := r.srv.Stats()
	if stats.FramesIn < 2 {
		t.Errorf("FramesIn=%d, want at least the hello and the sense frame", stats.FramesIn)
	}
	if stats.FramesOut < 2 {
		t.Errorf("FramesOut=%d, want at least the hello reply and one pose", stats.FramesOut)
	}
}

// TestWriteWithNoConsumerIsANoOp pins that an unattended socket costs the tick
// nothing and reports nothing: a "drop" per tick with nobody connected would
// bury the real drops it exists to make visible.
func TestWriteWithNoConsumerIsANoOp(t *testing.T) {
	voc := toyVoc(t)
	logBuf := &syncBuffer{}
	srv, err := New(Config{Dir: t.TempDir(), Vocabulary: voc, HeartbeatEvery: -1},
		nil, newRecordingSense(), nil, senselog.New(logBuf))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()
	for i := 0; i < 100; i++ {
		if err := srv.Write(adaptor.Pose{}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if drops := srv.Stats().Drops; drops != 0 {
		t.Errorf("Stats().Drops=%d with nobody connected, want 0", drops)
	}
	if text := string(logBuf.Bytes()); text != "" {
		t.Errorf("an unattended socket logged:\n%s", text)
	}
}
