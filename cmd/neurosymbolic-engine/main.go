// Command neurosymbolic-engine is the Go entry point for the
// neurosymbolic-system runtime.
//
// t1 (go module scaffold and CI) only establishes the module, the build/test
// matrix, and a single "version" verb — enough for CI to prove the toolchain
// builds a static binary on linux/amd64 and linux/arm64 with CGO_ENABLED=0.
// Later tasks add the actual engine verbs (see CLAUDE.md's "The runtime
// being extracted"); keep this file small so it stays an obvious place to
// wire a new verb into, not a growing dumping ground.
package main

import (
	"fmt"
	"os"
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
  neurosymbolic-engine <command>

Commands:
  version    print the engine's version and revision
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable core of main: it takes argv (excluding argv[0]) and
// the two output streams explicitly, so tests never need to fork a process
// or capture os.Stdout/os.Stderr.
func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 1
	}

	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "neurosymbolic-engine %s (%s)\n", version, revision)
		return 0
	default:
		fmt.Fprintf(stderr, "error: unknown command %q\n", args[0])
		fmt.Fprint(stderr, usageText)
		return 1
	}
}
