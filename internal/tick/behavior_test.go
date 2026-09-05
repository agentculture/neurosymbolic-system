package tick

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestBindDefaultsChannelsAndAttachesTrajectories(t *testing.T) {
	v := toyVoc(t)
	b := behaviorFor(t, v, Behavior{
		Action:   "wide",
		Class:    ClassStoppable,
		Lifetime: Lifetime{DurationS: seconds(1)},
	})
	if !reflect.DeepEqual(b.Channels, []string{"ch_a", "ch_b", "ch_c"}) {
		t.Fatalf("channels = %v, want the action's claims", b.Channels)
	}
	if b.Name != "wide" {
		t.Fatalf("name = %q, want it defaulted to the action name", b.Name)
	}
	for _, channel := range b.Channels {
		if b.Trajectories[channel] == nil {
			t.Fatalf("no trajectory attached for channel %q", channel)
		}
	}
}

func TestBindRefusesWhatTheVocabularyDoesNotDeclare(t *testing.T) {
	v := toyVoc(t)
	cases := []struct {
		name string
		b    Behavior
		want string
	}{
		{
			name: "undeclared action",
			b:    Behavior{Action: "no-such-action", Class: ClassStoppable, Lifetime: Lifetime{Loops: true}},
			want: "does not declare",
		},
		{
			name: "undeclared channel",
			b: Behavior{Action: "ramp", Class: ClassStoppable, Channels: []string{"ch_zzz"},
				Lifetime: Lifetime{Loops: true}},
			want: "does not declare",
		},
		{
			name: "unknown contention class",
			b:    Behavior{Action: "ramp", Class: "eager", Lifetime: Lifetime{Loops: true}},
			want: "contention class",
		},
		{
			name: "out-of-domain param",
			b: Behavior{Action: "ramp", Class: ClassStoppable, Lifetime: Lifetime{Loops: true},
				Params: map[string]float64{"gain": 4}},
			want: "outside the declared",
		},
		{
			name: "undeclared param",
			b: Behavior{Action: "ramp", Class: ClassStoppable, Lifetime: Lifetime{Loops: true},
				Params: map[string]float64{"nope": 0.5}},
			want: "does not declare",
		},
		{
			name: "one-shot with no duration",
			b:    Behavior{Action: "ramp", Class: ClassStoppable},
			want: "one-shot with no duration_s",
		},
		{
			name: "non-positive duration",
			b: Behavior{Action: "ramp", Class: ClassStoppable,
				Lifetime: Lifetime{DurationS: seconds(0)}},
			want: "must be > 0",
		},
		{
			name: "non-finite duration",
			b: Behavior{Action: "ramp", Class: ClassStoppable,
				Lifetime: Lifetime{DurationS: seconds(math.Inf(1))}},
			want: "must be finite",
		},
		{
			name: "duplicate channel claim",
			b: Behavior{Action: "ramp", Class: ClassStoppable, Lifetime: Lifetime{Loops: true},
				Channels: []string{"ch_a", "ch_a"}},
			want: "twice",
		},
		{
			name: "no action and no contribute",
			b:    Behavior{Name: "empty", Class: ClassStoppable, Lifetime: Lifetime{Loops: true}},
			want: "names no action",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Bind(v, tc.b)
			if err == nil {
				t.Fatalf("Bind accepted %+v, want a refusal", tc.b)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err.Error(), tc.want)
			}
		})
	}
}

// A one-shot whose action loops is refused rather than silently made to loop:
// a lifetime the engine reinterprets is a behavior nobody can predict. The
// refusal still names why, so the fix is obvious.
func TestBindRefusalNamesALoopingAction(t *testing.T) {
	_, err := Bind(toyVoc(t), Behavior{Action: "cycle", Class: ClassStoppable})
	if err == nil {
		t.Fatal("Bind accepted a one-shot with no duration")
	}
	if !strings.Contains(err.Error(), "its action declares that it loops") {
		t.Fatalf("error = %q, want it to name the action's own looping", err.Error())
	}
}

// A sensor-driven behavior carries its own contribution function and needs no
// action, but its channels are still checked.
func TestBindAcceptsAContributeOnlyBehavior(t *testing.T) {
	v := toyVoc(t)
	b := behaviorFor(t, v, Behavior{
		Name:       "reactive",
		Class:      ClassStoppable,
		Channels:   []string{"ch_b"},
		Lifetime:   Lifetime{Loops: true},
		Contribute: func(float64) Contribution { return Contribution{"ch_b": {2}} },
	})
	got := b.Contribution(0)
	if !reflect.DeepEqual(got["ch_b"], []float64{2}) {
		t.Fatalf("contribution = %v, want the function's value", got)
	}
}

// A pure behavior samples the vocabulary's trajectory, and the same local time
// always yields the same values — which is what makes a tick replayable.
func TestContributionSamplesTheDeclaredTrajectory(t *testing.T) {
	v := toyVoc(t)
	b := behaviorFor(t, v, Behavior{Action: "ramp", Class: ClassStoppable,
		Lifetime: Lifetime{DurationS: seconds(1)}})
	traj := trajectoryFor(t, v, "ramp", "ch_a")
	for _, tLocal := range []float64{0, 0.25, 0.5, 0.9, 1.5} {
		got := b.Contribution(tLocal)["ch_a"]
		want := traj.At(tLocal)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("at t=%v contribution = %v, want %v", tLocal, got, want)
		}
	}
}

// A claimed channel with no trajectory abstains rather than inventing a value.
func TestContributionAbstainsOnAChannelWithNoTrajectory(t *testing.T) {
	v := toyVoc(t)
	b := behaviorFor(t, v, Behavior{Action: "ramp", Class: ClassStoppable,
		Channels: []string{"ch_a", "ch_c"}, Lifetime: Lifetime{DurationS: seconds(1)}})
	got := b.Contribution(0.1)
	if _, present := got["ch_c"]; present {
		t.Fatalf("contribution = %v, want ch_c absent (an abstention)", got)
	}
	if _, present := got["ch_a"]; !present {
		t.Fatalf("contribution = %v, want ch_a driven", got)
	}
}

// A Contribute function that names a channel the behavior does not claim
// cannot smuggle it into the tick.
func TestContributionIgnoresUnclaimedChannels(t *testing.T) {
	b := Behavior{
		Name: "sneaky", Class: ClassStoppable, Channels: []string{"ch_a"},
		Contribute: func(float64) Contribution {
			return Contribution{"ch_a": {1, 1}, "ch_c": {9, 9, 9}}
		},
	}
	got := b.Contribution(0)
	if _, present := got["ch_c"]; present {
		t.Fatalf("contribution = %v, want the unclaimed ch_c dropped", got)
	}
}

func TestLifetimeExpiry(t *testing.T) {
	oneShot := Lifetime{DurationS: seconds(2)}
	if oneShot.IsExpired(1.999) {
		t.Fatal("a one-shot expired before its duration")
	}
	if !oneShot.IsExpired(2) {
		t.Fatal("a one-shot did not expire at its duration")
	}
	forever := Lifetime{Loops: true}
	if forever.IsExpired(1e9) {
		t.Fatal("a looping-forever lifetime expired")
	}
	bounded := Lifetime{Loops: true, DurationS: seconds(3)}
	if !bounded.IsExpired(3) {
		t.Fatal("a bounded looping lifetime did not expire")
	}
}
