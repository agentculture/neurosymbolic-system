package provider_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/provider"
	"github.com/agentculture/neurosymbolic-system/internal/sense"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// fastCompletionServer answers immediately, cycling through a couple of
// replies — enough variety to exercise both a successful write and, every so
// often, a malformed one, while never blocking.
func fastCompletionServer(t *testing.T) *httptest.Server {
	t.Helper()
	var n int
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		malformed := n%7 == 0
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if malformed {
			_, _ = w.Write([]byte(`{"choices":[]}`))
			return
		}
		type reply struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		var resp reply
		resp.Choices = append(resp.Choices, struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}{})
		resp.Choices[0].Message.Content = "ok"
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// TestNoDataRaceOver10000FakeTicks is acceptance criterion 5 (spec c41): drive
// the tick engine, the provider's worker, and a concurrent snapshot writer for
// 10,000 fake ticks. Run with `go test -race` — the race detector is the
// assertion; this test's job is only to keep all three actually concurrent for
// long enough to give it something to catch.
func TestNoDataRaceOver10000FakeTicks(t *testing.T) {
	srv := fastCompletionServer(t)
	defer srv.Close()

	voc := testVocabulary(t)
	clock := tick.NewFakeClock(testPeriod)
	logger := senselog.New(io.Discard)
	snap := sense.New()
	sink := adaptor.NewRecordingSink()

	engine, err := tick.New(voc, tick.Config{Period: testPeriod, Clock: clock, Ticker: clock, Log: logger}, sink)
	if err != nil {
		t.Fatalf("tick.New: %v", err)
	}

	p, err := provider.New(provider.Config{
		Name: "verdict", Kind: provider.KindCompletion, BaseURL: srv.URL,
		Output: "verdict", Inputs: []string{"question"},
		Timeout: 200 * time.Millisecond, QueueDepth: 2, Cadence: 3,
	}, snap, logger, srv.Client())
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}
	defer p.Close()
	p.SetView(snap)
	p.SetClock(clock)

	if !engine.Send(tick.SetSeamCmd{Seam: p.Driver()}) {
		t.Fatal("the engine refused the seam command")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.Run(ctx) }()

	// A concurrent writer feeding the SAME snapshot the provider's driver
	// reads and the provider's worker writes to — the third concurrent actor
	// c41 asks for, racing genuinely independent fields against the
	// provider's own "verdict"/"verdict_latency_s" writes.
	stopWriter := make(chan struct{})
	var writerWG sync.WaitGroup
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		i := 0
		for {
			select {
			case <-stopWriter:
				return
			default:
			}
			i++
			snap.Update(map[string]any{"question": i}, time.Now())
		}
	}()

	const ticks = 10000
	clock.Advance(ticks * testPeriod)

	close(stopWriter)
	writerWG.Wait()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("engine.Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the engine did not exit within 10s")
	}

	if got := engine.Stats().Ticks; got != ticks {
		t.Fatalf("Stats().Ticks = %d, want %d", got, ticks)
	}

	// Give any still-in-flight worker calls a moment to land, then read the
	// snapshot and Stats one more time — purely to exercise those read paths
	// concurrently with whatever the worker goroutine is still doing as this
	// test tears down, which is exactly what -race needs to see.
	deadline := time.Now().Add(2 * time.Second)
	for p.Stats().Requests == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	_ = snap.View(time.Now())
	t.Logf("provider stats: %+v", p.Stats())
}
