package tick

import (
	"context"
	"time"
)

// This file is the ONLY place in package tick allowed to read the wall clock.
// TestNoWallClockInLoop scans every other non-test source here for time.Now,
// time.Since, time.Sleep and time.Tick and fails on a hit, so the loop stays
// deterministic under an injected Clock and testable at any tick rate.

// RealClock is the production Clock: the wall clock.
type RealClock struct{}

// Now returns the current time.
func (RealClock) Now() time.Time { return time.Now() }

// realTicker is the production Ticker, backed by time.Ticker.
type realTicker struct {
	ticker *time.Ticker
}

// NewRealTicker returns a Ticker firing every period. The caller must Stop it.
func NewRealTicker(period time.Duration) Ticker {
	return &realTicker{ticker: time.NewTicker(period)}
}

// Wait blocks until the next tick, or until ctx is done.
func (r *realTicker) Wait(ctx context.Context) bool {
	select {
	case <-r.ticker.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Stop releases the underlying ticker.
func (r *realTicker) Stop() { r.ticker.Stop() }
