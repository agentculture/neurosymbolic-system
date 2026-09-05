package provider

import (
	"fmt"
	"time"
)

// Provider kinds.
const (
	KindEmbedding  = "embedding"
	KindCompletion = "completion"
)

// Defaults, mirroring the donor's depth-2 speech queue and a half-tick-budget
// HTTP timeout — generous next to the 20 ms tick period, since a provider call
// is a background worker, never the tick thread.
const (
	DefaultQueueDepth  = 2
	DefaultTimeout     = 500 * time.Millisecond
	DefaultCadence     = 1
	DefaultMaxTokens   = 8
	DefaultSystemPromt = "Answer in one short word."
)

// Config describes one decision provider.
type Config struct {
	// Name identifies this provider in every senselog line and in Stats. It
	// must be non-empty and is never a robot channel/field/action name — it
	// names the PROVIDER, a config-time identity, not a plant literal.
	Name string
	// Kind is KindEmbedding or KindCompletion.
	Kind string
	// BaseURL is the OpenAI-compatible gateway, e.g. "http://localhost:8000".
	BaseURL string
	// Model is the model id sent as the request's "model" field.
	Model string
	// APIKeyEnv, when non-empty, names the environment variable New reads for
	// the bearer token. Resolved once at New — never on the tick goroutine.
	APIKeyEnv string
	// Inputs names the sense fields rendered into the request.
	Inputs []string
	// Output is the sense field this provider writes on success.
	Output string
	// Labels is the closed label set a KindEmbedding provider classifies
	// against. Required, and non-empty, for KindEmbedding; unused otherwise.
	Labels []string
	// Timeout bounds one HTTP call. Zero means DefaultTimeout.
	Timeout time.Duration
	// QueueDepth is the bounded request queue's capacity. Zero means
	// DefaultQueueDepth.
	QueueDepth int
	// Cadence is "enqueue a request every N ticks". Zero or one means every
	// tick.
	Cadence int
	// SystemPrompt is the KindCompletion system message. Empty means
	// DefaultSystemPromt.
	SystemPrompt string
	// MaxTokens bounds a KindCompletion reply. Zero means DefaultMaxTokens.
	MaxTokens int
}

// validate fills in defaults and refuses a config fail-closed: an unusable
// provider is refused at New, never admitted and left to abstain forever
// silently for a reason nobody can see.
func (c *Config) validate() error {
	if c.Name == "" {
		return fmt.Errorf("provider: a config needs a Name")
	}
	switch c.Kind {
	case KindEmbedding, KindCompletion:
	default:
		return fmt.Errorf(
			"provider %q: Kind %q is neither %q nor %q", c.Name, c.Kind, KindEmbedding, KindCompletion)
	}
	if c.BaseURL == "" {
		return fmt.Errorf("provider %q: a config needs a BaseURL", c.Name)
	}
	if c.Output == "" {
		return fmt.Errorf("provider %q: a config needs an Output field", c.Name)
	}
	if len(c.Inputs) == 0 {
		return fmt.Errorf("provider %q: a config needs at least one Input field", c.Name)
	}
	if c.Kind == KindEmbedding && len(c.Labels) == 0 {
		return fmt.Errorf("provider %q: a %s provider needs at least one Label", c.Name, KindEmbedding)
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.QueueDepth <= 0 {
		c.QueueDepth = DefaultQueueDepth
	}
	if c.Cadence <= 0 {
		c.Cadence = DefaultCadence
	}
	if c.SystemPrompt == "" {
		c.SystemPrompt = DefaultSystemPromt
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = DefaultMaxTokens
	}
	return nil
}
