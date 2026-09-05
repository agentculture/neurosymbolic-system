package compose

import (
	"bytes"
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The regression: the default disposition must be restored the INSTANT the
// first signal arrives, and BEFORE the run context is cancelled — otherwise
// the whole graceful-shutdown window is a window in which a second SIGINT is
// swallowed by an already-cancelled context.
func TestWatchFirstStopSignalRestoresBeforeCancelling(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	sigs <- syscall.SIGINT

	var order []string
	var stderr bytes.Buffer
	watchFirstStopSignal(
		sigs,
		make(chan struct{}),
		func() { order = append(order, "restore") },
		func() { order = append(order, "cancel") },
		&stderr,
	)

	if want := []string{"restore", "cancel"}; strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v (the handler goes first, so the next signal kills)", order, want)
	}
	line := stderr.String()
	for _, want := range []string{"[SENSE stage=run source=signal event=stop]", "interrupt", "second signal"} {
		if !strings.Contains(line, want) {
			t.Errorf("stderr = %q, want it to contain %q", line, want)
		}
	}
}

// The run finishing on its own is not a signal: nothing is cancelled and
// nothing is logged, but the handler is still released.
func TestWatchFirstStopSignalReleasesTheHandlerWhenTheRunEnds(t *testing.T) {
	done := make(chan struct{})
	close(done)

	restored := false
	cancelled := false
	var stderr bytes.Buffer
	watchFirstStopSignal(make(chan os.Signal), done,
		func() { restored = true }, func() { cancelled = true }, &stderr)

	if !restored {
		t.Error("the handler was not released when the run ended")
	}
	if cancelled {
		t.Error("the context was cancelled without a signal")
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want nothing logged for an ordinary end of run", stderr.String())
	}
}

// End to end over the real handler: a delivered SIGTERM cancels the returned
// context. The SECOND signal's behaviour cannot be exercised here — by
// construction it would terminate the test process — which is why the
// ordering above is asserted directly and the contract is documented in
// `run`'s usage text.
func TestNotifyFirstStopSignalCancelsOnADeliveredSignal(t *testing.T) {
	var stderr bytes.Buffer
	ctx, stop := notifyFirstStopSignal(context.Background(), &stderr)
	defer stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the context was not cancelled by SIGTERM")
	}
}

// The stop function is safe to call more than once — Main defers it, and a
// caller that also calls it explicitly must not panic on a double close.
func TestNotifyFirstStopSignalStopIsIdempotent(t *testing.T) {
	var stderr bytes.Buffer
	_, stop := notifyFirstStopSignal(context.Background(), &stderr)
	stop()
	stop()
}

// `run`'s usage text is where an operator looks for this, so the promise and
// the implementation are pinned to each other.
func TestUsageTextDocumentsTheSecondSignal(t *testing.T) {
	for _, want := range []string{"SIGINT", "SIGTERM", "SECOND"} {
		if !strings.Contains(UsageText, want) {
			t.Errorf("UsageText does not mention %q:\n%s", want, UsageText)
		}
	}
}
