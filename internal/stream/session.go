package stream

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// session is one live peer: a reader goroutine draining inbound frames, a
// writer goroutine draining the bounded outbound queue, and the direct write
// path the control frames take.
//
// Only ONE exists per Server at a time. The two write paths share wmu so a
// direct control frame can never interleave inside a queued telemetry frame —
// a half-frame on the wire desynchronises the peer permanently.
type session struct {
	srv    *Server
	r      io.Reader
	w      io.Writer
	closer io.Closer

	out  chan []byte
	quit chan struct{}

	wmu        sync.Mutex
	once       sync.Once
	writerDone chan struct{}

	// queued counts frames ACCEPTED onto the outbound queue; written counts
	// frames the writer goroutine has finished writing. The shutdown flush
	// waits for the two to meet.
	//
	// "the channel is empty" is NOT the same question, and the difference is a
	// real race: the writer receives a frame (emptying the channel) and only
	// then locks wmu, so a flush that stopped at an empty channel could let the
	// end frame win that lock and overtake the very pose it was waiting for.
	// A counter the writer bumps AFTER its Write has no such window.
	queued  atomic.Uint64
	written atomic.Uint64

	// greeted is touched only by the reader goroutine.
	greeted bool
}

func newSession(srv *Server, r io.Reader, w io.Writer, closer io.Closer) *session {
	return &session{
		srv: srv, r: r, w: w, closer: closer,
		out:        make(chan []byte, srv.cfg.OutboundQueue),
		quit:       make(chan struct{}),
		writerDone: make(chan struct{}),
	}
}

func (s *session) start() {
	go s.writeLoop()
	go s.readLoop()
}

// shutdown releases the session without waiting. It is safe to call from the
// reader goroutine itself, which is what makes "the peer hung up" and "the
// server closed" the same code path.
func (s *session) shutdown() {
	s.once.Do(func() {
		close(s.quit)
		if s.closer != nil {
			_ = s.closer.Close()
		}
	})
}

// sendWithin is send with a bound on how long a wedged peer may hold up the
// shutdown.
//
// The end-of-stream frame must be DELIVERED when anyone is listening, but the
// writer it goes to may be a pipe whose reader has stopped, and a Close that
// waited on that forever would turn a dead consumer into a dead engine — the
// exact inversion this package exists to prevent. So the write is handed to its
// own goroutine and given a grace period; past that, shutdown proceeds and the
// frame lands if and when the peer ever reads again.
func (s *session) sendWithin(payload any, grace time.Duration) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.send(payload)
	}()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

// flushWithin waits for the outbound queue to drain, bounded by the same grace.
//
// It is what makes the engine's SETTLING NEUTRAL POSE deterministic. That pose
// is the last thing the tick loop writes on the way out — a robot must not be
// left holding whatever the last tick happened to compose — and it goes onto
// the bounded queue like any other telemetry. The end-of-stream frame, by
// contrast, takes the DIRECT write path. Without this wait the two race for
// wmu: the end frame can be written between two queued poses, and the shutdown
// that follows closes the writer with the settle pose still in the queue, so
// the consumer's last word from the robot is a pose from before the stop.
//
// Draining first costs nothing when the peer is reading (the queue is empty and
// this returns on the first poll) and is bounded by the grace when it is not —
// a wedged consumer still cannot wedge the engine's own shutdown, which is the
// property the grace exists to protect.
//
// It returns once every frame ACCEPTED onto the queue has been WRITTEN — not
// merely once the channel is empty. The distinction is the whole correctness of
// this function: the writer goroutine receives a frame from the channel and
// only afterwards locks wmu, so between those two moments the queue is empty
// while the frame is still unwritten, and an end frame racing for wmu would
// win. Waiting on the written counter closes that window.
//
// A writer that dies mid-drain ends the wait too (writerDone), because there is
// then nothing left that could ever advance the counter.
func (s *session) flushWithin(grace time.Duration) {
	if grace <= 0 {
		return
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	poll := time.NewTicker(flushPollEvery)
	defer poll.Stop()
	for {
		if s.written.Load() >= s.queued.Load() {
			return
		}
		select {
		case <-poll.C:
		case <-s.quit:
			return
		case <-s.writerDone:
			return
		case <-deadline.C:
			return
		}
	}
}

// waitWithin is wait with the same bound, for the same reason.
func (s *session) waitWithin(grace time.Duration) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-s.writerDone:
	case <-timer.C:
	}
}

// wait blocks until the writer goroutine has finished.
//
// It deliberately does NOT wait for the reader: a stdio session's reader is
// parked in a read on the parent's pipe, which only ends when the parent goes
// away, and a Close that waited for it would hang the engine's own shutdown.
// The writer is the one that must finish, because it owns the wire.
func (s *session) wait() { <-s.writerDone }

// send writes one frame directly, bypassing the queue. It is the control path:
// the hello reply, every refusal, every management result and the end-of-stream
// frame. None of those comes from the tick goroutine, so blocking here is safe,
// and a dropped one would leave the peer waiting for something that is never
// coming.
func (s *session) send(payload any) error {
	body, err := encodeFrame(payload)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	select {
	case <-s.quit:
		return errors.New("stream: the session is closed")
	default:
	}
	if _, err := s.w.Write(body); err != nil {
		return err
	}
	s.srv.framesOut.Add(1)
	return nil
}

// enqueue puts one telemetry frame on the bounded queue, or DROPS it with a
// named line. It never blocks: pose, event and heartbeat frames are emitted
// from the tick goroutine, and at 50 Hz the budget is 20 ms.
//
// The drop is newest-first — the frame in hand is the one discarded — so a
// consumer that catches up receives a contiguous older window rather than a
// shuffled one.
func (s *session) enqueue(kind string, payload any) {
	select {
	case <-s.quit:
		return
	default:
	}
	body, err := encodeFrame(payload)
	if err != nil {
		s.srv.drop(kind, "frame-too-large", err.Error())
		return
	}
	select {
	case s.out <- body:
		s.queued.Add(1)
	default:
		s.srv.drop(kind, "backpressure", fmt.Sprintf(
			"the consumer is not reading; the outbound queue is full at %d frames",
			cap(s.out)))
	}
}

func (s *session) writeLoop() {
	defer close(s.writerDone)
	for {
		select {
		case body := <-s.out:
			s.wmu.Lock()
			_, err := s.w.Write(body)
			s.wmu.Unlock()
			if err != nil {
				s.srv.note("writer", "outbound", "the peer's connection failed: "+
					firstLine(err.Error()))
				s.srv.detach(s)
				return
			}
			s.written.Add(1)
			s.srv.framesOut.Add(1)
		case <-s.quit:
			return
		}
	}
}

func (s *session) readLoop() {
	// readerEnded, not detach: on a socket the peer hung up and the session
	// goes with it, but on stdio the WRITE half is still usable and the
	// endpoint has one more thing to say — see Server.readerEnded.
	defer s.srv.readerEnded(s)
	for {
		body, err := readFrame(s.r)
		if err != nil {
			if errors.Is(err, errFrameTooLarge) {
				_ = s.send(newError(CodeUser, fmt.Sprintf(
					"a frame declares a length above the %d-byte maximum", MaxFrameBytes),
					"split the payload; an oversize length prefix cannot be skipped "+
						"safely, so the connection is closed", ""))
			}
			return
		}
		s.srv.framesIn.Add(1)
		if !s.dispatch(body) {
			return
		}
	}
}

// dispatch handles one inbound frame and reports whether the session survives
// it. A protocol violation — a wrong version, a missing handshake, an unreadable
// frame — is fatal; a request the endpoint simply cannot serve is answered with
// an error frame and the stream continues.
func (s *session) dispatch(body []byte) bool {
	var h header
	if err := json.Unmarshal(body, &h); err != nil {
		_ = s.send(newError(CodeUser,
			"a frame is not a JSON object: "+firstLine(err.Error()),
			"send a JSON object carrying at least \"v\" and \"kind\"", ""))
		return false
	}
	if h.V == nil {
		_ = s.send(newError(CodeUser, fmt.Sprintf(
			"a frame declares no protocol version; this engine speaks version %d",
			Version),
			fmt.Sprintf("add \"v\": %d to every frame", Version), ""))
		return false
	}
	if *h.V != Version {
		_ = s.send(newError(CodeUser, fmt.Sprintf(
			"the client speaks protocol version %d; this engine speaks version %d",
			*h.V, Version),
			"upgrade the client or the engine so both speak the same version; a "+
				"half-understood wire protocol is worse than a refused connection", ""))
		return false
	}

	if !s.greeted {
		if h.Kind != KindHello {
			_ = s.send(newError(CodeUser, fmt.Sprintf(
				"the first frame is %q; the first frame must be %q", h.Kind, KindHello),
				fmt.Sprintf("send {\"v\": %d, \"kind\": %q, \"client\": \"...\"} first",
					Version, KindHello), ""))
			return false
		}
		return s.handleHello(body)
	}

	switch h.Kind {
	case KindHello:
		_ = s.send(newError(CodeUser,
			"this session has already been greeted",
			"send one hello per connection", ""))
		return false
	case KindSense:
		return s.handleSense(body)
	case KindIntent:
		return s.handleIntent(body)
	case KindMgmt:
		return s.handleMgmt(body)
	default:
		_ = s.send(newError(CodeUser,
			fmt.Sprintf("the engine has no handler for a %q frame", h.Kind),
			fmt.Sprintf("send one of: %q, %q, %q", KindSense, KindIntent, KindMgmt), ""))
		return true
	}
}

func (s *session) handleHello(body []byte) bool {
	var in helloIn
	_ = json.Unmarshal(body, &in) // the client name is informational
	s.greeted = true
	s.srv.note("hello", KindHello, "a client attached: "+sanitizeClient(in.Client))
	return s.send(helloOut{
		V: Version, Kind: KindHello, EngineVersion: s.srv.cfg.EngineVersion,
	}) == nil
}

func sanitizeClient(name string) string {
	if name == "" {
		return "(unnamed)"
	}
	if len(name) > 64 {
		return name[:64]
	}
	return name
}

func (s *session) handleSense(body []byte) bool {
	if s.srv.sense == nil {
		_ = s.send(newError(CodeEnv,
			"no sense sink is installed, so sense frames have nowhere to land",
			"give the stream a SenseSink at construction", ""))
		return true
	}
	var in senseIn
	if err := json.Unmarshal(body, &in); err != nil {
		_ = s.send(newError(CodeUser,
			"a sense frame could not be decoded: "+firstLine(err.Error()),
			"send \"fields\" as an object of name to value (or null)", ""))
		return true
	}
	// Values are passed through exactly as decoded. A transport that coerced
	// them would be reinterpreting a robot's readings.
	s.srv.sense.Update(in.Fields, s.srv.cfg.Clock.Now())
	return true
}

func (s *session) handleIntent(body []byte) bool {
	eng := s.srv.engine.Load()
	if eng == nil {
		_ = s.send(newError(CodeEnv,
			"no engine is attached, so an intent has nowhere to go",
			"call Attach with the engine this stream sinks for", ""))
		return true
	}
	var in intentIn
	if err := json.Unmarshal(body, &in); err != nil {
		_ = s.send(newError(CodeUser,
			"an intent frame could not be decoded: "+firstLine(err.Error()),
			fmt.Sprintf("send \"op\" as %q or %q", OpAdmit, OpEvict), ""))
		return true
	}

	switch in.Op {
	case OpEvict:
		if in.Name == "" {
			_ = s.send(newError(CodeUser,
				"an evict intent names nothing to evict",
				"give \"name\" a behavior id or library name", in.ID))
			return true
		}
		s.sendCommand(tick.EvictCmd{Name: in.Name}, in.ID)
	case OpAdmit:
		s.admit(eng, in)
	default:
		_ = s.send(newError(CodeUser,
			fmt.Sprintf("an intent declares op %q", in.Op),
			fmt.Sprintf("use %q or %q", OpAdmit, OpEvict), in.ID))
	}
	return true
}

// admit binds the requested behavior and forwards it.
//
// Binding HERE, when a vocabulary is configured, is what turns "the robot
// silently ignored my intent" into a refusal the client can read: the engine
// would bind it too, but its refusal is a named drop on stderr that the peer
// never sees.
func (s *session) admit(eng *tick.Engine, in intentIn) {
	class := tick.StopClass(in.Class)
	if in.Class == "" {
		// The polite default: it drives, and yields to a stopping behavior.
		class = tick.ClassStoppable
	}
	behavior := tick.Behavior{
		Name:     in.Name,
		ID:       in.ID,
		Class:    class,
		Channels: in.Channels,
		Action:   in.Action,
		Params:   in.Params,
		Lifetime: tick.Lifetime{DurationS: in.DurationS, Loops: in.Loops},
	}
	if s.srv.cfg.Vocabulary != nil {
		bound, err := tick.Bind(s.srv.cfg.Vocabulary, behavior)
		if err != nil {
			_ = s.send(newError(CodeUser, firstLine(err.Error()),
				"name an action, class and lifetime this robot's adaptor config "+
					"declares; an out-of-domain intent is refused, never clamped",
				in.ID))
			return
		}
		behavior = bound
	}
	s.sendCommand(tick.AdmitCmd{Behavior: behavior}, in.ID)
}

// sendCommand forwards one command through the engine's non-blocking Send. A
// full inbox is the engine's own named drop; the client is told so it does not
// wait for an effect that is never coming.
func (s *session) sendCommand(cmd tick.Command, id string) {
	eng := s.srv.engine.Load()
	if eng == nil || eng.Send(cmd) {
		return
	}
	_ = s.send(newError(CodeEnv,
		"the engine's inbox is full, so the intent was dropped rather than blocking "+
			"the tick",
		"send intents at a lower rate; the tick budget belongs to the robot", id))
}

// handleMgmt answers a management frame on ITS OWN goroutine.
//
// A management verb may be slow (a rules file re-read, a doctor probe). Running
// it on the reader goroutine would pause every inbound sense frame behind it,
// and running it on the tick goroutine would spend the robot's budget on an
// operator's question. Neither is acceptable, so it runs on neither.
func (s *session) handleMgmt(body []byte) bool {
	var in mgmtIn
	if err := json.Unmarshal(body, &in); err != nil {
		_ = s.send(newError(CodeUser,
			"a management frame could not be decoded: "+firstLine(err.Error()),
			"send \"id\" and \"verb\"", ""))
		return true
	}
	if s.srv.mgmt == nil {
		_ = s.send(newError(CodeEnv,
			"no management handler installed",
			"give the stream a MgmtHandler at construction, or use the CLI's own "+
				"one-off exec path", in.ID))
		return true
	}
	raw := make(json.RawMessage, len(body))
	copy(raw, body)
	go func() {
		result := s.srv.mgmt.HandleJSON(raw)
		if len(result) == 0 {
			result = json.RawMessage("null")
		}
		_ = s.send(mgmtResultOut{
			V: Version, Kind: KindMgmtResult, ID: in.ID, Result: result,
		})
	}()
	return true
}
