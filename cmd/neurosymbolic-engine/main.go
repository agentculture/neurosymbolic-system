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

Run "neurosymbolic-engine run" with no flags for that command's own usage.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable core of main: it takes argv (excluding argv[0]) and
// the two output streams explicitly, so tests never need to fork a process
// or capture os.Stdout/os.Stderr.
func run(args []string, stdout, stderr *os.File) int {
	jsonMode := clifmt.HasJSONFlag(args)
	rest, _ := clifmt.StripJSONFlag(args)

	if len(rest) == 0 {
		fmt.Fprint(stderr, usageText)
		return clifmt.ExitUser
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
		fmt.Fprint(stderr, usageText)
		return clifmt.ExitUser
	}

	handler := &mgmt.Handler{Version: version, Revision: revision}
	resp := handler.Handle(mgmt.Request{Verb: verb, Args: verbArgs, JSON: jsonMode})

	if resp.Stdout != "" {
		fmt.Fprint(stdout, resp.Stdout)
	}
	if resp.Stderr != "" {
		fmt.Fprint(stderr, resp.Stderr)
	}
	return resp.Code
}
