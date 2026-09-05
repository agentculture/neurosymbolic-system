package tick

import (
	"bytes"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/senselog"
)

// Acceptance criterion 1, at the composition level: every emitted pose fills
// every declared channel, and an unclaimed channel is the adaptor's declared
// neutral.
func TestComposeFillsEveryChannelWithTheDeclaredNeutral(t *testing.T) {
	v := toyVoc(t)
	pose := Compose(v, map[string]string{"ch_a": "driver", "ch_b": Unowned, "ch_c": Unowned},
		map[string]Contribution{"driver": {"ch_a": {3, 4}}}, nil)

	for _, ch := range v.Channels() {
		values, present := pose[ch.Name]
		if !present {
			t.Fatalf("channel %q is missing from the composed pose", ch.Name)
		}
		if len(values) != ch.Arity {
			t.Fatalf("channel %q carries %d values, want its arity %d",
				ch.Name, len(values), ch.Arity)
		}
	}
	if !reflect.DeepEqual(pose["ch_a"], []float64{3, 4}) {
		t.Fatalf("ch_a = %v, want the owner's values", pose["ch_a"])
	}
	if !reflect.DeepEqual(pose["ch_b"], []float64{5}) {
		t.Fatalf("ch_b = %v, want the declared neutral [5]", pose["ch_b"])
	}
	if !reflect.DeepEqual(pose["ch_c"], []float64{1, 2, 3}) {
		t.Fatalf("ch_c = %v, want the declared neutral [1 2 3]", pose["ch_c"])
	}
}

// An owner that abstained on its channel leaves it at neutral rather than at a
// stale value: arbitration already fell the channel through, and composition
// must not resurrect it.
func TestComposeLeavesAnAbstainedChannelNeutral(t *testing.T) {
	v := toyVoc(t)
	pose := Compose(v, map[string]string{"ch_a": "driver"},
		map[string]Contribution{"driver": {}}, nil)
	if !reflect.DeepEqual(pose["ch_a"], []float64{0, 0}) {
		t.Fatalf("ch_a = %v, want the declared neutral", pose["ch_a"])
	}
}

// A value of the wrong width is REFUSED, never reshaped, and the refusal names
// itself on one grep-able line.
func TestComposeDropsAWrongWidthValueAndNamesIt(t *testing.T) {
	v := toyVoc(t)
	buf := &bytes.Buffer{}
	var drops atomic.Uint64
	log := &dropLog{logger: testLogger(buf), drops: &drops}

	pose := Compose(v, map[string]string{"ch_c": "driver"},
		map[string]Contribution{"driver": {"ch_c": {1, 2}}}, log)

	if !reflect.DeepEqual(pose["ch_c"], []float64{1, 2, 3}) {
		t.Fatalf("ch_c = %v, want the declared neutral after a refused value", pose["ch_c"])
	}
	if drops.Load() != 1 {
		t.Fatalf("drops = %d, want 1", drops.Load())
	}
	line, err := senselog.Parse(strings.TrimRight(buf.String(), "\n"))
	if err != nil {
		t.Fatalf("the drop line is not SENSE grammar: %v (%q)", err, buf.String())
	}
	if !line.Dropped || line.Reason != "arity" {
		t.Fatalf("parsed drop = %+v, want a drop naming reason=arity", line)
	}
}

// The composed pose owns its values: a caller mutating what it handed in (or
// what it got back) cannot reach into the engine's next tick.
func TestComposeCopiesTheOwnersValues(t *testing.T) {
	v := toyVoc(t)
	source := []float64{3, 4}
	pose := Compose(v, map[string]string{"ch_a": "driver"},
		map[string]Contribution{"driver": {"ch_a": source}}, nil)
	source[0] = 99
	if pose["ch_a"][0] != 3 {
		t.Fatalf("ch_a = %v, want the pose to hold its own copy", pose["ch_a"])
	}
}
