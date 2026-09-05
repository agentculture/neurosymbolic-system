package ruleeval_test

import (
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/ruleeval"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// The bus isolates one rider's panic from the others: it loses its turn as one
// NAMED drop and every other rider still runs.
func TestBusIsolatesAPanickingDriver(t *testing.T) {
	h := newHarness(t, emptyCfg(), nil)
	logger := senselog.New(h.log)

	ran := 0
	bus := ruleeval.NewBus(logger).
		Add("boom", func(tick.TickContext) { panic("a rider blew up") }).
		Add("quiet", func(tick.TickContext) { ran++ })
	if !h.eng.Send(tick.SetSeamCmd{Seam: bus.Compose()}) {
		t.Fatal("the engine refused the composed seam")
	}
	h.start()
	defer h.stop()
	h.ticks(3)

	if ran != 3 {
		t.Fatalf("the second rider ran %d times, want 3 — one broken rider must not "+
			"silence another", ran)
	}
	var panics []senselog.Line
	for _, line := range h.lines() {
		if line.Stage == ruleeval.StageRule && line.Event == "boom" && line.Reason == "panic" {
			panics = append(panics, line)
		}
	}
	if len(panics) != 3 {
		t.Fatalf("got %d named panic drops, want one per tick:\n%s", len(panics), h.log.String())
	}
	if panics[0].Source != "bus" {
		t.Fatalf("the panic drop's source = %q, want the bus", panics[0].Source)
	}
}

func TestBusRunsDriversInOrder(t *testing.T) {
	h := newHarness(t, emptyCfg(), nil)
	var order []string
	bus := ruleeval.NewBus(senselog.New(h.log))
	seam := bus.Compose(
		func(tick.TickContext) { order = append(order, "first") },
		func(tick.TickContext) { order = append(order, "second") },
	)
	if !h.eng.Send(tick.SetSeamCmd{Seam: seam}) {
		t.Fatal("the engine refused the composed seam")
	}
	h.start()
	defer h.stop()
	h.ticks(1)

	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("drivers ran in order %v, want registration order", order)
	}
}
