package tick

import "github.com/agentculture/neurosymbolic-system/internal/adaptor"

// Unowned is the owner id of a channel no behavior owns this tick. Composition
// resolves such a channel to the adaptor's declared neutral, so a pose is
// COMPLETE either way.
const Unowned = ""

// Arbitrate assigns each declared channel a single owner, and is one of the two
// pure functions of the contention core: no I/O, no clock, no state.
//
// behaviors is oldest-first (admission order), so "later in the slice" means
// "more recently admitted". The owner of a channel is the claimant with the
// highest class priority, ties broken by most-recently-admitted. A channel no
// behavior claims maps to Unowned, and a passive behavior therefore wins a
// channel only when nothing non-passive claims it.
//
// With contribs (this tick's {behavior id: Contribution}) the result is
// ABSTENTION-AWARE: a claimant with no contribution this tick — a missing id,
// or a channel it left out — is skipped for that channel, so the channel falls
// through to the next-priority claimant instead of being frozen. Pass nil
// (what Admit does) for purely claim-based selection.
func Arbitrate(channels []string, behaviors []Behavior, contribs map[string]Contribution) map[string]string {
	owners := make(map[string]string, len(channels))
	for _, channel := range channels {
		owners[channel] = Unowned
		bestIndex := -1
		for i := range behaviors {
			b := behaviors[i]
			if !b.Claims(channel) {
				continue
			}
			if contribs != nil {
				contribution, ok := contribs[b.ID]
				if !ok {
					continue
				}
				if values, has := contribution[channel]; !has || values == nil {
					continue
				}
			}
			// Strictly-greater keeps the FIRST of an equal-priority run from
			// winning; ">=" hands the tie to the later (more recent) entry,
			// which is the donor's max-by-(priority, index).
			if bestIndex < 0 || b.Class.Priority() >= behaviors[bestIndex].Class.Priority() {
				bestIndex = i
			}
		}
		if bestIndex >= 0 {
			owners[channel] = behaviors[bestIndex].ID
		}
	}
	return owners
}

// AdmitResult is the outcome of admitting a behavior.
//
// Evicted holds the ids of the stoppable behaviors a stopping admission
// removed. Blocked names the newcomer's channels it will NOT own yet because a
// higher-priority incumbent holds them — informational, not a failure: the
// newcomer stays active and takes the channel once the incumbent ends.
type AdmitResult struct {
	Evicted []string
	Blocked []string
}

// Admit decides what admitting newBehavior removes, and which of its channels
// it must wait for. It is the second pure function of the contention core.
//
// behaviors is the current live set, oldest-first. A passive newcomer never
// removes anything and is expected to yield, so its Blocked is left empty. Any
// other newcomer that is stopping removes the stoppable behaviors it shares a
// channel with; Blocked is then computed against the prospective set with the
// newcomer as the most recent entry.
//
// Admission is TOTAL — every behavior is accepted. Contention a newcomer cannot
// win by removal is simply resolved per tick: it waits, yielding the channel to
// a higher-priority incumbent until that incumbent ends. (MicroDuck's engine
// instead REFUSES a newcomer that shares a channel with an unstoppable or
// stopping incumbent. Total admission is the shape ported here because it keeps
// admission decoupled from tick-time contention; a consumer that wants a
// refusal can read Blocked and evict the newcomer itself.)
func Admit(channels []string, newBehavior Behavior, behaviors []Behavior) AdmitResult {
	if newBehavior.Class == ClassPassive {
		return AdmitResult{}
	}

	var evicted []string
	if newBehavior.Class == ClassStopping {
		for _, b := range behaviors {
			if b.Class != ClassStoppable {
				continue
			}
			if sharesChannel(b, newBehavior) {
				evicted = append(evicted, b.ID)
			}
		}
	}

	evictedIDs := make(map[string]bool, len(evicted))
	for _, id := range evicted {
		evictedIDs[id] = true
	}
	prospective := make([]Behavior, 0, len(behaviors)+1)
	for _, b := range behaviors {
		if !evictedIDs[b.ID] {
			prospective = append(prospective, b)
		}
	}
	// The newcomer goes last so it wins same-priority ties, matching the order
	// the engine will hold the live set in once it appends.
	prospective = append(prospective, newBehavior)

	owners := Arbitrate(channels, prospective, nil)
	var blocked []string
	for _, channel := range newBehavior.Channels {
		if owners[channel] != newBehavior.ID {
			blocked = append(blocked, channel)
		}
	}
	return AdmitResult{Evicted: evicted, Blocked: sortedStrings(blocked)}
}

func sharesChannel(a, b Behavior) bool {
	for _, channel := range a.Channels {
		if b.Claims(channel) {
			return true
		}
	}
	return false
}

// Compose assembles one COMPLETE pose from this tick's ownership and
// contributions.
//
// It starts from the vocabulary's neutral, so a channel nothing owns resolves
// to a real value rather than being left out of the target: a partial pose is
// not a pose, and a transport handed one would have to invent the rest.
//
// A contribution whose arity disagrees with the channel's declaration is
// DROPPED with a named senselog line and the channel keeps its neutral. That is
// the fail-closed reading: the engine will not reshape a value it cannot
// interpret, and a drop nobody can see is indistinguishable from a silent
// no-op. log may be nil.
func Compose(
	v *adaptor.Vocabulary,
	ownership map[string]string,
	contribs map[string]Contribution,
	log *dropLog,
) adaptor.Pose {
	pose := v.Neutral()
	for _, ch := range v.Channels() {
		owner := ownership[ch.Name]
		if owner == Unowned {
			continue
		}
		values, ok := contribs[owner][ch.Name]
		if !ok || values == nil {
			continue
		}
		if len(values) != ch.Arity {
			log.drop("compose", ch.Name, "arity",
				"the owning behavior returned a value of the wrong width")
			continue
		}
		pose[ch.Name] = cloneValues(values)
	}
	return pose
}
