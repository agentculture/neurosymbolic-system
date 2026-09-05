package rules

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// The fixed declarative schema. Anything outside these sets is refused at every
// level — no fn/code/source/exec/free-form fields, ever.
var (
	topLevelFields = set(
		"schema_version", "active_mode", "react", "inhibit", "modes", "event", "event_default",
	)
	reactFields     = set("id", "enabled", "when", "run", "params", "cooldown_s", "hysteresis", "duration_s", "say")
	inhibitFields   = set("id", "enabled", "when", "disable", "cooldown_s", "hysteresis")
	predicateFields = set("field", "op", "value")
	groupFields     = set("all", "any")

	eventFields = set(
		"source", "type", "enabled", "priority", "urgency", "llm_evaluate",
		"inject_template", "voice", "sense", "dedupe",
	)
	eventDefaultFields = set("priority", "urgency", "llm_evaluate", "voice")

	reactRequired        = []string{"id", "run", "when"}
	inhibitRequired      = []string{"id", "disable", "when"}
	eventRequired        = []string{"source", "type", "priority", "urgency"}
	eventDefaultRequired = []string{"priority", "urgency"}
)

// file is one parsed-and-validated rules file: its ordered rules (react and
// inhibit interleaved as written, so a merge can keep positions), its
// tombstones, and its modes.
type file struct {
	path          string
	schemaVersion int
	ordered       []Rule
	disabled      []string
	modes         map[string]map[string]float64
	activeMode    string
	events        []Event
	eventDisabled []string
	eventDefault  *EventDefault
}

// validate turns one decoded TOML document into a validated file.
func validate(path string, data map[string]any) (*file, error) {
	c := ctx{path: path}

	if unknown := unknownKeys(data, topLevelFields); len(unknown) > 0 {
		return nil, c.errf(
			"the allowed sections are: "+sortedKeys(topLevelFields),
			"unexpected top-level field(s) %s — a rules file is declarative data only",
			quoteList(unknown),
		)
	}

	version, err := validateSchemaVersion(c, data)
	if err != nil {
		return nil, err
	}

	f := &file{path: path, schemaVersion: version, modes: map[string]map[string]float64{}}

	for _, kind := range []string{KindReact, KindInhibit} {
		raw, present := data[kind]
		if !present {
			continue
		}
		entries, ok := asMaps(raw)
		if !ok {
			return nil, c.errf(
				fmt.Sprintf("write it as a [[%s]] array of tables", kind),
				"'%s' must be a list of rule tables", kind,
			)
		}
		for i, entry := range entries {
			rule, tombstone, err := validateRule(c, kind, i, entry, version, nil)
			if err != nil {
				return nil, err
			}
			if tombstone != "" {
				f.disabled = append(f.disabled, tombstone)
				continue
			}
			rule.Source = path
			f.ordered = append(f.ordered, *rule)
		}
	}

	if err := checkDuplicateIDs(c, f); err != nil {
		return nil, err
	}

	if f.modes, err = validateModes(c, data["modes"]); err != nil {
		return nil, err
	}
	if f.activeMode, err = validateActiveMode(c, data["active_mode"], f.modes); err != nil {
		return nil, err
	}

	if raw, present := data["event"]; present {
		entries, ok := asMaps(raw)
		if !ok {
			return nil, c.errf(
				"write it as an [[event]] array of tables",
				"'event' must be a list of event tables",
			)
		}
		for i, entry := range entries {
			event, tombstone, err := validateEvent(c, i, entry)
			if err != nil {
				return nil, err
			}
			if tombstone != "" {
				f.eventDisabled = append(f.eventDisabled, tombstone)
				continue
			}
			event.Path = path
			f.events = append(f.events, *event)
		}
	}
	if err := checkDuplicateEventIDs(c, f); err != nil {
		return nil, err
	}

	if f.eventDefault, err = validateEventDefault(c, data["event_default"]); err != nil {
		return nil, err
	}

	return f, nil
}

// applyVocabulary re-checks a validated file's names against a robot's
// vocabulary. It is a SEPARATE pass so a nil Vocabulary skips exactly the
// name checks and nothing else.
//
// events is the "<source>/<type>" set every [[event]] entry in the WHOLE
// loaded config declares (see eventFieldSet). Those keys are predicate fields
// internal/events.Router publishes per tick, so they are valid independent of
// the robot's vocabulary — which never declares them, and should not: an
// event is routing metadata, not a plant reading.
func applyVocabulary(f *file, vocab Vocabulary, events map[string]bool) error {
	if vocab == nil {
		return nil
	}
	c := ctx{path: f.path}
	for _, rule := range f.ordered {
		rc := c.rule(rule.ID)
		if err := checkPredicateNames(rc, rule.When, vocab, events); err != nil {
			return err
		}
		if rule.Kind == KindReact {
			if !vocab.HasAction(rule.Run) {
				return rc.errf(
					"use an action this robot's vocabulary declares",
					"runs unknown action '%s'", rule.Run,
				)
			}
			if rule.DurationS == nil && vocab.ActionLoops(rule.Run) {
				return rc.errf(
					fmt.Sprintf("add duration_s = <seconds> to react rule '%s'", rule.ID),
					"runs '%s', a looping action, with no duration_s — admitting it would let "+
						"it hold its channel forever", rule.Run,
				)
			}
			for _, key := range sortedMapKeys(rule.Params) {
				lo, hi, ok := vocab.ActionParam(rule.Run, key)
				if !ok {
					return rc.errf(
						fmt.Sprintf("remove it, or use a parameter '%s' declares", rule.Run),
						"params has parameter '%s', which action '%s' does not declare",
						key, rule.Run,
					)
				}
				if v := rule.Params[key]; v < lo || v > hi {
					return rc.errf(
						fmt.Sprintf("use a value in [%g, %g]", lo, hi),
						"params.%s is %g, outside the range action '%s' accepts",
						key, v, rule.Run,
					)
				}
			}
		}
		for _, name := range rule.Disable {
			if !vocab.HasAction(name) {
				return rc.errf(
					"use an action this robot's vocabulary declares",
					"disable names unknown action '%s'", name,
				)
			}
		}
	}
	return nil
}

func checkPredicateNames(c ctx, p Predicate, vocab Vocabulary, events map[string]bool) error {
	if !p.IsLeaf() {
		for _, child := range append(append([]Predicate{}, p.All...), p.Any...) {
			if err := checkPredicateNames(c, child, vocab, events); err != nil {
				return err
			}
		}
		return nil
	}
	if vocab.HasField(p.Field) {
		return nil
	}
	if events[p.Field] {
		// An event field is BOOLEAN by construction: the router publishes it
		// as true for exactly one tick, and it is absent otherwise. An
		// ordered or equality comparison over it is a rule that can never
		// mean what its author thought, so it is refused rather than left to
		// silently never fire.
		if !eventFieldOps[p.Op] {
			return c.errf(
				"an event field is present-for-one-tick or absent; use "+sortedKeys(eventFieldOps),
				"when.field '%s' is an [[event]] entry, which op '%s' cannot test",
				p.Field, p.Op,
			)
		}
		return nil
	}
	return c.errf(
		"use a sense field this robot's vocabulary declares, or an [[event]] "+
			"entry's 'source/type' declared by one of the loaded rules files",
		"when.field '%s' is unknown", p.Field,
	)
}

func validateSchemaVersion(c ctx, data map[string]any) (int, error) {
	fix := fmt.Sprintf("set schema_version = %d or %d at the top of the file",
		SchemaVersion1, SchemaVersion2)
	raw, present := data["schema_version"]
	if !present {
		return 0, c.errf(fix, "missing 'schema_version' — expected schema_version = %d or %d",
			SchemaVersion1, SchemaVersion2)
	}
	v, ok := raw.(int64)
	if !ok || (int(v) != SchemaVersion1 && int(v) != SchemaVersion2) {
		return 0, c.errf(fix, "unknown schema_version %v — expected schema_version = %d or %d",
			raw, SchemaVersion1, SchemaVersion2)
	}
	return int(v), nil
}

// validateRule validates one [[react]]/[[inhibit]] entry. It returns either a
// rule, or the id of a tombstone (enabled = false).
func validateRule(c ctx, kind string, index int, raw map[string]any, version int, _ Vocabulary) (*Rule, string, error) {
	allowed := reactFields
	required := reactRequired
	if kind == KindInhibit {
		allowed = inhibitFields
		required = inhibitRequired
	}

	// The positional label is the id slot until the entry's own id is known,
	// so even an entry whose id is missing or malformed names a place.
	label := fmt.Sprintf("%s[%d]", kind, index)
	if id, ok := raw["id"].(string); ok && strings.TrimSpace(id) != "" {
		label = id
	}
	rc := c.rule(label)

	if unknown := unknownKeys(raw, allowed); len(unknown) > 0 {
		return nil, "", rc.errf(
			"allowed fields: "+sortedKeys(allowed),
			"has unexpected field(s) %s", quoteList(unknown),
		)
	}

	tombstone, err := isTombstone(rc, raw)
	if err != nil {
		return nil, "", err
	}

	id, ok := raw["id"].(string)
	if !ok || strings.TrimSpace(id) == "" {
		return nil, "", rc.errf("give the rule a non-empty string id",
			"'id' must be a non-empty string (got %v)", raw["id"])
	}

	// A tombstone is validated only for what still means something on a
	// disabled entry — an id, and no unknown fields. The rest of a copied
	// stanza is inert data the operator kept for reference; refusing it would
	// make disabling a shipped rule harder than writing one.
	if tombstone {
		return nil, id, nil
	}

	var missing []string
	for _, key := range required {
		if _, present := raw[key]; !present {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, "", rc.errf(
			"a "+kind+" rule requires: "+strings.Join(required, ", "),
			"is missing required field(s) %s", quoteList(missing),
		)
	}

	when, err := validatePredicate(rc, raw["when"], version, 0)
	if err != nil {
		return nil, "", err
	}
	cooldown, err := nonNegative(rc, raw, "cooldown_s", DefaultCooldownS)
	if err != nil {
		return nil, "", err
	}
	hysteresis, err := nonNegative(rc, raw, "hysteresis", DefaultHysteresis)
	if err != nil {
		return nil, "", err
	}

	rule := &Rule{
		ID:         id,
		Kind:       kind,
		When:       when,
		CooldownS:  cooldown,
		Hysteresis: hysteresis,
	}

	if kind == KindInhibit {
		if rule.Disable, err = validateDisable(rc, raw["disable"]); err != nil {
			return nil, "", err
		}
		return rule, "", nil
	}

	run, ok := raw["run"].(string)
	if !ok || strings.TrimSpace(run) == "" {
		return nil, "", rc.errf("name the action to run",
			"'run' must be a non-empty string (got %v)", raw["run"])
	}
	rule.Run = run

	if rule.DurationS, err = positive(rc, raw, "duration_s"); err != nil {
		return nil, "", err
	}
	if rule.Params, err = validateParams(rc, raw["params"]); err != nil {
		return nil, "", err
	}
	if rule.Say, err = validateSay(rc, raw["say"]); err != nil {
		return nil, "", err
	}
	return rule, "", nil
}

func isTombstone(c ctx, raw map[string]any) (bool, error) {
	value, present := raw["enabled"]
	if !present {
		return false, nil
	}
	enabled, ok := value.(bool)
	if !ok {
		return false, c.errf(
			"use enabled = false to disable a rule of this id from a lower layer",
			"'enabled' must be a boolean (got %v)", value,
		)
	}
	return !enabled, nil
}

func validatePredicate(c ctx, raw any, version, depth int) (Predicate, error) {
	table, ok := raw.(map[string]any)
	if !ok {
		return Predicate{}, c.errf(
			"write when = { field = ..., op = ..., value = ... }",
			"'when' must be a table (got %v)", raw,
		)
	}

	_, hasAll := table["all"]
	_, hasAny := table["any"]
	if hasAll || hasAny {
		return validateGroup(c, table, version, depth, hasAll, hasAny)
	}

	if unknown := unknownKeys(table, predicateFields); len(unknown) > 0 {
		return Predicate{}, c.errf(
			"allowed fields: "+sortedKeys(predicateFields),
			"when has unexpected field(s) %s", quoteList(unknown),
		)
	}

	fieldName, ok := table["field"].(string)
	if !ok || strings.TrimSpace(fieldName) == "" {
		return Predicate{}, c.errf("name the sense field to test",
			"when.field must be a non-empty string (got %v)", table["field"])
	}
	op, ok := table["op"].(string)
	if !ok || !Comparators[op] {
		return Predicate{}, c.errf("use one of: "+sortedKeys(Comparators),
			"when.op '%v' is unknown", table["op"])
	}
	value, err := validatePredicateValue(c, table, op)
	if err != nil {
		return Predicate{}, err
	}
	return Predicate{Field: fieldName, Op: op, Value: value}, nil
}

func validateGroup(c ctx, table map[string]any, version, depth int, hasAll, hasAny bool) (Predicate, error) {
	if version < SchemaVersion2 {
		key := "all"
		if hasAny {
			key = "any"
		}
		return Predicate{}, c.errf(
			fmt.Sprintf("set schema_version = %d to use all/any conjunction", SchemaVersion2),
			"when uses '%s', which schema_version %d does not have", key, version,
		)
	}
	if hasAll && hasAny {
		return Predicate{}, c.errf(
			"use exactly one of 'all' or 'any' per predicate table",
			"when carries both 'all' and 'any'",
		)
	}
	if unknown := unknownKeys(table, groupFields); len(unknown) > 0 {
		key := "all"
		if hasAny {
			key = "any"
		}
		return Predicate{}, c.errf(
			fmt.Sprintf("a '%s' group carries nothing but its list of children", key),
			"when has unexpected field(s) %s beside '%s'", quoteList(unknown), key,
		)
	}
	// depth 0 is the root, depth 1 a group inside it. A group at depth 2 would
	// be a third level — refused, so a predicate stays readable at a glance.
	if depth >= 2 {
		return Predicate{}, c.errf(
			"flatten it: an all/any group may nest one level deep, no more",
			"when nests all/any deeper than one level",
		)
	}

	key := "all"
	if hasAny {
		key = "any"
	}
	children, ok := asMaps(table[key])
	if !ok {
		return Predicate{}, c.errf(
			fmt.Sprintf("write %s = [ { field = ..., op = ... }, ... ]", key),
			"when.%s must be a list of predicate tables (got %v)", key, table[key],
		)
	}
	if len(children) == 0 {
		return Predicate{}, c.errf(
			fmt.Sprintf("give '%s' at least one child predicate, or drop the group", key),
			"when.%s is an empty list", key,
		)
	}
	parsed := make([]Predicate, 0, len(children))
	for _, child := range children {
		p, err := validatePredicate(c, child, version, depth+1)
		if err != nil {
			return Predicate{}, err
		}
		parsed = append(parsed, p)
	}
	if hasAny {
		return Predicate{Any: parsed}, nil
	}
	return Predicate{All: parsed}, nil
}

func validatePredicateValue(c ctx, table map[string]any, op string) (any, error) {
	raw, present := table["value"]

	switch {
	case booleanOps[op]:
		if present && raw != nil {
			return nil, c.errf(
				"remove 'value' for is_true/is_false predicates",
				"when: op '%s' takes no 'value' (got %v)", op, raw,
			)
		}
		return nil, nil

	case orderedOps[op] || durationOps[op]:
		if !present {
			return nil, c.errf("provide a numeric 'value'",
				"when: op '%s' requires a numeric 'value'", op)
		}
		v, ok := toFloat(raw)
		if !ok {
			return nil, c.errf("provide a numeric 'value'",
				"when: op '%s' requires a numeric 'value' (got %v)", op, raw)
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, c.errf("provide a finite number",
				"when: 'value' for op '%s' must be a finite number (got %v)", op, raw)
		}
		if v < 0 {
			return nil, c.errf("provide a value >= 0",
				"when: 'value' for op '%s' must be >= 0 (got %v)", op, v)
		}
		return v, nil

	default: // equality ops
		if !present {
			return nil, c.errf("provide a scalar 'value'",
				"when: op '%s' requires a 'value' field", op)
		}
		switch v := raw.(type) {
		case string, bool:
			return v, nil
		default:
			f, ok := toFloat(raw)
			if !ok {
				return nil, c.errf("use a string, boolean, or number",
					"when.value must be a scalar for op '%s' (got %v)", op, raw)
			}
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, c.errf("provide a finite number",
					"when: 'value' for op '%s' must be a finite number (got %v)", op, raw)
			}
			return f, nil
		}
	}
}

func validateParams(c ctx, raw any) (map[string]float64, error) {
	if raw == nil {
		return nil, nil
	}
	table, ok := raw.(map[string]any)
	if !ok {
		return nil, c.errf("write params = { name = <number>, ... }",
			"'params' must be a table (got %v)", raw)
	}
	params := make(map[string]float64, len(table))
	for _, key := range sortedMapKeys(table) {
		v, ok := toFloat(table[key])
		if !ok {
			return nil, c.errf("parameter overrides are numbers",
				"params.%s must be a number (got %v)", key, table[key])
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, c.errf("provide a finite number",
				"params.%s must be a finite number (got %v)", key, table[key])
		}
		params[key] = v
	}
	return params, nil
}

func validateDisable(c ctx, raw any) ([]string, error) {
	fix := "list at least one action name to disable"
	items, ok := raw.([]any)
	if !ok {
		return nil, c.errf(fix, "'disable' must be a non-empty list of action names (got %v)", raw)
	}
	if len(items) == 0 {
		return nil, c.errf(fix, "'disable' is an empty list")
	}
	seen := map[string]bool{}
	names := make([]string, 0, len(items))
	for _, item := range items {
		name, ok := item.(string)
		if !ok || strings.TrimSpace(name) == "" {
			return nil, c.errf(fix, "'disable' has a non-string entry (got %v)", item)
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func validateSay(c ctx, raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	text, ok := raw.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", c.errf(
			"give say the words to speak, or remove the field entirely",
			"'say' must be a non-empty string (got %v)", raw,
		)
	}
	// Refused, never truncated: a rules file is operator data, and an
	// actuator with a real-world cost must not be handed an unbounded one.
	if n := utf8.RuneCountInString(text); n > MaxSayChars {
		return "", c.errf(
			fmt.Sprintf("shorten the utterance to at most %d characters", MaxSayChars),
			"'say' is %d characters, over the %d-character limit", n, MaxSayChars,
		)
	}
	return text, nil
}

func validateModes(c ctx, raw any) (map[string]map[string]float64, error) {
	if raw == nil {
		return map[string]map[string]float64{}, nil
	}
	table, ok := raw.(map[string]any)
	if !ok {
		return nil, c.errf("write [modes.<name>] tables of numbers",
			"'modes' must be a table (got %v)", raw)
	}
	modes := make(map[string]map[string]float64, len(table))
	for _, name := range sortedMapKeys(table) {
		body, ok := table[name].(map[string]any)
		if !ok {
			return nil, c.errf("write [modes."+name+"] as a table of numbers",
				"mode '%s' must be a table (got %v)", name, table[name])
		}
		params := make(map[string]float64, len(body))
		for _, key := range sortedMapKeys(body) {
			v, ok := toFloat(body[key])
			if !ok {
				return nil, c.errf(
					"a mode is a flat bag of numbers — no nested tables, strings, or booleans",
					"mode '%s' has unexpected field '%s': it is not a number (got %v)",
					name, key, body[key],
				)
			}
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, c.errf("provide a finite number",
					"mode '%s' parameter '%s' must be a finite number (got %v)", name, key, body[key])
			}
			params[key] = v
		}
		modes[name] = params
	}
	return modes, nil
}

func validateActiveMode(c ctx, raw any, modes map[string]map[string]float64) (string, error) {
	names := strings.Join(sortedMapKeys(modes), ", ")
	if raw == nil {
		if len(modes) > 0 {
			return "", c.errf(
				"set active_mode to one of: "+names,
				"defines mode(s) %s but selects no 'active_mode'", names,
			)
		}
		return "", nil
	}
	name, ok := raw.(string)
	if !ok || modes[name] == nil {
		fix := "use one of: " + names
		if len(modes) == 0 {
			fix = "define a [modes.<name>] table first, or drop active_mode"
		}
		return "", c.errf(fix, "'active_mode' %v is not a defined mode", raw)
	}
	return name, nil
}

// validateEvent validates one [[event]] entry. It returns either an event, or
// the "source/type" id of a tombstone (enabled = false).
func validateEvent(c ctx, index int, raw map[string]any) (*Event, string, error) {
	label := fmt.Sprintf("event[%d]", index)
	source, sourceOK := raw["source"].(string)
	eventType, typeOK := raw["type"].(string)
	if sourceOK && strings.TrimSpace(source) != "" && typeOK && strings.TrimSpace(eventType) != "" {
		label = source + "/" + eventType
	}
	rc := c.event(label)

	if unknown := unknownKeys(raw, eventFields); len(unknown) > 0 {
		return nil, "", rc.errf(
			"allowed fields: "+sortedKeys(eventFields),
			"has unexpected field(s) %s", quoteList(unknown),
		)
	}

	tombstone, err := isTombstone(rc, raw)
	if err != nil {
		return nil, "", err
	}

	if !sourceOK || strings.TrimSpace(source) == "" {
		return nil, "", rc.errf("give the event a non-empty string source",
			"'source' must be a non-empty string (got %v)", raw["source"])
	}
	if !typeOK || strings.TrimSpace(eventType) == "" {
		return nil, "", rc.errf("give the event a non-empty string type",
			"'type' must be a non-empty string (got %v)", raw["type"])
	}
	id := source + "/" + eventType

	// A tombstone is validated only for what still means something on a
	// disabled entry — source/type and no unknown fields, same as a rule.
	if tombstone {
		return nil, id, nil
	}

	var missing []string
	for _, key := range eventRequired {
		if _, present := raw[key]; !present {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, "", rc.errf(
			"an event requires: "+strings.Join(eventRequired, ", "),
			"is missing required field(s) %s", quoteList(missing),
		)
	}

	priority, err := requiredEnum(rc, raw, "priority", priorities)
	if err != nil {
		return nil, "", err
	}
	urgency, err := requiredEnum(rc, raw, "urgency", urgencies)
	if err != nil {
		return nil, "", err
	}
	llmEvaluate, err := optionalBool(rc, raw, "llm_evaluate", DefaultLLMEvaluate)
	if err != nil {
		return nil, "", err
	}
	voice, err := optionalEnum(rc, raw, "voice", voices, DefaultVoice)
	if err != nil {
		return nil, "", err
	}
	injectTemplate, err := optionalString(rc, raw, "inject_template")
	if err != nil {
		return nil, "", err
	}
	sense, err := optionalString(rc, raw, "sense")
	if err != nil {
		return nil, "", err
	}
	dedupe, err := optionalString(rc, raw, "dedupe")
	if err != nil {
		return nil, "", err
	}

	return &Event{
		Source:         source,
		Type:           eventType,
		Priority:       priority,
		Urgency:        urgency,
		LLMEvaluate:    llmEvaluate,
		InjectTemplate: injectTemplate,
		Voice:          voice,
		Sense:          sense,
		Dedupe:         dedupe,
	}, "", nil
}

// validateEventDefault validates the optional [event_default] table.
func validateEventDefault(c ctx, raw any) (*EventDefault, error) {
	if raw == nil {
		return nil, nil
	}
	table, ok := raw.(map[string]any)
	if !ok {
		return nil, c.errf(
			"write [event_default] with priority, urgency, and optionally "+
				"llm_evaluate and voice",
			"'event_default' must be a table (got %v)", raw,
		)
	}
	if unknown := unknownKeys(table, eventDefaultFields); len(unknown) > 0 {
		return nil, c.errf(
			"allowed fields: "+sortedKeys(eventDefaultFields),
			"'event_default' has unexpected field(s) %s", quoteList(unknown),
		)
	}
	var missing []string
	for _, key := range eventDefaultRequired {
		if _, present := table[key]; !present {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, c.errf(
			"'event_default' requires: "+strings.Join(eventDefaultRequired, ", "),
			"'event_default' is missing required field(s) %s", quoteList(missing),
		)
	}
	priority, err := requiredEnum(c, table, "priority", priorities)
	if err != nil {
		return nil, err
	}
	urgency, err := requiredEnum(c, table, "urgency", urgencies)
	if err != nil {
		return nil, err
	}
	llmEvaluate, err := optionalBool(c, table, "llm_evaluate", DefaultLLMEvaluate)
	if err != nil {
		return nil, err
	}
	voice, err := optionalEnum(c, table, "voice", voices, DefaultVoice)
	if err != nil {
		return nil, err
	}
	return &EventDefault{Priority: priority, Urgency: urgency, LLMEvaluate: llmEvaluate, Voice: voice}, nil
}

// requiredEnum reads a required string field and checks it against domain.
func requiredEnum(c ctx, raw map[string]any, name string, domain map[string]bool) (string, error) {
	value, ok := raw[name].(string)
	if !ok || !domain[value] {
		return "", c.errf(
			"use one of: "+sortedKeys(domain),
			"'%s' %v is not one of: %s", name, raw[name], sortedKeys(domain),
		)
	}
	return value, nil
}

// optionalEnum reads an optional string field against domain, defaulting to
// def when absent.
func optionalEnum(c ctx, raw map[string]any, name string, domain map[string]bool, def string) (string, error) {
	value, present := raw[name]
	if !present || value == nil {
		return def, nil
	}
	text, ok := value.(string)
	if !ok || !domain[text] {
		return "", c.errf(
			"use one of: "+sortedKeys(domain),
			"'%s' %v is not one of: %s", name, value, sortedKeys(domain),
		)
	}
	return text, nil
}

// optionalBool reads an optional boolean field, defaulting to def when absent.
func optionalBool(c ctx, raw map[string]any, name string, def bool) (bool, error) {
	value, present := raw[name]
	if !present || value == nil {
		return def, nil
	}
	b, ok := value.(bool)
	if !ok {
		return false, c.errf(
			fmt.Sprintf("use a boolean for '%s'", name),
			"'%s' must be a boolean (got %v)", name, value,
		)
	}
	return b, nil
}

// optionalString reads an optional non-empty string field, empty when absent.
func optionalString(c ctx, raw map[string]any, name string) (string, error) {
	value, present := raw[name]
	if !present || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", c.errf(
			fmt.Sprintf("give '%s' a non-empty string, or remove the field entirely", name),
			"'%s' must be a non-empty string (got %v)", name, value,
		)
	}
	return text, nil
}

// checkDuplicateEventIDs refuses two events (live or tombstoned) sharing a
// "source/type" identity within the SAME file — exactly the rule-id check,
// applied to the event namespace.
func checkDuplicateEventIDs(c ctx, f *file) error {
	seen := map[string]bool{}
	for _, id := range append(eventIDs(f.events), f.eventDisabled...) {
		if seen[id] {
			return c.event(id).errf(
				"rename one of them — every event source/type must be unique across the file",
				"duplicate event in %s", f.path,
			)
		}
		seen[id] = true
	}
	return nil
}

func eventIDs(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.ID()
	}
	return out
}

func checkDuplicateIDs(c ctx, f *file) error {
	seen := map[string]bool{}
	for _, id := range append(ids(f.ordered), f.disabled...) {
		if seen[id] {
			return c.rule(id).errf(
				"rename one of them — every rule id must be unique across react + inhibit",
				"duplicate rule id in %s", f.path,
			)
		}
		seen[id] = true
	}
	return nil
}

func ids(rules []Rule) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = r.ID
	}
	return out
}

// --------------------------------------------------------------------------- //
// decoded-value helpers                                                       //
// --------------------------------------------------------------------------- //

func nonNegative(c ctx, raw map[string]any, name string, def float64) (float64, error) {
	value, present := raw[name]
	if !present || value == nil {
		return def, nil
	}
	v, ok := toFloat(value)
	if !ok {
		return 0, c.errf("provide a number of seconds >= 0",
			"'%s' must be a number (got %v)", name, value)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, c.errf("provide a finite number",
			"'%s' must be a finite number (got %v)", name, value)
	}
	if v < 0 {
		return 0, c.errf("provide a value >= 0", "'%s' must be >= 0 (got %v)", name, v)
	}
	return v, nil
}

func positive(c ctx, raw map[string]any, name string) (*float64, error) {
	value, present := raw[name]
	if !present || value == nil {
		return nil, nil
	}
	v, ok := toFloat(value)
	if !ok {
		return nil, c.errf("provide a number of seconds > 0",
			"'%s' must be a number (got %v)", name, value)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil, c.errf("provide a finite number",
			"'%s' must be a finite number (got %v)", name, value)
	}
	if v <= 0 {
		return nil, c.errf("provide a value > 0", "'%s' must be > 0 (got %v)", name, v)
	}
	return &v, nil
}

// toFloat accepts the two numeric types a TOML decode produces, and refuses a
// bool (which would otherwise slip through as 0/1 in a laxer conversion).
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

// asMaps normalizes both shapes a TOML decode produces for a list of tables:
// [[array]] of tables, and an inline array of inline tables.
func asMaps(v any) ([]map[string]any, bool) {
	switch list := v.(type) {
	case []map[string]any:
		return list, true
	case []any:
		out := make([]map[string]any, 0, len(list))
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, m)
		}
		return out, true
	default:
		return nil, false
	}
}

func unknownKeys(table map[string]any, allowed map[string]bool) []string {
	var unknown []string
	for key := range table {
		if !allowed[key] {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return unknown
}

func quoteList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "'" + n + "'"
	}
	return strings.Join(quoted, ", ")
}
