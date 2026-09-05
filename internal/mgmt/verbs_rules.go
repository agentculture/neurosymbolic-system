package mgmt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/agentculture/neurosymbolic-system/internal/adaptor"
	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
	"github.com/agentculture/neurosymbolic-system/internal/rules"
)

// loadVocabulary loads an adaptor config by extension: .json through
// adaptor.LoadJSON, .toml through adaptor.LoadTOML, anything else refused
// naming both. An empty path means "no vocabulary" (nil), which is what lets
// rules check/list validate a file's shape with no robot attached.
func loadVocabulary(path string) (rules.Vocabulary, error) {
	if path == "" {
		return nil, nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return adaptor.LoadJSON(path)
	case ".toml":
		return adaptor.LoadTOML(path)
	default:
		return nil, clifmt.NewUserError(
			fmt.Sprintf("--adaptor %q has an unrecognized extension", path),
			"pass a .json or .toml adaptor config",
		)
	}
}

// rulesError converts a *rules.Error into the CliError shape: its What half
// becomes the message (with the file/rule-id prefix rules.Error.Error()
// itself renders), its Fix half becomes the remediation. Splitting rather
// than reusing Error() verbatim is what lets "error:"/"hint:" stay on
// separate lines instead of one "what — fix" sentence.
func rulesError(err error) *clifmt.CliError {
	var rerr *rules.Error
	if errors.As(err, &rerr) {
		msg := "rules: " + rerr.Path + ": "
		if rerr.RuleID != "" {
			msg += fmt.Sprintf("rule '%s': ", rerr.RuleID)
		}
		msg += rerr.What
		return clifmt.NewUserError(msg, rerr.Fix)
	}
	var cliErr *clifmt.CliError
	if errors.As(err, &cliErr) {
		return cliErr
	}
	return clifmt.NewUserError(err.Error(), "")
}

// verbRulesCheck is `rules check <file>... [--adaptor <path>]`: load the
// given files as one layer, with no robot attached unless --adaptor is
// given, and report success as a one-line summary or failure as the
// loader's own refusal.
func verbRulesCheck(_ *Handler, args []string) (verbResult, error) {
	adaptorPath, _, files := extractStringFlag(args, "adaptor")
	if bad, found := rejectUnknownFlags(files); found {
		return verbResult{}, clifmt.NewUserError(
			fmt.Sprintf("rules check does not recognize %q", bad),
			"run: rules check <file>... [--adaptor <path>]")
	}
	if len(files) == 0 {
		return verbResult{}, clifmt.NewUserError(
			"rules check needs at least one file", "run: rules check <file>... [--adaptor <path>]")
	}

	vocab, err := loadVocabulary(adaptorPath)
	if err != nil {
		return verbResult{}, asCliError(err, clifmt.ExitUser, "")
	}

	cfg, err := rules.Load([][]string{files}, vocab)
	if err != nil {
		return verbResult{}, rulesError(err)
	}

	summary := fmt.Sprintf("rules: %d react, %d inhibit, %d event entries, schema_version %d",
		len(cfg.React), len(cfg.Inhibit), len(cfg.Events), cfg.SchemaVersion)
	return verbResult{
		Text: summary,
		Value: map[string]any{
			"react":          len(cfg.React),
			"inhibit":        len(cfg.Inhibit),
			"event_entries":  len(cfg.Events),
			"schema_version": cfg.SchemaVersion,
		},
	}, nil
}

// predicateSummary renders a Predicate compactly for `rules list`: a leaf as
// "field op value", a group as "all(...)"/"any(...)" of its children's own
// summaries.
func predicateSummary(p rules.Predicate) string {
	if p.IsLeaf() {
		if p.Value == nil {
			return fmt.Sprintf("%s %s", p.Field, p.Op)
		}
		return fmt.Sprintf("%s %s %v", p.Field, p.Op, p.Value)
	}
	kind, children := "all", p.All
	if len(p.Any) > 0 {
		kind, children = "any", p.Any
	}
	parts := make([]string, len(children))
	for i, child := range children {
		parts[i] = predicateSummary(child)
	}
	return fmt.Sprintf("%s(%s)", kind, strings.Join(parts, ", "))
}

type ruleListing struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Predicate string `json:"predicate,omitempty"`
	Priority  string `json:"priority,omitempty"`
	Urgency   string `json:"urgency,omitempty"`
}

// verbRulesList is `rules list <file>... [--adaptor <path>]`: print each
// rule's id, kind and predicate summary, react rules first then inhibit,
// then every [[event]] entry as `event <source>/<type> priority=<p>
// urgency=<u>` — in the merged file order rules.Load produces.
func verbRulesList(_ *Handler, args []string) (verbResult, error) {
	adaptorPath, _, files := extractStringFlag(args, "adaptor")
	if bad, found := rejectUnknownFlags(files); found {
		return verbResult{}, clifmt.NewUserError(
			fmt.Sprintf("rules list does not recognize %q", bad),
			"run: rules list <file>... [--adaptor <path>]")
	}
	if len(files) == 0 {
		return verbResult{}, clifmt.NewUserError(
			"rules list needs at least one file", "run: rules list <file>... [--adaptor <path>]")
	}

	vocab, err := loadVocabulary(adaptorPath)
	if err != nil {
		return verbResult{}, asCliError(err, clifmt.ExitUser, "")
	}

	cfg, err := rules.Load([][]string{files}, vocab)
	if err != nil {
		return verbResult{}, rulesError(err)
	}

	var listings []ruleListing
	var lines []string
	for _, r := range append(append([]rules.Rule{}, cfg.React...), cfg.Inhibit...) {
		listings = append(listings, ruleListing{ID: r.ID, Kind: r.Kind, Predicate: predicateSummary(r.When)})
		lines = append(lines, fmt.Sprintf("%s %s %s", r.ID, r.Kind, predicateSummary(r.When)))
	}
	for _, e := range cfg.Events {
		listings = append(listings, ruleListing{
			ID: e.ID(), Kind: "event", Priority: e.Priority, Urgency: e.Urgency,
		})
		lines = append(lines, fmt.Sprintf("event %s priority=%s urgency=%s", e.ID(), e.Priority, e.Urgency))
	}
	text := "(no rules)"
	if len(lines) > 0 {
		text = strings.Join(lines, "\n")
	}
	return verbResult{Text: text, Value: listings}, nil
}

// verbRulesMigrate is `rules migrate <file> [--out <path>] [--force]`: write
// a schema_version-2 twin of <file> with every rule unchanged, leaving the
// input untouched.
func verbRulesMigrate(_ *Handler, args []string) (verbResult, error) {
	outPath, _, rest := extractStringFlag(args, "out")
	force, rest := extractBoolFlag(rest, "force")
	if bad, found := rejectUnknownFlags(rest); found {
		return verbResult{}, clifmt.NewUserError(
			fmt.Sprintf("rules migrate does not recognize %q", bad),
			"run: rules migrate <file> [--out <path>] [--force]")
	}
	if len(rest) != 1 {
		return verbResult{}, clifmt.NewUserError(
			fmt.Sprintf("rules migrate takes exactly one input file, got %d", len(rest)),
			"run: rules migrate <file> [--out <path>] [--force]")
	}
	inPath := rest[0]

	dest := outPath
	if dest == "" {
		dest = defaultMigrateOutPath(inPath)
	}

	inAbs, err := filepath.Abs(inPath)
	if err != nil {
		return verbResult{}, clifmt.NewUserError(fmt.Sprintf("could not resolve %q: %v", inPath, err), "")
	}
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return verbResult{}, clifmt.NewUserError(fmt.Sprintf("could not resolve %q: %v", dest, err), "")
	}
	if inAbs == destAbs {
		return verbResult{}, clifmt.NewUserError(
			fmt.Sprintf("the output path %q is the input file itself", dest),
			"pass a different --out, or accept the default <name>.v2.toml")
	}
	if _, statErr := os.Stat(dest); statErr == nil && !force {
		return verbResult{}, clifmt.NewUserError(
			fmt.Sprintf("%q already exists", dest), "pass --force to overwrite it")
	}

	before, err := rules.LoadFile(inPath, nil)
	if err != nil {
		return verbResult{}, rulesError(err)
	}

	raw, err := os.ReadFile(inPath) // #nosec G304 -- an operator-supplied rules path is the input
	if err != nil {
		return verbResult{}, clifmt.NewEnvError(fmt.Sprintf("could not read %q: %v", inPath, err), "")
	}
	migrated, rewrote := rewriteSchemaVersion(string(raw))
	if !rewrote && before.SchemaVersion != rules.SchemaVersion2 {
		return verbResult{}, clifmt.NewEnvError(
			fmt.Sprintf("could not find a schema_version = 1 line to rewrite in %q", inPath),
			"the file may already declare schema_version = 2, or use unusual formatting",
		)
	}

	// The migrated bytes are validated in a TEMP file beside dest and only
	// then renamed over it. dest is never opened for writing and never
	// removed: with --force it may be a file an operator already has, and
	// neither a partial write nor a failed validation may cost them it.
	verify := func(tmpPath string) error {
		after, loadErr := rules.LoadFile(tmpPath, nil)
		if loadErr != nil {
			return loadErr
		}
		if !sameRules(before, after) {
			return errDifferentRuleSet
		}
		return nil
	}
	if err := replaceFileAtomically(dest, []byte(migrated), verify); err != nil {
		var verifyErr *verificationError
		if errors.As(err, &verifyErr) {
			return verbResult{}, clifmt.NewEnvError(
				fmt.Sprintf("migrating %q produced a different rule set: %s", inPath, verifyErr.cause),
				"this is a bug in migrate's schema_version rewrite, not a mistake in "+
					"the input; the previous "+dest+" is untouched",
			)
		}
		return verbResult{}, clifmt.NewEnvError(fmt.Sprintf("could not write %q: %v", dest, err), "")
	}

	text := fmt.Sprintf("rules: migrated %s -> %s (schema_version %d)", inPath, dest, rules.SchemaVersion2)
	return verbResult{
		Text:  text,
		Value: map[string]any{"in": inPath, "out": dest, "schema_version": rules.SchemaVersion2},
	}, nil
}

func defaultMigrateOutPath(inPath string) string {
	ext := filepath.Ext(inPath)
	base := strings.TrimSuffix(inPath, ext)
	return base + ".v2" + ext
}

// sameRules reports whether before and after are the same configuration in
// everything but the schema version — migrate's whole contract, that bumping
// schema_version changes nothing else.
//
// It compares the WHOLE loaded Config, not a hand-picked list of fields. The
// list version compared ids, kinds, run names and predicates, and therefore
// could not see a difference in say text, params, cooldowns, modes, the
// active mode or the event table — which is exactly how a rewrite that
// corrupted a multi-line `say` body passed verification and was written to
// disk. A field this function forgets is a corruption it cannot catch, so it
// forgets nothing by construction: any field added to rules.Config is
// compared the day it exists.
func sameRules(before, after *rules.Config) bool {
	return reflect.DeepEqual(comparableConfig(before), comparableConfig(after))
}

// comparableConfig is one Config reduced to the parts migrate must preserve
// exactly. Three things are cleared, and only three:
//
//   - SchemaVersion, the one value migrate exists to change;
//   - each Rule's Source and each Event's Path, which are the file each was
//     read from — necessarily the input for one side and migrate's temporary
//     candidate for the other, so comparing them would fail every time.
func comparableConfig(cfg *rules.Config) rules.Config {
	out := *cfg
	out.SchemaVersion = 0
	out.React = withoutSources(cfg.React)
	out.Inhibit = withoutSources(cfg.Inhibit)
	out.Events = withoutPaths(cfg.Events)
	return out
}

func withoutSources(in []rules.Rule) []rules.Rule {
	if in == nil {
		return nil
	}
	out := make([]rules.Rule, len(in))
	for i, rule := range in {
		rule.Source = ""
		out[i] = rule
	}
	return out
}

func withoutPaths(in []rules.Event) []rules.Event {
	if in == nil {
		return nil
	}
	out := make([]rules.Event, len(in))
	for i, event := range in {
		event.Path = ""
		out[i] = event
	}
	return out
}

// verbRulesReload is `rules reload <file>...`: ask the live engine's
// installed Reloader to re-read and validate the named files. With none
// installed (always true for the one-off exec front — there is no live
// engine beside it) it reports ExitEnv naming the stream endpoint instead.
func verbRulesReload(h *Handler, args []string) (verbResult, error) {
	if h.Reloader == nil {
		return verbResult{}, noLiveEngine()
	}
	if len(args) == 0 {
		return verbResult{}, clifmt.NewUserError(
			"rules reload needs at least one file", "run: rules reload <file>...")
	}
	if err := h.Reloader.Reload(args); err != nil {
		return verbResult{}, clifmt.NewUserError(
			fmt.Sprintf("reload refused: %v", err),
			"the previous rule set is still active; fix the new files and retry",
		)
	}
	text := fmt.Sprintf("rules: reloaded %d file(s)", len(args))
	return verbResult{Text: text, Value: map[string]any{"reloaded": args}}, nil
}
