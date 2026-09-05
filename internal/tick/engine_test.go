package tick

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
)

// harness is one engine, its fake clock, its recording sink and its log,
// running in a goroutine a test drives tick by tick.
type harness struct {
	t     *testing.T
	voc   *adaptor.Vocabulary
	eng   *Engine
	clock *FakeClock
	sink  *adaptor.RecordingSink
	log   *bytes.Buffer

	cancel context.CancelFunc
	done   chan error
}

// newHarness builds an engine at the given period. tune may adjust the config
// before New; the base layer is off unless it turns it on.
func newHarness(t *testing.T, period time.Duration, tune func(*Config)) *harness {
	t.Helper()
	v := toyVoc(t)
	clock := NewFakeClock(period)
	log := &bytes.Buffer{}
	cfg := Config{
		Period: period,
		Clock:  clock,
		Ticker: clock,
		Log:    testLogger(log),
	}
	if tune != nil {
		tune(&cfg)
	}
	sink := adaptor.NewRecordingSink()
	eng, err := New(v, cfg, sink)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &harness{t: t, voc: v, eng: eng, clock: clock, sink: sink, log: log}
}

// start runs the engine in its own goroutine.
func (h *harness) start() {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan error, 1)
	go func() { h.done <- h.eng.Run(ctx) }()
}

// stop cancels the run and returns Run's error, failing the test if the loop
// does not exit promptly.
func (h *harness) stop() error {
	h.t.Helper()
	h.cancel()
	return h.wait()
}

func (h *harness) wait() error {
	h.t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(5 * time.Second):
		h.t.Fatal("the engine did not exit within 5s")
		return nil
	}
}

// poses returns the poses streamed to the sink.
func (h *harness) poses() []adaptor.Pose { return h.sink.Written() }

// logLines returns the parsed SENSE lines the run emitted.
func (h *harness) logLines() []senselog.Line {
	h.t.Helper()
	var out []senselog.Line
	for _, raw := range strings.Split(strings.TrimRight(h.log.String(), "\n"), "\n") {
		if raw == "" {
			continue
		}
		line, err := senselog.Parse(raw)
		if err != nil {
			h.t.Fatalf("the engine emitted a line that is not SENSE grammar: %v (%q)", err, raw)
		}
		out = append(out, line)
	}
	return out
}

func (h *harness) hasDrop(source, reason string) bool {
	for _, line := range h.logLines() {
		if line.Dropped && line.Source == source && strings.Contains(line.Reason, reason) {
			return true
		}
	}
	return false
}

// assertComplete is acceptance criterion 1: every emitted pose fills every
// declared channel at that channel's arity.
func (h *harness) assertComplete(poses []adaptor.Pose) {
	h.t.Helper()
	if len(poses) == 0 {
		h.t.Fatal("no pose was streamed")
	}
	for i, pose := range poses {
		if len(pose) != len(h.voc.Channels()) {
			h.t.Fatalf("pose %d carries %d channels, want all %d", i, len(pose),
				len(h.voc.Channels()))
		}
		for _, ch := range h.voc.Channels() {
			values, present := pose[ch.Name]
			if !present {
				h.t.Fatalf("pose %d is missing channel %q", i, ch.Name)
			}
			if len(values) != ch.Arity {
				h.t.Fatalf("pose %d channel %q carries %d values, want %d",
					i, ch.Name, len(values), ch.Arity)
			}
		}
	}
}

func TestNewRefusesAnUnrunnableConfig(t *testing.T) {
	v := toyVoc(t)
	sink := adaptor.NewRecordingSink()
	cases := []struct {
		name string
		voc  *adaptor.Vocabulary
		cfg  Config
		sink adaptor.Sink
		want string
	}{
		{"no vocabulary", nil, Config{Period: time.Millisecond}, sink, "needs a vocabulary"},
		{"no sink", v, Config{Period: time.Millisecond}, nil, "needs a sink"},
		{"no period", v, Config{}, sink, "must be greater than zero"},
		{"base layer with no action", v, Config{Period: time.Millisecond, BaseLayer: true},
			sink, "no base action is named"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.voc, tc.cfg, tc.sink); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestRunRefusesAnUndeclaredBaseAction(t *testing.T) {
	h := newHarness(t, 20*time.Millisecond, func(c *Config) {
		c.BaseLayer = true
		c.BaseAction = "no-such-action"
	})
	err := h.eng.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("Run error = %v, want a refusal naming the undeclared base action", err)
	}
}

// Acceptance criterion 1: every emitted pose is complete, an unclaimed channel
// carries the declared neutral, and a driven channel carries the owner's value.
func TestEveryPoseIsCompleteAndUnclaimedChannelsAreNeutral(t *testing.T) {
	h := newHarness(t, 20*time.Millisecond, nil)
	h.eng.Send(AdmitCmd{Behavior: Behavior{
		Action: "ramp", Class: ClassStoppable,
		Lifetime: Lifetime{DurationS: seconds(1)}, AdmittedAt: h.clock.Now(),
	}})
	h.start()
	h.clock.Advance(5 * 20 * time.Millisecond)
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	poses := h.poses()
	h.assertComplete(poses)
	for i, pose := range poses {
		if pose["ch_b"][0] != 5 {
			t.Fatalf("pose %d ch_b = %v, want the declared neutral [5]", i, pose["ch_b"])
		}
		if pose["ch_c"][2] != 3 {
			t.Fatalf("pose %d ch_c = %v, want the declared neutral [1 2 3]", i, pose["ch_c"])
		}
	}
	if poses[0]["ch_a"][0] == 0 && poses[1]["ch_a"][0] == 0 {
		t.Fatalf("ch_a never moved: %v then %v", poses[0]["ch_a"], poses[1]["ch_a"])
	}
}

// Acceptance criterion 4, and criterion 3's timing half: pose frames over an
// action's duration match its declared trajectory sampled at the tick period,
// at 20, 50 and 100 Hz alike. The engine reads no wall clock, so the same
// assertion holds at every rate.
func TestPoseFramesMatchTheTrajectoryAtEveryRate(t *testing.T) {
	const tolerance = 1e-9
	for _, hz := range []float64{20, 50, 100} {
		t.Run(fmt.Sprintf("%.0fHz", hz), func(t *testing.T) {
			period := time.Duration(float64(time.Second) / hz)
			h := newHarness(t, period, nil)
			traj := trajectoryFor(t, h.voc, "ramp", "ch_a")

			h.eng.Send(AdmitCmd{Behavior: Behavior{
				Action: "ramp", Class: ClassStoppable,
				Lifetime: Lifetime{DurationS: seconds(1)}, AdmittedAt: h.clock.Now(),
			}})
			h.start()

			const ticks = 10
			h.clock.Advance(ticks * period)
			if err := h.stop(); err != nil {
				t.Fatalf("Run: %v", err)
			}

			poses := h.poses()
			h.assertComplete(poses)
			if len(poses) < ticks {
				t.Fatalf("streamed %d poses, want at least %d", len(poses), ticks)
			}
			for k := 1; k <= ticks; k++ {
				tLocal := float64(k) * period.Seconds()
				want := traj.At(tLocal)
				got := poses[k-1]["ch_a"]
				for i := range want {
					if math.Abs(got[i]-want[i]) > tolerance {
						t.Fatalf("tick %d ch_a[%d] = %v, want %v (trajectory at t=%v)",
							k, i, got[i], want[i], tLocal)
					}
				}
			}
		})
	}
}

// Acceptance criterion 1's hardest half: an owner that abstains on a channel
// yields it to the next claimant THE SAME TICK, rather than freezing it.
func TestAbstainingOwnerYieldsTheChannelTheSameTick(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, nil)

	// A passive base holding ch_a at a value nothing else produces, and a
	// higher-priority reactive behavior that only speaks on even ticks.
	h.eng.Send(AdmitCmd{Behavior: Behavior{
		Name: "base", Class: ClassPassive, Channels: []string{"ch_a"},
		Lifetime:   Lifetime{Loops: true},
		AdmittedAt: h.clock.Now(),
		Contribute: func(float64) Contribution { return Contribution{"ch_a": {-1, -1}} },
	}})
	h.eng.Send(AdmitCmd{Behavior: Behavior{
		Name: "reactive", Class: ClassStoppable, Channels: []string{"ch_a"},
		Lifetime:   Lifetime{Loops: true},
		AdmittedAt: h.clock.Now(),
		Contribute: func(tLocal float64) Contribution {
			if int(math.Round(tLocal/period.Seconds()))%2 == 0 {
				return Contribution{"ch_a": {8, 8}}
			}
			return Contribution{} // nothing to say: abstain
		},
	}})
	h.start()
	h.clock.Advance(4 * period)
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	poses := h.poses()
	h.assertComplete(poses)
	for k := 1; k <= 4; k++ {
		got := poses[k-1]["ch_a"][0]
		want := -1.0
		if k%2 == 0 {
			want = 8
		}
		if got != want {
			t.Fatalf("tick %d ch_a = %v, want %v (an abstaining owner must yield the "+
				"channel, not freeze it)", k, got, want)
		}
	}
}

// The base layer is seeded before the first tick as a PASSIVE behavior, so an
// idle robot keeps moving and gives the channel up the moment something else
// claims it. The engine never names the action; the config does.
func TestBaseLayerIsSeededPassive(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, func(c *Config) {
		c.BaseLayer = true
		c.BaseAction = "hum"
	})
	h.start()
	h.clock.Advance(2 * period)

	// Nothing else is active: the base owns both of its channels and moves.
	poses := h.poses()
	h.assertComplete(poses)
	if poses[0]["ch_a"][0] == poses[1]["ch_a"][0] {
		t.Fatalf("the base layer did not move ch_a: %v then %v",
			poses[0]["ch_a"], poses[1]["ch_a"])
	}
	if poses[0]["ch_b"][0] == 5 {
		t.Fatalf("ch_b = %v, want the base layer driving it rather than the neutral",
			poses[0]["ch_b"])
	}

	// A stoppable claimant takes ch_a and the base keeps ch_b.
	h.eng.Send(AdmitCmd{Behavior: Behavior{
		Name: "driver", Class: ClassStoppable, Channels: []string{"ch_a"},
		Lifetime:   Lifetime{Loops: true},
		Contribute: func(float64) Contribution { return Contribution{"ch_a": {42, 42}} },
	}})
	h.clock.Advance(period)
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	poses = h.poses()
	last := poses[2]
	if last["ch_a"][0] != 42 {
		t.Fatalf("ch_a = %v, want the stoppable claimant to outrank the passive base",
			last["ch_a"])
	}
	if last["ch_b"][0] == 5 {
		t.Fatalf("ch_b = %v, want the base layer still driving the channel nothing "+
			"else claims", last["ch_b"])
	}
}

// The seam runs exactly once per tick, AFTER the pose has streamed, and sees
// the pose that was actually written.
func TestSeamRunsOncePerTickAfterTheWrite(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, nil)

	type observation struct {
		tick    int
		written int
		pose    adaptor.Pose
		owner   string
		names   []string
	}
	var seen []observation
	h.eng.Send(SetSeamCmd{Seam: func(ctx TickContext) {
		seen = append(seen, observation{
			tick:    ctx.Tick,
			written: len(h.sink.Written()),
			pose:    ctx.Pose(),
			owner:   ctx.Ownership()["ch_a"],
			names:   ctx.ActiveNames(),
		})
	}})
	h.eng.Send(AdmitCmd{Behavior: Behavior{
		Action: "ramp", Class: ClassStoppable,
		Lifetime: Lifetime{DurationS: seconds(1)}, AdmittedAt: h.clock.Now(),
	}})
	h.start()
	h.clock.Advance(3 * period)
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(seen) != 3 {
		t.Fatalf("the seam ran %d times, want once per tick (3)", len(seen))
	}
	for i, obs := range seen {
		if obs.tick != i+1 {
			t.Fatalf("seam saw tick %d, want the 1-based %d", obs.tick, i+1)
		}
		if obs.written != i+1 {
			t.Fatalf("at tick %d the sink had %d poses, want the seam to run AFTER the "+
				"write (%d)", obs.tick, obs.written, i+1)
		}
		streamed := h.poses()[i]
		for channel, values := range streamed {
			for j := range values {
				if obs.pose[channel][j] != values[j] {
					t.Fatalf("tick %d: the seam's pose disagrees with what streamed on %q",
						obs.tick, channel)
				}
			}
		}
		if obs.owner == Unowned {
			t.Fatalf("tick %d: ownership of ch_a is Unowned, want the admitted behavior",
				obs.tick)
		}
		if len(obs.names) != 1 || obs.names[0] != "ramp" {
			t.Fatalf("tick %d: active names = %v, want [ramp]", obs.tick, obs.names)
		}
	}
}

// A seam admits and evicts synchronously — it is on the tick goroutine — and
// publishes events to whatever consumers were registered.
func TestSeamAdmitsEvictsAndEmits(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, nil)

	var events []Event
	h.eng.OnEvent(func(e Event) { events = append(events, e) })

	h.eng.Send(SetSeamCmd{Seam: func(ctx TickContext) {
		switch ctx.Tick {
		case 1:
			if _, err := ctx.Admit(Behavior{
				Name: "late", Class: ClassStoppable, Channels: []string{"ch_a"},
				Lifetime:   Lifetime{Loops: true},
				Contribute: func(float64) Contribution { return Contribution{"ch_a": {6, 6}} },
			}); err != nil {
				t.Errorf("ctx.Admit: %v", err)
			}
		case 3:
			if removed := ctx.Evict("late"); removed != 1 {
				t.Errorf("ctx.Evict removed %d, want 1", removed)
			}
		}
		ctx.Emit(Event{Name: "observed", Data: map[string]any{"tick": ctx.Tick}})
	}})
	h.start()
	h.clock.Advance(4 * period)
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	poses := h.poses()
	if poses[0]["ch_a"][0] != 0 {
		t.Fatalf("tick 1 ch_a = %v, want the neutral (the admit happens after the write)",
			poses[0]["ch_a"])
	}
	if poses[1]["ch_a"][0] != 6 || poses[2]["ch_a"][0] != 6 {
		t.Fatalf("ticks 2-3 ch_a = %v, %v, want the admitted behavior driving",
			poses[1]["ch_a"], poses[2]["ch_a"])
	}
	if poses[3]["ch_a"][0] != 0 {
		t.Fatalf("tick 4 ch_a = %v, want the neutral after the eviction", poses[3]["ch_a"])
	}
	if len(events) != 4 {
		t.Fatalf("consumers saw %d events, want 4", len(events))
	}
}

// A behavior whose lifetime elapses releases its channels, which fall back to
// the neutral (or to the next claimant).
func TestExpiredBehaviorReleasesItsChannel(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, nil)
	h.eng.Send(AdmitCmd{Behavior: Behavior{
		Name: "brief", Class: ClassStoppable, Channels: []string{"ch_a"},
		Lifetime:   Lifetime{DurationS: seconds(0.05)},
		AdmittedAt: h.clock.Now(),
		Contribute: func(float64) Contribution { return Contribution{"ch_a": {3, 3}} },
	}})
	h.start()
	h.clock.Advance(4 * period)
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	poses := h.poses()
	if poses[0]["ch_a"][0] != 3 || poses[1]["ch_a"][0] != 3 {
		t.Fatalf("ticks 1-2 ch_a = %v, %v, want the behavior driving",
			poses[0]["ch_a"], poses[1]["ch_a"])
	}
	if poses[2]["ch_a"][0] != 0 {
		t.Fatalf("tick 3 ch_a = %v, want the neutral once the 50 ms lifetime elapsed",
			poses[2]["ch_a"])
	}
}

// A full inbox is a NAMED drop, never backpressure: Send never blocks, Stats
// counts the loss immediately, and the next tick reports it on one grep-able
// line.
func TestFullInboxIsANamedDropNeverABlock(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, func(c *Config) { c.InboxSize = 1 })

	accepted := 0
	for i := 0; i < 4; i++ {
		if h.eng.Send(EvictCmd{Name: "nobody"}) {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("%d commands were accepted, want 1 (the inbox holds one)", accepted)
	}
	if got := h.eng.Stats().Drops; got != 3 {
		t.Fatalf("Stats().Drops = %d, want 3", got)
	}

	h.start()
	h.clock.Advance(period)
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !h.hasDrop("inbox", "full") {
		t.Fatalf("no inbox drop line was emitted; the log was:\n%s", h.log.String())
	}
}

// A behavior the vocabulary refuses is a named drop, not a panic and not a
// silent no-op: the tick survives and the reason is on stderr.
func TestARefusedAdmissionIsANamedDrop(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, nil)
	h.eng.Send(AdmitCmd{Behavior: Behavior{
		Action: "no-such-action", Class: ClassStoppable, Lifetime: Lifetime{Loops: true},
	}})
	h.start()
	h.clock.Advance(2 * period)
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !h.hasDrop("admit", "refused") {
		t.Fatalf("no refusal line was emitted; the log was:\n%s", h.log.String())
	}
	h.assertComplete(h.poses())
}

// Sink errors are tolerated up to MaxErrors CONSECUTIVE failures; a success in
// between resets the count. At the ceiling Run returns the error.
func TestSinkErrorsAreToleratedUntilTheCeiling(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, func(c *Config) { c.MaxErrors = 3 })
	boom := errors.New("the transport is gone")
	h.sink.Err = boom
	h.start()
	h.clock.Advance(3 * period)

	err := h.wait()
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("Run error = %v, want it to wrap the sink's error", err)
	}
	if got := h.eng.Stats().SinkErrors; got != 3 {
		t.Fatalf("Stats().SinkErrors = %d, want 3", got)
	}
	if !h.hasDrop("sink", "write-failed") {
		t.Fatalf("no sink drop line was emitted; the log was:\n%s", h.log.String())
	}
}

func TestATransientSinkErrorResetsTheCount(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, func(c *Config) { c.MaxErrors = 2 })
	h.sink.Err = errors.New("one bad write")
	h.start()
	h.clock.Advance(period)
	h.sink.Err = nil
	h.clock.Advance(period)
	h.sink.Err = errors.New("another bad write")
	h.clock.Advance(period)

	// Two failures separated by a success is not a ceiling: the loop is alive.
	if err := h.stop(); err != nil {
		t.Fatalf("Run error = %v, want nil: the failures were not consecutive", err)
	}
}

// The loop settles to neutral on exit unless the config says not to.
func TestSettleWritesOneNeutralPoseOnExit(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, nil)
	h.eng.Send(AdmitCmd{Behavior: Behavior{
		Name: "driver", Class: ClassStoppable, Channels: []string{"ch_a"},
		Lifetime:   Lifetime{Loops: true},
		Contribute: func(float64) Contribution { return Contribution{"ch_a": {9, 9}} },
	}})
	h.start()
	h.clock.Advance(2 * period)
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	poses := h.poses()
	last := poses[len(poses)-1]
	if last["ch_a"][0] != 0 {
		t.Fatalf("the last pose ch_a = %v, want the settling neutral", last["ch_a"])
	}
	if poses[len(poses)-2]["ch_a"][0] != 9 {
		t.Fatalf("the tick before the settle = %v, want the driver's value",
			poses[len(poses)-2]["ch_a"])
	}
}

func TestSettleCanBeTurnedOff(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, func(c *Config) { c.Settle = Bool(false) })
	h.eng.Send(AdmitCmd{Behavior: Behavior{
		Name: "driver", Class: ClassStoppable, Channels: []string{"ch_a"},
		Lifetime:   Lifetime{Loops: true},
		Contribute: func(float64) Contribution { return Contribution{"ch_a": {9, 9}} },
	}})
	h.start()
	h.clock.Advance(2 * period)
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	poses := h.poses()
	if len(poses) != 2 {
		t.Fatalf("streamed %d poses, want exactly the 2 ticks and no settle", len(poses))
	}
	if poses[1]["ch_a"][0] != 9 {
		t.Fatalf("the last pose = %v, want the driver's value left standing", poses[1]["ch_a"])
	}
}

// An overrunning tick is COUNTED and reported as one episode — an entry line
// and a summary naming the streak length — never one line per tick. The loop
// neither skips nor double-ticks to catch up: a skipped tick is a seam that
// silently did not run.
func TestOverrunIsCountedAndReportedAsAnEpisode(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, nil)
	h.eng.Send(SetSeamCmd{Seam: func(ctx TickContext) {
		if ctx.Tick <= 3 {
			h.clock.Elapse(period + 10*time.Millisecond)
		}
	}})
	h.start()
	h.clock.Advance(5 * period)
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	stats := h.eng.Stats()
	if stats.Ticks != 5 {
		t.Fatalf("Stats().Ticks = %d, want 5: an overrun never skips or double-ticks",
			stats.Ticks)
	}
	if stats.Overruns != 3 {
		t.Fatalf("Stats().Overruns = %d, want 3", stats.Overruns)
	}
	if len(h.poses()) != 6 {
		t.Fatalf("streamed %d poses, want 5 ticks plus the settle", len(h.poses()))
	}

	var entries, summaries int
	for _, line := range h.logLines() {
		if line.Source != "budget" {
			continue
		}
		if strings.Contains(line.Detail, "suppressed") {
			summaries++
			if !strings.Contains(line.Detail, "suppressed 3 ticks") {
				t.Fatalf("summary detail = %q, want it to name the 3-tick streak",
					line.Detail)
			}
			continue
		}
		entries++
	}
	if entries != 1 || summaries != 1 {
		t.Fatalf("the overrun episode produced %d entry and %d summary lines, want 1 and 1",
			entries, summaries)
	}
}

// A tick that fits the budget is not an overrun at any rate — the comparison is
// against the configured period, never a compiled-in 20 ms.
func TestBudgetIsMeasuredAgainstTheConfiguredPeriod(t *testing.T) {
	for _, hz := range []float64{20, 50, 100} {
		t.Run(fmt.Sprintf("%.0fHz", hz), func(t *testing.T) {
			period := time.Duration(float64(time.Second) / hz)
			h := newHarness(t, period, nil)
			h.eng.Send(SetSeamCmd{Seam: func(TickContext) {
				h.clock.Elapse(period / 2)
			}})
			h.start()
			h.clock.Advance(4 * period)
			if err := h.stop(); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := h.eng.Stats().Overruns; got != 0 {
				t.Fatalf("Stats().Overruns = %d at %.0f Hz, want 0: half a period is "+
					"within budget", got, hz)
			}
		})
	}
}

// A rider that panics loses its turn on that tick and NOTHING else: the pose
// already streamed, the loop keeps ticking on schedule, the drop names the
// reason once, and the engine still accepts commands afterwards. A dead tick
// loop would leave a robot frozen at whatever the last pose happened to be,
// which is the worse failure.
func TestASeamPanicIsIsolatedFromTheLoop(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, nil)

	h.eng.Send(SetSeamCmd{Seam: func(ctx TickContext) {
		if ctx.Tick == 3 {
			panic("a rider blew up\nwith a second line")
		}
	}})
	h.start()
	h.clock.Advance(10 * period)

	stats := h.eng.Stats()
	if stats.Ticks != 10 {
		t.Fatalf("Stats().Ticks = %d, want 10: a seam panic must not stop the loop",
			stats.Ticks)
	}
	if stats.SeamPanics != 1 {
		t.Fatalf("Stats().SeamPanics = %d, want 1", stats.SeamPanics)
	}
	if len(h.poses()) != 10 {
		t.Fatalf("streamed %d poses, want 10 (the settle has not happened yet)",
			len(h.poses()))
	}
	h.assertComplete(h.poses())

	var panics []senselog.Line
	for _, line := range h.logLines() {
		if line.Dropped && line.Reason == "panic" {
			panics = append(panics, line)
		}
	}
	if len(panics) != 1 {
		t.Fatalf("got %d panic drop lines, want exactly 1: %+v", len(panics), panics)
	}
	if panics[0].Source != "seam" {
		t.Fatalf("the panic drop names source %q, want seam", panics[0].Source)
	}
	if panics[0].Detail != "a rider blew up" {
		t.Fatalf("the panic drop detail = %q, want the recovered value's first line "+
			"(a multi-line detail would break the one-line grammar)", panics[0].Detail)
	}

	// The engine is still taking work: admit a behavior through the inbox and
	// watch it drive its channel on the next tick.
	if !h.eng.Send(AdmitCmd{Behavior: Behavior{
		Name: "after", Class: ClassStoppable, Channels: []string{"ch_a"},
		Lifetime:   Lifetime{Loops: true},
		Contribute: func(float64) Contribution { return Contribution{"ch_a": {4, 4}} },
	}}) {
		t.Fatal("the inbox refused a command after the panic")
	}
	h.clock.Advance(period)
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	poses := h.poses()
	if poses[10]["ch_a"][0] != 4 {
		t.Fatalf("tick 11 ch_a = %v, want the behavior admitted after the panic driving it",
			poses[10]["ch_a"])
	}
}

// One panicking event consumer cannot silence another: each callback is
// isolated, and every panic is its own named drop.
func TestAPanickingEventConsumerDoesNotSilenceTheOthers(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, nil)

	var before, after int
	h.eng.OnEvent(func(Event) { before++ })
	h.eng.OnEvent(func(Event) { panic("this consumer is broken") })
	h.eng.OnEvent(func(Event) { after++ })

	h.eng.Send(SetSeamCmd{Seam: func(ctx TickContext) {
		ctx.Emit(Event{Name: "observed"})
	}})
	h.start()
	h.clock.Advance(3 * period)
	if err := h.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if before != 3 || after != 3 {
		t.Fatalf("consumers ran %d and %d times, want 3 each: a panicking consumer "+
			"must not silence the ones around it", before, after)
	}
	if got := h.eng.Stats().SeamPanics; got != 3 {
		t.Fatalf("Stats().SeamPanics = %d, want 3 (one per tick)", got)
	}
	for _, line := range h.logLines() {
		if line.Dropped && line.Reason == "panic" && line.Source != "event" {
			t.Fatalf("a consumer panic was reported with source %q, want event",
				line.Source)
		}
	}
}

// Nothing this package emits may reach stdout, and everything it emits must
// parse as SENSE grammar — logLines() would have failed the test otherwise.
func TestEveryEmittedLineIsSenseGrammar(t *testing.T) {
	const period = 20 * time.Millisecond
	h := newHarness(t, period, func(c *Config) { c.InboxSize = 1 })
	h.sink.Err = errors.New("a bad write")
	for i := 0; i < 3; i++ {
		h.eng.Send(EvictCmd{Name: "nobody"})
	}
	h.eng.Send(AdmitCmd{Behavior: Behavior{Action: "nope", Class: ClassStoppable}})
	h.start()
	h.clock.Advance(2 * period)
	_ = h.stop()

	lines := h.logLines()
	if len(lines) == 0 {
		t.Fatal("the run emitted no lines at all")
	}
	for _, line := range lines {
		if line.Stage != "tick" {
			t.Fatalf("line %+v carries stage %q, want the package's single stage token",
				line, line.Stage)
		}
		if line.Dropped && line.Reason == "" {
			t.Fatalf("line %+v is a drop with no reason", line)
		}
	}
}
