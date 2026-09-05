package compose

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
)

// Main is `run` as a process: parse argv, build the runtime, install the signal
// handler, drive the loop, and render any refusal through the CLI error
// contract. It returns the process exit code.
//
// SIGTERM and SIGINT cancel the run context, which is a GRACEFUL stop: the tick
// loop finishes its current tick, writes one settling neutral pose so the robot
// does not hold whatever the last tick happened to compose, and the endpoint
// sends the peer an end-of-stream frame naming the reason. A second signal is
// left to the default disposition — an operator who asks twice gets the abrupt
// stop they asked for, and a shutdown that could not be interrupted would be
// its own kind of wedge.
//
// A SIGKILL, by construction, does none of that. That is why the endpoint has a
// heartbeat at all: a consumer that sees no beat for two intervals settles its
// own robot, and the engine's cooperation is not required for that to work.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, build Build) int {
	opts, err := ParseArgs(args)
	if err != nil {
		return renderError(err, stderr)
	}

	runtime, err := New(opts, build, stdin, stdout, stderr)
	if err != nil {
		return renderError(err, stderr)
	}
	// Whatever outlives a Run — a provider's worker goroutine and its HTTP
	// client — is released here, on every exit path including a refused run.
	defer runtime.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := runtime.Run(ctx); err != nil {
		return renderError(err, stderr)
	}
	return clifmt.ExitSuccess
}

// renderError writes one refusal in the two-line text contract — "error: ..."
// then "hint: ..." — and returns its exit code. `run` has no --json mode: its
// stdout is either a wire or silent, so a structured refusal would have nowhere
// to go that is not already the protocol's own error frame.
func renderError(err error, stderr io.Writer) int {
	var cliErr *clifmt.CliError
	if !errors.As(err, &cliErr) {
		cliErr = clifmt.NewEnvError(err.Error(), "")
	}
	_ = clifmt.Emit(stderr, cliErr, false)
	return cliErr.Code
}

// MainOS is Main bound to this process's own streams, for a caller that has
// nothing to inject.
func MainOS(args []string, build Build) int {
	return Main(args, os.Stdin, os.Stdout, os.Stderr, build)
}
