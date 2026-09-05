// Package mgmt is the engine's management surface: a transport-agnostic
// request/response core that both a one-off exec invocation
// (cmd/neurosymbolic-engine/main.go) and, later, a live socket front answer
// through identically.
//
// A Request names a verb, its positional arguments, and whether the caller
// wants JSON. Handle runs the verb and returns a Response already rendered
// for that caller — Stdout/Stderr are ready-to-write strings in the shape
// internal/clifmt defines (results on stdout, {code,message,remediation}
// errors on stderr, exit 0/1/2), and Result carries the same success value
// structurally, for a caller (the socket front, a future test) that wants the
// value rather than its rendered text.
//
// Handle never blocks on anything but its own verb body: every verb here is a
// bounded, in-process computation (parse a file, format a string, ask an
// installed Reloader/StatusSource) with no network I/O and no wait on the
// tick thread, which is what "answered without pausing the stream" (t9's
// acceptance criterion 2) requires from the caller's side — the stream lives
// in internal/stream (t8), entirely separate from this package.
//
// Every verb body runs under clifmt.Guard, so a panic anywhere below Handle
// becomes an ExitEnv CliError rather than a process crash or a leaked Go
// stack trace.
package mgmt
