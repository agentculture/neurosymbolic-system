package bench

import "github.com/agentculture/neurosymbolic-system/internal/adaptor"

// nullSink is the write half of the bench's engine: an O(1) discard, so the
// measured tick time is the engine's own work — evaluate, arbitrate, compose,
// write — and never a transport's. adaptor.RecordingSink would grow without
// bound across 10,000 ticks and confound the RSS reading this package exists
// to take.
type nullSink struct{}

// Write discards pose and never fails.
func (nullSink) Write(adaptor.Pose) error { return nil }
