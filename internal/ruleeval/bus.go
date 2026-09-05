package ruleeval

import (
	"fmt"
	"strings"

	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// busSource is the senselog source token every bus line carries.
const busSource = "bus"

// Bus is the fault-isolating fan-out of the engine's ONE seam — the donor's
// TickBus (reachy/behavior/rule_engine.py).
//
// The engine calls one TickSeam per tick; real deployments want several riders
// on it (rule evaluation, a goto lane, an export feed, metrics). Each rider runs
// in registration order, and a rider that PANICS loses its turn as one named
// drop while every other rider still runs. That is the whole point: the engine's
// own recover is a last resort for the tick, not a fan-out, and one broken rider
// must never silence another.
//
// A Bus is built once at composition and not mutated afterwards; Add is not
// safe to call while the seam is running.
type Bus struct {
	logger  *senselog.Logger
	drivers []namedDriver
}

type namedDriver struct {
	name   string
	driver tick.TickSeam
}

// NewBus returns an empty Bus writing its drops to logger. A nil logger means
// senselog.Default(), which is stderr.
func NewBus(logger *senselog.Logger) *Bus {
	if logger == nil {
		logger = senselog.Default()
	}
	return &Bus{logger: logger}
}

// Add registers a named driver and returns the bus, for chaining. The name is
// what a panic drop is filed under, so give it something a grep can find. A nil
// driver is ignored.
func (b *Bus) Add(name string, driver tick.TickSeam) *Bus {
	if driver == nil {
		return b
	}
	if name == "" {
		name = fmt.Sprintf("driver-%d", len(b.drivers))
	}
	b.drivers = append(b.drivers, namedDriver{name: name, driver: driver})
	return b
}

// Compose appends drivers (auto-named "driver-<n>") to whatever Add registered
// and returns the composed seam to hand to tick.SetSeamCmd.
func (b *Bus) Compose(drivers ...tick.TickSeam) tick.TickSeam {
	for _, driver := range drivers {
		b.Add("", driver)
	}
	composed := append([]namedDriver(nil), b.drivers...)
	return func(ctx tick.TickContext) {
		for _, entry := range composed {
			b.call(entry, ctx)
		}
	}
}

// call runs one driver with its panic isolated as a NAMED drop. A layer whose
// drops are invisible is indistinguishable from a layer that silently no-ops,
// so the reason is always on the log.
func (b *Bus) call(entry namedDriver, ctx tick.TickContext) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		b.logger.Drop(StageRule, busSource, entry.name, "panic",
			firstLine(fmt.Sprint(recovered)))
	}()
	entry.driver(ctx)
}

// firstLine reduces a recovered value to the one line the SENSE grammar's fixed
// shape allows: a panic value carrying a newline would split one drop into two
// unparseable lines.
func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	return text
}
