package adaptor_test

import (
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
)

const (
	reachyFixture    = "reachy_vocabulary.json"
	microduckFixture = "microduck_vocabulary.json"
)

func load(t *testing.T, fixture string) *adaptor.Vocabulary {
	t.Helper()
	path := filepath.Join("testdata", fixture)
	v, err := adaptor.LoadJSON(path)
	if err != nil {
		t.Fatalf("LoadJSON(%s): %v", path, err)
	}
	return v
}

func TestLoadJSONAcceptsBothDonorShapes(t *testing.T) {
	reachy := load(t, reachyFixture)
	if got := len(reachy.Channels()); got != 3 {
		t.Errorf("reachy channels = %d, want 3", got)
	}
	if !reachy.HasField("rms_ratio") || !reachy.HasAction("orient-to-sound") {
		t.Error("reachy vocabulary is missing a name its own rules use")
	}

	duck := load(t, microduckFixture)
	if got := len(duck.Channels()); got != 6 {
		t.Errorf("microduck channels = %d, want 6", got)
	}
	if !duck.HasField("tof_nearest_m") || !duck.HasAction("stop") {
		t.Error("microduck vocabulary is missing one of its own names")
	}

	// The two robots share no channel vocabulary beyond "head": the point of
	// declaring it is that the engine can hold both without knowing either.
	if duck.HasField("rms_ratio") {
		t.Error("microduck vocabulary must not know a reachy sense field")
	}
	if reachy.HasAction("stop") {
		t.Error("reachy vocabulary must not know a microduck action")
	}
}

func TestOriginIsTheConfigPath(t *testing.T) {
	v := load(t, reachyFixture)
	want := filepath.Join("testdata", reachyFixture)
	if v.Origin() != want {
		t.Errorf("Origin() = %q, want %q", v.Origin(), want)
	}
}

// The names reachy's SHIPPED rules reference (reachy/behavior/default_rules.toml:
// fields pat / rms_ratio / transcript, actions pet-reaction / orient-to-sound /
// speak). The reachy-shaped vocabulary accepts them; the microduck-shaped one
// refuses them, naming the first undeclared field and its own origin.
var (
	shippedRuleFields  = []string{"pat", "rms_ratio", "transcript"}
	shippedRuleActions = []string{"pet-reaction", "orient-to-sound", "speak"}
)

func TestCheckReferencesAcceptsTheDeclaringVocabulary(t *testing.T) {
	v := load(t, reachyFixture)
	if err := v.CheckReferences(shippedRuleFields, shippedRuleActions); err != nil {
		t.Fatalf("reachy rules refused by the reachy vocabulary: %v", err)
	}
}

func TestCheckReferencesRefusesForeignNames(t *testing.T) {
	v := load(t, microduckFixture)
	err := v.CheckReferences(shippedRuleFields, shippedRuleActions)
	if err == nil {
		t.Fatal("microduck vocabulary accepted reachy's rule names")
	}
	msg := err.Error()
	// The FIRST undeclared field, not a later one and not an action.
	if !strings.Contains(msg, "pat") {
		t.Errorf("error does not name the first undeclared field: %q", msg)
	}
	if strings.Contains(msg, "rms_ratio") || strings.Contains(msg, "pet-reaction") {
		t.Errorf("error should stop at the first undeclared name: %q", msg)
	}
	if !strings.Contains(msg, filepath.Join("testdata", microduckFixture)) {
		t.Errorf("error does not name the vocabulary origin: %q", msg)
	}
	assertErrorShape(t, err)
}

func TestCheckReferencesRefusesAnUndeclaredAction(t *testing.T) {
	v := load(t, reachyFixture)
	err := v.CheckReferences([]string{"pat"}, []string{"speak", "wag-tail"})
	if err == nil {
		t.Fatal("an undeclared action was accepted")
	}
	if !strings.Contains(err.Error(), "wag-tail") {
		t.Errorf("error does not name the undeclared action: %q", err)
	}
	assertErrorShape(t, err)
}

func TestHasFieldAndHasActionAreExact(t *testing.T) {
	v := load(t, reachyFixture)
	for _, name := range []string{"", "rms_rati", "RMS_RATIO", "pat_state"} {
		if v.HasField(name) {
			t.Errorf("HasField(%q) = true, want false", name)
		}
	}
	for _, name := range []string{"", "speaking", "Speak"} {
		if v.HasAction(name) {
			t.Errorf("HasAction(%q) = true, want false", name)
		}
	}
}

func TestActionLoops(t *testing.T) {
	v := load(t, reachyFixture)
	if !v.ActionLoops("feel-alive") {
		t.Error("feel-alive should loop")
	}
	if v.ActionLoops("pet-reaction") {
		t.Error("pet-reaction is a one-shot")
	}
	if v.ActionLoops("no-such-action") {
		t.Error("an unknown action must not report looping")
	}
}

func TestActionParam(t *testing.T) {
	v := load(t, reachyFixture)
	min, max, ok := v.ActionParam("nod", "amp")
	if !ok {
		t.Fatal("nod.amp should be declared")
	}
	if min != 0.0 || max != 45.0 {
		t.Errorf("nod.amp domain = [%v, %v], want [0, 45]", min, max)
	}
	if _, _, ok := v.ActionParam("nod", "wobble"); ok {
		t.Error("an undeclared param reported ok")
	}
	if _, _, ok := v.ActionParam("no-such-action", "amp"); ok {
		t.Error("a param on an unknown action reported ok")
	}
}

func TestValidateParams(t *testing.T) {
	v := load(t, reachyFixture)

	if err := v.ValidateParams("nod", map[string]float64{"amp": 12.0, "period": 0.8}); err != nil {
		t.Fatalf("in-domain params refused: %v", err)
	}
	if err := v.ValidateParams("nod", nil); err != nil {
		t.Fatalf("omitting every param should be fine: %v", err)
	}
	// Boundaries are inclusive.
	if err := v.ValidateParams("nod", map[string]float64{"amp": 45.0}); err != nil {
		t.Errorf("the domain's upper bound was refused: %v", err)
	}

	over := v.ValidateParams("nod", map[string]float64{"amp": 60.0})
	if over == nil {
		t.Fatal("an out-of-range value was accepted")
	}
	if !strings.Contains(over.Error(), "60") || !strings.Contains(over.Error(), "amp") {
		t.Errorf("error should name the param and the value: %q", over)
	}
	assertErrorShape(t, over)

	unknown := v.ValidateParams("nod", map[string]float64{"wobble": 1.0})
	if unknown == nil {
		t.Fatal("an unknown param key was accepted")
	}
	if !strings.Contains(unknown.Error(), "wobble") {
		t.Errorf("error should name the unknown key: %q", unknown)
	}

	if err := v.ValidateParams("no-such-action", nil); err == nil {
		t.Error("params for an unknown action were accepted")
	}
	if err := v.ValidateParams("nod", map[string]float64{"amp": math.NaN()}); err == nil {
		t.Error("a NaN param value was accepted")
	}
}

// Refused, never clamped: the value the caller passed is still theirs after a
// rejection, and nothing in the vocabulary was mutated to make it fit.
func TestValidateParamsNeverClamps(t *testing.T) {
	v := load(t, reachyFixture)
	params := map[string]float64{"amp": 60.0}
	if err := v.ValidateParams("nod", params); err == nil {
		t.Fatal("expected a refusal")
	}
	if params["amp"] != 60.0 {
		t.Errorf("ValidateParams mutated the caller's params: %v", params)
	}
	if _, max, _ := v.ActionParam("nod", "amp"); max != 45.0 {
		t.Errorf("ValidateParams widened the declared domain to %v", max)
	}
}

func TestNeutralIsACompletePose(t *testing.T) {
	for _, fixture := range []string{reachyFixture, microduckFixture} {
		v := load(t, fixture)
		pose := v.Neutral()
		if len(pose) != len(v.Channels()) {
			t.Errorf("%s: neutral pose has %d channels, want %d",
				fixture, len(pose), len(v.Channels()))
		}
		for _, ch := range v.Channels() {
			values, ok := pose[ch.Name]
			if !ok {
				t.Errorf("%s: neutral pose is missing channel %q", fixture, ch.Name)
				continue
			}
			if len(values) != ch.Arity {
				t.Errorf("%s: neutral %q arity = %d, want %d",
					fixture, ch.Name, len(values), ch.Arity)
			}
		}
	}
}

func TestNeutralReturnsAFreshPoseEachCall(t *testing.T) {
	v := load(t, reachyFixture)
	first := v.Neutral()
	for name := range first {
		first[name][0] = 99.0
	}
	second := v.Neutral()
	for name, values := range second {
		if values[0] != 0.0 {
			t.Fatalf("mutating a returned pose leaked into the vocabulary (%s)", name)
		}
	}
}

func TestSensesCarryTypesAndAgeFields(t *testing.T) {
	v := load(t, reachyFixture)
	byName := map[string]adaptor.Sense{}
	for _, s := range v.Senses() {
		byName[s.Name] = s
	}
	if got := byName["doa"].Type; got != adaptor.SenseFloat {
		t.Errorf("doa type = %q, want float", got)
	}
	if got := byName["doa"].AgeField; got != "doa_age_s" {
		t.Errorf("doa age field = %q, want doa_age_s", got)
	}
	if got := byName["pat"].Type; got != adaptor.SenseBool {
		t.Errorf("pat type = %q, want bool", got)
	}
	if got := byName["transcript"].Type; got != adaptor.SenseString {
		t.Errorf("transcript type = %q, want string", got)
	}
	duck := load(t, microduckFixture)
	for _, s := range duck.Senses() {
		if s.Name == "gravity" && s.Type != adaptor.SenseVec3 {
			t.Errorf("gravity type = %q, want vec3", s.Type)
		}
	}
}

func TestActionsExposeClaimsAndTrajectories(t *testing.T) {
	v := load(t, reachyFixture)
	actions := v.Actions()
	var feelAlive *adaptor.Action
	for i := range actions {
		if actions[i].Name == "feel-alive" {
			feelAlive = &actions[i]
		}
	}
	if feelAlive == nil {
		t.Fatal("feel-alive is missing from Actions()")
	}
	if len(feelAlive.Claims) != 3 {
		t.Errorf("feel-alive claims %d channels, want 3", len(feelAlive.Claims))
	}
	for _, channel := range feelAlive.Claims {
		traj, ok := feelAlive.Trajectories[channel]
		if !ok || traj == nil {
			t.Errorf("feel-alive has no trajectory for claimed channel %q", channel)
		}
	}
}

// ---------------------------------------------------------------------------
// Load-time refusals. Every one names the offending action and channel, and
// every message follows the `adaptor: <origin>: <what> — <fix>` shape.
// ---------------------------------------------------------------------------

func TestParseRefusals(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		wants []string
	}{
		{
			name:  "unparseable JSON",
			body:  `{`,
			wants: []string{"adaptor:"},
		},
		{
			name:  "no channels",
			body:  `{"channels": [], "senses": [], "actions": []}`,
			wants: []string{"channel"},
		},
		{
			name: "duplicate channel",
			body: `{"channels": [
				{"name": "a", "arity": 1, "neutral": [0]},
				{"name": "a", "arity": 1, "neutral": [0]}], "actions": []}`,
			wants: []string{"a"},
		},
		{
			name:  "neutral of the wrong arity",
			body:  `{"channels": [{"name": "a", "arity": 2, "neutral": [0]}], "actions": []}`,
			wants: []string{"a", "neutral"},
		},
		{
			name:  "arity below one",
			body:  `{"channels": [{"name": "a", "arity": 0, "neutral": []}], "actions": []}`,
			wants: []string{"arity"},
		},
		{
			name: "unknown sense type",
			body: `{"channels": [{"name": "a", "arity": 1, "neutral": [0]}],
				"senses": [{"name": "s", "type": "quaternion"}], "actions": []}`,
			wants: []string{"s", "quaternion"},
		},
		{
			name: "age field naming an undeclared sense",
			body: `{"channels": [{"name": "a", "arity": 1, "neutral": [0]}],
				"senses": [{"name": "s", "type": "float", "age_field": "s_age_s"}],
				"actions": []}`,
			wants: []string{"s_age_s"},
		},
		{
			name: "action claiming an undeclared channel",
			body: `{"channels": [{"name": "a", "arity": 1, "neutral": [0]}],
				"actions": [{"name": "go", "claims": ["b"],
					"trajectories": {"b": {"easing": {"kind": "linear",
						"from": [0], "to": [1], "duration_s": 1}}}}]}`,
			wants: []string{"go", "b"},
		},
		{
			name: "action missing a trajectory for a claimed channel",
			body: `{"channels": [{"name": "a", "arity": 1, "neutral": [0]}],
				"actions": [{"name": "go", "claims": ["a"], "trajectories": {}}]}`,
			wants: []string{"go", "a"},
		},
		{
			name: "trajectory for an unclaimed channel",
			body: `{"channels": [{"name": "a", "arity": 1, "neutral": [0]},
					{"name": "b", "arity": 1, "neutral": [0]}],
				"actions": [{"name": "go", "claims": ["a"], "trajectories": {
					"a": {"easing": {"kind": "linear", "from": [0], "to": [1],
						"duration_s": 1}},
					"b": {"easing": {"kind": "linear", "from": [0], "to": [1],
						"duration_s": 1}}}}]}`,
			wants: []string{"go", "b"},
		},
		{
			name: "keyframe value of the wrong arity",
			body: `{"channels": [{"name": "a", "arity": 2, "neutral": [0, 0]}],
				"actions": [{"name": "go", "claims": ["a"], "trajectories": {
					"a": {"keyframes": [{"t": 0, "value": [0, 0]},
						{"t": 1, "value": [1]}]}}}]}`,
			wants: []string{"go", "a"},
		},
		{
			name: "keyframes not starting at zero",
			body: `{"channels": [{"name": "a", "arity": 1, "neutral": [0]}],
				"actions": [{"name": "go", "claims": ["a"], "trajectories": {
					"a": {"keyframes": [{"t": 0.5, "value": [0]},
						{"t": 1, "value": [1]}]}}}]}`,
			wants: []string{"go", "a"},
		},
		{
			name: "keyframe times going backwards",
			body: `{"channels": [{"name": "a", "arity": 1, "neutral": [0]}],
				"actions": [{"name": "go", "claims": ["a"], "trajectories": {
					"a": {"keyframes": [{"t": 0, "value": [0]}, {"t": 1, "value": [1]},
						{"t": 0.5, "value": [2]}]}}}]}`,
			wants: []string{"go", "a"},
		},
		{
			name: "both keyframes and an easing",
			body: `{"channels": [{"name": "a", "arity": 1, "neutral": [0]}],
				"actions": [{"name": "go", "claims": ["a"], "trajectories": {
					"a": {"keyframes": [{"t": 0, "value": [0]}],
						"easing": {"kind": "linear", "from": [0], "to": [1],
							"duration_s": 1}}}}]}`,
			wants: []string{"go", "a"},
		},
		{
			name: "neither keyframes nor an easing",
			body: `{"channels": [{"name": "a", "arity": 1, "neutral": [0]}],
				"actions": [{"name": "go", "claims": ["a"],
					"trajectories": {"a": {}}}]}`,
			wants: []string{"go", "a"},
		},
		{
			name: "unknown easing kind",
			body: `{"channels": [{"name": "a", "arity": 1, "neutral": [0]}],
				"actions": [{"name": "go", "claims": ["a"], "trajectories": {
					"a": {"easing": {"kind": "bounce", "from": [0], "to": [1],
						"duration_s": 1}}}}]}`,
			wants: []string{"go", "a", "bounce"},
		},
		{
			name: "easing endpoint of the wrong arity",
			body: `{"channels": [{"name": "a", "arity": 2, "neutral": [0, 0]}],
				"actions": [{"name": "go", "claims": ["a"], "trajectories": {
					"a": {"easing": {"kind": "linear", "from": [0], "to": [1, 1],
						"duration_s": 1}}}}]}`,
			wants: []string{"go", "a"},
		},
		{
			name: "easing with a non-positive duration",
			body: `{"channels": [{"name": "a", "arity": 1, "neutral": [0]}],
				"actions": [{"name": "go", "claims": ["a"], "trajectories": {
					"a": {"easing": {"kind": "linear", "from": [0], "to": [1],
						"duration_s": 0}}}}]}`,
			wants: []string{"go", "a"},
		},
		{
			name: "action claiming nothing",
			body: `{"channels": [{"name": "a", "arity": 1, "neutral": [0]}],
				"actions": [{"name": "go", "claims": [], "trajectories": {}}]}`,
			wants: []string{"go"},
		},
		{
			name: "param domain inverted",
			body: `{"channels": [{"name": "a", "arity": 1, "neutral": [0]}],
				"actions": [{"name": "go", "claims": ["a"],
					"params": [{"name": "amp", "min": 5, "max": 1}],
					"trajectories": {"a": {"easing": {"kind": "linear",
						"from": [0], "to": [1], "duration_s": 1}}}}]}`,
			wants: []string{"go", "amp"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := adaptor.Parse([]byte(c.body))
			if err == nil {
				t.Fatal("an invalid adaptor config was accepted")
			}
			assertErrorShape(t, err)
			for _, want := range c.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestLoadJSONMissingFile(t *testing.T) {
	_, err := adaptor.LoadJSON(filepath.Join("testdata", "nope.json"))
	if err == nil {
		t.Fatal("a missing config file was accepted")
	}
	assertErrorShape(t, err)
	if !strings.Contains(err.Error(), "nope.json") {
		t.Errorf("error does not name the path: %q", err)
	}
}

// Every error is `adaptor: <origin>: <what> — <fix>`: prefixed, attributed, and
// carrying a remediation after an em dash, so a bad config never leaves an
// operator guessing which file to edit.
func assertErrorShape(t *testing.T, err error) {
	t.Helper()
	msg := err.Error()
	if !strings.HasPrefix(msg, "adaptor: ") {
		t.Errorf("error is not prefixed with %q: %q", "adaptor: ", msg)
	}
	if !strings.Contains(msg, " — ") {
		t.Errorf("error carries no remediation after an em dash: %q", msg)
	}
	if strings.Count(msg, ": ") < 2 {
		t.Errorf("error does not name its origin: %q", msg)
	}
}
