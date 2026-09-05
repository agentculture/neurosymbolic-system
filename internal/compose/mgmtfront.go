package compose

import (
	"encoding/json"
	"strings"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
	"github.com/agentculture/neurosymbolic-system/internal/mgmt"
)

// jsonFront adapts a mgmt.Handler onto the stream endpoint's MgmtHandler: it
// decodes one mgmt frame, dispatches the verb, and encodes the outcome as the
// `result` body of the mgmt_result frame that goes back.
//
// The SAME Handler answers a management frame and an argv invocation. That is
// the point of internal/mgmt existing at all: `status` over a socket and
// `status` from a shell are one verb body, so the two can never drift into
// answering the same question differently. The only thing this type adds is the
// wire shape.
type jsonFront struct{ handler *mgmt.Handler }

// mgmtCall is the fields this front reads off an inbound mgmt frame. The
// endpoint hands over the frame's bytes verbatim and interprets none of it —
// management verbs belong to the handler, not to the transport.
type mgmtCall struct {
	Verb string   `json:"verb"`
	Args []string `json:"args"`
	JSON bool     `json:"json"`
}

// mgmtReply is the `result` body of a mgmt_result frame.
//
// It always carries `code` — the process exit code the same call would have
// produced from argv (0 success, 1 user error, 2 environment error), so a
// client relaying one needs no translation — and exactly one of `result` or
// `error`. An error carries the full {code, message, remediation} contract: a
// refusal an operator cannot act on is a refusal they will work around.
type mgmtReply struct {
	Code   int              `json:"code"`
	Verb   string           `json:"verb"`
	Result any              `json:"result,omitempty"`
	Error  *clifmt.CliError `json:"error,omitempty"`
}

// HandleJSON answers one management frame. It runs on its own goroutine — never
// the tick goroutine, never the reader goroutine — so a slow verb (a rules
// re-read, a doctor probe) cannot pause the stream or the robot.
func (f jsonFront) HandleJSON(raw json.RawMessage) json.RawMessage {
	var call mgmtCall
	if err := json.Unmarshal(raw, &call); err != nil {
		return encodeReply(mgmtReply{
			Code: clifmt.ExitUser,
			Error: clifmt.NewUserError(
				"a management frame could not be decoded: "+err.Error(),
				`send "verb" as a string and "args" as a list of strings`),
		})
	}

	// A noun group may be written as one token ("rules reload") or split
	// across verb and args; both reach mgmt.ParseVerb as the same token list,
	// so the wire cannot mean something the argv front does not.
	verb, args, ok := mgmt.ParseVerb(append(strings.Fields(call.Verb), call.Args...))
	if !ok {
		return encodeReply(mgmtReply{
			Code: clifmt.ExitUser,
			Verb: call.Verb,
			Error: clifmt.NewUserError(
				"a management frame names no verb to run",
				`give "verb" a command name, e.g. "status" or "rules reload"`),
		})
	}

	resp := f.handler.Handle(mgmt.Request{Verb: verb, Args: args, JSON: call.JSON})
	reply := mgmtReply{Code: resp.Code, Verb: call.Verb}
	if cliErr, isErr := resp.Result.(*clifmt.CliError); isErr {
		reply.Error = cliErr
	} else {
		reply.Result = resp.Result
	}
	return encodeReply(reply)
}

// encodeReply marshals a reply, falling back to a hand-built error body when
// even that fails — a management call that answered with nothing would leave
// the client waiting for a frame that is never coming.
func encodeReply(reply mgmtReply) json.RawMessage {
	body, err := json.Marshal(reply)
	if err == nil {
		return body
	}
	return json.RawMessage(`{"code":2,"error":{"code":2,` +
		`"message":"the management result could not be encoded",` +
		`"remediation":"this is a bug in the engine, not a mistake in the request"}}`)
}
