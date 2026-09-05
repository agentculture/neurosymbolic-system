package mgmt

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
)

// Request is one management call: a verb, its positional/flag arguments (as
// argv, minus any --json token — a caller strips that with
// clifmt.StripJSONFlag before building a Request), and whether the caller
// wants a JSON response.
//
// Verb is dot-separated for a noun group: "rules check", "rules list",
// "rules migrate" and "rules reload" arrive as "rules.check", "rules.list",
// "rules.migrate" and "rules.reload"; every other verb ("version", "whoami",
// "doctor", "status") is its own bare name.
type Request struct {
	Verb string
	Args []string
	JSON bool
}

// Response is a Request answered: Code is the process exit code this call
// implies (clifmt.ExitSuccess/ExitUser/ExitEnv), Stdout/Stderr are the
// already-rendered text a caller writes verbatim to those streams (never
// both non-empty for one Response), and Result is the same outcome as a Go
// value — the success result on Code 0, a *clifmt.CliError otherwise — for a
// caller that wants the structured value instead of parsing its own
// rendering back out.
type Response struct {
	Code   int
	Stdout string
	Stderr string
	Result any
}

// Reloader lets a live engine answer "rules reload": re-read and validate the
// named files and, only if the new set validates cleanly, swap it in.
//
// Reload MUST keep the previously active rule set in effect if the new one
// fails validation — a `rules reload` that broke a running robot because one
// operator typo made it into an overlay would be worse than the reload never
// happening. Handler has no Reloader installed at all in the one-off exec
// front (there is no live engine to reload); rules.reload then answers
// ExitEnv with a remediation pointing at a restart or the stream endpoint.
type Reloader interface {
	Reload(paths []string) error
}

// StatusSource lets a live engine answer "status" with whatever it considers
// its current state (active behaviors, tick count, uptime, ...). Handler
// renders whatever Status returns as the verb's result, unmodified.
type StatusSource interface {
	Status() (any, error)
}

// Handler dispatches Requests to verb bodies and renders their outcome.
// Version/Revision/Module back "whoami" and "version"; Reloader/Status are
// optional — nil means "no live engine", which every verb that needs one
// reports the same way (see noLiveEngine below).
type Handler struct {
	Version  string
	Revision string
	// Module is the module path whoami reports. Empty means "read it from
	// this binary's own build info" (see verbs_misc.go); a test sets it
	// explicitly to avoid depending on how the test binary itself was built.
	Module string

	Reloader Reloader
	Status   StatusSource
}

// verbFunc is one verb's body: given its argv (already stripped of --json)
// it returns a JSON-able success result plus the human-text rendering of
// that result, or an error (ideally a *clifmt.CliError; anything else is
// normalized to ExitEnv by clifmt.Guard).
type verbFunc func(h *Handler, args []string) (verbResult, error)

// verbResult is a verb's success outcome in both shapes Response needs: Text
// for a human reading stdout, Value for --json (and for a caller, like a
// future socket front, that wants the Go value directly via Response.Result).
type verbResult struct {
	Text  string
	Value any
}

func (h *Handler) verbs() map[string]verbFunc {
	return map[string]verbFunc{
		"version":       verbVersion,
		"whoami":        verbWhoami,
		"doctor":        verbDoctor,
		"status":        verbStatus,
		"bench":         verbBench,
		"rules.check":   verbRulesCheck,
		"rules.list":    verbRulesList,
		"rules.migrate": verbRulesMigrate,
		"rules.reload":  verbRulesReload,
	}
}

// nounGroups are the verb names that take a sub-verb. A group's request is
// dispatched under "<noun>.<sub-verb>"; ParseVerb is the ONE place that
// folding happens, so the argv front and the stream endpoint's mgmt frames
// cannot come to disagree about what "rules reload" means.
func nounGroups() map[string]bool {
	return map[string]bool{"rules": true}
}

// ParseVerb turns a caller's tokens into a Request's Verb and Args.
//
// A noun group folds its sub-verb into the dispatch key ("rules", "reload" ->
// "rules.reload"); every other verb is its own bare name and the rest of the
// tokens are its arguments. ok is false only for an empty token list, or for a
// noun group given no sub-verb at all — neither has a verb to dispatch, and
// reporting them as an unknown command named "" would be a worse message than
// the caller's own usage text.
func ParseVerb(tokens []string) (verb string, args []string, ok bool) {
	if len(tokens) == 0 {
		return "", nil, false
	}
	if nounGroups()[tokens[0]] {
		if len(tokens) < 2 {
			return "", nil, false
		}
		return tokens[0] + "." + tokens[1], tokens[2:], true
	}
	return tokens[0], tokens[1:], true
}

// verbNames is the sorted verb list, in the user-facing spelling ("rules
// check", not the internal dispatch key "rules.check"), for an "unknown
// command" remediation.
func (h *Handler) verbNames() []string {
	verbs := h.verbs()
	names := make([]string, 0, len(verbs))
	for name := range verbs {
		names = append(names, strings.Replace(name, ".", " ", 1))
	}
	sort.Strings(names)
	return names
}

// Handle runs req and returns its already-rendered Response. It never
// panics: every verb body runs under clifmt.Guard, so an unexpected panic or
// bare error becomes an ExitEnv CliError instead of propagating.
func (h *Handler) Handle(req Request) Response {
	fn, ok := h.verbs()[req.Verb]
	if !ok {
		err := clifmt.NewUserError(
			fmt.Sprintf("unknown command %q", req.Verb),
			"known commands: "+strings.Join(h.verbNames(), ", "),
		)
		return h.renderError(err, req.JSON)
	}

	var result verbResult
	guardErr := clifmt.Guard(func() error {
		r, err := fn(h, req.Args)
		if err != nil {
			return err
		}
		result = r
		return nil
	})
	if guardErr != nil {
		return h.renderError(guardErr, req.JSON)
	}
	return h.renderResult(result, req.JSON)
}

func (h *Handler) renderResult(r verbResult, jsonMode bool) Response {
	if jsonMode {
		var buf bytes.Buffer
		if err := clifmt.EmitResultJSON(&buf, r.Value); err != nil {
			cliErr := clifmt.NewEnvError(fmt.Sprintf("could not render result as JSON: %v", err), "")
			return h.renderError(cliErr, jsonMode)
		}
		return Response{Code: clifmt.ExitSuccess, Stdout: buf.String(), Result: r.Value}
	}
	var buf bytes.Buffer
	clifmt.EmitResult(&buf, r.Text)
	return Response{Code: clifmt.ExitSuccess, Stdout: buf.String(), Result: r.Value}
}

func (h *Handler) renderError(err *clifmt.CliError, jsonMode bool) Response {
	var buf bytes.Buffer
	_ = clifmt.Emit(&buf, err, jsonMode) // writing to a bytes.Buffer never fails
	return Response{Code: err.Code, Stderr: buf.String(), Result: err}
}

// noLiveEngine is the CliError every verb that needs an installed
// Reloader/StatusSource returns when Handler has none — the one-off exec
// front never has one, since there is nothing to reload or report on without
// a running engine process.
func noLiveEngine() *clifmt.CliError {
	return clifmt.NewEnvError(
		"no live engine",
		"restart with the new files or run through the stream endpoint",
	)
}

// asCliError normalizes an arbitrary error into a *clifmt.CliError, passing
// one through unchanged. Verb bodies use this so a lower-level error (a
// rules.Error, an adaptor error, a plain os error) always reaches Handle as
// something clifmt.Guard's errors.As will not have to re-derive.
func asCliError(err error, code int, remediation string) *clifmt.CliError {
	var cliErr *clifmt.CliError
	if errors.As(err, &cliErr) {
		return cliErr
	}
	return &clifmt.CliError{Code: code, Message: err.Error(), Remediation: remediation}
}
