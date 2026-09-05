package mgmt

import "strings"

// This file exists because Go's flag package stops parsing at the first
// non-flag argument (https://pkg.go.dev/flag#FlagSet.Parse) — "rules check
// <file> --adaptor <path>" would leave "--adaptor" and <path> sitting in
// FlagSet.Args() unparsed, rather than recognized as a flag that happens to
// come after a positional argument. Every verb here takes its flags in
// whatever order a caller writes them, so flags are extracted by a linear
// scan instead: each extract* helper finds its flag ANYWHERE in args, removes
// it (and its value, for the string form), and returns what is left for the
// next extraction or for positional use.

// extractStringFlag finds --name VALUE or --name=VALUE anywhere in args and
// returns VALUE, whether it was found, and args with that occurrence
// removed. Only the first occurrence is honored; a caller passing the flag
// twice gets the first value silently, which matches this repo's other CLI
// front ends closely enough not to need its own refusal.
func extractStringFlag(args []string, name string) (value string, found bool, rest []string) {
	flag := "--" + name
	prefix := flag + "="
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case !found && a == flag:
			found = true
			if i+1 < len(args) {
				value = args[i+1]
				i++
			}
		case !found && strings.HasPrefix(a, prefix):
			found = true
			value = strings.TrimPrefix(a, prefix)
		default:
			rest = append(rest, a)
		}
	}
	return value, found, rest
}

// extractBoolFlag finds a bare --name anywhere in args and returns whether it
// was present, and args with every occurrence removed.
func extractBoolFlag(args []string, name string) (found bool, rest []string) {
	flag := "--" + name
	rest = make([]string, 0, len(args))
	for _, a := range args {
		if a == flag {
			found = true
			continue
		}
		rest = append(rest, a)
	}
	return found, rest
}

// rejectUnknownFlags refuses any remaining "--..." token, so a typo'd flag
// name is caught rather than silently treated as a positional file path.
func rejectUnknownFlags(args []string) (string, bool) {
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			return a, true
		}
	}
	return "", false
}
