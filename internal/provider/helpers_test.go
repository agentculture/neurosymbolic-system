package provider

import (
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// fakeCtx builds a tick.TickContext carrying only Now/Tick — everything this
// package's driver reads. Its unexported engine-backed methods (Ownership,
// Pose, Admit, Evict, ActiveNames, Emit) are never called by provider.tick,
// so leaving them zero-valued is safe for these unit tests; the full-engine
// tests instead drive a real *tick.Engine.
func fakeCtx(tickNumber int, now time.Time) tick.TickContext {
	return tick.TickContext{Now: now, Tick: tickNumber}
}

// testVocabJSON is a small vocabulary naming nothing either donor robot names
// (internal/adaptor's donor-literal guard scans for exactly that). It declares
// one channel, the two sense fields a KindEmbedding provider writes plus one a
// KindCompletion provider writes, and one action a react rule can admit.
const testVocabJSON = `{
  "channels": [
    {"name": "ch_a", "arity": 1, "neutral": [0.0]}
  ],
  "senses": [
    {"name": "mood", "type": "string"},
    {"name": "mood_score", "type": "float"},
    {"name": "verdict", "type": "string"},
    {"name": "verdict_latency_s", "type": "float"}
  ],
  "actions": [
    {
      "name": "react-action",
      "claims": ["ch_a"],
      "trajectories": {
        "ch_a": {"easing": {"kind": "hold", "from": [1.0], "to": [1.0], "duration_s": 1.0}}
      }
    }
  ]
}`

func testVoc(t *testing.T) *adaptor.Vocabulary {
	t.Helper()
	v, err := adaptor.Parse([]byte(testVocabJSON))
	if err != nil {
		t.Fatalf("parsing the test vocabulary: %v", err)
	}
	return v
}

// mapViewer is a Viewer over a fixed map, for tests that do not need a real
// sense.Snapshot.
type mapViewer map[string]any

func (m mapViewer) View(_ time.Time) map[string]any { return m }
