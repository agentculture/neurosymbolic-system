package provider_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/provider"
	"github.com/agentculture/neurosymbolic-system/internal/sense"
	"github.com/agentculture/neurosymbolic-system/internal/senselog"
	"github.com/agentculture/neurosymbolic-system/internal/tick"
)

// sleeping2sServer answers every request only after 2 real seconds, or when
// the request's own context is cancelled first. It is the fixture acceptance
// criterion 1 names: the worker calling it must never block the tick thread.
func sleeping2sServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
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

// TestSleepingProviderProducesZeroOverrunsAndOneDropPerAbstention is
// acceptance criterion 1: a fixture provider that sleeps 2s (real wall-clock
// time, unrelated to the engine's fake tick clock) must never cause a tick
// overrun, and each abstention episode logs exactly one entry line.
//
// The engine advances its FAKE clock, which delivers ticks with no real-time
// wait at all: the tick thread's only interaction with the provider is a
// non-blocking channel send, so however long the real HTTP call in the worker
// goroutine takes is invisible to the tick budget.
func TestSleepingProviderProducesZeroOverrunsAndOneDropPerAbstention(t *testing.T) {
	// srv is deliberately never Close()d: httptest.Server.Close() blocks
	// until every outstanding handler returns, and a client-side context
	// timeout does not reliably force-close the underlying POST connection
	// promptly (a well-known net/http rough edge for a request whose body has
	// been sent but whose handler is blocked with nothing left to read) — so
	// Close() would sit for the fixture's full 2s despite every abstention
	// this test cares about landing in tens of milliseconds. The listener is
	// reclaimed when the test binary exits.
	srv := sleeping2sServer(t)

	voc := testVocabulary(t)
	clock := tick.NewFakeClock(testPeriod)
	logBuf := &bytes.Buffer{}
	logger := senselog.New(logBuf)

	sink := adaptor.NewRecordingSink()
	engine, err := tick.New(voc, tick.Config{Period: testPeriod, Clock: clock, Ticker: clock, Log: logger}, sink)
	if err != nil {
		t.Fatalf("tick.New: %v", err)
	}

	// A short client timeout, so the worker's HTTP call fails long before the
	// fixture's 2s sleep ever answers — this is the "timeout" abstention path,
	// and QueueDepth 1 makes a second concurrent tick's enqueue a
	// "queue-full" abstention while the first is still in flight.
	p, err := provider.New(provider.Config{
		Name: "verdict", Kind: provider.KindCompletion, BaseURL: srv.URL,
		Output: "verdict", Inputs: []string{"question"},
		Timeout: 30 * time.Millisecond, QueueDepth: 1,
	}, sense.New(), logger, &http.Client{Timeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}

	if !engine.Send(tick.SetSeamCmd{Seam: p.Driver()}) {
		t.Fatal("the engine refused the seam command")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.Run(ctx) }()

	// 100 fake ticks with nothing consuming real wall-clock time on the tick
	// thread: each one either enqueues (succeeding) or abstains, but the
	// engine's own budget accounting never sees the fixture's 2s sleep.
	clock.Advance(100 * testPeriod)

	// Give the worker's in-flight call(s) real time to actually time out
	// against the sleeping fixture, so this test exercises the "timeout"
	// abstention path the fixture exists for, not just "queue-full".
	deadline := time.Now().Add(2 * time.Second)
	for p.Stats().Drops["timeout"] == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if p.Stats().Drops["timeout"] == 0 {
		t.Fatal("expected at least one \"timeout\" drop against the sleeping fixture")
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

	if got := engine.Stats().Overruns; got != 0 {
		t.Fatalf("Stats().Overruns = %d, want 0 — the sleeping fixture must never reach the tick thread", got)
	}
	if engine.Stats().Ticks != 100 {
		t.Fatalf("Stats().Ticks = %d, want 100", engine.Stats().Ticks)
	}

	// Stop the worker before reading logBuf: it is a plain bytes.Buffer, not
	// safe for a concurrent reader while the worker goroutine might still be
	// mid-write to it.
	p.Close()

	// Every abstention is logged per EPISODE, not per tick: a 100-tick run
	// gated the whole way through on one continuous reason must not produce
	// anywhere near 100 lines.
	dropLines := 0
	for _, raw := range strings.Split(strings.TrimRight(logBuf.String(), "\n"), "\n") {
		if raw == "" {
			continue
		}
		line, err := senselog.Parse(raw)
		if err != nil {
			t.Fatalf("unparseable SENSE line %q: %v", raw, err)
		}
		if line.Dropped {
			dropLines++
		}
	}
	if dropLines == 0 {
		t.Fatal("expected at least one named drop line for the provider's abstentions")
	}
	if dropLines > 10 {
		t.Fatalf("got %d drop lines over 100 ticks — episodes should collapse to very few, not one per tick", dropLines)
	}
}
