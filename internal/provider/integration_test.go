package provider_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/provider"
	"github.com/agentculture/neurosymbolic-system/internal/ruleeval"
	"github.com/agentculture/neurosymbolic-system/internal/rules"
	"github.com/agentculture/neurosymbolic-system/internal/sense"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// A vocabulary naming nothing either donor robot names (internal/adaptor's
// donor-literal guard scans for exactly that leak).
const integrationVocab = `{
  "channels": [
    {"name": "ch_a", "arity": 1, "neutral": [0.0]}
  ],
  "senses": [
    {"name": "question", "type": "string"},
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

const testPeriod = 20 * time.Millisecond

// completionRequestBody mirrors provider's own wire shape, kept local to
// avoid depending on the package's unexported test-only names.
type completionRequestBody struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

type completionReplyBody struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// gatedCompletionServer answers every request with word, but only after a
// caller closes (or sends to) release — letting a test pin the exact wall-clock
// moment the HTTP call is allowed to complete, independent of the engine's
// fake tick clock.
func gatedCompletionServer(t *testing.T, word string, release <-chan struct{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req completionRequestBody
		_ = json.NewDecoder(r.Body).Decode(&req)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		var resp completionReplyBody
		item := struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		}{}
		item.Message.Role = "assistant"
		item.Message.Content = word
		resp.Choices = append(resp.Choices, item)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func testVocabulary(t *testing.T) *adaptor.Vocabulary {
	t.Helper()
	v, err := adaptor.Parse([]byte(integrationVocab))
	if err != nil {
		t.Fatalf("parsing test vocabulary: %v", err)
	}
	return v
}

// TestRuleFiresOnTickAfterProviderResultNeverOnTheRequestingTick is acceptance
// criterion 2: a rule bound to a provider's output field fires on the tick
// after the provider's HTTP call lands, never on the tick that enqueued it.
func TestRuleFiresOnTickAfterProviderResultNeverOnTheRequestingTick(t *testing.T) {
	release := make(chan struct{})
	srv := gatedCompletionServer(t, "Yes.", release)
	defer srv.Close()

	voc := testVocabulary(t)
	clock := tick.NewFakeClock(testPeriod)
	logBuf := &bytes.Buffer{}
	logger := senselog.New(logBuf)
	snap := sense.New()
	snap.Update(map[string]any{"question": "well?"}, clock.Now())

	sink := adaptor.NewRecordingSink()
	engine, err := tick.New(voc, tick.Config{Period: testPeriod, Clock: clock, Ticker: clock, Log: logger}, sink)
	if err != nil {
		t.Fatalf("tick.New: %v", err)
	}

	cfg := &rules.Config{
		SchemaVersion: 2,
		React: []rules.Rule{{
			ID:   "verdict-react",
			Kind: rules.KindReact,
			When: rules.Predicate{Field: "verdict", Op: "is_true"},
			Run:  "react-action",
		}},
	}
	evaluator, err := ruleeval.New(ruleeval.Config{
		Rules: cfg, Vocabulary: voc, Snapshot: snap, Logger: logger,
	})
	if err != nil {
		t.Fatalf("ruleeval.New: %v", err)
	}

	p, err := provider.New(provider.Config{
		Name: "verdict", Kind: provider.KindCompletion, BaseURL: srv.URL,
		Output: "verdict", Inputs: []string{"question"},
	}, snap, logger, srv.Client())
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}
	p.SetView(snap)
	p.SetClock(clock)

	var events []tick.Event
	var mu sync.Mutex
	engine.OnEvent(func(ev tick.Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})

	bus := ruleeval.NewBus(logger)
	bus.Add("provider", p.Driver())
	bus.Add("rules", evaluator.Seam())
	if !engine.Send(tick.SetSeamCmd{Seam: bus.Compose()}) {
		t.Fatal("the engine refused the seam command")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.Run(ctx) }()

	fireTick := func() int {
		mu.Lock()
		defer mu.Unlock()
		for _, ev := range events {
			if ev.Name == ruleeval.EventFire && ev.Data["rule"] == "verdict-react" {
				tickNum, _ := ev.Data["tick"].(int)
				return tickNum
			}
		}
		return 0
	}

	// Tick 1: the provider enqueues the request; the gated server has not
	// answered yet, so the rule cannot have fired.
	clock.Advance(testPeriod)
	if got := fireTick(); got != 0 {
		t.Fatalf("the rule fired on tick %d before the provider ever answered", got)
	}
	if p.Stats().Results != 0 {
		t.Fatalf("Stats().Results = %d before the gate was released, want 0", p.Stats().Results)
	}

	// Release the HTTP response and wait for the worker to write the result.
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for p.Stats().Results == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if p.Stats().Results == 0 {
		t.Fatal("the provider never wrote a result after the gate was released")
	}

	// Tick 2 (and, if the write landed after tick 2's seam, tick 3): the rule
	// must fire on a LATER tick — never tick 1, the one that requested it.
	clock.Advance(testPeriod)
	if fireTick() == 0 {
		clock.Advance(testPeriod)
	}
	got := fireTick()
	if got == 0 {
		t.Fatal("the rule never fired even after the provider's result landed")
	}
	if got <= 1 {
		t.Fatalf("the rule fired on tick %d, want strictly after tick 1 (the requesting tick)", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("engine.Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the engine did not exit within 5s")
	}

	// Stop the worker before reading logBuf: it is a plain bytes.Buffer, not
	// safe for a concurrent reader while the worker goroutine might still be
	// mid-write to it.
	p.Close()

	if !strings.Contains(logBuf.String(), "stage=") {
		t.Log("no SENSE lines were written; that is fine, this test is about fire timing")
	}
}

// TestNoProviderConfiguredEngineStartsAndRuleAbstains is acceptance criterion
// 3: with no provider wired at all, a rule bound to its output field simply
// never sees a reading, and the engine runs normally.
func TestNoProviderConfiguredEngineStartsAndRuleAbstains(t *testing.T) {
	voc := testVocabulary(t)
	clock := tick.NewFakeClock(testPeriod)
	logger := senselog.New(&bytes.Buffer{})
	snap := sense.New()
	sink := adaptor.NewRecordingSink()

	engine, err := tick.New(voc, tick.Config{Period: testPeriod, Clock: clock, Ticker: clock, Log: logger}, sink)
	if err != nil {
		t.Fatalf("tick.New: %v", err)
	}

	cfg := &rules.Config{
		SchemaVersion: 2,
		React: []rules.Rule{{
			ID: "verdict-react", Kind: rules.KindReact,
			When: rules.Predicate{Field: "verdict", Op: "is_true"}, Run: "react-action",
		}},
	}
	evaluator, err := ruleeval.New(ruleeval.Config{Rules: cfg, Vocabulary: voc, Snapshot: snap, Logger: logger})
	if err != nil {
		t.Fatalf("ruleeval.New: %v", err)
	}
	if !engine.Send(tick.SetSeamCmd{Seam: evaluator.Seam()}) {
		t.Fatal("the engine refused the seam command")
	}

	var fires int
	var mu sync.Mutex
	engine.OnEvent(func(ev tick.Event) {
		if ev.Name == ruleeval.EventFire {
			mu.Lock()
			fires++
			mu.Unlock()
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.Run(ctx) }()

	clock.Advance(50 * testPeriod)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("engine.Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the engine did not exit within 5s")
	}

	mu.Lock()
	got := fires
	mu.Unlock()
	if got != 0 {
		t.Fatalf("fires = %d over 50 ticks with no provider ever configured, want 0", got)
	}
	if engine.Stats().Ticks != 50 {
		t.Fatalf("Stats().Ticks = %d, want 50 — the engine must start and run normally", engine.Stats().Ticks)
	}
}
