package rules

import (
	"os"
	"sort"

	"github.com/BurntSushi/toml"
)

// Load reads, validates and merges a stack of rules LAYERS.
//
// Each layer is a list of file paths. Within one layer files simply merge, and
// a duplicate rule id across two files of the SAME layer is refused naming both
// paths — inside one layer there is no precedence to appeal to, so a collision
// is an authoring mistake rather than an override.
//
// Across layers, a LATER layer overrides an earlier one PER RULE ID:
//
//   - an id in both layers — the later entry wins WHOLESALE, keeping the
//     earlier layer's ordering position. Whole-entry replacement, never a
//     field-by-field merge: a rule's when/run/params are one thought, and a
//     half-shipped/half-local hybrid is a rule nobody wrote;
//   - an id only in an earlier layer — carried through untouched, which is what
//     lets an upgraded box gain newly shipped rules without touching its own
//     file;
//   - an id only in a later layer — appended;
//   - an id tombstoned (enabled = false) by a later layer — removed. A
//     tombstone naming an id no layer defines is INERT, not an error: a rule
//     removed upstream must not brick a box that had disabled it.
//
// Modes merge by name (later wins per name), and a later layer's active_mode
// wins only if it selects one.
//
// vocab is optional. A nil Vocabulary leaves field and action NAMES unchecked
// — everything else is validated exactly the same — which is what lets a rules
// file be checked with no robot present.
func Load(layers [][]string, vocab Vocabulary) (*Config, error) {
	merged := &layerResult{modes: map[string]map[string]float64{}}
	for _, paths := range layers {
		layer, err := loadLayer(paths, vocab)
		if err != nil {
			return nil, err
		}
		if layer == nil {
			continue
		}
		merged = mergeLayers(merged, layer)
	}
	return merged.config(), nil
}

// LoadFile is Load over a single file in a single layer.
func LoadFile(path string, vocab Vocabulary) (*Config, error) {
	return Load([][]string{{path}}, vocab)
}

// layerResult is one layer's contribution: its rules in order, its tombstoned
// ids, its modes, and its events.
type layerResult struct {
	ordered        []Rule
	disabled       map[string]bool
	modes          map[string]map[string]float64
	activeMode     string
	schemaVersion  int
	events         []Event
	eventsDisabled map[string]bool
	eventDefault   *EventDefault
}

func loadLayer(paths []string, vocab Vocabulary) (*layerResult, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	layer := &layerResult{
		disabled:       map[string]bool{},
		modes:          map[string]map[string]float64{},
		eventsDisabled: map[string]bool{},
	}
	// Where each id was first seen in THIS layer, so a collision can name both
	// files rather than only the second one. Rules and events use separate
	// namespaces — a rule id and an event "source/type" never collide.
	origin := map[string]string{}
	eventOrigin := map[string]string{}

	for _, path := range paths {
		f, err := parseFile(path)
		if err != nil {
			return nil, err
		}
		if err := applyVocabulary(f, vocab); err != nil {
			return nil, err
		}
		for _, id := range append(ids(f.ordered), f.disabled...) {
			if first, clash := origin[id]; clash {
				return nil, (ctx{path: path}).rule(id).errf(
					"rename one of them, or move one into a higher layer to override the other",
					"duplicate rule id across two files of the same layer: also defined in %s",
					first,
				)
			}
			origin[id] = path
		}
		for _, id := range append(eventIDs(f.events), f.eventDisabled...) {
			if first, clash := eventOrigin[id]; clash {
				return nil, (ctx{path: path}).event(id).errf(
					"rename one of them, or move one into a higher layer to override the other",
					"duplicate event across two files of the same layer: also defined in %s",
					first,
				)
			}
			eventOrigin[id] = path
		}
		layer.ordered = append(layer.ordered, f.ordered...)
		for _, id := range f.disabled {
			layer.disabled[id] = true
		}
		for name, params := range f.modes {
			layer.modes[name] = params
		}
		if f.activeMode != "" {
			layer.activeMode = f.activeMode
		}
		layer.schemaVersion = f.schemaVersion
		layer.events = append(layer.events, f.events...)
		for _, id := range f.eventDisabled {
			layer.eventsDisabled[id] = true
		}
		if f.eventDefault != nil {
			layer.eventDefault = f.eventDefault
		}
	}
	return layer, nil
}

func parseFile(path string) (*file, error) {
	c := ctx{path: path}
	raw, err := os.ReadFile(path) // #nosec G304 -- an operator-supplied rules path is the input
	if err != nil {
		return nil, c.errf("check the path and the file's permissions",
			"could not be read: %v", err)
	}
	var data map[string]any
	if _, err := toml.Decode(string(raw), &data); err != nil {
		return nil, c.errf("fix the TOML syntax", "is not valid TOML: %v", err)
	}
	return validate(path, data)
}

// mergeLayers layers overlay over base, resolving collisions per RULE ID.
func mergeLayers(base, overlay *layerResult) *layerResult {
	overlayByID := make(map[string]Rule, len(overlay.ordered))
	for _, rule := range overlay.ordered {
		overlayByID[rule.ID] = rule
	}

	disabled := map[string]bool{}
	for id := range base.disabled {
		disabled[id] = true
	}
	for id := range overlay.disabled {
		disabled[id] = true
	}

	var ordered []Rule
	seen := map[string]bool{}
	for _, rule := range append(append([]Rule{}, base.ordered...), overlay.ordered...) {
		if seen[rule.ID] {
			continue
		}
		seen[rule.ID] = true
		winner := rule
		if replacement, ok := overlayByID[rule.ID]; ok {
			winner = replacement
		}
		// A tombstone removes the rule — unless this very layer redefined the
		// id, which revives it.
		if disabled[winner.ID] && !containsID(overlayByID, winner.ID) {
			continue
		}
		ordered = append(ordered, winner)
	}

	modes := make(map[string]map[string]float64, len(base.modes)+len(overlay.modes))
	for name, params := range base.modes {
		modes[name] = params
	}
	for name, params := range overlay.modes {
		modes[name] = params
	}

	activeMode := base.activeMode
	if overlay.activeMode != "" {
		activeMode = overlay.activeMode
	}
	if activeMode != "" && modes[activeMode] == nil {
		activeMode = ""
	}

	schemaVersion := base.schemaVersion
	if overlay.schemaVersion != 0 {
		schemaVersion = overlay.schemaVersion
	}

	remaining := map[string]bool{}
	for id := range disabled {
		if !containsID(overlayByID, id) {
			remaining[id] = true
		}
	}

	eventsDisabled := map[string]bool{}
	for id := range base.eventsDisabled {
		eventsDisabled[id] = true
	}
	for id := range overlay.eventsDisabled {
		eventsDisabled[id] = true
	}

	overlayEventByID := make(map[string]Event, len(overlay.events))
	for _, event := range overlay.events {
		overlayEventByID[event.ID()] = event
	}

	var events []Event
	seenEvents := map[string]bool{}
	for _, event := range append(append([]Event{}, base.events...), overlay.events...) {
		id := event.ID()
		if seenEvents[id] {
			continue
		}
		seenEvents[id] = true
		winner := event
		if replacement, ok := overlayEventByID[id]; ok {
			winner = replacement
		}
		if eventsDisabled[id] {
			if _, revived := overlayEventByID[id]; !revived {
				continue
			}
		}
		events = append(events, winner)
	}

	remainingEvents := map[string]bool{}
	for id := range eventsDisabled {
		if _, revived := overlayEventByID[id]; !revived {
			remainingEvents[id] = true
		}
	}

	eventDefault := base.eventDefault
	if overlay.eventDefault != nil {
		eventDefault = overlay.eventDefault
	}

	return &layerResult{
		ordered:        ordered,
		disabled:       remaining,
		modes:          modes,
		activeMode:     activeMode,
		schemaVersion:  schemaVersion,
		events:         events,
		eventsDisabled: remainingEvents,
		eventDefault:   eventDefault,
	}
}

func containsID(m map[string]Rule, id string) bool {
	_, ok := m[id]
	return ok
}

func (l *layerResult) config() *Config {
	cfg := &Config{
		SchemaVersion: l.schemaVersion,
		ActiveMode:    l.activeMode,
		Modes:         l.modes,
		Events:        l.events,
		EventDefault:  l.eventDefault,
	}
	if cfg.Modes == nil {
		cfg.Modes = map[string]map[string]float64{}
	}
	for _, rule := range l.ordered {
		if rule.Kind == KindInhibit {
			cfg.Inhibit = append(cfg.Inhibit, rule)
			continue
		}
		cfg.React = append(cfg.React, rule)
	}
	for id := range l.disabled {
		cfg.Disabled = append(cfg.Disabled, id)
	}
	sort.Strings(cfg.Disabled)
	return cfg
}
