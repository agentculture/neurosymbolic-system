package mgmt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

	// Event-keyed entries (internal/events, t6) do not exist in this
	// checkout yet; the count is reported as 0 until that dialect lands
	// beside react/inhibit in the same Config, so the summary line's shape
	// never has to change when it does.
	const eventEntries = 0
	summary := fmt.Sprintf("rules: %d react, %d inhibit, %d event entries, schema_version %d",
		len(cfg.React), len(cfg.Inhibit), eventEntries, cfg.SchemaVersion)
	return verbResult{
		Text: summary,
		Value: map[string]any{
			"react":          len(cfg.React),
			"inhibit":        len(cfg.Inhibit),
			"event_entries":  eventEntries,
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
	Predicate string `json:"predicate"`
}

// verbRulesList is `rules list <file>... [--adaptor <path>]`: print each
// rule's id, kind and predicate summary, react rules first then inhibit, in
// the merged file order rules.Load produces.
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
	text := "(no rules)"
	if len(lines) > 0 {
		text = strings.Join(lines, "\n")
	}
	return verbResult{Text: text, Value: listings}, nil
}

// schemaVersionLine matches the top-level `schema_version = 1` assignment (a
// leading `schema_version`, an `=`, the literal 1, and nothing else but
// whitespace or a trailing comment on the line). Migrate rewrites only this
// one captured group so every comment, blank line and rule body in the file
// survives byte-for-byte.
var schemaVersionLine = regexp.MustCompile(`(?m)^(\s*schema_version\s*=\s*)1(\s*(#.*)?)$`)

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
	migrated := schemaVersionLine.ReplaceAllString(string(raw), "${1}2$2")
	if migrated == string(raw) && before.SchemaVersion != rules.SchemaVersion2 {
		return verbResult{}, clifmt.NewEnvError(
			fmt.Sprintf("could not find a schema_version = 1 line to rewrite in %q", inPath),
			"the file may already declare schema_version = 2, or use unusual formatting",
		)
	}

	if err := os.WriteFile(dest, []byte(migrated), 0o600); err != nil {
		return verbResult{}, clifmt.NewEnvError(fmt.Sprintf("could not write %q: %v", dest, err), "")
	}

	after, err := rules.LoadFile(dest, nil)
	if err != nil || !sameRules(before, after) {
		_ = os.Remove(dest)
		detail := "the migrated file does not load"
		if err != nil {
			detail = err.Error()
		}
		return verbResult{}, clifmt.NewEnvError(
			fmt.Sprintf("migrating %q produced a different rule set: %s", inPath, detail),
			"this is a bug in migrate's schema_version rewrite, not a mistake in the input",
		)
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

// sameRules reports whether before and after carry the identical rule ids,
// kinds and predicate summaries in the same order — migrate's contract that
// bumping schema_version changes nothing else.
func sameRules(before, after *rules.Config) bool {
	beforeAll := append(append([]rules.Rule{}, before.React...), before.Inhibit...)
	afterAll := append(append([]rules.Rule{}, after.React...), after.Inhibit...)
	if len(beforeAll) != len(afterAll) {
		return false
	}
	for i := range beforeAll {
		b, a := beforeAll[i], afterAll[i]
		if b.ID != a.ID || b.Kind != a.Kind || b.Run != a.Run ||
			predicateSummary(b.When) != predicateSummary(a.When) {
			return false
		}
	}
	return true
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
