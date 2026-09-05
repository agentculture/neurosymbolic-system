package tick

import (
	"bytes"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
)

// The toy vocabulary every test in this package runs against.
//
// It deliberately names NOTHING either donor robot names: the engine learns its
// channels and actions from an adaptor config, and a test fixture that borrowed
// a real robot's words would quietly make the engine look plant-specific.
// Channel arities differ (2, 1, 3) so a composition bug that assumes a uniform
// width cannot hide.
const toyVocabulary = `{
  "channels": [
    {"name": "ch_a", "arity": 2, "neutral": [0.0, 0.0]},
    {"name": "ch_b", "arity": 1, "neutral": [5.0]},
    {"name": "ch_c", "arity": 3, "neutral": [1.0, 2.0, 3.0]}
  ],
  "senses": [
    {"name": "lux", "type": "float"}
  ],
  "actions": [
    {
      "name": "ramp",
      "claims": ["ch_a"],
      "params": [{"name": "gain", "min": 0.0, "max": 1.0}],
      "trajectories": {
        "ch_a": {"easing": {"kind": "linear", "from": [0.0, 0.0],
                            "to": [10.0, 20.0], "duration_s": 1.0}}
      }
    },
    {
      "name": "cycle",
      "claims": ["ch_a", "ch_b"],
      "loops": true,
      "trajectories": {
        "ch_a": {"keyframes": [{"t": 0.0, "value": [0.0, 0.0]},
                               {"t": 0.5, "value": [4.0, 8.0]},
                               {"t": 1.0, "value": [0.0, 0.0]}]},
        "ch_b": {"easing": {"kind": "ease_in_out", "from": [0.0],
                            "to": [3.0], "duration_s": 2.0}}
      }
    },
    {
      "name": "wide",
      "claims": ["ch_a", "ch_b", "ch_c"],
      "trajectories": {
        "ch_a": {"easing": {"kind": "hold", "from": [7.0, 7.0],
                            "to": [7.0, 7.0], "duration_s": 1.0}},
        "ch_b": {"easing": {"kind": "hold", "from": [9.0], "to": [9.0],
                            "duration_s": 1.0}},
        "ch_c": {"easing": {"kind": "hold", "from": [1.0, 1.0, 1.0],
                            "to": [1.0, 1.0, 1.0], "duration_s": 1.0}}
      }
    },
    {
      "name": "hum",
      "claims": ["ch_a", "ch_b"],
      "loops": true,
      "trajectories": {
        "ch_a": {"easing": {"kind": "linear", "from": [-1.0, -1.0],
                            "to": [1.0, 1.0], "duration_s": 2.0}},
        "ch_b": {"easing": {"kind": "linear", "from": [-1.0], "to": [1.0],
                            "duration_s": 2.0}}
      }
    }
  ]
}`

func toyVoc(t *testing.T) *adaptor.Vocabulary {
	t.Helper()
	v, err := adaptor.Parse([]byte(toyVocabulary))
	if err != nil {
		t.Fatalf("parsing the toy vocabulary: %v", err)
	}
	return v
}

// toyChannels is the toy vocabulary's channel list, in declaration order.
func toyChannels() []string { return []string{"ch_a", "ch_b", "ch_c"} }

// trajectoryFor returns one action's trajectory for one channel, so a test can
// compare what the engine streamed against adaptor.Trajectory.At directly.
func trajectoryFor(t *testing.T, v *adaptor.Vocabulary, action, channel string) *adaptor.Trajectory {
	t.Helper()
	for _, a := range v.Actions() {
		if a.Name == action {
			traj := a.Trajectories[channel]
			if traj == nil {
				t.Fatalf("action %q declares no trajectory for channel %q", action, channel)
			}
			return traj
		}
	}
	t.Fatalf("the toy vocabulary declares no action %q", action)
	return nil
}

// seconds returns a pointer to s, for Lifetime.DurationS.
func seconds(s float64) *float64 { return &s }

// behaviorFor is a bound behavior over the toy vocabulary, failing the test on
// a refusal.
func behaviorFor(t *testing.T, v *adaptor.Vocabulary, b Behavior) Behavior {
	t.Helper()
	bound, err := Bind(v, b)
	if err != nil {
		t.Fatalf("Bind(%+v): %v", b, err)
	}
	return bound
}

// claimant is a bare Behavior for the pure contention tests: no vocabulary, no
// trajectories, just an id, a class and its claims.
func claimant(id string, class StopClass, channels ...string) Behavior {
	return Behavior{ID: id, Name: id, Class: class, Channels: channels}
}

// contribution is a Contribution naming one value per channel, used to make a
// claimant's abstention (or non-abstention) explicit in a test.
func contribution(channels ...string) Contribution {
	c := Contribution{}
	for _, channel := range channels {
		c[channel] = []float64{0}
	}
	return c
}

// testLogger returns a senselog.Logger writing into buf.
func testLogger(buf *bytes.Buffer) *senselog.Logger { return senselog.New(buf) }
