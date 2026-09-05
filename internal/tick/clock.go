package tick

import (
	"context"
	"sync"
	"time"
)

// Clock is the engine's only source of "now".
//
// It is injected so a whole run is deterministic in a test and so the loop
// itself never reads the wall clock: at 50 Hz the budget is 20 ms, and a
// cadence-dependent tuning that only works at one tick rate is a bug class of
// its own. Every timing decision in this package goes through a Clock and a
// Ticker, and TestNoWallClockInLoop fails the build if a non-test source
// outside realclock.go names time.Now, time.Since, time.Sleep or time.Tick.
type Clock interface {
	Now() time.Time
}

// Ticker is what the loop waits on between ticks.
//
// Wait blocks until the next tick is due and reports true; it reports false
// once ctx is done or the ticker has stopped, which is how Run exits. Stop
// releases the ticker's resources and unblocks anything waiting on it; it is
// safe to call more than once.
type Ticker interface {
	Wait(ctx context.Context) bool
	Stop()
}

// FakeClock is the deterministic Clock+Ticker a test drives by hand. It lives
// in the non-test build on purpose: the consumers of this library (two robot
// CLIs and their own suites) need the same double, and a helper that only
// exists inside this package's _test.go files cannot be imported.
//
// Advance(d) delivers floor(d / period) ticks and returns only once the loop
// has FINISHED all of them and parked back in Wait, so a test never has to
// sleep or poll to know where the engine is. Elapse(d) moves the clock forward
// WITHOUT delivering a tick, which is how a test makes a tick's work look
// expensive enough to overrun the budget.
//
// It reads no wall clock; it starts at the Unix epoch.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	period time.Duration

	ticks chan time.Time
	acks  chan struct{}

	stopOnce sync.Once
	stopped  chan struct{}

	// waited is touched only by the goroutine calling Wait (the tick
	// goroutine), so it needs no lock. It suppresses the acknowledgement on
	// the first Wait, when no tick has been worked yet.
	waited bool
}

// NewFakeClock returns a FakeClock ticking at period, starting at the Unix
// epoch in UTC.
func NewFakeClock(period time.Duration) *FakeClock {
	return &FakeClock{
		now:     time.Unix(0, 0).UTC(),
		period:  period,
		ticks:   make(chan time.Time),
		acks:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

// Now returns the fake's current reading.
func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Elapse moves the clock forward without delivering a tick — the way a test
// spends a tick's budget inside a seam.
func (f *FakeClock) Elapse(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// Advance moves the clock forward by d, delivering one tick per whole period
// and returning once the loop has completed all of them.
//
// It returns early if the ticker is stopped or the loop exits, so a test can
// never deadlock on an engine that has already returned.
func (f *FakeClock) Advance(d time.Duration) {
	if f.period <= 0 {
		return
	}
	for count := int64(d / f.period); count > 0; count-- {
		f.mu.Lock()
		f.now = f.now.Add(f.period)
		at := f.now
		f.mu.Unlock()

		select {
		case f.ticks <- at:
		case <-f.stopped:
			return
		}
		select {
		case <-f.acks:
		case <-f.stopped:
			return
		}
	}
}

// Wait implements Ticker. The acknowledgement it sends on entry is what makes
// Advance synchronous: it means "the previous tick's work is finished".
func (f *FakeClock) Wait(ctx context.Context) bool {
	if f.waited {
		select {
		case f.acks <- struct{}{}:
		case <-f.stopped:
			return false
		case <-ctx.Done():
			return false
		}
	}
	f.waited = true

	select {
	case <-f.ticks:
		return true
	case <-f.stopped:
		return false
	case <-ctx.Done():
		return false
	}
}

// Stop implements Ticker and releases anything blocked in Wait or Advance.
func (f *FakeClock) Stop() {
	f.stopOnce.Do(func() { close(f.stopped) })
}
