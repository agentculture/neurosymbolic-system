package adaptor

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// LoadTOML reads and validates an adaptor config written as TOML, sharing
// every validation rule LoadJSON/Parse enforce (channel/sense/action name
// uniqueness, arity, trajectory compilation, param domains, ...) — the two
// front ends differ only in the decoder, never in what they accept.
//
// The document shape is deliberately identical to the JSON one: the struct
// tags on Channel documented "the identical document shape maps onto TOML
// unchanged" is true for every field that has no underscore (channel/param
// names, arity, min/max, claims, loops, keyframe t/value), but
// encoding/json's snake_case tags (age_field, duration_s) are NOT struct
// field names BurntSushi/toml's default case-insensitive matcher will find —
// it matches Go field names, not json tags. Rather than add toml struct tags
// to the shared config/Sense/Action/Easing types (which would mean editing
// vocabulary.go/trajectory.go, off limits for this follow-up), this file
// decodes into small toml-tagged mirror types local to itself and copies the
// result into the same exported types (Channel, Sense, Action, Param,
// Trajectory, Easing, Keyframe) parse() already builds a Vocabulary from —
// so the validation path below is line-for-line what Parse runs.
func LoadTOML(path string) (*Vocabulary, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the config path is operator-supplied by design
	if err != nil {
		return nil, newError(path, fmt.Sprintf("the config cannot be read (%v)", err),
			"check the path exists and is readable")
	}
	return parseTOML(data, path)
}

// tomlDoc mirrors config (see vocabulary.go) for TOML decoding. Channel and
// Param need no tags: their field names contain no underscore, so
// BurntSushi/toml's default matching already finds them.
type tomlDoc struct {
	Channels []Channel    `toml:"channels"`
	Senses   []tomlSense  `toml:"senses"`
	Actions  []tomlAction `toml:"actions"`
}

type tomlSense struct {
	Name     string    `toml:"name"`
	Type     SenseType `toml:"type"`
	AgeField string    `toml:"age_field"`
}

type tomlAction struct {
	Name         string                    `toml:"name"`
	Claims       []string                  `toml:"claims"`
	Loops        bool                      `toml:"loops"`
	Params       []Param                   `toml:"params"`
	Trajectories map[string]tomlTrajectory `toml:"trajectories"`
}

type tomlTrajectory struct {
	Keyframes []Keyframe  `toml:"keyframes"`
	Easing    *tomlEasing `toml:"easing"`
}

type tomlEasing struct {
	Kind      EasingKind `toml:"kind"`
	From      []float64  `toml:"from"`
	To        []float64  `toml:"to"`
	DurationS float64    `toml:"duration_s"`
}

func parseTOML(data []byte, origin string) (*Vocabulary, error) {
	var doc tomlDoc
	md, err := toml.Decode(string(data), &doc)
	if err != nil {
		return nil, newError(origin, fmt.Sprintf("the config is not valid TOML (%v)", err),
			"fix the document; unknown keys are refused as well as malformed syntax")
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, newError(origin,
			fmt.Sprintf("the config has unknown key(s): %v", undecoded),
			"remove them; unknown keys are refused as well as malformed syntax")
	}

	cfg := doc.toConfig()
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

// toConfig copies a decoded tomlDoc into the same config shape parse() (the
// JSON front end) builds a Vocabulary from, so both front ends run through
// literally the same validate* methods below this point.
func (d *tomlDoc) toConfig() config {
	cfg := config{Channels: d.Channels}
	for _, s := range d.Senses {
		cfg.Senses = append(cfg.Senses, Sense{Name: s.Name, Type: s.Type, AgeField: s.AgeField})
	}
	for _, a := range d.Actions {
		action := Action{Name: a.Name, Claims: a.Claims, Loops: a.Loops, Params: a.Params}
		if len(a.Trajectories) > 0 {
			action.Trajectories = make(map[string]*Trajectory, len(a.Trajectories))
			for channel, t := range a.Trajectories {
				traj := &Trajectory{Keyframes: t.Keyframes}
				if t.Easing != nil {
					traj.Easing = &Easing{
						Kind:      t.Easing.Kind,
						From:      t.Easing.From,
						To:        t.Easing.To,
						DurationS: t.Easing.DurationS,
					}
				}
				action.Trajectories[channel] = traj
			}
		}
		cfg.Actions = append(cfg.Actions, action)
	}
	return cfg
}
