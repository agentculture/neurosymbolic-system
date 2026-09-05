package provider

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/sense"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
)

// -- fixture servers ---------------------------------------------------------

// embeddingServer returns a fixed vector for every request — enough to warm
// up two labels with distinguishable embeddings and then classify a query
// against them.
func embeddingServer(t *testing.T, vectorFor func(input string) []float64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		vec := vectorFor(req.Input)
		resp := embeddingResponse{Data: []struct {
			Embedding []float64 `json:"embedding"`
		}{{Embedding: vec}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// completionServer always replies with the same word.
func completionServer(t *testing.T, word string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := completionResponse{Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Role: "assistant", Content: word}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// sleepingServer never answers before d elapses (or the request's own
// deadline fires first) — the fixture criterion 1 drives against.
func sleepingServer(t *testing.T, d time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(d):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embeddingResponse{})
	}))
}

func statusServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
}

func malformedServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	}))
}

// -- waiting helper -----------------------------------------------------------

// waitForCondition polls until fn returns true or the deadline passes,
// failing the test on timeout. Provider results land asynchronously off the
// tick goroutine, so tests that assert on Stats()/sink content need to wait
// rather than read once.
func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !fn() {
		t.Fatalf("condition not met within %v", timeout)
	}
}

// -- config validation --------------------------------------------------------

func TestConfigValidateRefusesUnknownKind(t *testing.T) {
	cfg := Config{Name: "n", Kind: "guess", BaseURL: "http://x", Output: "o", Inputs: []string{"i"}}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected an error for an unknown Kind")
	}
}

func TestConfigValidateFillsDefaults(t *testing.T) {
	cfg := Config{Name: "n", Kind: KindCompletion, BaseURL: "http://x", Output: "o", Inputs: []string{"i"}}
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.Timeout != DefaultTimeout {
		t.Errorf("Timeout = %v, want default %v", cfg.Timeout, DefaultTimeout)
	}
	if cfg.QueueDepth != DefaultQueueDepth {
		t.Errorf("QueueDepth = %d, want default %d", cfg.QueueDepth, DefaultQueueDepth)
	}
	if cfg.Cadence != DefaultCadence {
		t.Errorf("Cadence = %d, want default %d", cfg.Cadence, DefaultCadence)
	}
}

func TestConfigValidateRequiresLabelsForEmbedding(t *testing.T) {
	cfg := Config{Name: "n", Kind: KindEmbedding, BaseURL: "http://x", Output: "o", Inputs: []string{"i"}}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected an error: no Labels for a KindEmbedding provider")
	}
}

// -- New / warm-up -------------------------------------------------------------

func TestNewRefusesNilSink(t *testing.T) {
	cfg := Config{Name: "n", Kind: KindCompletion, BaseURL: "http://x", Output: "o", Inputs: []string{"i"}}
	if _, err := New(cfg, nil, nil, nil); err == nil {
		t.Fatal("expected an error for a nil sink")
	}
}

func TestEmbeddingWarmUpFetchesEachLabelOnce(t *testing.T) {
	var calls atomic.Int64
	srv := embeddingServer(t, func(input string) []float64 {
		calls.Add(1)
		switch input {
		case "happy":
			return []float64{1, 0}
		case "sad":
			return []float64{0, 1}
		default:
			return []float64{0.5, 0.5}
		}
	})
	defer srv.Close()

	snap := sense.New()
	cfg := Config{
		Name: "mood", Kind: KindEmbedding, BaseURL: srv.URL, Output: "mood",
		Inputs: []string{"text"}, Labels: []string{"happy", "sad"},
	}
	p, err := New(cfg, snap, senselog.New(&bytes.Buffer{}), srv.Client())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	if p.unconfigured {
		t.Fatal("provider marked unconfigured after a successful warm-up")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("warm-up made %d calls, want 2 (one per label)", got)
	}
	if len(p.labelRefs) != 2 {
		t.Fatalf("labelRefs has %d entries, want 2", len(p.labelRefs))
	}
}

func TestEmbeddingWarmUpFailureMarksUnconfiguredAndEngineStillStarts(t *testing.T) {
	srv := statusServer(t, http.StatusInternalServerError)
	defer srv.Close()

	buf := &bytes.Buffer{}
	snap := sense.New()
	cfg := Config{
		Name: "mood", Kind: KindEmbedding, BaseURL: srv.URL, Output: "mood",
		Inputs: []string{"text"}, Labels: []string{"happy"},
	}
	p, err := New(cfg, snap, senselog.New(buf), srv.Client())
	if err != nil {
		t.Fatalf("New should not fail on a warm-up error, got: %v", err)
	}
	defer p.Close()

	if !p.unconfigured {
		t.Fatal("expected the provider to be marked unconfigured after a failed warm-up")
	}
	if !strings.Contains(buf.String(), "reason=unconfigured") {
		t.Fatalf("expected a named unconfigured drop at warm-up, got log: %q", buf.String())
	}

	// The driver still runs without panicking, and every tick abstains.
	driver := p.Driver()
	for i := 1; i <= 5; i++ {
		driver(fakeCtx(i, time.Unix(0, 0)))
	}
	stats := p.Stats()
	if stats.Requests != 0 {
		t.Fatalf("Requests = %d, want 0 — an unconfigured provider must never enqueue", stats.Requests)
	}
}

// -- HTTP failure classification ----------------------------------------------

func TestEmbeddingHTTPErrorIsNamedDrop(t *testing.T) {
	warmup := embeddingServer(t, func(string) []float64 { return []float64{1, 0} })
	defer warmup.Close()

	p := newTestEmbeddingProvider(t, warmup.URL, warmup.Client())
	// Swap BaseURL after warm-up so the per-tick call fails, not the warm-up.
	bad := statusServer(t, http.StatusServiceUnavailable)
	defer bad.Close()
	p.cfg.BaseURL = bad.URL
	p.client = bad.Client()
	defer p.Close()

	driveOneRequest(t, p, map[string]any{"text": "hello"})

	waitForCondition(t, time.Second, func() bool {
		return p.Stats().Drops["http-503"] > 0
	})
}

func TestEmbeddingMalformedResponseIsNamedDrop(t *testing.T) {
	warmup := embeddingServer(t, func(string) []float64 { return []float64{1, 0} })
	defer warmup.Close()
	p := newTestEmbeddingProvider(t, warmup.URL, warmup.Client())

	bad := malformedServer(t)
	defer bad.Close()
	p.cfg.BaseURL = bad.URL
	p.client = bad.Client()
	defer p.Close()

	driveOneRequest(t, p, map[string]any{"text": "hello"})

	waitForCondition(t, time.Second, func() bool {
		return p.Stats().Drops["malformed"] > 0
	})
}

func TestUnreachableBaseURLIsNamedTimeoutDrop(t *testing.T) {
	warmup := embeddingServer(t, func(string) []float64 { return []float64{1, 0} })
	defer warmup.Close()
	p := newTestEmbeddingProvider(t, warmup.URL, warmup.Client())
	p.cfg.BaseURL = "http://127.0.0.1:1" // nothing listens here
	p.client = &http.Client{Timeout: 200 * time.Millisecond}
	defer p.Close()

	driveOneRequest(t, p, map[string]any{"text": "hello"})

	waitForCondition(t, 2*time.Second, func() bool {
		return p.Stats().Drops["timeout"] > 0
	})
}

// -- successful calls ----------------------------------------------------------

func TestEmbeddingProviderClassifiesBestLabel(t *testing.T) {
	srv := embeddingServer(t, func(input string) []float64 {
		switch input {
		case "happy":
			return []float64{1, 0}
		case "sad":
			return []float64{0, 1}
		case "joyful day":
			return []float64{0.9, 0.1}
		default:
			return []float64{0, 0}
		}
	})
	defer srv.Close()

	snap := sense.New()
	cfg := Config{
		Name: "mood", Kind: KindEmbedding, BaseURL: srv.URL, Output: "mood",
		Inputs: []string{"text"}, Labels: []string{"happy", "sad"},
	}
	p, err := New(cfg, snap, senselog.New(&bytes.Buffer{}), srv.Client())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	driveOneRequest(t, p, map[string]any{"text": "joyful day"})

	waitForCondition(t, time.Second, func() bool {
		v, ok := snap.Get("mood")
		return ok && v == "happy"
	})
	score, ok := snap.Get("mood_score")
	if !ok {
		t.Fatal("mood_score was never written")
	}
	if f, ok := score.(float64); !ok || f <= 0.5 {
		t.Fatalf("mood_score = %v, want a high similarity", score)
	}
}

func TestCompletionProviderWritesWordAndLatency(t *testing.T) {
	srv := completionServer(t, "Yes, definitely.")
	defer srv.Close()

	snap := sense.New()
	cfg := Config{
		Name: "verdict", Kind: KindCompletion, BaseURL: srv.URL, Output: "verdict",
		Inputs: []string{"question"},
	}
	p, err := New(cfg, snap, senselog.New(&bytes.Buffer{}), srv.Client())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	driveOneRequest(t, p, map[string]any{"question": "well?"})

	waitForCondition(t, time.Second, func() bool {
		v, ok := snap.Get("verdict")
		return ok && v == "yes"
	})
	if _, ok := snap.Get("verdict_latency_s"); !ok {
		t.Fatal("verdict_latency_s was never written")
	}
}

// -- helpers -------------------------------------------------------------------

func newTestEmbeddingProvider(t *testing.T, warmURL string, client *http.Client) *Provider {
	t.Helper()
	snap := sense.New()
	cfg := Config{
		Name: "mood", Kind: KindEmbedding, BaseURL: warmURL, Output: "mood",
		Inputs: []string{"text"}, Labels: []string{"happy"},
	}
	p, err := New(cfg, snap, senselog.New(&bytes.Buffer{}), client)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// driveOneRequest installs a fixed view and runs the driver once, which
// (queue not full, provider configured) enqueues exactly one request.
func driveOneRequest(t *testing.T, p *Provider, view map[string]any) {
	t.Helper()
	p.SetView(mapViewer(view))
	p.Driver()(fakeCtx(1, time.Unix(0, 0)))
}
