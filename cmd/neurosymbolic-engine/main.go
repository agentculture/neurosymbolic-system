// Command neurosymbolic-engine is the Go entry point for the
// neurosymbolic-system runtime.
//
// main.go is deliberately thin, and stays thin in both directions:
//
//   - every ONE-SHOT verb (version, whoami, doctor, status, the rules noun
//     group) becomes an internal/mgmt.Request handed to a mgmt.Handler, whose
//     already-rendered Response is written back verbatim. The verb bodies live
//     in internal/mgmt/verbs_*.go, so the stream endpoint's mgmt frames answer
//     the identical set of requests without duplicating a single verb;
//   - `run`, the long-lived one, goes straight to internal/compose, the
//     composition root that knows how the runtime is wired. main.go learns
//     nothing about engines, seams, sockets or signals.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
	"github.com/agentculture/neurosymbolic-system/internal/compose"
	"github.com/agentculture/neurosymbolic-system/internal/mgmt"
)

// version and revision are overridden at build time via:
//
//	go build -ldflags "-X main.version=<version> -X main.revision=<revision>"
//
// They must stay `var`, not `const` — -ldflags -X can only rewrite the
// initial value of a package-level string variable. Defaults are the
// "nobody stamped this build" case: a plain `go build` or `go run` without
// ldflags, e.g. a developer's local build.
var (
	version  = "0.0.0-dev"
	revision = "unknown"
)

const usageText = `neurosymbolic-engine - the neurosymbolic-system Go runtime

Usage:
  neurosymbolic-engine <command> [args...] [--json]

Commands:
  run [flags]             run the engine: senses in, poses out, on one tick
  version                 print the engine's version and revision
  whoami                  print the engine's version, revision and module path
  doctor                  run environment checks (toolchain, vendoring, ...)
  status                  report the live engine's state (needs a stream)
  bench [flags]           benchmark tick p50/p99/overruns/RSS under a synthetic load
  rules check <file>...   validate rules files with no robot attached
  rules list <file>...    list every rule's id, kind and predicate
  rules migrate <file>    write a schema_version-2 twin of a rules file
  rules reload <file>...  ask a live engine to re-read its rules files
  help                    print this usage

Run "neurosymbolic-engine run" with no flags for that command's own usage.
`

// helpVerb prints usageText on STDOUT and exits 0.
//
// It is not a mgmt verb: the usage text is this front end's own, and the
// socket front answers mgmt frames where a screen of argv help would mean
// nothing. It exists because a usage screen is a RESULT — an operator who
// asked for help got what they asked for — and the two-line error contract
// leaves nowhere else to put one. Before it, the only way to see the command
// list was to trigger a failure.
const helpVerb = "help"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable core of main: it takes argv (excluding argv[0]) and
// the two output streams explicitly, so tests never need to fork a process
// or capture os.Stdout/os.Stderr.
func run(args []string, stdout, stderr *os.File) int {
	jsonMode := clifmt.HasJSONFlag(args)
	rest, _ := clifmt.StripJSONFlag(args)

	handler := &mgmt.Handler{Version: version, Revision: revision}

	// A usage screen is not an error rendering. Both argv shapes that used to
	// print one — no command at all, and a noun group with no sub-verb — are
	// refusals, so they go through the SAME two-line error contract every
	// other failure uses (and honor --json, which a screen of text cannot).
	// `help` is where the usage screen lives now.
	if len(rest) == 0 {
		return emitError(stderr, jsonMode, clifmt.NewUserError(
			"no command given", remediation(handler.VerbNames(), "")))
	}

	if rest[0] == helpVerb {
		return emitHelp(stdout, jsonMode, handler.VerbNames())
	}

	// `run` is not a mgmt verb: it does not answer a question and return, it
	// IS the engine. It owns the process until a signal stops it, so it takes
	// its own flags and never passes through the Request/Response shape.
	if rest[0] == compose.Verb {
		return compose.Main(rest[1:], os.Stdin, stdout, stderr,
			compose.Build{Version: version, Revision: revision})
	}

	verb, verbArgs, ok := mgmt.ParseVerb(rest)
	if !ok {
		// ParseVerb refuses exactly one non-empty shape: a noun group with no
		// sub-verb. There is no verb to dispatch, so the refusal is rendered
		// here rather than as an unknown command named "".
		return emitError(stderr, jsonMode, clifmt.NewUserError(
			fmt.Sprintf("no sub-command given for '%s'", rest[0]),
			remediation(handler.VerbNames(), rest[0]+" ")))
	}

	resp := handler.Handle(mgmt.Request{Verb: verb, Args: verbArgs, JSON: jsonMode})

	if resp.Stdout != "" {
		fmt.Fprint(stdout, resp.Stdout)
	}
	if resp.Stderr != "" {
		fmt.Fprint(stderr, resp.Stderr)
	}
	return resp.Code
}

// emitError renders one argv-level refusal through the shared contract —
// "error:"/"hint:" on stderr in text mode, one JSON object in --json mode —
// and returns its exit code.
func emitError(stderr *os.File, jsonMode bool, err *clifmt.CliError) int {
	_ = clifmt.Emit(stderr, err, jsonMode)
	return err.Code
}

// emitHelp writes the usage screen to STDOUT as a result: the text verbatim,
// or, under --json, the same content as a structured object, so a caller
// parsing this CLI never has to scrape a screen.
func emitHelp(stdout *os.File, jsonMode bool, verbs []string) int {
	if jsonMode {
		_ = clifmt.EmitResultJSON(stdout, map[string]any{
			"usage":    usageText,
			"commands": append([]string{compose.Verb, helpVerb}, verbs...),
		})
		return clifmt.ExitSuccess
	}
	fmt.Fprint(stdout, usageText)
	return clifmt.ExitSuccess
}

// remediation is the hint half of the two argv-level refusals: how to get the
// usage screen, plus the commands that would have been valid here. prefix
// narrows the list to one noun group ("rules "), or lists everything when it
// is empty.
func remediation(verbs []string, prefix string) string {
	names := make([]string, 0, len(verbs)+1)
	if prefix == "" {
		names = append(names, compose.Verb)
	}
	for _, name := range verbs {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	return "run 'neurosymbolic-engine " + helpVerb + "' or one of: " + strings.Join(names, ", ")
}
