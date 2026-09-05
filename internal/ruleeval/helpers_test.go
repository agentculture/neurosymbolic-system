package ruleeval_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/ruleeval"
	"github.com/agentculture/neurosymbolic-system/internal/rules"
	"github.com/agentculture/neurosymbolic-system/internal/sense"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// The toy vocabulary every test in this package runs against. It names NOTHING
// either donor robot names — the evaluator learns its fields and actions from an
// adaptor config, and a fixture borrowing a real robot's words would quietly
// make the rules layer look plant-specific (internal/adaptor's donor-literal
// guard scans the non-test sources for exactly that leak).
const toyVocabulary = `{
  "channels": [
    {"name": "ch_a", "arity": 2, "neutral": [0.0, 0.0]},
    {"name": "ch_b", "arity": 1, "neutral": [5.0]}
  ],
  "senses": [
    {"name": "lux", "type": "float"},
    {"name": "warm", "type": "bool"},
    {"name": "tag", "type": "string"}
  ],
  "actions": [
    {
      "name": "ramp",
      "claims": ["ch_a"],
      "params": [{"name": "gain", "min": 0.0, "max": 1.0}],
      "trajectories": {
        "ch_a": {"easing": {"kind": "linear", "from": [0.0, 0.0],
                            "to": [10.0, 20.0], "duration_s": 1.0}}
      }
    },
    {
      "name": "blip",
      "claims": ["ch_b"],
      "params": [{"name": "gain", "min": 0.0, "max": 1.0}],
      "trajectories": {
        "ch_b": {"easing": {"kind": "hold", "from": [9.0], "to": [9.0],
                            "duration_s": 0.5}}
      }
    },
    {
      "name": "hum",
      "claims": ["ch_a"],
      "loops": true,
      "trajectories": {
        "ch_a": {"easing": {"kind": "linear", "from": [-1.0, -1.0],
                            "to": [1.0, 1.0], "duration_s": 2.0}}
      }
    }
  ]
}`

const (
	fieldLux  = "lux"
	fieldWarm = "warm"
	fieldTag  = "tag"

	actionRamp = "ramp"
	actionBlip = "blip"
	actionHum  = "hum"
)

func toyVoc(t *testing.T) *adaptor.Vocabulary {
	t.Helper()
	v, err := adaptor.Parse([]byte(toyVocabulary))
	if err != nil {
		t.Fatalf("parsing the toy vocabulary: %v", err)
	}
	return v
}

func seconds(s float64) *float64 { return &s }

// leaf builds a leaf predicate.
func leaf(field, op string, value any) rules.Predicate {
	return rules.Predicate{Field: field, Op: op, Value: value}
}

// harness is one engine, its fake clock, its evaluator and its log, running in
// a goroutine a test drives tick by tick.
type harness struct {
	t     *testing.T
	voc   *adaptor.Vocabulary
	eng   *tick.Engine
	clock *tick.FakeClock
	snap  *sense.Snapshot
	sink  *adaptor.RecordingSink
	eval  *ruleeval.Evaluator
	reg   *ruleeval.Registry
	log   *bytes.Buffer

	period time.Duration
	events []tick.Event

	cancel context.CancelFunc
	done   chan error
}

const testPeriod = 20 * time.Millisecond

// newHarness wires a rules config onto a real engine over the toy vocabulary.
// tune may adjust the evaluator config before New.
func newHarness(t *testing.T, cfg *rules.Config, tune func(*ruleeval.Config)) *harness {
	t.Helper()
	v := toyVoc(t)
	clock := tick.NewFakeClock(testPeriod)
	log := &bytes.Buffer{}
	logger := senselog.New(log)
	snap := sense.New()

	sink := adaptor.NewRecordingSink()
	engine, err := tick.New(v, tick.Config{
		Period: testPeriod,
		Clock:  clock,
		Ticker: clock,
		Log:    logger,
	}, sink)
	if err != nil {
		t.Fatalf("tick.New: %v", err)
	}

	ecfg := ruleeval.Config{
		Rules:      cfg,
		Vocabulary: v,
		Snapshot:   snap,
		Logger:     logger,
	}
	if tune != nil {
		tune(&ecfg)
	}
	evaluator, err := ruleeval.New(ecfg)
	if err != nil {
		t.Fatalf("ruleeval.New: %v", err)
	}

	h := &harness{
		t: t, voc: v, eng: engine, clock: clock, snap: snap, sink: sink,
		eval: evaluator, reg: evaluator.Registry(), log: log, period: testPeriod,
	}
	engine.OnEvent(func(ev tick.Event) { h.events = append(h.events, ev) })
	if !engine.Send(tick.SetSeamCmd{Seam: evaluator.Seam()}) {
		t.Fatal("the engine refused the seam command")
	}
	return h
}

func (h *harness) start() {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan error, 1)
	go func() { h.done <- h.eng.Run(ctx) }()
}

func (h *harness) stop() {
	h.t.Helper()
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		h.t.Fatal("the engine did not exit within 5s")
	}
}

// ticks advances the clock by n whole tick periods, returning once the loop has
// finished all of them.
func (h *harness) ticks(n int) {
	h.t.Helper()
	h.clock.Advance(time.Duration(n) * h.period)
}

// feed publishes one perception frame stamped at the clock's current reading.
func (h *harness) feed(fields map[string]any) {
	h.snap.Update(fields, h.clock.Now())
}

// lines parses every SENSE line the run wrote.
func (h *harness) lines() []senselog.Line {
	h.t.Helper()
	var out []senselog.Line
	for _, raw := range strings.Split(h.log.String(), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		line, err := senselog.Parse(raw)
		if err != nil {
			h.t.Fatalf("unparseable log line %q: %v", raw, err)
		}
		out = append(out, line)
	}
	return out
}

// ruleLines is every line this rules layer wrote for one rule id.
func (h *harness) ruleLines(ruleID string) []senselog.Line {
	h.t.Helper()
	var out []senselog.Line
	for _, line := range h.lines() {
		if line.Stage == ruleeval.StageRule && line.Event == ruleID {
			out = append(out, line)
		}
	}
	return out
}

// fires counts the fire events emitted for one rule id.
func (h *harness) fires(ruleID string) int {
	count := 0
	for _, ev := range h.events {
		if ev.Name == ruleeval.EventFire && ev.Data["rule"] == ruleID {
			count++
		}
	}
	return count
}

// suppressions returns the reason of every suppression event for one rule id,
// in order.
func (h *harness) suppressions(ruleID string) []string {
	var out []string
	for _, ev := range h.events {
		if ev.Name == ruleeval.EventSuppress && ev.Data["rule"] == ruleID {
			out = append(out, ev.Data["reason"].(string))
		}
	}
	return out
}

// eventsNamed returns every emitted event of one name.
func (h *harness) eventsNamed(name string) []tick.Event {
	var out []tick.Event
	for _, ev := range h.events {
		if ev.Name == name {
			out = append(out, ev)
		}
	}
	return out
}
