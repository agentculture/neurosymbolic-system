package tick

import (
	"sync"
	"sync/atomic"

	"github.com/agentculture/neurosymbolic-system/internal/senselog"
)

// stage is the senselog stage token every line this package emits carries, so
// one grep finds everything the loop reported.
const stage = "tick"

// dropLog is the engine's named-drop channel: every drop names its reason on
// one grep-able stderr line AND increments the counter Stats reports.
//
// A layer whose drops are invisible is indistinguishable from a layer that
// silently no-ops, so this is required rather than optional. A nil *dropLog is
// usable and counts nothing, which is what keeps Compose callable from a test
// without a logger.
type dropLog struct {
	logger *senselog.Logger
	drops  *atomic.Uint64

	// mu serializes writes to the logger. Almost every line comes from the
	// tick goroutine, but Send's full-inbox drop is emitted by the PRODUCER,
	// so two goroutines can reach the same io.Writer.
	mu sync.Mutex
}

func (d *dropLog) drop(source, event, reason, detail string) {
	if d == nil {
		return
	}
	if d.drops != nil {
		d.drops.Add(1)
	}
	d.line(source, event, reason, detail)
}

// line emits a drop line WITHOUT counting it, for a drop already counted where
// it happened. Send counts an inbox drop on the producer's goroutine so Stats
// is true immediately, and the tick goroutine reports the episode.
func (d *dropLog) line(source, event, reason, detail string) {
	if d == nil || d.logger == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.logger.Drop(stage, source, event, reason, detail)
}

func (d *dropLog) note(source, event, detail string) {
	if d == nil || d.logger == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.logger.Stage(stage, source, event, detail)
}
