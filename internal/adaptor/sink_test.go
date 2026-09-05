package adaptor_test

import (
	"errors"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
)

func TestRecordingSinkReceivesWhatItIsGivenUnchanged(t *testing.T) {
	var sink adaptor.Sink = adaptor.NewRecordingSink()
	rec := sink.(*adaptor.RecordingSink)

	v := load(t, reachyFixture)
	pose := v.Neutral()
	pose["antennas"] = []float64{12.5, -12.5}

	if err := sink.Write(pose); err != nil {
		t.Fatalf("Write: %v", err)
	}
	written := rec.Written()
	if len(written) != 1 {
		t.Fatalf("recorded %d poses, want 1", len(written))
	}
	got := written[0]
	if len(got) != len(pose) {
		t.Fatalf("recorded pose has %d channels, want %d", len(got), len(pose))
	}
	for name, want := range pose {
		values, ok := got[name]
		if !ok {
			t.Errorf("recorded pose is missing channel %q", name)
			continue
		}
		if len(values) != len(want) {
			t.Errorf("channel %q arity = %d, want %d", name, len(values), len(want))
			continue
		}
		for i := range want {
			if values[i] != want[i] {
				t.Errorf("channel %q[%d] = %v, want %v", name, i, values[i], want[i])
			}
		}
	}
}

func TestRecordingSinkRecordsEveryWriteInOrder(t *testing.T) {
	rec := adaptor.NewRecordingSink()
	for i := 0; i < 3; i++ {
		if err := rec.Write(adaptor.Pose{"c": {float64(i)}}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	written := rec.Written()
	if len(written) != 3 {
		t.Fatalf("recorded %d poses, want 3", len(written))
	}
	for i, pose := range written {
		if pose["c"][0] != float64(i) {
			t.Errorf("pose %d = %v, want %v", i, pose["c"][0], float64(i))
		}
	}
}

func TestRecordingSinkCanFail(t *testing.T) {
	boom := errors.New("sink is wedged")
	rec := adaptor.NewRecordingSink()
	rec.Err = boom
	err := rec.Write(adaptor.Pose{"c": {1}})
	if !errors.Is(err, boom) {
		t.Fatalf("Write error = %v, want %v", err, boom)
	}
	// A failed write is still observed: a sink that drops silently is worse
	// than one that says no.
	if len(rec.Written()) != 1 {
		t.Errorf("a failed write was not recorded")
	}
}

func TestWrittenReturnsACopyOfTheLog(t *testing.T) {
	rec := adaptor.NewRecordingSink()
	if err := rec.Write(adaptor.Pose{"c": {1}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	written := rec.Written()
	written[0] = nil
	if rec.Written()[0] == nil {
		t.Error("mutating the returned log corrupted the sink")
	}
}
