// Package provider is an OpenAI-compatible decision provider seam driver: a
// small-model or embeddings client, warmed at startup, called from a worker
// behind a bounded queue with a timeout, whose OUTPUT is written back into the
// engine's sense.Snapshot so a rule can predicate on it exactly like any other
// sense field.
//
// # The rules layer never learns about providers
//
// A Provider declares OUTPUT sense fields (the adaptor config must separately
// declare them as senses so a rule may predicate on them — that is
// configuration, not code; this package does not touch internal/adaptor). Each
// tick the driver (Driver, a tick.TickSeam) reads the snapshot view, builds a
// request from a declared INPUT set of fields, and enqueues it on a bounded
// channel (default depth 2, mirroring the donor's depth-2 speech queue) —
// never blocking the tick. A worker goroutine, one per provider, performs the
// HTTP call with a timeout and, on success, writes the outputs into the
// sense.SenseSink stamped with the worker's own clock reading. The rule that
// predicates on the output field therefore fires on a LATER tick than the one
// that triggered the request — never the same tick, because the write races
// the tick loop and can only land after it.
//
// # Two provider kinds
//
// Both speak the OpenAI-compatible JSON shapes reachy-mini-cli's
// reachy/stash/embeddings.py uses:
//
//   - KindEmbedding — POST {base_url}/v1/embeddings {"model","input"} ->
//     {"data":[{"embedding":[...]}]}. The output is the best-matching label
//     (by cosine similarity against reference embeddings fetched once at
//     warm-up) written as "<output>" (string) plus "<output>_score" (float).
//   - KindCompletion — POST {base_url}/v1/chat/completions with a system
//     prompt and the input fields rendered into the user message. The output
//     is the first word of the reply, lowercased, written as "<output>"
//     (string) plus "<output>_latency_s" (float).
//
// # Absence is always an abstention
//
// No provider configured, a full queue, an HTTP timeout, an HTTP error status,
// a malformed response, or a warm-up failure are ALL abstentions: nothing is
// written to the sink, and a rule bound to the output field simply never sees
// it turn true. Every abstention is one named senselog drop
// ("provider", "<name>", "<output-field>", "<reason>") using senselog.Streak,
// so a persistent failure logs once per episode rather than once per tick.
// Reasons: "queue-full", "timeout", "http-<status>", "malformed",
// "unconfigured".
//
// # The tick thread never constructs a client or performs I/O
//
// New resolves the API key, builds the *http.Client and — for KindEmbedding —
// fetches every label's reference embedding, ALL before the first tick. A
// warm-up failure marks the provider unconfigured (one named drop) rather than
// failing New: the engine still starts with the rule abstaining forever. The
// tick goroutine only ever does a non-blocking channel send; the worker
// goroutine is the only place an HTTP request is ever made.
package provider
