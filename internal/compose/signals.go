package compose

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// signalStopLine is the named stderr line the first stop signal prints. It
// follows the senselog grammar so an operator reading one stream sees the
// graceful stop in the same shape as every other named drop, and it names the
// escape hatch it just re-armed — a shutdown that wedges is exactly when
// nobody wants to go looking for documentation.
const signalStopLine = "[SENSE stage=run source=signal event=stop] %s — settling; " +
	"a second signal terminates immediately\n"

// notifyFirstStopSignal returns a context cancelled by the first SIGINT or
// SIGTERM, and a function that releases the handler.
//
// The handler is uninstalled as soon as that first signal is received, BEFORE
// the context is cancelled, so the next one gets the default disposition and
// kills the process. signal.NotifyContext cannot do this: it holds its
// handler until its own stop function runs, which for a `run` is after the
// graceful shutdown has finished — precisely the window an operator needs to
// be able to interrupt.
func notifyFirstStopSignal(parent context.Context, stderr io.Writer) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	done := make(chan struct{})
	restore := func() { signal.Stop(sigs) }
	go watchFirstStopSignal(sigs, done, restore, cancel, stderr)

	var once sync.Once
	return ctx, func() {
		once.Do(func() { close(done) })
		cancel()
	}
}

// watchFirstStopSignal is the testable half: it waits for either the first
// signal or the run finishing, and always restores the default disposition
// before doing anything else.
//
// Restoring FIRST, then cancelling, is the whole point. Between those two
// statements the process is already back to "a signal kills me", so an
// operator hammering Ctrl-C during a wedged settle is never waiting on this
// goroutine to make progress.
func watchFirstStopSignal(
	sigs <-chan os.Signal, done <-chan struct{}, restore, cancel func(), stderr io.Writer,
) {
	select {
	case sig := <-sigs:
		restore()
		cancel()
		fmt.Fprintf(stderr, signalStopLine, sig)
	case <-done:
		restore()
	}
}
