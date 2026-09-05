package tick

import (
	"reflect"
	"testing"
)

// The cases in this file are the donor's, ported from
// reachy-mini-cli/reachy/behavior/arbitration.py: its module docstring states
// the four-class rules and its two functions' docstrings state the tie-break,
// the abstention rule and the totality of admission. Each test names the rule
// it pins.

// "The owner of a channel is the candidate claiming it with the highest class
// priority" — and the classes rank passive < stoppable < stopping <
// unstoppable.
func TestArbitrateRanksTheFourClasses(t *testing.T) {
	channels := toyChannels()
	cases := []struct {
		name      string
		behaviors []Behavior
		want      string
	}{
		{
			name: "stoppable outranks passive",
			behaviors: []Behavior{
				claimant("p", ClassPassive, "ch_a"),
				claimant("s", ClassStoppable, "ch_a"),
			},
			want: "s",
		},
		{
			name: "stopping outranks stoppable, whatever the order",
			behaviors: []Behavior{
				claimant("x", ClassStopping, "ch_a"),
				claimant("s", ClassStoppable, "ch_a"),
			},
			want: "x",
		},
		{
			name: "unstoppable outranks stopping",
			behaviors: []Behavior{
				claimant("u", ClassUnstoppable, "ch_a"),
				claimant("x", ClassStopping, "ch_a"),
			},
			want: "u",
		},
		{
			name: "passive loses to everything that claims the channel",
			behaviors: []Behavior{
				claimant("s", ClassStoppable, "ch_a"),
				claimant("p", ClassPassive, "ch_a"),
			},
			want: "s",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owners := Arbitrate(channels, tc.behaviors, nil)
			if owners["ch_a"] != tc.want {
				t.Fatalf("owner of ch_a = %q, want %q", owners["ch_a"], tc.want)
			}
		})
	}
}

// "ties broken by most-recently-admitted": behaviors are oldest-first, so the
// later entry of an equal-priority pair wins.
func TestArbitrateBreaksTiesByRecency(t *testing.T) {
	owners := Arbitrate(toyChannels(), []Behavior{
		claimant("older", ClassStoppable, "ch_a"),
		claimant("newer", ClassStoppable, "ch_a"),
	}, nil)
	if owners["ch_a"] != "newer" {
		t.Fatalf("owner of ch_a = %q, want the most recently admitted %q",
			owners["ch_a"], "newer")
	}
}

// "A passive behavior (priority 0) therefore wins a channel only when nothing
// non-passive claims it" — per channel, not per behavior.
func TestPassiveOwnsOnlyWhatNothingElseClaims(t *testing.T) {
	owners := Arbitrate(toyChannels(), []Behavior{
		claimant("base", ClassPassive, "ch_a", "ch_b"),
		claimant("driver", ClassStoppable, "ch_a"),
	}, nil)
	if owners["ch_a"] != "driver" {
		t.Fatalf("owner of ch_a = %q, want driver", owners["ch_a"])
	}
	if owners["ch_b"] != "base" {
		t.Fatalf("owner of ch_b = %q, want the passive base", owners["ch_b"])
	}
}

// "A channel no behavior claims maps to None" — here, Unowned, and the key is
// still present so composition can fill it with the declared neutral.
func TestArbitrateLeavesUnclaimedChannelsUnowned(t *testing.T) {
	owners := Arbitrate(toyChannels(), []Behavior{
		claimant("driver", ClassStoppable, "ch_a"),
	}, nil)
	if _, present := owners["ch_c"]; !present {
		t.Fatal("ch_c is missing from the ownership map; every declared channel must appear")
	}
	if owners["ch_c"] != Unowned {
		t.Fatalf("owner of ch_c = %q, want Unowned", owners["ch_c"])
	}
}

// "a claimant whose contribution leaves a channel None is skipped for that
// channel, so it falls through to the next-priority claimant".
func TestArbitrateIsAbstentionAware(t *testing.T) {
	behaviors := []Behavior{
		claimant("base", ClassPassive, "ch_a"),
		claimant("reactive", ClassStoppable, "ch_a"),
	}
	contribs := map[string]Contribution{
		"base":     contribution("ch_a"),
		"reactive": {}, // claims ch_a, but has nothing to say this tick
	}
	owners := Arbitrate(toyChannels(), behaviors, contribs)
	if owners["ch_a"] != "base" {
		t.Fatalf("owner of ch_a = %q, want the base layer: an abstaining claimant must "+
			"yield the channel, not freeze it", owners["ch_a"])
	}

	// The same set with a live contribution puts the higher-priority claimant
	// back in charge, so the fall-through is the abstention and nothing else.
	contribs["reactive"] = contribution("ch_a")
	owners = Arbitrate(toyChannels(), behaviors, contribs)
	if owners["ch_a"] != "reactive" {
		t.Fatalf("owner of ch_a = %q, want reactive", owners["ch_a"])
	}
}

// "a candidate with no contribution this tick (missing id ...) abstains".
func TestArbitrateTreatsAMissingContributionAsAbstention(t *testing.T) {
	owners := Arbitrate(toyChannels(), []Behavior{
		claimant("base", ClassPassive, "ch_a"),
		claimant("ghost", ClassUnstoppable, "ch_a"),
	}, map[string]Contribution{"base": contribution("ch_a")})
	if owners["ch_a"] != "base" {
		t.Fatalf("owner of ch_a = %q, want base", owners["ch_a"])
	}
}

// "Without contribs ... selection is purely claim-based, as before."
func TestArbitrateWithoutContribsIsClaimBased(t *testing.T) {
	owners := Arbitrate(toyChannels(), []Behavior{
		claimant("base", ClassPassive, "ch_a"),
		claimant("silent", ClassUnstoppable, "ch_a"),
	}, nil)
	if owners["ch_a"] != "silent" {
		t.Fatalf("owner of ch_a = %q, want silent: without contributions nobody abstains",
			owners["ch_a"])
	}
}

// "A passive newcomer never removes anything and is expected to yield, so its
// blocked is left empty."
func TestAdmitPassiveNewcomerRemovesNothingAndIsNeverBlocked(t *testing.T) {
	incumbents := []Behavior{claimant("u", ClassUnstoppable, "ch_a")}
	got := Admit(toyChannels(), claimant("base", ClassPassive, "ch_a"), incumbents)
	if len(got.Evicted) != 0 {
		t.Fatalf("evicted = %v, want none", got.Evicted)
	}
	if len(got.Blocked) != 0 {
		t.Fatalf("blocked = %v, want none for a passive newcomer", got.Blocked)
	}
}

// "a stopping behavior removes the stoppable behaviors it shares a channel
// with; everything else removes nothing."
func TestAdmitStoppingEvictsSharedStoppables(t *testing.T) {
	incumbents := []Behavior{
		claimant("shared-stoppable", ClassStoppable, "ch_a"),
		claimant("elsewhere-stoppable", ClassStoppable, "ch_c"),
		claimant("shared-passive", ClassPassive, "ch_a"),
		claimant("shared-unstoppable", ClassUnstoppable, "ch_b"),
	}
	got := Admit(toyChannels(), claimant("halt", ClassStopping, "ch_a", "ch_b"), incumbents)
	if !reflect.DeepEqual(got.Evicted, []string{"shared-stoppable"}) {
		t.Fatalf("evicted = %v, want only the stoppable sharing a channel", got.Evicted)
	}
}

func TestAdmitNonStoppingClassesEvictNothing(t *testing.T) {
	incumbents := []Behavior{claimant("s", ClassStoppable, "ch_a")}
	for _, class := range []StopClass{ClassPassive, ClassStoppable, ClassUnstoppable} {
		got := Admit(toyChannels(), claimant("newcomer", class, "ch_a"), incumbents)
		if len(got.Evicted) != 0 {
			t.Fatalf("a %s newcomer evicted %v, want nothing", class, got.Evicted)
		}
	}
}

// "Admission is total — every behavior is accepted — so contention a newcomer
// cannot win by removal is simply resolved per tick (it waits ...)." The
// unstoppable incumbent keeps the channel and the newcomer is merely blocked.
func TestAdmissionIsTotalAgainstAnUnstoppableIncumbent(t *testing.T) {
	incumbents := []Behavior{claimant("u", ClassUnstoppable, "ch_a", "ch_b")}
	got := Admit(toyChannels(), claimant("newcomer", ClassStoppable, "ch_a", "ch_c"), incumbents)
	if len(got.Evicted) != 0 {
		t.Fatalf("evicted = %v, want nothing: an unstoppable is never removed by an add",
			got.Evicted)
	}
	if !reflect.DeepEqual(got.Blocked, []string{"ch_a"}) {
		t.Fatalf("blocked = %v, want [ch_a]: the newcomer waits for the channel it "+
			"cannot win and takes the one it can", got.Blocked)
	}

	// The newcomer is still admitted by the caller and owns what it can, which
	// is what "total" means: it is not refused, it waits.
	owners := Arbitrate(toyChannels(), append(incumbents,
		claimant("newcomer", ClassStoppable, "ch_a", "ch_c")), nil)
	if owners["ch_c"] != "newcomer" {
		t.Fatalf("owner of ch_c = %q, want newcomer", owners["ch_c"])
	}
	if owners["ch_a"] != "u" {
		t.Fatalf("owner of ch_a = %q, want the unstoppable incumbent", owners["ch_a"])
	}
}

// "new is newest -> wins same-priority ties": a stoppable newcomer takes a
// channel from an equally-ranked incumbent without evicting it.
func TestAdmitNewcomerWinsSamePriorityTies(t *testing.T) {
	incumbents := []Behavior{claimant("older", ClassStoppable, "ch_a")}
	got := Admit(toyChannels(), claimant("newer", ClassStoppable, "ch_a"), incumbents)
	if len(got.Evicted) != 0 {
		t.Fatalf("evicted = %v, want nothing", got.Evicted)
	}
	if len(got.Blocked) != 0 {
		t.Fatalf("blocked = %v, want nothing: the newcomer wins the tie by recency",
			got.Blocked)
	}
}

// "blocked is then computed against the prospective set", i.e. AFTER the
// stopping newcomer's evictions are applied.
func TestAdmitComputesBlockedAfterEviction(t *testing.T) {
	incumbents := []Behavior{
		claimant("s", ClassStoppable, "ch_a"),
		claimant("u", ClassUnstoppable, "ch_b"),
	}
	got := Admit(toyChannels(), claimant("halt", ClassStopping, "ch_a", "ch_b"), incumbents)
	if !reflect.DeepEqual(got.Evicted, []string{"s"}) {
		t.Fatalf("evicted = %v, want [s]", got.Evicted)
	}
	if !reflect.DeepEqual(got.Blocked, []string{"ch_b"}) {
		t.Fatalf("blocked = %v, want [ch_b]: ch_a is free once s is evicted, ch_b is "+
			"held by the unstoppable", got.Blocked)
	}
}

// "blocked = sorted(...)": the report is stable whatever order the channels
// were claimed in.
func TestAdmitBlockedIsSorted(t *testing.T) {
	incumbents := []Behavior{claimant("u", ClassUnstoppable, "ch_a", "ch_b", "ch_c")}
	got := Admit(toyChannels(), claimant("n", ClassStoppable, "ch_c", "ch_a", "ch_b"), incumbents)
	if !reflect.DeepEqual(got.Blocked, []string{"ch_a", "ch_b", "ch_c"}) {
		t.Fatalf("blocked = %v, want it sorted", got.Blocked)
	}
}

// A channel the newcomer claims that nothing resolves (because no declared
// channel matches) counts as blocked, matching the donor's `owners[channel] is
// None` branch.
func TestAdmitBlocksAnUndeclaredChannel(t *testing.T) {
	got := Admit(toyChannels(), claimant("n", ClassStoppable, "ch_zzz"), nil)
	if !reflect.DeepEqual(got.Blocked, []string{"ch_zzz"}) {
		t.Fatalf("blocked = %v, want [ch_zzz]", got.Blocked)
	}
}

func TestStopClassPriorityOrder(t *testing.T) {
	if !(ClassPassive.Priority() < ClassStoppable.Priority() &&
		ClassStoppable.Priority() < ClassStopping.Priority() &&
		ClassStopping.Priority() < ClassUnstoppable.Priority()) {
		t.Fatalf("class priorities are out of order: %d %d %d %d",
			ClassPassive.Priority(), ClassStoppable.Priority(),
			ClassStopping.Priority(), ClassUnstoppable.Priority())
	}
	if StopClass("nonsense").Valid() {
		t.Fatal("an unrecognized class reported itself valid")
	}
}
