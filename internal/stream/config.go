package stream

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// DefaultSocketName is the socket file created inside Config.Dir when the
// config names none.
const DefaultSocketName = "engine.sock"

// DefaultOutboundQueue is the bounded outbound queue's depth.
//
// It is a DROP boundary, not a buffer to be tuned upward until nothing is lost.
// Eight frames is a sixth of a second at 50 Hz: enough to ride out a consumer's
// scheduling hiccup, short enough that a wedged consumer is reported within a
// blink rather than serving seconds-stale poses.
const DefaultOutboundQueue = 8

// DefaultMaxInflightMgmt bounds how many management verbs one session may have
// running at once.
//
// A management verb answers on its own goroutine so a slow one (a rules
// re-read, a doctor probe) cannot pause the reader or spend the robot's tick
// budget. Unbounded, that is a lever: a client sending frames faster than the
// handler retires them starts a goroutine and holds a body copy of up to
// MaxFrameBytes for each, from one socket, after a handshake that costs
// nothing. Four is a bound, not a queue — an operator does not ask a robot five
// questions at once, and the fifth is refused with a named reason rather than
// buffered, which is the same answer this package gives a full outbound queue.
const DefaultMaxInflightMgmt = 4

// DefaultHeartbeatEvery is the interval between heartbeat frames.
//
// The heartbeat exists so a consumer can tell "the engine is alive and idle"
// from "the engine is gone": a lapse of two intervals is the consumer's cue to
// settle its robot to neutral (c37). It is driven by the injected clock through
// the engine's tick, never by a wall-clock timer, so a replay is deterministic.
const DefaultHeartbeatEvery = time.Second

// SenseSink receives the fields of every sense frame, with the clock reading
// the endpoint observed them at.
//
// The method set is deliberately exactly this: another package supplies the
// implementation that folds these fields into the per-tick snapshot, and this
// package must not learn what a sense MEANS. Values arrive as decoded JSON
// (float64, bool, string, []any, nil) and are passed through unchanged — a
// transport that coerced them would be reinterpreting a robot's readings.
type SenseSink interface {
	Update(fields map[string]any, now time.Time)
}

// MgmtHandler answers a management frame. It receives the frame's bytes
// verbatim and returns the JSON body to send back as the "result" of a
// mgmt_result frame.
//
// It is called on its OWN goroutine, never on the tick goroutine and never on
// the reader goroutine, so a slow verb cannot pause the stream or the robot.
// A nil handler is an honest configuration: every management frame is answered
// with a structured error naming what is missing.
type MgmtHandler interface {
	HandleJSON(raw json.RawMessage) json.RawMessage
}

// Config is the endpoint's tunable half.
type Config struct {
	// Dir is the CONSUMER-PROVIDED directory the unix socket is created in.
	// The library never picks a path: where a robot's runtime state lives is
	// the robot CLI's decision, and a library guessing /tmp is how a socket
	// ends up world-writable. Required unless TCPAddr is set or this is a
	// stdio server.
	Dir string

	// SocketName is the socket file's name inside Dir. Empty means
	// DefaultSocketName.
	SocketName string

	// TCPAddr, when set, listens on TCP instead of a unix socket. It is
	// REFUSED unless InsecureTCP is also set: on a robot, a TCP listener lets
	// anything that can route to the box admit intents, and a socket file at
	// least inherits the filesystem's answer to "who is allowed".
	TCPAddr     string
	InsecureTCP bool

	// HeartbeatEvery is the heartbeat interval. Zero means
	// DefaultHeartbeatEvery; negative disables heartbeats entirely, which is
	// what a test that only cares about pose frames wants.
	HeartbeatEvery time.Duration

	// OutboundQueue is the bounded outbound queue's depth. Zero means
	// DefaultOutboundQueue.
	OutboundQueue int

	// MaxInflightMgmt bounds the management verbs one session may have
	// running at once. Zero or less means DefaultMaxInflightMgmt: there is no
	// "unlimited" setting, because the unbounded case is the bug.
	MaxInflightMgmt int

	// EngineVersion is echoed in the hello reply so a client can refuse an
	// engine it does not understand by name rather than by symptom.
	EngineVersion string

	// Vocabulary, when set, binds an admit intent HERE — so an undeclared
	// action comes back to the client as a structured refusal naming the
	// offender, instead of vanishing into a named drop on the tick goroutine
	// that the client never sees. When nil the intent is forwarded unbound and
	// the engine binds it (and drops it) itself.
	Vocabulary *adaptor.Vocabulary

	// Clock is the reading handed to SenseSink.Update. It defaults to the
	// engine's wall clock; a test injects the same tick.FakeClock the engine
	// runs on so a whole session is deterministic.
	Clock tick.Clock

	// listen is the listener factory. It is unexported on purpose: it exists
	// so a test can record every listener this package creates, not so a
	// consumer can supply a transport the security argument above does not
	// cover.
	listen func(network, addr string) (net.Listener, error)
}

// normalize fills the defaults in and refuses a config that cannot be served.
func (c *Config) normalize(stdio bool) error {
	if c.listen == nil {
		c.listen = defaultListen
	}
	if c.OutboundQueue <= 0 {
		c.OutboundQueue = DefaultOutboundQueue
	}
	if c.MaxInflightMgmt <= 0 {
		c.MaxInflightMgmt = DefaultMaxInflightMgmt
	}
	if c.HeartbeatEvery == 0 {
		c.HeartbeatEvery = DefaultHeartbeatEvery
	}
	if c.SocketName == "" {
		c.SocketName = DefaultSocketName
	}
	if c.Clock == nil {
		c.Clock = tick.RealClock{}
	}
	if stdio {
		return nil
	}
	if c.TCPAddr != "" {
		if !c.InsecureTCP {
			return fmt.Errorf(
				"stream: a TCP listen address (%s) is refused — a TCP listener lets "+
					"anything that can reach this host admit intents to the robot; use a "+
					"unix socket, or set InsecureTCP to accept that", c.TCPAddr)
		}
		return nil
	}
	return c.checkDir()
}

// checkDir refuses an empty or non-directory Dir, because a socket the library
// invented a path for is a socket nobody owns.
func (c *Config) checkDir() error {
	if c.Dir == "" {
		return errors.New(
			"stream: no socket directory was given — name the directory the consumer " +
				"owns its runtime state in; the library never picks one")
	}
	info, err := os.Stat(c.Dir)
	if err != nil {
		return fmt.Errorf(
			"stream: the socket directory %s cannot be read (%v) — create it, or name "+
				"one that exists", c.Dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf(
			"stream: the socket directory %s is not a directory — name a directory, "+
				"not a file", c.Dir)
	}
	return nil
}
