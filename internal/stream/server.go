package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// stage is the senselog stage token every line this package emits carries, so
// one grep finds everything the endpoint reported.
const stage = "stream"

// socketMode is the socket file's permission: owner only. A world-writable
// socket lets any local user admit intents to a robot.
const socketMode os.FileMode = 0o600

// endFrameGrace bounds how long Close waits on a peer that has stopped reading.
// A consumer that wedged must not be able to wedge the engine's own shutdown.
const endFrameGrace = 250 * time.Millisecond

// flushGrace bounds the shutdown's outbound-queue drain — see
// session.flushWithin and closeWithReason for what it buys.
//
// It is its OWN constant, smaller than endFrameGrace, rather than a share of
// it. The end frame's guarantee is the stronger of the two and must not be
// weakened to strengthen the settle pose's: a consumer that never learns the
// stream ended waits for a heartbeat that is never coming, which is strictly
// worse than one whose last pose is a tick stale. So the drain gets a separate,
// deliberately short bound, and the end frame keeps its full 250 ms after it.
// Against a peer that has stopped reading entirely this makes a shutdown cost
// up to 100 ms more than it used to; against one that is reading it costs
// nothing at all, because the queue is already empty.
const flushGrace = 100 * time.Millisecond

// flushPollEvery is how often that drain re-checks the outbound queue. Short
// next to flushGrace, so a drain that finishes early is noticed promptly, and
// long enough that the polling costs nothing measurable.
const flushPollEvery = time.Millisecond

// reasonStdinClosed is the end frame's reason when a stdio peer closed the
// pipe. It is a variable only because endReason holds a pointer; treat it as a
// constant of the wire, since a consumer greps for it.
var reasonStdinClosed = "stdin-closed"

// Stats is the endpoint's cumulative accounting, readable from any goroutine.
type Stats struct {
	// FramesIn counts inbound frames that were read and dispatched.
	FramesIn uint64
	// FramesOut counts frames actually written to a peer.
	FramesOut uint64
	// Drops counts work this endpoint refused rather than performed: an
	// outbound telemetry frame never written (a full queue, or a frame that
	// would not encode) and a management frame turned away because the session
	// already had its bound of verbs in flight. Every one of them is also a
	// named senselog line, so a grep of the log and a read of the counters
	// agree.
	Drops uint64
	// Refused counts connections turned away because the socket already had
	// an owner.
	Refused uint64
}

// Server is the stream endpoint.
//
// It is the engine's adaptor.Sink (Write), an optional tick.TickSeam rider
// (Seam, which drives heartbeats off the injected clock) and — once Attach has
// been called — a tick.Engine event consumer. It owns exactly one live session
// at a time: a robot has one head, and two clients issuing intents onto one
// tick is a contention nobody can debug.
type Server struct {
	cfg   Config
	sense SenseSink
	mgmt  MgmtHandler
	log   *senselog.Logger

	// stdio holds the reader/writer pair for a NewStdio server; listener is
	// nil in that case, and vice versa.
	stdio    bool
	stdioIn  io.Reader
	stdioOut io.Writer

	engine atomic.Pointer[tick.Engine]

	mu       sync.Mutex
	listener net.Listener
	addr     string
	sess     *session
	closed   bool

	// logMu serializes senselog writes: the tick goroutine, a reader
	// goroutine and a management goroutine can all reach the same io.Writer.
	logMu sync.Mutex

	// stdinClosed is closed once a STDIO session's reader ends; endReason
	// records why the stream is ending when the ENDPOINT learns it before the
	// engine does. Both are nil/unset for a socket server.
	stdinClosed chan struct{}
	stdinOnce   sync.Once
	endReason   atomic.Pointer[string]

	// lastBeat is owned by the goroutine calling Seam — the tick goroutine.
	lastBeat time.Time

	ticks     atomic.Uint64
	framesIn  atomic.Uint64
	framesOut atomic.Uint64
	drops     atomic.Uint64
	refused   atomic.Uint64

	closeOnce sync.Once
}

// New builds a socket server.
//
// eng may be nil, and usually is: tick.New needs its Sink at construction and
// this Server IS that sink, so the two cannot both be built with the other in
// hand. The composition root builds the server, builds the engine over it, then
// closes the cycle with Attach. Passing a non-nil eng here is the same as
// calling Attach immediately.
func New(cfg Config, eng *tick.Engine, sense SenseSink, mgmt MgmtHandler,
	log *senselog.Logger) (*Server, error) {
	return newServer(cfg, eng, sense, mgmt, log, false, nil, nil)
}

// NewStdio builds a server speaking the identical framing over r and w — the
// child-process transport. A consumer that spawns the engine gets lifecycle
// ownership for free: the parent's exit closes the pipes, the reader ends, and
// the engine's supervisor sees it. senselog stays on stderr, so w carries only
// protocol frames.
//
// There is no socket, so Config.Dir, TCPAddr and InsecureTCP are ignored.
func NewStdio(cfg Config, eng *tick.Engine, sense SenseSink, mgmt MgmtHandler,
	log *senselog.Logger, r io.Reader, w io.Writer) (*Server, error) {
	if r == nil || w == nil {
		return nil, errors.New(
			"stream: a stdio endpoint needs both a reader and a writer — give it the " +
				"child's stdin and stdout")
	}
	return newServer(cfg, eng, sense, mgmt, log, true, r, w)
}

func newServer(cfg Config, eng *tick.Engine, sense SenseSink, mgmt MgmtHandler,
	log *senselog.Logger, stdio bool, r io.Reader, w io.Writer) (*Server, error) {
	if err := cfg.normalize(stdio); err != nil {
		return nil, err
	}
	if log == nil {
		log = senselog.Default()
	}
	s := &Server{
		cfg: cfg, sense: sense, mgmt: mgmt, log: log,
		stdio: stdio, stdioIn: r, stdioOut: w,
	}
	if stdio {
		s.stdinClosed = make(chan struct{})
		// A stdio endpoint has exactly one peer and it is known at
		// construction, so its session exists from the start rather than from
		// Serve. That is not a convenience: a pose written between construction
		// and Serve would otherwise vanish with no drop line, which is precisely
		// the invisible no-op this package refuses to have.
		s.sess = newSession(s, r, w, nil)
	}
	if eng != nil {
		s.Attach(eng)
	}
	return s, nil
}

// Attach closes the construction cycle: it records the engine intents are
// forwarded to and registers this server as an OnEvent consumer, so every event
// a seam emits reaches the peer as an event frame.
//
// The consumer runs on the tick goroutine, so it only ever ENQUEUES — a peer
// that stopped reading costs the robot one named drop, not a stalled tick.
func (s *Server) Attach(eng *tick.Engine) {
	if eng == nil || !s.engine.CompareAndSwap(nil, eng) {
		return
	}
	eng.OnEvent(s.onEvent)
}

// Addr is the socket path (or TCP address) this server listens on, or "" before
// Listen and for a stdio server.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// Stats returns the cumulative accounting. Safe from any goroutine.
func (s *Server) Stats() Stats {
	return Stats{
		FramesIn:  s.framesIn.Load(),
		FramesOut: s.framesOut.Load(),
		Drops:     s.drops.Load(),
		Refused:   s.refused.Load(),
	}
}

// Listen creates the listener. It is a no-op for a stdio server, which has
// nothing to listen on.
//
// The socket is created inside Config.Dir and chmod'ed to 0600 immediately. A
// STALE socket file — the remains of a crashed engine — is removed first, but
// only when it really is a socket: unlinking an arbitrary file because it sits
// at the configured path would make a typo destructive.
func (s *Server) Listen() error {
	if s.stdio {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return errors.New("stream: the endpoint is already listening")
	}
	if s.cfg.TCPAddr != "" {
		l, err := s.cfg.listen("tcp", s.cfg.TCPAddr)
		if err != nil {
			return fmt.Errorf("stream: cannot listen on %s: %w", s.cfg.TCPAddr, err)
		}
		s.listener, s.addr = l, l.Addr().String()
		return nil
	}

	path := filepath.Join(s.cfg.Dir, s.cfg.SocketName)
	if err := removeStaleSocket(path); err != nil {
		return err
	}
	l, err := s.cfg.listen("unix", path)
	if err != nil {
		return fmt.Errorf(
			"stream: cannot create the socket %s (%v) — check the directory is writable "+
				"and no other engine owns it", path, err)
	}
	if err := os.Chmod(path, socketMode); err != nil { // #nosec G302 -- 0600 is the point
		_ = l.Close()
		return fmt.Errorf(
			"stream: cannot restrict %s to %#o (%v) — the socket would be readable by "+
				"other local users, so the endpoint refuses to serve it",
			path, socketMode, err)
	}
	s.listener, s.addr = l, path
	return nil
}

// removeStaleSocket unlinks the path only when it is a socket file.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stream: %s cannot be examined (%v)", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf(
			"stream: %s already exists and is not a socket — move it aside, or name a "+
				"different socket", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("stream: the stale socket %s cannot be removed (%v)", path, err)
	}
	return nil
}

// Serve accepts and serves connections until ctx is done or Close is called.
//
// For a stdio server it attaches the single session over the configured reader
// and writer and returns when that session ends.
//
// Exactly ONE session is live at a time. A second connection is answered with a
// structured error frame and closed: the alternative — two clients admitting
// intents onto one tick — is a contention with no owner and no way to attribute
// what the robot did.
func (s *Server) Serve(ctx context.Context) {
	if s.stdio {
		s.serveStdio(ctx)
		return
	}
	s.mu.Lock()
	l := s.listener
	s.mu.Unlock()
	if l == nil {
		return
	}

	go func() {
		<-ctx.Done()
		s.closeWithReason("context-cancelled")
	}()

	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		s.handleConn(conn)
	}
}

func (s *Server) serveStdio(ctx context.Context) {
	s.mu.Lock()
	sess := s.sess
	closed := s.closed
	s.mu.Unlock()
	if sess == nil || closed {
		return
	}

	go func() {
		<-ctx.Done()
		s.closeWithReason("context-cancelled")
	}()
	sess.start()
	sess.wait()
}

// handleConn admits one connection as the session owner, or refuses it.
func (s *Server) handleConn(conn net.Conn) {
	s.mu.Lock()
	busy := s.sess != nil || s.closed
	if !busy {
		s.sess = newSession(s, conn, conn, conn)
	}
	sess := s.sess
	s.mu.Unlock()

	if busy {
		s.refused.Add(1)
		frame := newError(CodeUser,
			"the engine's stream already has an owner",
			"close the live connection first; one socket carries one session, because "+
				"two clients issuing intents onto one tick is a contention with no owner",
			"")
		s.writeDirect(conn, frame)
		_ = conn.Close()
		return
	}
	sess.start()
}

// detach clears the session, freeing the socket for the next owner. It does not
// emit an end frame: the peer is the one that left.
func (s *Server) detach(sess *session) {
	s.mu.Lock()
	if s.sess == sess {
		s.sess = nil
	}
	s.mu.Unlock()
	sess.shutdown()
}

// readerEnded is what a session's reader goroutine reports when it stops — EOF
// or a read error. The two transports mean COMPLETELY different things by it,
// and conflating them is the bug this split exists to fix.
//
//   - On a SOCKET, the peer hung up. The connection is dead in both directions,
//     so the session is detached and the socket is freed for the next owner.
//     The engine keeps running: a robot's tick loop does not belong to whoever
//     last connected, and an engine that stopped every time a debugger
//     disconnected would be unusable.
//   - On STDIO, the parent closed the pipe (or went away). That is the end of
//     the ENGINE's life, because the parent owns it — but the write half is
//     very much still usable, and the session is deliberately left alive so the
//     settling neutral pose and the end frame can still reach a parent that is
//     still reading stdout. Tearing the writer down here is what used to leave
//     the tick loop spinning in an orphan with nowhere to write.
func (s *Server) readerEnded(sess *session) {
	if !s.stdio {
		s.detach(sess)
		return
	}
	s.note("reader", KindEnd, "the stdio peer closed its end of the pipe")
	s.endReason.Store(&reasonStdinClosed)
	s.stdinOnce.Do(func() { close(s.stdinClosed) })
}

// StdinClosed reports the stdio peer's departure, and is nil for a socket
// server.
//
// It is the ONE thing a composition root needs to stop an engine whose parent
// has gone away: the endpoint learns it (its reader hit EOF) and the tick loop
// cannot, since a sink is written to and never called back. A consumer selects
// on it and cancels the run, which is what turns "the pipes closed" into a
// clean stop — settle pose, end frame, exit 0 — instead of an orphan process
// ticking into a closed pipe until somebody signals it.
//
// It is deliberately NOT offered for a socket server. A socket peer
// disconnecting must not stop the engine, so there is no channel there to
// misuse: the difference between the two transports is in the type, not in a
// caution somebody has to read.
func (s *Server) StdinClosed() <-chan struct{} {
	if !s.stdio {
		return nil
	}
	return s.stdinClosed
}

// Close stops the listener, tells the peer the stream ended and waits for the
// session's goroutines. It is idempotent.
func (s *Server) Close() { s.closeWithReason("closed") }

// closeWithReason is Close with the end frame's reason spelled out. The end
// frame takes the DIRECT write path: a dropped end-of-stream leaves a consumer
// waiting for a heartbeat that is never coming, which is the failure c37 exists
// to close.
//
// The three steps happen in this order, and the order is the contract:
//
//  1. FLUSH what is already queued. The engine's settling neutral pose is the
//     last thing it writes on the way out, and it rides the ordinary bounded
//     queue; the end frame does not. Sending the end frame first would let it
//     overtake that pose, and closing the writer first would discard it — in
//     either case the consumer's last word from the robot would be a pose from
//     before the stop, and settling to neutral is precisely the thing a
//     consumer must not have to guess about.
//  2. Send the end frame, naming why.
//  3. Close the session and wait for the writer.
//
// Each step carries its own bound (flushGrace, then endFrameGrace twice), so a
// peer that has stopped reading still cannot wedge the engine's shutdown, and
// the end frame keeps exactly the grace it had before the flush existed.
func (s *Server) closeWithReason(reason string) {
	s.closeOnce.Do(func() {
		// The endpoint sometimes knows the CAUSE before the engine reports the
		// consequence: a stdio peer closing the pipe stops the run, and the
		// engine then returns "stopped" (or a sink error, because its writes
		// are going nowhere). "stdin-closed" is the honest reason in both
		// cases, so a reason the endpoint recorded wins.
		if known := s.endReason.Load(); known != nil {
			reason = *known
		}
		s.mu.Lock()
		s.closed = true
		listener, sess := s.listener, s.sess
		s.sess = nil
		s.mu.Unlock()

		if listener != nil {
			_ = listener.Close()
		}
		if sess != nil {
			sess.flushWithin(flushGrace)
			sess.sendWithin(endOut{V: Version, Kind: KindEnd, Reason: reason},
				endFrameGrace)
			sess.shutdown()
			sess.waitWithin(endFrameGrace)
		}
	})
}

// RunWith runs the engine's Run and guarantees the peer is told when it
// returns, with the reason.
//
// It exists because "the engine stopped" is not something the stream can
// observe on its own — the sink is written to, never called back — and a
// consumer that learns about a stopped engine only by a heartbeat lapse settles
// its robot a second late. A composition root that prefers its own supervision
// can call Close itself instead; this is the convenience that makes the
// guarantee hard to forget.
func (s *Server) RunWith(ctx context.Context, run func(context.Context) error) error {
	err := run(ctx)
	if err != nil {
		s.closeWithReason("engine-error: " + firstLine(err.Error()))
	} else {
		s.closeWithReason("engine-stopped")
	}
	return err
}

// Write implements adaptor.Sink: it enqueues one pose frame.
//
// It NEVER blocks and NEVER returns an error. A stream consumer is telemetry,
// not a transport: a slow or absent one must cost the robot nothing, and a sink
// error here would count against the engine's consecutive-failure ceiling and
// eventually stop a robot because somebody's debugger was paused.
func (s *Server) Write(pose adaptor.Pose) error {
	number := s.ticks.Add(1)
	sess := s.session()
	if sess == nil {
		return nil
	}
	sess.enqueue(KindPose, poseOut{
		V: Version, Kind: KindPose, Tick: number, Channels: pose,
	})
	return nil
}

// Seam is a tick.TickSeam: install it (or call it from a seam that composes
// several riders) to drive heartbeats off the engine's injected clock.
//
// Heartbeats ride the tick rather than a wall-clock timer so that a replay
// under a fake clock produces the same frames, and so the interval means
// "engine time" rather than "the timer goroutine got scheduled". The first tick
// arms the interval; a beat is emitted on the tick whose Now crosses it.
func (s *Server) Seam(c tick.TickContext) {
	if s.cfg.HeartbeatEvery <= 0 {
		return
	}
	if s.lastBeat.IsZero() {
		s.lastBeat = c.Now
		return
	}
	if c.Now.Sub(s.lastBeat) < s.cfg.HeartbeatEvery {
		return
	}
	s.lastBeat = s.lastBeat.Add(s.cfg.HeartbeatEvery)
	sess := s.session()
	if sess == nil {
		return
	}
	sess.enqueue(KindHeartbeat, heartbeatOut{
		V: Version, Kind: KindHeartbeat, Tick: uint64(c.Tick),
		Now: c.Now.UTC().Format(time.RFC3339Nano),
	})
}

// onEvent is the tick.Engine event consumer registered by Attach. It runs on
// the tick goroutine, so it only enqueues.
func (s *Server) onEvent(ev tick.Event) {
	sess := s.session()
	if sess == nil {
		return
	}
	sess.enqueue(KindEvent, eventOut{
		V: Version, Kind: KindEvent, Name: ev.Name, Data: ev.Data,
	})
}

func (s *Server) session() *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sess
}

// writeDirect writes one frame straight to w, for a refusal on a connection
// that never became a session.
func (s *Server) writeDirect(w io.Writer, payload any) {
	body, err := encodeFrame(payload)
	if err != nil {
		return
	}
	if _, err := w.Write(body); err == nil {
		s.framesOut.Add(1)
	}
}

// drop records one dropped outbound frame: a counter AND a named line. A layer
// whose drops are invisible is indistinguishable from one that silently
// no-ops.
func (s *Server) drop(source, reason, detail string) {
	s.drops.Add(1)
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.log.Drop(stage, source, "outbound", reason, detail)
}

// note emits one non-drop stage line.
func (s *Server) note(source, event, detail string) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.log.Stage(stage, source, event, detail)
}

// firstLine reduces a message to something that fits the SENSE grammar's
// one-line shape.
func firstLine(text string) string {
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			return text[:i]
		}
	}
	return text
}
