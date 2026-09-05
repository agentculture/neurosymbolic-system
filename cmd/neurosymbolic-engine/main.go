// Command neurosymbolic-engine is the Go entry point for the
// neurosymbolic-system runtime.
//
// main.go is deliberately thin: it turns argv into an internal/mgmt.Request,
// hands it to a mgmt.Handler, and writes back whatever the Handler already
// rendered. Every verb's actual body — version, whoami, doctor, status, the
// rules noun group — lives in internal/mgmt/verbs_*.go, so a future socket
// front (the stream endpoint) can answer the identical set of requests
// without duplicating a single verb.
package main

import (
	"fmt"
	"os"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
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
  version                 print the engine's version and revision
  whoami                  print the engine's version, revision and module path
  doctor                  run environment checks (toolchain, vendoring, ...)
  status                  report the live engine's state (needs a stream)
  rules check <file>...   validate rules files with no robot attached
  rules list <file>...    list every rule's id, kind and predicate
  rules migrate <file>    write a schema_version-2 twin of a rules file
  rules reload <file>...  ask a live engine to re-read its rules files
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

	verb, verbArgs, ok := parseVerb(rest)
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

// parseVerb turns argv into an internal/mgmt Request's Verb/Args: the
// "rules" noun group folds its sub-verb into a dot-separated name
// ("rules check" -> "rules.check"), everything else is its own bare verb.
// ok is false only when "rules" is given with no sub-verb at all, which has
// no verb to dispatch — that is the one case main handles itself rather than
// letting mgmt.Handler report "unknown command" for an empty string.
func parseVerb(rest []string) (verb string, args []string, ok bool) {
	if rest[0] == "rules" {
		if len(rest) < 2 {
			return "", nil, false
		}
		return "rules." + rest[1], rest[2:], true
	}
	return rest[0], rest[1:], true
}
