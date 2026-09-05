package compose

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
	"github.com/agentculture/neurosymbolic-system/internal/provider"
	"github.com/agentculture/neurosymbolic-system/internal/sense"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
)

// providerStage is the senselog/status token this package files provider
// wiring under. It names a LAYER of the runtime, never anything plant-specific.
const providerStage = "provider"

// providerDoc is one provider config's ON-DISK shape.
//
// It is a mirror of provider.Config rather than that struct decoded directly,
// for the same reason internal/adaptor's TOML front end mirrors its own types:
// the runtime struct carries no serialization tags, and BurntSushi/toml matches
// Go FIELD names, so `base_url` and `api_key_env` would silently not decode.
// Mirroring keeps the on-disk vocabulary snake_case and identical between TOML
// and JSON, and keeps the decision about what an operator may write in one
// place instead of spread across another package's field names.
//
// One field is deliberately NOT a straight copy. provider.Config.Timeout is a
// time.Duration; on disk it is `timeout_s`, a number of SECONDS, because this
// runtime's convention is that a unit lives in the name — a cadence-dependent
// tuning that lost its unit is a bug class of its own, and "timeout = 500"
// would be ambiguous in exactly the way that convention exists to prevent.
//
// The three BOUNDS are pointers for one reason: an omitted knob and a knob
// written as 0 are different requests, and only a pointer can tell them
// apart. Omitted means "whatever provider.Config.validate decides"; written
// means the operator chose it, and a chosen zero, negative or non-finite
// bound is REFUSED (see checkBounds) rather than silently swapped for the
// default. Silently defaulting produces the worst kind of config: a knob the
// operator can see in their own file and cannot affect.
type providerDoc struct {
	Name         string   `toml:"name" json:"name"`
	Kind         string   `toml:"kind" json:"kind"`
	BaseURL      string   `toml:"base_url" json:"base_url"`
	Model        string   `toml:"model" json:"model"`
	APIKeyEnv    string   `toml:"api_key_env" json:"api_key_env"`
	Inputs       []string `toml:"inputs" json:"inputs"`
	Output       string   `toml:"output" json:"output"`
	Labels       []string `toml:"labels" json:"labels"`
	TimeoutS     *float64 `toml:"timeout_s" json:"timeout_s"`
	QueueDepth   *int     `toml:"queue_depth" json:"queue_depth"`
	Cadence      *int     `toml:"cadence" json:"cadence"`
	SystemPrompt string   `toml:"system_prompt" json:"system_prompt"`
	MaxTokens    int      `toml:"max_tokens" json:"max_tokens"`
}

// checkBounds refuses an explicitly written non-positive or non-finite bound,
// naming the field and the file. An omitted bound (a nil pointer) is not
// checked at all: it is not a value anybody wrote.
//
// It is FAIL-CLOSED for the same reason the rules loader refuses an
// out-of-range param instead of clamping it: `timeout_s = 0` almost certainly
// means "no timeout" to whoever typed it, and quietly reading it as "500 ms"
// is the engine reinterpreting a command it was given.
func (d providerDoc) checkBounds(path string) error {
	// NaN fails every comparison, so !(v > 0) catches it as well as zero and
	// negatives; an infinity passes that test and needs its own clause.
	if t := d.TimeoutS; t != nil && (!(*t > 0) || math.IsInf(*t, 0)) {
		return providerFileError(path,
			fmt.Sprintf("timeout_s is %v, which is not a positive, finite number of seconds", *t),
			"give timeout_s a value above 0, or omit it entirely to take the default")
	}
	if d.QueueDepth != nil && *d.QueueDepth < 1 {
		return providerFileError(path,
			fmt.Sprintf("queue_depth is %d, which is not a positive request-queue capacity",
				*d.QueueDepth),
			"give queue_depth a value of at least 1, or omit it entirely to take the default")
	}
	if d.Cadence != nil && *d.Cadence < 1 {
		return providerFileError(path,
			fmt.Sprintf("cadence is %d, which is not a positive number of ticks", *d.Cadence),
			"give cadence a value of at least 1 (1 means every tick), or omit it "+
				"entirely to take the default")
	}
	return nil
}

// toConfig is the mirror copied into the runtime struct. Zero values are left
// zero on purpose: provider.Config.validate is what fills the defaults in, so
// there is exactly one place a default is decided.
func (d providerDoc) toConfig() provider.Config {
	cfg := provider.Config{
		Name:         d.Name,
		Kind:         d.Kind,
		BaseURL:      d.BaseURL,
		Model:        d.Model,
		APIKeyEnv:    d.APIKeyEnv,
		Inputs:       d.Inputs,
		Output:       d.Output,
		Labels:       d.Labels,
		SystemPrompt: d.SystemPrompt,
		MaxTokens:    d.MaxTokens,
	}
	// checkBounds has already refused any written value these could carry
	// that validate would have had to correct, so a nil pointer here means
	// exactly "omitted" and the zero it leaves is the "take the default"
	// signal validate reads.
	if d.TimeoutS != nil {
		cfg.Timeout = time.Duration(*d.TimeoutS * float64(time.Second))
	}
	if d.QueueDepth != nil {
		cfg.QueueDepth = *d.QueueDepth
	}
	if d.Cadence != nil {
		cfg.Cadence = *d.Cadence
	}
	return cfg
}

// loadProviderConfig reads one provider config by extension, refusing an
// unknown key in both formats — a misspelled knob that silently did nothing
// would be a provider an operator believes they tuned.
func loadProviderConfig(path string) (provider.Config, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- an operator-supplied config path is the input
	if err != nil {
		return provider.Config{}, clifmt.NewUserError(
			fmt.Sprintf("the provider config %s cannot be read (%v)", path, err),
			"check the path exists and is readable")
	}

	isTOML, err := isTOMLProviderConfig(path)
	if err != nil {
		return provider.Config{}, err
	}

	var doc providerDoc
	if isTOML {
		md, decodeErr := toml.Decode(string(data), &doc)
		if decodeErr != nil {
			return provider.Config{}, providerFileError(path,
				fmt.Sprintf("the config is not valid TOML (%v)", decodeErr),
				"fix the document; unknown keys are refused as well as malformed syntax")
		}
		if undecoded := md.Undecoded(); len(undecoded) > 0 {
			return provider.Config{}, providerFileError(path,
				fmt.Sprintf("the config has unknown key(s): %v", undecoded),
				"remove them; a knob that silently did nothing would be a provider "+
					"you believe you tuned")
		}
	} else {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if decodeErr := dec.Decode(&doc); decodeErr != nil {
			return provider.Config{}, providerFileError(path,
				fmt.Sprintf("the config is not valid JSON (%v)", decodeErr),
				"fix the document; unknown keys are refused as well as malformed syntax")
		}
		// A json.Decoder stops at the end of the first value, so a second
		// document or trailing garbage would be silently ignored — a config
		// an operator edited that has no effect and reports no error. See
		// internal/adaptor's identical check.
		if eofErr := decodedToEOF(dec); eofErr != nil {
			return provider.Config{}, providerFileError(path,
				fmt.Sprintf("the config has trailing content after the JSON document (%v)", eofErr),
				"a provider config is exactly ONE JSON document; remove everything after it")
		}
	}
	if err := doc.checkBounds(path); err != nil {
		return provider.Config{}, err
	}
	return doc.toConfig(), nil
}

// isTOMLProviderConfig picks the decoder by extension, refusing anything else
// by name rather than guessing at the format.
func isTOMLProviderConfig(path string) (bool, error) {
	switch strings.ToLower(extOf(path)) {
	case ".toml":
		return true, nil
	case ".json":
		return false, nil
	default:
		return false, clifmt.NewUserError(
			fmt.Sprintf("--%s %q has an unrecognized extension", flagProvider, path),
			"pass a .toml or .json provider config")
	}
}

func providerFileError(path, what, fix string) error {
	return clifmt.NewUserError(fmt.Sprintf("provider: %s: %s", path, what), fix)
}

// checkProviderVocabulary refuses a provider whose input or output fields this
// robot does not declare.
//
// It is the composition root's check because the composition root is the only
// thing holding both halves: internal/provider deliberately never imports
// internal/adaptor (a provider is not plant-specific), and internal/adaptor has
// never heard of providers. Neither can make this check alone, and without it
// the two failure modes are both silent: a typo'd INPUT renders as an empty
// field in every request forever, and a typo'd OUTPUT writes a sense field no
// rule is allowed to name, so the provider works perfectly and nothing ever
// reacts to it. A provider that silently does nothing is worse than one that
// refuses to start.
//
// The derived fields a provider also writes ("<output>_score",
// "<output>_latency_s") are NOT required here. They only matter if a rule keys
// on one, and the rules loader already refuses a rule naming an undeclared
// field — so requiring them would refuse configs that are perfectly correct.
func checkProviderVocabulary(cfg provider.Config, voc *adaptor.Vocabulary) error {
	for _, field := range cfg.Inputs {
		if !voc.HasField(field) {
			return clifmt.NewUserError(
				fmt.Sprintf("provider %q reads input field %q, which %s does not declare",
					cfg.Name, field, voc.Origin()),
				"declare it as a sense in the adaptor config, or name one that is "+
					"declared; a provider reading a field that does not exist sends an "+
					"empty request forever")
		}
	}
	if !voc.HasField(cfg.Output) {
		return clifmt.NewUserError(
			fmt.Sprintf("provider %q writes output field %q, which %s does not declare",
				cfg.Name, cfg.Output, voc.Origin()),
			"declare it as a sense in the adaptor config; an undeclared output is a "+
				"field no rule is allowed to key on, so the provider would run and "+
				"nothing would ever react to it")
	}
	return nil
}

// namedProvider pairs a live provider with the name its config gave it.
//
// internal/provider keeps that name inside its own Config and exposes no
// getter, and reaching into another package for a field it chose not to export
// would be the wrong fix. The composition root read the name out of the file,
// so the composition root is what remembers it — for the bus rider's label and
// for the key its Stats appear under in `status`.
type namedProvider struct {
	Name     string
	Provider *provider.Provider
}

// buildProviders loads every --provider config, constructs it against the
// runtime's own snapshot, and hands back the live providers in flag order.
//
// The snapshot is BOTH the sink a provider writes its decision into and the
// view it reads its inputs from. That is the whole shape of this seam: a
// provider's output is an ordinary sense field, so a rule predicates on it
// exactly like any other reading, and neither the rules layer nor the engine
// ever learns that a provider exists.
//
// A warm-up failure is NOT an error here — that is internal/provider's own
// contract. An unreachable gateway marks the provider unconfigured with one
// named drop and the engine still starts, with the bound rule abstaining
// forever. A robot that refused to boot because a side-car model was down
// would be a robot taken out by its least important dependency.
func buildProviders(
	paths []string, voc *adaptor.Vocabulary, snapshot *sense.Snapshot, log *senselog.Logger,
) ([]namedProvider, error) {
	built := make([]namedProvider, 0, len(paths))
	for _, path := range paths {
		cfg, err := loadProviderConfig(path)
		if err != nil {
			closeProviders(built)
			return nil, err
		}
		if err := checkProviderVocabulary(cfg, voc); err != nil {
			closeProviders(built)
			return nil, err
		}
		// A nil *http.Client means "build one carrying this config's timeout as
		// a hard ceiling" — constructed HERE, at composition, never on the tick
		// thread, where building a client is the single most expensive mistake
		// the donor made (425-1213 ms, a 21-61x overrun).
		p, err := provider.New(cfg, snapshot, log, nil)
		if err != nil {
			closeProviders(built)
			return nil, clifmt.NewUserError(err.Error(),
				fmt.Sprintf("fix %s; an unusable provider is refused at startup rather "+
					"than admitted and left abstaining forever", path))
		}
		p.SetView(snapshot)
		built = append(built, namedProvider{Name: cfg.Name, Provider: p})
	}
	return built, nil
}

// closeProviders stops every provider's worker goroutine. It is called both on
// a partial build failure and on shutdown: a provider left running holds a
// goroutine and an HTTP client nobody owns.
func closeProviders(providers []namedProvider) {
	for _, p := range providers {
		p.Provider.Close()
	}
}
