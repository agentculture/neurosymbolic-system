package bench

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
)

// This file generates a synthetic vocabulary and a synthetic v2 rules file for
// the load Run drives. Every name it mints is synthetic — f0..fN for sense
// fields, ch0..ch3 for channels, act0..act7 for actions — so
// TestNoDonorLiteralsInEngineSources never has reason to flag this package: a
// benchmark load has no business knowing either donor robot's vocabulary.

// numChannels and numActions are fixed by the spec (4 channels, 8 actions);
// only --rules and --fields scale.
const (
	numChannels = 4
	numActions  = 8
	channelDOF  = 3
)

func channelName(i int) string { return fmt.Sprintf("ch%d", i) }
func actionName(i int) string  { return fmt.Sprintf("act%d", i) }
func fieldName(i int) string   { return fmt.Sprintf("f%d", i) }

// fieldIsBool decides a field's declared type: roughly a third bool, the rest
// float — "mixed float/bool" per the design, not an even split.
func fieldIsBool(i int) bool { return i%3 == 0 }

// vocabDoc mirrors adaptor's own (unexported) on-disk config shape: the same
// three fields, the same json tags. Building it from adaptor's own exported
// types means this generator can never drift from what LoadJSON actually
// accepts.
type vocabDoc struct {
	Channels []adaptor.Channel `json:"channels"`
	Senses   []adaptor.Sense   `json:"senses"`
	Actions  []adaptor.Action  `json:"actions"`
}

// generateVocabulary builds the synthetic adaptor config as JSON bytes:
// numChannels channels, cfg.Fields senses of mixed float/bool, numActions
// actions each claiming one channel round-robin with a short (0.3s) linear
// easing trajectory. No action loops, so no rule needs a duration_s bound.
func generateVocabulary(cfg Config) ([]byte, error) {
	doc := vocabDoc{
		Channels: make([]adaptor.Channel, numChannels),
		Senses:   make([]adaptor.Sense, cfg.Fields),
		Actions:  make([]adaptor.Action, numActions),
	}
	for i := 0; i < numChannels; i++ {
		doc.Channels[i] = adaptor.Channel{
			Name: channelName(i), Arity: channelDOF, Neutral: make([]float64, channelDOF),
		}
	}
	for i := 0; i < cfg.Fields; i++ {
		typ := adaptor.SenseFloat
		if fieldIsBool(i) {
			typ = adaptor.SenseBool
		}
		doc.Senses[i] = adaptor.Sense{Name: fieldName(i), Type: typ}
	}
	for i := 0; i < numActions; i++ {
		channel := channelName(i % numChannels)
		to := make([]float64, channelDOF)
		for j := range to {
			to[j] = 1.0 + float64(j)
		}
		doc.Actions[i] = adaptor.Action{
			Name:   actionName(i),
			Claims: []string{channel},
			Loops:  false,
			Trajectories: map[string]*adaptor.Trajectory{
				channel: {Easing: &adaptor.Easing{
					Kind: adaptor.EasingLinear, From: make([]float64, channelDOF), To: to, DurationS: 0.3,
				}},
			},
		}
	}
	return json.Marshal(doc)
}

// ruleGen holds the fixed inputs a rules-file generation pass needs beside the
// rule index, so the leaf/group helpers stay pure functions of their
// arguments rather than reaching for package state.
type ruleGen struct {
	fields int
	rng    *rand.Rand
}

// generateRulesTOML builds a synthetic schema_version = 2 rules file over
// cfg.Rules entries: leaf and all/any group predicates in rotation, varying
// cooldowns, and one [[inhibit]] rule every 20 entries in place of a react
// rule — exactly the mix the design calls for.
func generateRulesTOML(cfg Config, rng *rand.Rand) string {
	g := ruleGen{fields: cfg.Fields, rng: rng}
	var b strings.Builder
	b.WriteString("schema_version = 2\n\n")
	for i := 0; i < cfg.Rules; i++ {
		if (i+1)%20 == 0 {
			g.writeInhibitRule(&b, i)
			continue
		}
		g.writeReactRule(&b, i)
	}
	return b.String()
}

func (g ruleGen) writeReactRule(b *strings.Builder, i int) {
	fmt.Fprintf(b, "[[react]]\n")
	fmt.Fprintf(b, "id = %q\n", fmt.Sprintf("r%d", i))
	fmt.Fprintf(b, "run = %q\n", actionName(i%numActions))
	fmt.Fprintf(b, "when = %s\n", g.predicateTOML(i))
	fmt.Fprintf(b, "cooldown_s = %.2f\n\n", cooldownFor(i))
}

func (g ruleGen) writeInhibitRule(b *strings.Builder, i int) {
	fmt.Fprintf(b, "[[inhibit]]\n")
	fmt.Fprintf(b, "id = %q\n", fmt.Sprintf("r%d", i))
	fmt.Fprintf(b, "disable = [%q]\n", actionName((i/20)%numActions))
	fmt.Fprintf(b, "when = %s\n", g.predicateTOML(i))
	fmt.Fprintf(b, "cooldown_s = %.2f\n\n", cooldownFor(i))
}

// cooldownFor spreads cooldowns across [0.2s, 5.0s) deterministically by rule
// index, so two runs of the same --rules generate the identical file.
func cooldownFor(i int) float64 {
	return 0.2 + float64(i%25)*0.2
}

// predicateTOML rotates leaf / all-group / any-group in a fixed 3-cycle by
// rule index, referencing g.fields-many synthetic fields.
func (g ruleGen) predicateTOML(i int) string {
	switch i % 3 {
	case 0:
		return g.leafTOML(g.fieldFor(i, 0))
	case 1:
		return fmt.Sprintf("{ all = [ %s, %s ] }", g.leafTOML(g.fieldFor(i, 0)), g.leafTOML(g.fieldFor(i, 1)))
	default:
		return fmt.Sprintf("{ any = [ %s, %s ] }", g.leafTOML(g.fieldFor(i, 0)), g.leafTOML(g.fieldFor(i, 1)))
	}
}

// fieldFor picks a deterministic field index for rule i's nth predicate leaf,
// spread across however many fields the vocabulary declares.
func (g ruleGen) fieldFor(i, n int) int {
	if g.fields < 1 {
		return 0
	}
	return (i*7 + n*13) % g.fields
}

func (g ruleGen) leafTOML(fieldIdx int) string {
	name := fieldName(fieldIdx)
	if fieldIsBool(fieldIdx) {
		return fmt.Sprintf("{ field = %q, op = %q }", name, "is_true")
	}
	value := g.rng.Float64() * 10.0
	return fmt.Sprintf("{ field = %q, op = %q, value = %.4f }", name, "gt", value)
}
