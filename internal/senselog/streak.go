package senselog

import (
	"strconv"
	"strings"
)

// Streak is a per-key (source, event) gated-episode tracker mirroring the
// donor's #99 fix: a gated streak logs ONE entry line naming its reason, one
// more line only when the reason CHANGES mid-streak, and ONE summary line
// when the streak ends — never one line per tick. A streak still open when
// the process stops emits no summary; its entry (and any reason-change)
// line is the record.
type Streak struct {
	logger *Logger

	stage  string
	source string
	event  string

	open    bool
	reason  string
	reasons []string
	ticks   int
}

// NewStreak returns a Streak bound to this Logger and the given
// stage/source/event key.
func (l *Logger) NewStreak(stage, source, event string) *Streak {
	return &Streak{logger: l, stage: stage, source: source, event: event}
}

// Enter records one gated tick under reason. It logs a "dropped
// reason=<reason>" line only when the streak opens (this is the first Enter
// since the last End/Fire) or when reason differs from the streak's current
// reason. Every call — logged or not — counts one tick toward the eventual
// summary.
func (s *Streak) Enter(tick int, reason string) {
	_ = tick // tick length is tracked as the count of Enter calls, not this argument

	s.ticks++
	if !s.open {
		s.open = true
		s.reason = reason
		s.reasons = append(s.reasons, reason)
		s.logger.Drop(s.stage, s.source, s.event, reason, "")
		return
	}
	if reason == s.reason {
		return
	}
	s.reason = reason
	if !contains(s.reasons, reason) {
		s.reasons = append(s.reasons, reason)
	}
	s.logger.Drop(s.stage, s.source, s.event, reason, "")
}

// End closes an open streak with ONE summary line naming every reason seen
// (first-seen order) and the streak length, then resets. A no-op when no
// streak is open.
func (s *Streak) End(tick int) {
	_ = tick // no timestamps/tick numbers appear in the SENSE line by design

	if !s.open {
		return
	}
	summary := strings.Join(s.reasons, ",")
	s.logger.Drop(s.stage, s.source, s.event, summary, suppressedDetail(s.ticks))
	s.reset()
}

// Fire ends any open streak (the summary line is emitted BEFORE the fire
// line) and then logs the fire itself via Stage.
func (s *Streak) Fire(tick int, detail string) {
	s.End(tick)
	s.logger.Stage(s.stage, s.source, s.event, detail)
}

func (s *Streak) reset() {
	s.open = false
	s.reason = ""
	s.reasons = nil
	s.ticks = 0
}

func contains(items []string, item string) bool {
	for _, v := range items {
		if v == item {
			return true
		}
	}
	return false
}

func suppressedDetail(ticks int) string {
	return "suppressed " + strconv.Itoa(ticks) + " ticks"
}
