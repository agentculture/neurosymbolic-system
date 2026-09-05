package adaptor

// Sink is the transport seam: where a composed pose goes.
//
// It is deliberately the narrowest interface in the package. The library never
// knows what a pose MEANS on a given plant — millimetres or metres, degrees or
// radians, a servo bus or a simulator — so a Sink receives what it is given
// UNCHANGED and the consumer's implementation does the conversion at its own
// boundary, exactly once. Nothing here clamps, rescales, or drops a channel; a
// library that quietly reinterpreted a target would make two robots impossible
// to reason about at the same time.
//
// A Sink is held open for the whole loop by the composition root, never opened
// per tick: at 50 Hz the budget is 20 ms, and constructing a client on the tick
// thread is the single most expensive mistake the donor made.
type Sink interface {
	Write(pose Pose) error
}

// RecordingSink is the test double for Sink: it keeps every pose it is handed,
// unchanged and in order, so a test can assert on what the engine actually
// streamed rather than on what it meant to.
//
// It lives in the non-test build on purpose — the consumers of this library
// (two robot CLIs and their own test suites) need the same double, and a helper
// that only exists inside this package's _test.go files cannot be imported.
//
// It is not safe for concurrent use; the tick thread is a single writer by
// construction.
type RecordingSink struct {
	// Err, when non-nil, is returned by every Write. The pose is still
	// recorded: a failing sink that also swallowed the evidence would make a
	// wedged transport indistinguishable from an engine that never composed.
	Err error

	written []Pose
}

// NewRecordingSink returns an empty RecordingSink.
func NewRecordingSink() *RecordingSink {
	return &RecordingSink{}
}

// Write records the pose exactly as given — the same map, no copy, no
// normalisation — and returns Err.
func (r *RecordingSink) Write(pose Pose) error {
	r.written = append(r.written, pose)
	return r.Err
}

// Written returns the recorded poses in write order. The slice is a copy, so a
// caller cannot corrupt the log; the Pose values in it are the originals.
func (r *RecordingSink) Written() []Pose {
	out := make([]Pose, len(r.written))
	copy(out, r.written)
	return out
}

// Reset drops the recorded poses, keeping Err.
func (r *RecordingSink) Reset() {
	r.written = nil
}
