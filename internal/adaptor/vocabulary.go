// Package adaptor is the engine's robot vocabulary: the channels it composes
// onto, the sense fields a rule may key on, and the action names a rule may
// run — every one of them DECLARED at startup from an adaptor config rather
// than compiled in.
//
// The reason is the whole reason this repository exists. One physical robot
// generalizes to nothing; two is what forces the seam. Reachy Mini and
// MicroDuck have disjoint channel sets, disjoint senses and disjoint action
// libraries, so any name either robot uses is, by construction, wrong for the
// other. An engine that hard-codes one of them can only ever drive one robot.
// So the engine holds no robot literal at all: it is handed a Vocabulary, and
// everything downstream — arbitration, composition, the rules loader — asks the
// vocabulary rather than a constant. TestNoDonorLiteralsInEngineSources
// enforces that mechanically.
//
// A Vocabulary is also the fail-closed gate on a rules file: a rule naming a
// sense field or an action this robot does not declare is REFUSED, naming the
// offender and this config's path, rather than silently never firing. Same for
// action params — an out-of-range value is refused, never clamped, because a
// robot that quietly reinterprets a bad command is worse than one that says no.
//
// Config format: JSON today, decoded with encoding/json so this package keeps
// the runtime's zero-third-party-dependency policy. The struct tags are
// deliberately snake_case so the identical document shape maps onto TOML
// unchanged; a TOML front end (LoadTOML, sharing this validation) is a
// follow-up once the shared decoder lands, and no field name here should be
// chosen in a way that would have to change then.
//
// Every error reads `adaptor: <origin>: <what> — <fix>`.
package adaptor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
)

// inlineOrigin is the origin reported by Parse, which has no path to name.
const inlineOrigin = "(inline)"

// SenseType is the declared type of one sense field. It is deliberately a small
// closed set: the rules layer needs to know whether a predicate on a field is
// numeric, boolean, textual or a 3-vector, and nothing beyond that.
type SenseType string

// The four sense types. A robot whose reading does not fit one of these should
// be projected onto the ones that do at the composition root, not modelled here.
const (
	SenseFloat  SenseType = "float"
	SenseBool   SenseType = "bool"
	SenseString SenseType = "string"
	SenseVec3   SenseType = "vec3"
)

func validSenseTypes() []SenseType {
	return []SenseType{SenseFloat, SenseBool, SenseString, SenseVec3}
}

// EasingKind names one of the closed-form trajectory shapes.
type EasingKind string

// The three easing kinds.
//
//   - EasingLinear interpolates From to To at a constant rate.
//   - EasingEaseInOut is the cosine ease: symmetric, zero velocity at both
//     endpoints, which is what keeps a servo from snapping at the start.
//   - EasingHold is a step: From for the whole duration, To once it elapses.
//     It is the honest shape for a discrete channel (a canned skill, an audio
//     cue) where an interpolated value in between would be meaningless.
const (
	EasingLinear    EasingKind = "linear"
	EasingEaseInOut EasingKind = "ease_in_out"
	EasingHold      EasingKind = "hold"
)

func validEasingKinds() []EasingKind {
	return []EasingKind{EasingLinear, EasingEaseInOut, EasingHold}
}

// Channel is a group of degrees of freedom claimed and resolved atomically.
// Arity is how many numbers one sample of it carries, and Neutral is the value
// an unclaimed channel falls to, so a composed target is always COMPLETE and
// never partial.
type Channel struct {
	Name    string    `json:"name"`
	Arity   int       `json:"arity"`
	Neutral []float64 `json:"neutral"`
}

// Sense is one field of the per-tick snapshot a rule predicate may read.
//
// AgeField optionally names another declared float sense carrying this
// reading's freshness in seconds. Freshness is a first-class field rather than
// a call-time computation so that everything reading one tick sees the same
// age: a one-shot admitted mid-tick and the rule predicate that admitted it
// must not disagree about how old the reading was.
type Sense struct {
	Name     string    `json:"name"`
	Type     SenseType `json:"type"`
	AgeField string    `json:"age_field,omitempty"`
}

// Param is one declared knob of an action, with its closed domain. Both bounds
// are inclusive.
type Param struct {
	Name string  `json:"name"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
}

// Action is an OPAQUE name plus the channels it claims, the trajectory it
// drives each claimed channel with, and the params it accepts. The engine never
// interprets the name — that is precisely what lets the same engine run
// "orient-to-sound" on one robot and "waddle" on another.
//
// Trajectories is keyed by channel name and holds exactly the claimed channels:
// a claim with no trajectory, or a trajectory for an unclaimed channel, is
// refused at load.
type Action struct {
	Name         string                 `json:"name"`
	Claims       []string               `json:"claims"`
	Loops        bool                   `json:"loops"`
	Params       []Param                `json:"params,omitempty"`
	Trajectories map[string]*Trajectory `json:"trajectories"`
}

// Pose is one complete target, keyed by channel name. Every declared channel is
// present in a complete pose, each with its channel's arity — see
// Vocabulary.Neutral.
type Pose map[string][]float64

// config is the on-disk document shape. It exists separately from Vocabulary so
// that Vocabulary's indexes cannot be constructed by decoding, only by Parse —
// an un-validated Vocabulary is not a reachable state.
type config struct {
	Channels []Channel `json:"channels"`
	Senses   []Sense   `json:"senses,omitempty"`
	Actions  []Action  `json:"actions"`
}

// Vocabulary is a validated adaptor config: the complete set of names one robot
// exposes to the engine. It is immutable after Parse.
type Vocabulary struct {
	origin   string
	channels []Channel
	senses   []Sense
	actions  []Action

	channelByName map[string]Channel
	senseByName   map[string]Sense
	actionByName  map[string]*Action
}

// LoadJSON reads and validates an adaptor config from path. The path becomes
// the vocabulary's origin, so every later refusal says which file to edit.
func LoadJSON(path string) (*Vocabulary, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the config path is operator-supplied by design
	if err != nil {
		return nil, newError(path, fmt.Sprintf("the config cannot be read (%v)", err),
			"check the path exists and is readable")
	}
	return parse(data, path)
}

// Parse validates an adaptor config held in memory. Prefer LoadJSON: an error
// from Parse can only attribute itself to "(inline)".
func Parse(data []byte) (*Vocabulary, error) {
	return parse(data, inlineOrigin)
}

func parse(data []byte, origin string) (*Vocabulary, error) {
	var cfg config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, newError(origin, fmt.Sprintf("the config is not valid JSON (%v)", err),
			"fix the document; unknown keys are refused as well as malformed syntax")
	}

	v := &Vocabulary{
		origin:        origin,
		channels:      cfg.Channels,
		senses:        cfg.Senses,
		actions:       cfg.Actions,
		channelByName: make(map[string]Channel, len(cfg.Channels)),
		senseByName:   make(map[string]Sense, len(cfg.Senses)),
		actionByName:  make(map[string]*Action, len(cfg.Actions)),
	}
	if err := v.validateChannels(); err != nil {
		return nil, err
	}
	if err := v.validateSenses(); err != nil {
		return nil, err
	}
	if err := v.validateActions(); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *Vocabulary) validateChannels() error {
	if len(v.channels) == 0 {
		return newError(v.origin, "the config declares no channels",
			"declare at least one channel with a name, an arity and a neutral")
	}
	for _, ch := range v.channels {
		if ch.Name == "" {
			return newError(v.origin, "a channel is declared without a name",
				"give every channel a unique name")
		}
		if _, dup := v.channelByName[ch.Name]; dup {
			return newError(v.origin, fmt.Sprintf("channel %q is declared twice", ch.Name),
				"channel names must be unique")
		}
		if ch.Arity < 1 {
			return newError(v.origin,
				fmt.Sprintf("channel %q declares arity %d", ch.Name, ch.Arity),
				"arity must be at least 1")
		}
		if len(ch.Neutral) != ch.Arity {
			return newError(v.origin,
				fmt.Sprintf("channel %q declares a neutral of %d values but arity %d",
					ch.Name, len(ch.Neutral), ch.Arity),
				fmt.Sprintf("give the neutral exactly %d values", ch.Arity))
		}
		for i, value := range ch.Neutral {
			if !isFinite(value) {
				return newError(v.origin,
					fmt.Sprintf("channel %q neutral index %d is %v", ch.Name, i, value),
					"every neutral value must be a finite number")
			}
		}
		v.channelByName[ch.Name] = ch
	}
	return nil
}

func (v *Vocabulary) validateSenses() error {
	for _, s := range v.senses {
		if s.Name == "" {
			return newError(v.origin, "a sense is declared without a name",
				"give every sense a unique name")
		}
		if _, dup := v.senseByName[s.Name]; dup {
			return newError(v.origin, fmt.Sprintf("sense %q is declared twice", s.Name),
				"sense names must be unique")
		}
		if !isValidSenseType(s.Type) {
			return newError(v.origin,
				fmt.Sprintf("sense %q declares type %q", s.Name, string(s.Type)),
				"use one of: "+joinSenseTypes())
		}
		v.senseByName[s.Name] = s
	}
	// Age links are resolved in a second pass so declaration order is free.
	for _, s := range v.senses {
		if s.AgeField == "" {
			continue
		}
		age, ok := v.senseByName[s.AgeField]
		if !ok {
			return newError(v.origin,
				fmt.Sprintf("sense %q links age field %q, which is not declared",
					s.Name, s.AgeField),
				fmt.Sprintf("declare %q as a float sense, or drop the age_field link",
					s.AgeField))
		}
		if age.Type != SenseFloat {
			return newError(v.origin,
				fmt.Sprintf("sense %q links age field %q, which is declared %q",
					s.Name, s.AgeField, string(age.Type)),
				"a freshness field carries seconds, so it must be declared float")
		}
	}
	return nil
}

func (v *Vocabulary) validateActions() error {
	for i := range v.actions {
		a := &v.actions[i]
		if a.Name == "" {
			return newError(v.origin, "an action is declared without a name",
				"give every action a unique name")
		}
		if _, dup := v.actionByName[a.Name]; dup {
			return newError(v.origin, fmt.Sprintf("action %q is declared twice", a.Name),
				"action names must be unique")
		}
		if err := v.validateClaims(a); err != nil {
			return err
		}
		if err := v.validateParams(a); err != nil {
			return err
		}
		if err := v.validateTrajectories(a); err != nil {
			return err
		}
		v.actionByName[a.Name] = a
	}
	return nil
}

func (v *Vocabulary) validateClaims(a *Action) error {
	if len(a.Claims) == 0 {
		return newError(v.origin, fmt.Sprintf("action %q claims no channel", a.Name),
			"list at least one declared channel under claims")
	}
	seen := make(map[string]bool, len(a.Claims))
	for _, channel := range a.Claims {
		if _, ok := v.channelByName[channel]; !ok {
			return newError(v.origin,
				fmt.Sprintf("action %q claims channel %q, which is not declared",
					a.Name, channel),
				"claim one of: "+strings.Join(v.channelNames(), ", "))
		}
		if seen[channel] {
			return newError(v.origin,
				fmt.Sprintf("action %q claims channel %q twice", a.Name, channel),
				"list each claimed channel once")
		}
		seen[channel] = true
	}
	return nil
}

func (v *Vocabulary) validateParams(a *Action) error {
	seen := make(map[string]bool, len(a.Params))
	for _, p := range a.Params {
		if p.Name == "" {
			return newError(v.origin,
				fmt.Sprintf("action %q declares a param without a name", a.Name),
				"give every param a unique name")
		}
		if seen[p.Name] {
			return newError(v.origin,
				fmt.Sprintf("action %q declares param %q twice", a.Name, p.Name),
				"param names must be unique within an action")
		}
		if !isFinite(p.Min) || !isFinite(p.Max) {
			return newError(v.origin,
				fmt.Sprintf("action %q param %q declares a non-finite bound", a.Name, p.Name),
				"both bounds must be finite numbers")
		}
		if p.Min > p.Max {
			return newError(v.origin,
				fmt.Sprintf("action %q param %q declares the domain [%v, %v]",
					a.Name, p.Name, p.Min, p.Max),
				"min must be less than or equal to max")
		}
		seen[p.Name] = true
	}
	return nil
}

func (v *Vocabulary) validateTrajectories(a *Action) error {
	for channel := range a.Trajectories {
		if _, ok := v.channelByName[channel]; !ok {
			return newError(v.origin,
				fmt.Sprintf("action %q declares a trajectory for channel %q, "+
					"which is not declared", a.Name, channel),
				"declare the channel, or remove the trajectory")
		}
		if !containsString(a.Claims, channel) {
			return newError(v.origin,
				fmt.Sprintf("action %q declares a trajectory for channel %q "+
					"without claiming it", a.Name, channel),
				fmt.Sprintf("add %q to the action's claims, or remove the trajectory",
					channel))
		}
	}
	for _, channel := range a.Claims {
		traj, ok := a.Trajectories[channel]
		if !ok || traj == nil {
			return newError(v.origin,
				fmt.Sprintf("action %q claims channel %q but declares no trajectory for it",
					a.Name, channel),
				"give every claimed channel a keyframes or easing trajectory")
		}
		ch := v.channelByName[channel]
		if err := traj.compile(v.origin, a, channel, ch.Arity); err != nil {
			return err
		}
	}
	return nil
}

// Origin is the path (or "(inline)") this vocabulary was loaded from. Every
// error names it so an operator knows which file to edit.
func (v *Vocabulary) Origin() string { return v.origin }

// Channels returns the declared channels in declaration order. The returned
// slice is a copy; the Channel values in it are safe to keep.
func (v *Vocabulary) Channels() []Channel {
	out := make([]Channel, len(v.channels))
	copy(out, v.channels)
	return out
}

// Senses returns the declared sense fields in declaration order.
func (v *Vocabulary) Senses() []Sense {
	out := make([]Sense, len(v.senses))
	copy(out, v.senses)
	return out
}

// Actions returns the declared actions in declaration order. The slice is a
// copy, but each Action's Trajectories map is shared and must be treated as
// read-only: a Trajectory is immutable after load and safe to sample from the
// tick thread.
func (v *Vocabulary) Actions() []Action {
	out := make([]Action, len(v.actions))
	copy(out, v.actions)
	return out
}

// Neutral returns a fresh COMPLETE pose: every declared channel at its declared
// neutral. Composition starts from this, so a channel nothing claims resolves
// to a real value rather than being left out of the target.
func (v *Vocabulary) Neutral() Pose {
	out := make(Pose, len(v.channels))
	for _, ch := range v.channels {
		values := make([]float64, len(ch.Neutral))
		copy(values, ch.Neutral)
		out[ch.Name] = values
	}
	return out
}

// HasField reports whether name is a declared sense field.
func (v *Vocabulary) HasField(name string) bool {
	_, ok := v.senseByName[name]
	return ok
}

// HasAction reports whether name is a declared action.
func (v *Vocabulary) HasAction(name string) bool {
	_, ok := v.actionByName[name]
	return ok
}

// ActionLoops reports whether the named action repeats. An undeclared action
// reports false; callers that care about the difference should ask HasAction.
func (v *Vocabulary) ActionLoops(name string) bool {
	a, ok := v.actionByName[name]
	return ok && a.Loops
}

// ActionParam returns the declared inclusive domain of one action param. ok is
// false when either the action or the param is undeclared.
func (v *Vocabulary) ActionParam(action, param string) (min, max float64, ok bool) {
	a, found := v.actionByName[action]
	if !found {
		return 0, 0, false
	}
	for _, p := range a.Params {
		if p.Name == param {
			return p.Min, p.Max, true
		}
	}
	return 0, 0, false
}

// CheckReferences refuses any name a rules file uses that this robot does not
// declare, naming the FIRST offender and this vocabulary's origin.
//
// This is the fail-closed gate on the rules layer. A rule keyed on a field this
// robot has no reading for, or running an action it has no trajectory for, is
// not a rule that "just never fires" — it is a rule the operator believes is
// working. Refusing at load is the only way that mistake is ever visible.
//
// Fields are checked before actions, and checking stops at the first refusal.
func (v *Vocabulary) CheckReferences(fields []string, actions []string) error {
	for _, name := range fields {
		if !v.HasField(name) {
			return newError(v.origin,
				fmt.Sprintf("a rule keys on sense field %q, which this robot does not declare",
					name),
				"declare the field in this config, or key the rule on one of: "+
					strings.Join(v.senseNames(), ", "))
		}
	}
	for _, name := range actions {
		if !v.HasAction(name) {
			return newError(v.origin,
				fmt.Sprintf("a rule runs action %q, which this robot does not declare", name),
				"declare the action in this config, or run one of: "+
					strings.Join(v.actionNames(), ", "))
		}
	}
	return nil
}

// ValidateParams refuses an unknown param key or a value outside its declared
// domain. Out of range is REFUSED, never clamped, and never truncated: a robot
// that quietly reinterprets a bad command is worse than one that says no. An
// omitted param is fine — the action's own default stands.
func (v *Vocabulary) ValidateParams(action string, params map[string]float64) error {
	a, ok := v.actionByName[action]
	if !ok {
		return newError(v.origin,
			fmt.Sprintf("params were given for action %q, which is not declared", action),
			"declare the action in this config, or use one of: "+
				strings.Join(v.actionNames(), ", "))
	}
	for key, value := range params {
		min, max, found := v.ActionParam(action, key)
		if !found {
			return newError(v.origin,
				fmt.Sprintf("action %q was given param %q, which it does not declare",
					a.Name, key),
				"use one of: "+strings.Join(paramNames(a), ", "))
		}
		if !isFinite(value) {
			return newError(v.origin,
				fmt.Sprintf("action %q param %q was given %v", a.Name, key, value),
				"a param value must be a finite number")
		}
		if value < min || value > max {
			return newError(v.origin,
				fmt.Sprintf("action %q param %q was given %v, outside the declared [%v, %v]",
					a.Name, key, value, min, max),
				"pass a value inside the declared domain; out-of-domain values are "+
					"refused, never clamped")
		}
	}
	return nil
}

func (v *Vocabulary) channelNames() []string {
	out := make([]string, 0, len(v.channels))
	for _, ch := range v.channels {
		out = append(out, ch.Name)
	}
	return out
}

func (v *Vocabulary) senseNames() []string {
	out := make([]string, 0, len(v.senses))
	for _, s := range v.senses {
		out = append(out, s.Name)
	}
	return out
}

func (v *Vocabulary) actionNames() []string {
	out := make([]string, 0, len(v.actions))
	for _, a := range v.actions {
		out = append(out, a.Name)
	}
	return out
}

func paramNames(a *Action) []string {
	out := make([]string, 0, len(a.Params))
	for _, p := range a.Params {
		out = append(out, p.Name)
	}
	return out
}

// newError builds the one error shape this package emits:
//
//	adaptor: <origin>: <what> — <fix>
//
// The fix half is not decoration. A refusal an operator cannot act on is a
// refusal they will work around by disabling the check.
func newError(origin, what, fix string) error {
	return fmt.Errorf("adaptor: %s: %s — %s", origin, what, fix)
}

func isFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

func isValidSenseType(t SenseType) bool {
	for _, valid := range validSenseTypes() {
		if t == valid {
			return true
		}
	}
	return false
}

func joinSenseTypes() string {
	names := make([]string, 0, len(validSenseTypes()))
	for _, t := range validSenseTypes() {
		names = append(names, string(t))
	}
	return strings.Join(names, ", ")
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
