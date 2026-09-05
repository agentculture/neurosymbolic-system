package adaptor_test

import (
	"math"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
)

// oneAction builds a single-channel vocabulary around one trajectory, which is
// the only supported way to get a validated Trajectory: a trajectory that never
// passed load-time validation has no business being sampled.
func oneAction(t *testing.T, arity int, trajectoryJSON string, loops bool) *adaptor.Trajectory {
	t.Helper()
	neutral := "[0"
	for i := 1; i < arity; i++ {
		neutral += ", 0"
	}
	neutral += "]"
	loopsJSON := "false"
	if loops {
		loopsJSON = "true"
	}
	body := `{"channels": [{"name": "c", "arity": ` + itoa(arity) + `,
			"neutral": ` + neutral + `}],
		"actions": [{"name": "go", "claims": ["c"], "loops": ` + loopsJSON + `,
			"trajectories": {"c": ` + trajectoryJSON + `}}]}`
	v, err := adaptor.Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	traj := v.Actions()[0].Trajectories["c"]
	if traj == nil {
		t.Fatal("no trajectory was built")
	}
	return traj
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func assertAt(t *testing.T, traj *adaptor.Trajectory, tLocal float64, want []float64) {
	t.Helper()
	got := traj.At(tLocal)
	if len(got) != len(want) {
		t.Fatalf("At(%v) arity = %d, want %d", tLocal, len(got), len(want))
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Fatalf("At(%v) = %v, want %v", tLocal, got, want)
		}
	}
}

func TestKeyframeInterpolation(t *testing.T) {
	traj := oneAction(t, 2, `{"keyframes": [
		{"t": 0, "value": [0, 10]},
		{"t": 1, "value": [10, 0]},
		{"t": 3, "value": [30, 0]}]}`, false)

	assertAt(t, traj, 0, []float64{0, 10})
	assertAt(t, traj, 0.5, []float64{5, 5})
	assertAt(t, traj, 1, []float64{10, 0})
	assertAt(t, traj, 2, []float64{20, 0})
	assertAt(t, traj, 3, []float64{30, 0})
}

func TestTrajectoryHoldsPastTheEnd(t *testing.T) {
	traj := oneAction(t, 1, `{"keyframes": [
		{"t": 0, "value": [0]}, {"t": 1, "value": [7]}]}`, false)
	assertAt(t, traj, 1.0, []float64{7})
	assertAt(t, traj, 5.0, []float64{7})
	assertAt(t, traj, 1e6, []float64{7})
}

func TestTrajectoryClampsBeforeTheStart(t *testing.T) {
	traj := oneAction(t, 1, `{"keyframes": [
		{"t": 0, "value": [3]}, {"t": 1, "value": [7]}]}`, false)
	assertAt(t, traj, -1.0, []float64{3})
}

func TestLoopingActionWrapsByDuration(t *testing.T) {
	traj := oneAction(t, 1, `{"keyframes": [
		{"t": 0, "value": [0]}, {"t": 2, "value": [4]}]}`, true)
	assertAt(t, traj, 0.5, []float64{1})
	assertAt(t, traj, 2.5, []float64{1}) // one full cycle later
	assertAt(t, traj, 4.5, []float64{1})
	assertAt(t, traj, 2.0, []float64{0}) // exactly one period wraps to the start
}

func TestNonLoopingActionDoesNotWrap(t *testing.T) {
	traj := oneAction(t, 1, `{"keyframes": [
		{"t": 0, "value": [0]}, {"t": 2, "value": [4]}]}`, false)
	assertAt(t, traj, 2.5, []float64{4})
}

func TestSingleKeyframeIsAConstant(t *testing.T) {
	traj := oneAction(t, 1, `{"keyframes": [{"t": 0, "value": [2]}]}`, true)
	assertAt(t, traj, 0, []float64{2})
	assertAt(t, traj, 99, []float64{2})
	if traj.Duration() != 0 {
		t.Errorf("Duration() = %v, want 0", traj.Duration())
	}
}

func TestLinearEasing(t *testing.T) {
	traj := oneAction(t, 2, `{"easing": {"kind": "linear",
		"from": [0, 4], "to": [10, 0], "duration_s": 2}}`, false)
	assertAt(t, traj, 0, []float64{0, 4})
	assertAt(t, traj, 1, []float64{5, 2})
	assertAt(t, traj, 2, []float64{10, 0})
	assertAt(t, traj, 9, []float64{10, 0})
}

func TestEaseInOutEasing(t *testing.T) {
	traj := oneAction(t, 1, `{"easing": {"kind": "ease_in_out",
		"from": [0], "to": [10], "duration_s": 2}}`, false)
	assertAt(t, traj, 0, []float64{0})
	assertAt(t, traj, 1, []float64{5}) // symmetric: half way in time, half way in value
	assertAt(t, traj, 2, []float64{10})

	// Slower than linear at the start, faster in the middle.
	quarter := traj.At(0.5)[0]
	if quarter >= 2.5 {
		t.Errorf("ease_in_out at a quarter = %v, want < 2.5 (linear)", quarter)
	}
	if quarter <= 0 {
		t.Errorf("ease_in_out at a quarter = %v, want > 0", quarter)
	}
}

func TestHoldEasing(t *testing.T) {
	traj := oneAction(t, 1, `{"easing": {"kind": "hold",
		"from": [1], "to": [9], "duration_s": 2}}`, false)
	assertAt(t, traj, 0, []float64{1})
	assertAt(t, traj, 1.999, []float64{1})
	assertAt(t, traj, 2, []float64{9})
	assertAt(t, traj, 50, []float64{9})
}

func TestLoopingEasingWraps(t *testing.T) {
	traj := oneAction(t, 1, `{"easing": {"kind": "linear",
		"from": [0], "to": [10], "duration_s": 2}}`, true)
	assertAt(t, traj, 1, []float64{5})
	assertAt(t, traj, 3, []float64{5})
	assertAt(t, traj, 5, []float64{5})
}

// At is pure: sampling never mutates the trajectory, and the caller may keep or
// mutate the returned slice without corrupting the next tick's sample.
func TestAtIsPure(t *testing.T) {
	traj := oneAction(t, 2, `{"keyframes": [
		{"t": 0, "value": [1, 2]}, {"t": 1, "value": [3, 4]}]}`, false)
	first := traj.At(0)
	first[0] = 99
	assertAt(t, traj, 0, []float64{1, 2})

	for i := 0; i < 3; i++ {
		assertAt(t, traj, 0.5, []float64{2, 3})
	}
}

func TestAtRejectsNothingAndSurvivesNaN(t *testing.T) {
	traj := oneAction(t, 1, `{"easing": {"kind": "linear",
		"from": [0], "to": [10], "duration_s": 2}}`, false)
	// A NaN clock is a caller bug, but the tick core must not panic on it.
	got := traj.At(math.NaN())
	if len(got) != 1 {
		t.Fatalf("At(NaN) arity = %d, want 1", len(got))
	}
}

func TestDurationReportsTheTrajectoryLength(t *testing.T) {
	kf := oneAction(t, 1, `{"keyframes": [
		{"t": 0, "value": [0]}, {"t": 2.5, "value": [1]}]}`, false)
	if kf.Duration() != 2.5 {
		t.Errorf("keyframe Duration() = %v, want 2.5", kf.Duration())
	}
	easing := oneAction(t, 1, `{"easing": {"kind": "hold",
		"from": [0], "to": [1], "duration_s": 0.75}}`, false)
	if easing.Duration() != 0.75 {
		t.Errorf("easing Duration() = %v, want 0.75", easing.Duration())
	}
}
