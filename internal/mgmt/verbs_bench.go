package mgmt

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/bench"
	"github.com/agentculture/neurosymbolic-system/internal/clifmt"
)

// benchUsage is bench's own usage, rendered as the remediation of any flag
// refusal.
const benchUsage = "run: bench [--rules N] [--fields N] [--ticks N] [--period <duration>] " +
	"[--rss-ceiling <size>]"

// verbBench is `bench`: assemble a synthetic 200-rule/20-field load (by
// default) the way internal/compose assembles a real robot — a vocabulary, a
// v2 rules file, a compiled evaluator — but over a null sink with no
// transport, tick it at a real --period for --ticks ticks, and report tick
// p50/p99/max, overruns and steady-state RSS against --rss-ceiling.
//
// Acceptance criterion 1: bench exits non-zero (ExitUser, so process exit
// code 1) when any tick overran the budget or RSS exceeded the ceiling. The
// full report is still the failure's message, so a caller sees the numbers
// that failed rather than a bare "it failed".
func verbBench(_ *Handler, args []string) (verbResult, error) {
	cfg := bench.DefaultConfig()

	rulesStr, _, rest := extractStringFlag(args, "rules")
	fieldsStr, _, rest := extractStringFlag(rest, "fields")
	ticksStr, _, rest := extractStringFlag(rest, "ticks")
	periodStr, _, rest := extractStringFlag(rest, "period")
	ceilingStr, _, rest := extractStringFlag(rest, "rss-ceiling")
	if bad, found := rejectUnknownFlags(rest); found {
		return verbResult{}, clifmt.NewUserError(
			fmt.Sprintf("bench does not recognize %q", bad), benchUsage)
	}
	if len(rest) > 0 {
		return verbResult{}, clifmt.NewUserError(
			fmt.Sprintf("bench takes no positional arguments, got %q", rest[0]), benchUsage)
	}

	if rulesStr != "" {
		n, err := strconv.Atoi(rulesStr)
		if err != nil {
			return verbResult{}, clifmt.NewUserError(
				fmt.Sprintf("--rules %q is not an integer", rulesStr), benchUsage)
		}
		cfg.Rules = n
	}
	if fieldsStr != "" {
		n, err := strconv.Atoi(fieldsStr)
		if err != nil {
			return verbResult{}, clifmt.NewUserError(
				fmt.Sprintf("--fields %q is not an integer", fieldsStr), benchUsage)
		}
		cfg.Fields = n
	}
	if ticksStr != "" {
		n, err := strconv.Atoi(ticksStr)
		if err != nil {
			return verbResult{}, clifmt.NewUserError(
				fmt.Sprintf("--ticks %q is not an integer", ticksStr), benchUsage)
		}
		cfg.Ticks = n
	}
	if periodStr != "" {
		d, err := time.ParseDuration(periodStr)
		if err != nil {
			return verbResult{}, clifmt.NewUserError(
				fmt.Sprintf("--period %q is not a duration", periodStr), benchUsage)
		}
		cfg.Period = d
	}
	if ceilingStr != "" {
		b, err := parseSize(ceilingStr)
		if err != nil {
			return verbResult{}, clifmt.NewUserError(
				fmt.Sprintf("--rss-ceiling %q: %v", ceilingStr, err), benchUsage)
		}
		cfg.RSSCeiling = b
	}

	result, err := bench.Run(cfg)
	if err != nil {
		return verbResult{}, asCliError(err, clifmt.ExitEnv, "")
	}

	if !result.OK {
		return verbResult{}, clifmt.NewUserError(
			result.Table(),
			"reduce --rules/--fields, raise --period, or raise --rss-ceiling only if the "+
				"numbers genuinely no longer fit the budget — never to make a real "+
				"regression pass")
	}
	return verbResult{Text: result.Table(), Value: result}, nil
}

// parseSize parses a byte count with an optional case-insensitive KB/MB/GB
// suffix (decimal, 1024-based — "32MB" means 32*1024*1024 bytes, matching how
// an operator reads a memory ceiling). A bare number is bytes.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	upper := strings.ToUpper(s)
	multiplier := int64(1)
	switch {
	case strings.HasSuffix(upper, "GB"):
		multiplier = 1024 * 1024 * 1024
		s = s[:len(s)-2]
	case strings.HasSuffix(upper, "MB"):
		multiplier = 1024 * 1024
		s = s[:len(s)-2]
	case strings.HasSuffix(upper, "KB"):
		multiplier = 1024
		s = s[:len(s)-2]
	case strings.HasSuffix(upper, "B"):
		s = s[:len(s)-1]
	}
	s = strings.TrimSpace(s)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("expected a byte count, optionally suffixed KB/MB/GB (got %q)", s)
	}
	if n <= 0 {
		return 0, fmt.Errorf("must be greater than zero (got %d)", n)
	}
	return n * multiplier, nil
}
