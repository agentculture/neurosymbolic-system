// Package conformance replays donor-derived fixtures through the engine.
//
// A fixture is four files in one directory — a vocabulary, a rules stack, a
// sense trace and the expected event/ownership/pose trace — and nothing else.
// The point is falsifiability: the two donor robots already have rule engines
// with a year of behaviour baked into their test suites, and the only honest
// way to claim this runtime replaces them is to replay what their tests assert
// and show the same decisions come out.
//
// # Why a whole trace rather than a set of assertions
//
// A hand-written assertion tests what its author remembered to look at. A
// recorded trace is TOTAL: every tick carries its events, its channel ownership
// and the completeness of its pose, so a regression that moves a fire by one
// tick, adds a second suppression frame, or hands a channel to the wrong owner
// fails the fixture even though nobody predicted that failure. `-update`
// regenerates the traces; a regenerated trace is worthless until it has been
// re-checked against the donor test it came from, which is why every case's
// check is written down in docs/verification/2026-09-05-donor-conformance.md.
//
// # What is compared, and what deliberately is not
//
// Pose VALUES are not compared. A donor's trajectories live in Python behaviour
// functions this runtime does not port; the shapes in a fixture's adaptor.toml
// are legible stand-ins, and comparing their numbers would be comparing the
// fixture to itself. Pose COMPLETENESS is compared — every tick must carry
// every declared channel, which is the property the engine actually promises
// (an unclaimed channel falls to neutral, so a target is never partial).
//
// # Determinism
//
// Everything is driven by tick.FakeClock: the loop reads no wall clock, and
// Advance returns only once the tick it delivered has been fully worked, so a
// replay is a pure function of its four files. Nothing here opens a socket,
// spawns a process or touches the network.
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/ruleeval"
	"github.com/agentculture/neurosymbolic-system/internal/rules"
	"github.com/agentculture/neurosymbolic-system/internal/sense"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// The fixture file names. A case directory carries the ones it needs; an
// adaptor.toml or a rules layer missing from the case directory is looked up
// one level higher, so the cases of one donor share one vocabulary instead of
// keeping N copies that can drift apart.
const (
	AdaptorFile  = "adaptor.toml"
	RulesFile    = "rules.toml"
	SensesFile   = "senses.jsonl"
	ExpectedFile = "expected.jsonl"
	CaseFile     = "case.toml"
)

// DefaultPeriod is the replay tick period when case.toml names none.
const DefaultPeriod = 20 * time.Millisecond

// EventFrame is one event the seam emitted, as it is recorded in a trace.
type EventFrame struct {
	Name string         `json:"name"`
	Data map[string]any `json:"data"`
}

// Frame is one tick of a trace: what was emitted, who owned what, and which
// channels the composed pose carried.
type Frame struct {
	Tick         int               `json:"tick"`
	Events       []EventFrame      `json:"events"`
	Ownership    map[string]string `json:"ownership"`
	PoseChannels []string          `json:"pose_channels"`
}

// Trace is one replay's result.
type Trace struct {
	// Frames is one entry per tick, in tick order.
	Frames []Frame
	// Poses is every pose the sink was handed, including the settling neutral
	// pose the engine writes as it exits.
	Poses []adaptor.Pose
	// SenseLog is everything the run wrote to its senselog. It is captured
	// rather than discarded so a test can assert the stderr half of the
	// observability contract without spawning a process.
	SenseLog string
}

// senseFrame is one line of senses.jsonl.
type senseFrame struct {
	Tick   int            `json:"tick"`
	Fields map[string]any `json:"fields"`
}

// preadmit is one behavior the harness admits before the first tick, standing
// in for a consumer-side injected intent.
//
// It exists because two donor behaviours cannot be expressed in a rules file at
// all. An ABSTAINING behavior is one of them: a vocabulary must give every
// claimed channel a trajectory, so a data-only action can never decline a
// channel, while the donor's sense-driven entries (its `wants_sense=True`
// library entries) do exactly that whenever they have nothing to say. The other
// is a behavior a rule never fires but a CLI or an agent injects, which is what
// an inhibit has to be able to evict.
type preadmit struct {
	// Name is the behavior's library name — what an inhibit's `disable` and an
	// already-active check match on.
	Name string `toml:"name"`
	// Action is the vocabulary action whose claimed channels this behavior
	// takes. Its trajectories are NOT sampled when AbstainUnlessField is set.
	Action string `toml:"action"`
	// Class is the contention class; empty means stoppable.
	Class string `toml:"class"`
	// Loops asks for a repeating admission; DurationS bounds it.
	Loops     bool     `toml:"loops"`
	DurationS *float64 `toml:"duration_s"`
	// AbstainUnlessField, when set, makes this behavior contribute only on the
	// ticks where that sense field has a reading — and ABSTAIN on every other
	// tick, yielding its channels to whatever claims them next.
	AbstainUnlessField string `toml:"abstain_unless_field"`
}

// caseConfig is the optional case.toml: everything about a replay that is not
// the vocabulary, the rules or the senses.
type caseConfig struct {
	// PeriodMS is the tick period in milliseconds. It is per-case because the
	// donor tests are written at two different cadences (0.25 s steps for the
	// reachy rule-engine tests, 50 Hz for microduck's), and a runtime whose
	// tunings only worked at one rate would be a bug class of its own.
	PeriodMS float64 `toml:"period_ms"`
	// Ticks is how many ticks to run; zero means "as many as senses.jsonl
	// mentions".
	Ticks int `toml:"ticks"`
	// BaseAction is seeded as the passive base layer. Empty means none.
	BaseAction string `toml:"base_action"`
	// RuleLayers are the rules files, ONE LAYER PER ENTRY, in order — the same
	// spelling `run --rules a.toml --rules b.toml` uses. Empty means the single
	// layer RulesFile.
	RuleLayers []string `toml:"rule_layers"`
	// Preadmit is what to admit before the first tick.
	Preadmit []preadmit `toml:"preadmit"`
}

// Replay runs the fixture in dir and returns its trace.
//
// It assembles the runtime the way the composition root does — vocabulary,
// snapshot, evaluator over the one registry, a fan-out bus on the ONE seam,
// engine — but over a fake clock and a recording sink, so nothing listens and
// nothing sleeps.
func Replay(dir string) (Trace, error) {
	cfg, err := loadCase(dir)
	if err != nil {
		return Trace{}, err
	}
	voc, err := loadVocabulary(dir)
	if err != nil {
		return Trace{}, err
	}
	layers, err := ruleLayers(dir, cfg)
	if err != nil {
		return Trace{}, err
	}
	ruleCfg, err := rules.Load(layers, voc)
	if err != nil {
		return Trace{}, fmt.Errorf("%s: rules: %w", dir, err)
	}
	frames, lastTick, err := loadSenses(dir)
	if err != nil {
		return Trace{}, err
	}
	ticks := cfg.Ticks
	if ticks <= 0 {
		ticks = lastTick
	}
	if ticks <= 0 {
		return Trace{}, fmt.Errorf("%s: the fixture runs zero ticks — give %s at least "+
			"one line, or set ticks in %s", dir, SensesFile, CaseFile)
	}

	logBuf := &strings.Builder{}
	log := senselog.New(logBuf)
	snapshot := sense.New()
	registry := ruleeval.NewRegistry()
	eval, err := ruleeval.New(ruleeval.Config{
		Rules: ruleCfg, Vocabulary: voc, Snapshot: snapshot,
		Registry: registry, Logger: log,
	})
	if err != nil {
		return Trace{}, fmt.Errorf("%s: %w", dir, err)
	}

	period := cfg.period()
	clock := tick.NewFakeClock(period)
	sink := adaptor.NewRecordingSink()
	engine, err := tick.New(voc, tick.Config{
		Period:     period,
		BaseLayer:  cfg.BaseAction != "",
		BaseAction: cfg.BaseAction,
		Clock:      clock,
		Ticker:     clock,
		Log:        log,
	}, sink)
	if err != nil {
		return Trace{}, fmt.Errorf("%s: %w", dir, err)
	}

	rec := &recorder{}
	engine.OnEvent(rec.observe)
	seam := ruleeval.NewBus(log).
		Add(ruleeval.StageRule, eval.Tick).
		Add("record", rec.tick).
		Compose()
	if !engine.Send(tick.SetSeamCmd{Seam: seam}) {
		return Trace{}, fmt.Errorf("%s: the engine refused the tick seam", dir)
	}
	for _, spec := range cfg.Preadmit {
		behavior, err := spec.behavior(voc, snapshot)
		if err != nil {
			return Trace{}, fmt.Errorf("%s: %s: %w", dir, CaseFile, err)
		}
		if !engine.Send(tick.AdmitCmd{Behavior: behavior}) {
			return Trace{}, fmt.Errorf("%s: the engine refused a preadmit command", dir)
		}
	}

	done := make(chan error, 1)
	go func() { done <- engine.Run(context.Background()) }()
	for n := 1; n <= ticks; n++ {
		if fields := frames[n]; len(fields) > 0 {
			snapshot.Update(fields, clock.Now())
		}
		clock.Advance(period)
	}
	clock.Stop()
	if err := <-done; err != nil {
		return Trace{}, fmt.Errorf("%s: the engine stopped early: %w", dir, err)
	}

	return Trace{Frames: rec.frames, Poses: sink.Written(), SenseLog: logBuf.String()}, nil
}

// recorder is the seam's last rider plus the event consumer that feeds it.
//
// Every event a tick emits arrives on the tick goroutine while the rule rider
// is running, so by the time this rider is called the tick's events are all in
// hand and can be filed against the tick that produced them. There is no lock
// because there is no second writer: the engine calls both on one goroutine.
type recorder struct {
	pending []EventFrame
	frames  []Frame
}

func (r *recorder) observe(event tick.Event) {
	r.pending = append(r.pending, EventFrame{Name: event.Name, Data: event.Data})
}

func (r *recorder) tick(ctx tick.TickContext) {
	events := r.pending
	if events == nil {
		events = []EventFrame{}
	}
	r.pending = nil
	r.frames = append(r.frames, Frame{
		Tick:         ctx.Tick,
		Events:       events,
		Ownership:    ctx.Ownership(),
		PoseChannels: sortedKeys(ctx.Pose()),
	})
}

// behavior turns one preadmit entry into the behavior to admit.
func (p preadmit) behavior(voc *adaptor.Vocabulary, view *sense.Snapshot) (tick.Behavior, error) {
	name := p.Name
	if name == "" {
		name = p.Action
	}
	if name == "" {
		return tick.Behavior{}, errors.New("a preadmit entry names neither a name nor an action")
	}
	class := tick.StopClass(p.Class)
	if p.Class == "" {
		class = tick.ClassStoppable
	}
	b := tick.Behavior{
		Name:     name,
		Class:    class,
		Action:   p.Action,
		Lifetime: tick.Lifetime{Loops: p.Loops, DurationS: p.DurationS},
	}
	if p.AbstainUnlessField == "" {
		return b, nil
	}

	// An abstaining behavior drives its channels itself: it claims the action's
	// channels but never samples the action's trajectories, so the fixture's
	// trajectory shapes cannot accidentally decide whether it abstains.
	claims, arities, err := claimsOf(voc, p.Action)
	if err != nil {
		return tick.Behavior{}, err
	}
	field := p.AbstainUnlessField
	b.Action = ""
	b.Channels = claims
	b.Contribute = func(float64) tick.Contribution {
		if _, ok := view.Get(field); !ok {
			return nil // abstain: every claimed channel falls through
		}
		out := make(tick.Contribution, len(claims))
		for i, channel := range claims {
			values := make([]float64, arities[i])
			for j := range values {
				values[j] = 1.0
			}
			out[channel] = values
		}
		return out
	}
	return b, nil
}

// claimsOf is the action's claimed channels and their arities.
func claimsOf(voc *adaptor.Vocabulary, action string) ([]string, []int, error) {
	for _, a := range voc.Actions() {
		if a.Name != action {
			continue
		}
		claims := append([]string(nil), a.Claims...)
		arities := make([]int, len(claims))
		for i, name := range claims {
			for _, ch := range voc.Channels() {
				if ch.Name == name {
					arities[i] = ch.Arity
				}
			}
		}
		return claims, arities, nil
	}
	return nil, nil, fmt.Errorf("action %q is not declared by %s", action, voc.Origin())
}

// period is the case's tick period, defaulted and validated.
func (c caseConfig) period() time.Duration {
	if c.PeriodMS <= 0 {
		return DefaultPeriod
	}
	return time.Duration(c.PeriodMS * float64(time.Millisecond))
}

// loadCase reads case.toml, which is optional: a case with no knobs at all is
// a valid case.
func loadCase(dir string) (caseConfig, error) {
	var cfg caseConfig
	path := filepath.Join(dir, CaseFile)
	data, err := os.ReadFile(path) // #nosec G304 - a fixture path under testdata
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// loadVocabulary reads the case's adaptor.toml, falling back to the donor
// directory's.
func loadVocabulary(dir string) (*adaptor.Vocabulary, error) {
	path, err := resolve(dir, AdaptorFile)
	if err != nil {
		return nil, err
	}
	voc, err := adaptor.LoadTOML(path)
	if err != nil {
		return nil, err
	}
	return voc, nil
}

// ruleLayers resolves the case's rule layer stack, one layer per entry.
func ruleLayers(dir string, cfg caseConfig) ([][]string, error) {
	names := cfg.RuleLayers
	if len(names) == 0 {
		names = []string{RulesFile}
	}
	out := make([][]string, 0, len(names))
	for _, name := range names {
		path, err := resolve(dir, name)
		if err != nil {
			return nil, err
		}
		out = append(out, []string{path})
	}
	return out, nil
}

// resolve finds name in dir, else in dir's parent. The fallback is what lets
// every case of one donor share one vocabulary and one shipped rules file
// without N copies of each drifting apart.
func resolve(dir, name string) (string, error) {
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	up := filepath.Join(filepath.Dir(dir), name)
	if _, err := os.Stat(up); err == nil {
		return up, nil
	}
	return "", fmt.Errorf("%s: no %s here or in %s", dir, name, filepath.Dir(dir))
}

// loadSenses reads senses.jsonl into {tick: frame} and reports the last tick
// any line mentions.
//
// A field carried as JSON null CLEARS that field, which is the one way a
// transport says "this reading is gone"; a field a line does not mention is
// left exactly as it was. Both are the snapshot's own contract, not something
// this loader invents.
func loadSenses(dir string) (map[int]map[string]any, int, error) {
	path := filepath.Join(dir, SensesFile)
	data, err := os.ReadFile(path) // #nosec G304 - a fixture path under testdata
	if err != nil {
		return nil, 0, err
	}
	frames := map[int]map[string]any{}
	last := 0
	for i, line := range strings.Split(string(data), "\n") {
		text := strings.TrimSpace(line)
		if text == "" || strings.HasPrefix(text, "//") {
			continue
		}
		var frame senseFrame
		if err := json.Unmarshal([]byte(text), &frame); err != nil {
			return nil, 0, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		if frame.Tick < 1 {
			return nil, 0, fmt.Errorf("%s:%d: tick %d — ticks are 1-based",
				path, i+1, frame.Tick)
		}
		if _, dup := frames[frame.Tick]; dup {
			return nil, 0, fmt.Errorf("%s:%d: tick %d is given twice",
				path, i+1, frame.Tick)
		}
		frames[frame.Tick] = frame.Fields
		if frame.Tick > last {
			last = frame.Tick
		}
	}
	return frames, last, nil
}

// sortedKeys is a pose's channel names in a stable order.
func sortedKeys(pose adaptor.Pose) []string {
	out := make([]string, 0, len(pose))
	for name := range pose {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Marshal renders one frame as the single JSON line a trace file carries.
//
// Both sides of a comparison go through here, so the recorded frame and the
// expected one are normalised identically (JSON has one number type; a Go int
// and the float it parses back as must not read as a difference).
func Marshal(frame Frame) (string, error) {
	raw, err := json.Marshal(frame)
	if err != nil {
		return "", err
	}
	var round Frame
	if err := json.Unmarshal(raw, &round); err != nil {
		return "", err
	}
	out, err := json.Marshal(round)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// MarshalTrace renders every frame as one line, newline-terminated.
func MarshalTrace(frames []Frame) (string, error) {
	var b strings.Builder
	for _, frame := range frames {
		line, err := Marshal(frame)
		if err != nil {
			return "", err
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// ParseTrace reads an expected.jsonl back into frames.
func ParseTrace(path string) ([]Frame, error) {
	data, err := os.ReadFile(path) // #nosec G304 - a fixture path under testdata
	if err != nil {
		return nil, err
	}
	var out []Frame
	for i, line := range strings.Split(string(data), "\n") {
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		var frame Frame
		if err := json.Unmarshal([]byte(text), &frame); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		out = append(out, frame)
	}
	return out, nil
}
