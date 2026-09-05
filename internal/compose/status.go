package compose

import (
	"sync"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/stream"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// Status is what the `status` verb answers with, over the stream's mgmt frames
// or through any other front that installs this StatusSource.
//
// Every number here is either a cumulative counter the engine and the endpoint
// already keep, or the last tick's resolved state. Nothing is computed on
// demand and nothing is sampled off the tick: an operator's question must not
// spend the robot's 20 ms budget.
type Status struct {
	// Tick is the last completed tick's 1-based number, and UptimeS is
	// engine-clock seconds since the first one — the INJECTED clock, so a
	// replay reports the same numbers a live run did.
	Tick    int     `json:"tick"`
	UptimeS float64 `json:"uptime_s"`

	Ticks      uint64 `json:"ticks"`
	Overruns   uint64 `json:"overruns"`
	Drops      uint64 `json:"drops"`
	SinkErrors uint64 `json:"sink_errors"`
	SeamPanics uint64 `json:"seam_panics"`

	FramesIn    uint64 `json:"frames_in"`
	FramesOut   uint64 `json:"frames_out"`
	StreamDrops uint64 `json:"stream_drops"`
	Refused     uint64 `json:"refused"`

	// Active names the live behaviors oldest-first and Ownership is the
	// {channel: owner id} the last tick resolved to, with the engine's Unowned
	// token for a channel nothing owned. Together they answer "what is the
	// robot doing, and who decided that" — which no counter can.
	Active    []string          `json:"active"`
	Ownership map[string]string `json:"ownership"`

	// RuleLayers is how many rule layers are in force, and ActiveMode which
	// [modes.*] table is selected, so a `rules reload` is verifiable from
	// status rather than on faith.
	RuleLayers int    `json:"rule_layers"`
	ActiveMode string `json:"active_mode,omitempty"`
}

// statusRider is a seam rider that records each tick's resolved state, and the
// mgmt.StatusSource that reads it back.
//
// It is a RIDER rather than a getter on the engine because Ownership and
// ActiveNames only exist inside a TickContext: reaching for them from another
// goroutine would be reading state the tick goroutine owns. Recording them once
// per tick under a mutex is the cheap, honest way to make them answerable from
// a management goroutine.
type statusRider struct {
	mu        sync.Mutex
	tick      int
	startedAt time.Time
	now       time.Time
	active    []string
	ownership map[string]string

	engine *tick.Engine
	server *stream.Server
	lane   *rulesLane
}

// bind attaches the sources whose counters Status reports. It runs at
// composition, before the first tick.
func (s *statusRider) bind(engine *tick.Engine, server *stream.Server, lane *rulesLane) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.engine, s.server, s.lane = engine, server, lane
}

// Tick records this tick's resolved state. It copies, because the maps and
// slices a TickContext hands out are the caller's own only until the next tick
// reuses the underlying storage.
func (s *statusRider) Tick(ctx tick.TickContext) {
	active := ctx.ActiveNames()
	ownership := ctx.Ownership()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startedAt.IsZero() {
		s.startedAt = ctx.Now
	}
	s.tick, s.now = ctx.Tick, ctx.Now
	s.active, s.ownership = active, ownership
}

// Status implements mgmt.StatusSource. It never returns an error: "the engine
// has not ticked yet" is a real answer (every counter zero), not a failure.
func (s *statusRider) Status() (any, error) {
	s.mu.Lock()
	out := Status{
		Tick:      s.tick,
		Active:    append([]string(nil), s.active...),
		Ownership: copyMap(s.ownership),
	}
	if !s.startedAt.IsZero() {
		out.UptimeS = s.now.Sub(s.startedAt).Seconds()
	}
	engine, server, lane := s.engine, s.server, s.lane
	s.mu.Unlock()

	if engine != nil {
		stats := engine.Stats()
		out.Ticks, out.Overruns = stats.Ticks, stats.Overruns
		out.Drops, out.SinkErrors, out.SeamPanics = stats.Drops, stats.SinkErrors, stats.SeamPanics
	}
	if server != nil {
		stats := server.Stats()
		out.FramesIn, out.FramesOut = stats.FramesIn, stats.FramesOut
		out.StreamDrops, out.Refused = stats.Drops, stats.Refused
	}
	if lane != nil {
		out.RuleLayers, out.ActiveMode = lane.Layers(), lane.ActiveMode()
	}
	if out.Active == nil {
		out.Active = []string{}
	}
	return out, nil
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
