// Package events routes source/type-keyed events against a rules.Config's
// [[event]] entries and [event_default] table.
//
// An event is ROUTING METADATA, never a behavior input: Route resolves a
// priority, urgency, LLM-evaluate hint, voice hint and (optionally) a
// rendered inject template, and applies a dedupe window — it never claims a
// channel, never runs an action, and is never consulted by arbitration. The
// one-tick surface a tick loop is meant to read is TickFields, a map of
// "<source>/<type>": true for every event routed since the previous call.
//
// This package imports only internal/rules, internal/senselog, and stdlib.
package events

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/rules"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
)

// DefaultWindow is the default dedupe window, mirroring reachy_nova's
// NOVA_SENSE_DEDUPE_S default (config/nervous-system/rules.yaml,
// reachy_nova/harness/bus.py's DEFAULT_DEDUPE_WINDOW_S).
const DefaultWindow = 10 * time.Second

// Event is one incoming event to route.
type Event struct {
	// Source and Type together form the "<source>/<type>" identity a
	// rules.Event or rules.EventDefault entry resolves against.
	Source string
	Type   string
	// Payload renders an entry's InjectTemplate's "{key}" placeholders.
	Payload map[string]any
}

// Routed is the outcome of resolving one Event.
type Routed struct {
	Source string
	Type   string

	Priority    string
	Urgency     string
	LLMEvaluate bool
	Voice       string
	Sense       string

	// Inject is InjectTemplate rendered against Payload, or "" when the
	// resolved entry (or default) carries no template.
	Inject string

	// Deliver is false exactly when Voice is "none": the event is still
	// routed and recorded, but must never reach a voice/model surface. See
	// rules.VoiceNone and reachy_nova's bus.py VOICE_MARKERS/route_event.
	Deliver bool
}

// Router resolves Events against a *rules.Config's [[event]] entries, with a
// per-key dedupe window.
type Router struct {
	cfg    *rules.Config
	byID   map[string]rules.Event
	logger *senselog.Logger
	window time.Duration

	mu       sync.Mutex
	lastAt   map[string]time.Time
	tickKeys map[string]bool
}

// Option configures a Router at construction.
type Option func(*Router)

// WithWindow overrides the dedupe window (default DefaultWindow).
func WithWindow(d time.Duration) Option {
	return func(r *Router) { r.window = d }
}

// WithLogger overrides the senselog destination (default senselog.Default(),
// which writes to os.Stderr).
func WithLogger(l *senselog.Logger) Option {
	return func(r *Router) { r.logger = l }
}

// New builds a Router from a validated *rules.Config.
func New(cfg *rules.Config, opts ...Option) *Router {
	r := &Router{
		cfg:      cfg,
		byID:     make(map[string]rules.Event, len(cfg.Events)),
		logger:   senselog.Default(),
		window:   DefaultWindow,
		lastAt:   map[string]time.Time{},
		tickKeys: map[string]bool{},
	}
	for _, e := range cfg.Events {
		r.byID[e.ID()] = e
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Route resolves ev against its "<source>/<type>" entry, or [event_default]
// when no entry matches, and applies dedupe.
//
// The dedupe identity is the resolved entry's Dedupe field when set, else
// "<source>/<type>". A second event sharing that identity within the
// configured window returns ok=false — a named drop
// (senselog "[SENSE stage=event source=<source> event=<type>] dropped
// reason=dedupe ...") rather than an error; distinct identities never
// collide with each other or with themselves outside the window.
func (r *Router) Route(now time.Time, ev Event) (Routed, bool) {
	id := ev.Source + "/" + ev.Type
	entry, hasEntry := r.byID[id]

	routed := Routed{Source: ev.Source, Type: ev.Type}
	dedupeKey := id

	switch {
	case hasEntry:
		routed.Priority = entry.Priority
		routed.Urgency = entry.Urgency
		routed.LLMEvaluate = entry.LLMEvaluate
		routed.Voice = entry.Voice
		routed.Sense = entry.Sense
		routed.Inject = renderTemplate(entry.InjectTemplate, ev.Payload)
		if entry.Dedupe != "" {
			dedupeKey = entry.Dedupe
		}
	case r.cfg != nil && r.cfg.EventDefault != nil:
		def := r.cfg.EventDefault
		routed.Priority = def.Priority
		routed.Urgency = def.Urgency
		routed.LLMEvaluate = def.LLMEvaluate
		routed.Voice = def.Voice
	}
	if routed.Voice == "" {
		routed.Voice = rules.DefaultVoice
	}
	routed.Deliver = routed.Voice != rules.VoiceNone

	if !r.admit(now, dedupeKey) {
		r.logger.Drop("event", ev.Source, ev.Type, "dedupe",
			fmt.Sprintf("key=%s window=%s", dedupeKey, r.window))
		return Routed{}, false
	}

	r.mu.Lock()
	r.tickKeys[id] = true
	r.mu.Unlock()

	return routed, true
}

// admit reports whether key may fire now — false when it fired within the
// last window — and, when admitting, records now as its new last-fire time.
func (r *Router) admit(now time.Time, key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if last, ok := r.lastAt[key]; ok && now.Sub(last) < r.window {
		return false
	}
	r.lastAt[key] = now
	return true
}

// TickFields returns the one-tick sense-field surface: "<source>/<type>":
// true for every event Route admitted (i.e. not deduped) since the previous
// call to TickFields, then clears it. now is accepted for symmetry with a
// tick-driven caller but does not gate the result.
func (r *Router) TickFields(now time.Time) map[string]any {
	_ = now
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]any, len(r.tickKeys))
	for k := range r.tickKeys {
		out[k] = true
	}
	r.tickKeys = map[string]bool{}
	return out
}

// renderTemplate substitutes "{key}" placeholders in tmpl from payload.
// A placeholder naming a key payload does not have is left LITERAL — never
// blanked — so a partial payload still produces a readable, if incomplete,
// sentence.
func renderTemplate(tmpl string, payload map[string]any) string {
	if tmpl == "" {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(tmpl); {
		if tmpl[i] == '{' {
			if end := strings.IndexByte(tmpl[i:], '}'); end >= 0 {
				key := tmpl[i+1 : i+end]
				if v, ok := payload[key]; ok {
					fmt.Fprintf(&b, "%v", v)
					i += end + 1
					continue
				}
			}
		}
		b.WriteByte(tmpl[i])
		i++
	}
	return b.String()
}
