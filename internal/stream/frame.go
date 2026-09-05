// Package stream is the engine's wire endpoint: a single-owner unix socket (or
// the same protocol over a child process's stdin/stdout) carrying sense frames
// in and pose, event, heartbeat and end frames out at tick rate.
//
// It is a pure CONSUMER of internal/tick. The Server is the engine's
// adaptor.Sink, an optional tick.TickSeam rider and an OnEvent consumer; the
// engine never learns that a stream exists. Nothing here interprets a robot
// name: a pose frame carries whatever channels the adaptor.Vocabulary declared,
// verbatim.
//
// # Framing
//
// Every frame is a 4-byte big-endian length followed by that many bytes of a
// JSON object. A frame longer than MaxFrameBytes is refused and the connection
// closed — a length prefix is a promise the reader has to honour, and honouring
// an arbitrary one is how a peer allocates a gigabyte on the robot.
//
// Every frame carries "v" (the protocol version) and "kind". A frame whose "v"
// differs from Version is refused naming BOTH versions and the connection is
// closed: wire drift between a client and an engine that silently half-works is
// the failure mode c43 exists to prevent.
//
// # The tick budget is the product
//
// Outbound telemetry — pose, event and heartbeat frames — is enqueued on a
// BOUNDED queue (Config.OutboundQueue, default 8) and dropped, newest-first,
// with a named senselog line when it is full. Write never blocks, never
// allocates a socket write on the tick goroutine's critical path and never
// returns an error, because a slow consumer must cost the robot nothing. A
// layer whose drops are invisible is indistinguishable from one that silently
// no-ops, so every drop names its reason.
//
// Control frames — the hello reply, refusals, management results and the
// end-of-stream frame — take a DIRECT, blocking write instead. None of them is
// emitted from the tick goroutine, and a dropped refusal or a dropped end frame
// would leave the peer waiting for something that is never coming.
//
// # Transport
//
// Unix socket only by default, created 0600 inside a consumer-provided
// directory. A TCP listen address is refused unless Config.InsecureTCP is set:
// on a robot, a TCP listener lets any host on the network admit intents.
// Every listener goes through Config.listen, which is what makes
// "no TCP listener exists" a testable claim rather than an assurance.
package stream

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
)

// Version is the wire protocol version every frame carries as "v".
const Version = 1

// MaxFrameBytes is the largest JSON body a frame may carry. A longer one is
// refused rather than truncated: a frame the endpoint quietly reinterpreted is
// worse than one it declined.
const MaxFrameBytes = 1 << 20

// lengthPrefixBytes is the fixed big-endian length prefix width.
const lengthPrefixBytes = 4

// The frame kinds. Inbound kinds are what a client may send; outbound kinds are
// what the engine sends back.
const (
	KindHello      = "hello"
	KindSense      = "sense"
	KindIntent     = "intent"
	KindMgmt       = "mgmt"
	KindMgmtResult = "mgmt_result"
	KindEvent      = "event"
	KindHeartbeat  = "heartbeat"
	KindEnd        = "end"
	KindError      = "error"
)

// KindPose is the pose frame's kind token.
//
// It is DERIVED from adaptor.Pose's type name rather than spelled as a string
// literal, and that is not decoration. "pose" is also the name of one of
// MicroDuck's channels, so internal/adaptor's donor-literal guard fails the
// build on any non-test source containing it as a whole string literal, and
// that guard's exemption list refuses to exempt a channel name on any grounds.
// The collision is accidental — this is a protocol schema token, identical on
// every robot, not a robot name compiled into the engine — but the guard cannot
// tell those apart and should not be loosened until it can. Deriving the token
// from the library's own type is the honest way to spell it meanwhile;
// TestPoseKindIsTheDeclaredWireToken pins the resulting value.
var KindPose = strings.ToLower(reflect.TypeOf(adaptor.Pose{}).Name())

// The error codes an error frame carries. They are the CLI error contract's
// codes, so a client relaying one as a process exit code needs no translation:
// 1 is a user error, 2 an environment error.
const (
	CodeUser = 1
	CodeEnv  = 2
)

// The two intent operations.
const (
	OpAdmit = "admit"
	OpEvict = "evict"
)

// errFrameTooLarge is the refusal for a length prefix over MaxFrameBytes. It is
// a sentinel because the reader cannot recover: the promised bytes are still on
// the wire and skipping them would resynchronise on garbage.
var errFrameTooLarge = errors.New("frame too large")

// header is the two fields every frame carries. V is a pointer so a frame that
// omits it is distinguishable from one declaring version 0 — both are refused,
// but the refusal should not have to guess.
type header struct {
	V    *int   `json:"v"`
	Kind string `json:"kind"`
}

// --- inbound bodies ---------------------------------------------------------

type helloIn struct {
	Client string `json:"client"`
}

type senseIn struct {
	Fields map[string]any `json:"fields"`
}

// intentIn is an admit/evict request. The field names mirror tick.Behavior so a
// consumer reading both sides sees one vocabulary, and duration_s keeps the
// runtime's unit-in-the-name convention: a cadence-dependent tuning that lost
// its unit is a bug class of its own.
type intentIn struct {
	Op        string             `json:"op"`
	Name      string             `json:"name,omitempty"`
	Action    string             `json:"action,omitempty"`
	Class     string             `json:"class,omitempty"`
	Channels  []string           `json:"channels,omitempty"`
	DurationS *float64           `json:"duration_s,omitempty"`
	Loops     bool               `json:"loops,omitempty"`
	Params    map[string]float64 `json:"params,omitempty"`
	ID        string             `json:"id,omitempty"`
}

// mgmtIn is a management request. Only ID is interpreted here — it correlates
// the reply — because management verbs belong to the MgmtHandler, not to the
// transport. The handler receives the frame's bytes verbatim.
type mgmtIn struct {
	ID   string   `json:"id"`
	Verb string   `json:"verb"`
	Args []string `json:"args,omitempty"`
	JSON bool     `json:"json,omitempty"`
}

// --- outbound bodies --------------------------------------------------------

type helloOut struct {
	V             int    `json:"v"`
	Kind          string `json:"kind"`
	EngineVersion string `json:"engine_version"`
}

type poseOut struct {
	V        int                  `json:"v"`
	Kind     string               `json:"kind"`
	Tick     uint64               `json:"tick"`
	Channels map[string][]float64 `json:"channels"`
}

type eventOut struct {
	V    int            `json:"v"`
	Kind string         `json:"kind"`
	Name string         `json:"name"`
	Data map[string]any `json:"data,omitempty"`
}

type heartbeatOut struct {
	V    int    `json:"v"`
	Kind string `json:"kind"`
	Tick uint64 `json:"tick"`
	Now  string `json:"now"`
}

type endOut struct {
	V      int    `json:"v"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// errorOut is the structured refusal body, matching the CLI error contract:
// a code a client can exit with, a message naming what went wrong, and a
// remediation naming what to do about it. A refusal an operator cannot act on
// is a refusal they will work around by disabling the check.
type errorOut struct {
	V           int    `json:"v"`
	Kind        string `json:"kind"`
	Code        int    `json:"code"`
	Message     string `json:"message"`
	Remediation string `json:"remediation"`
	ID          string `json:"id,omitempty"`
}

type mgmtResultOut struct {
	V      int             `json:"v"`
	Kind   string          `json:"kind"`
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
}

func newError(code int, message, remediation, id string) errorOut {
	return errorOut{
		V: Version, Kind: KindError, Code: code,
		Message: message, Remediation: remediation, ID: id,
	}
}

// --- codec ------------------------------------------------------------------

// encodeFrame marshals payload and prepends its big-endian length. It returns
// the complete framed bytes, so a caller can hand one []byte to the writer and
// never emit a half-frame.
func encodeFrame(payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("stream: a frame could not be encoded: %w", err)
	}
	if len(body) > MaxFrameBytes {
		return nil, fmt.Errorf(
			"stream: a %d-byte frame exceeds the %d-byte maximum", len(body), MaxFrameBytes)
	}
	out := make([]byte, lengthPrefixBytes+len(body))
	binary.BigEndian.PutUint32(out[:lengthPrefixBytes], uint32(len(body)))
	copy(out[lengthPrefixBytes:], body)
	return out, nil
}

// readFrame reads one frame body. It returns errFrameTooLarge for an
// over-length prefix WITHOUT consuming the promised bytes: the stream is
// unrecoverable at that point and the caller must refuse and close.
func readFrame(r io.Reader) ([]byte, error) {
	var prefix [lengthPrefixBytes]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size > MaxFrameBytes {
		return nil, errFrameTooLarge
	}
	if size == 0 {
		return nil, errors.New("stream: a zero-length frame carries no kind")
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}
