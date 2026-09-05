package clifmt

import "strings"

// HasJSONFlag reports whether argv contains --json or --json=<value>
// anywhere. main() pre-scans raw argv with this before any verb-specific
// parsing happens, so a parse-time failure (an unknown verb, a bad flag)
// still renders through Emit's JSON shape when the caller asked for it — verb
// parsing itself never gets a chance to observe --json if parsing fails
// first.
func HasJSONFlag(argv []string) bool {
	for _, a := range argv {
		if isJSONFlag(a) {
			return true
		}
	}
	return false
}

// StripJSONFlag returns argv with every --json / --json=<value> token
// removed, plus whether any were found. Verb dispatch operates on the
// stripped slice so --json is recognized no matter where in the invocation it
// appears, rather than only in a fixed position.
func StripJSONFlag(argv []string) (rest []string, found bool) {
	rest = make([]string, 0, len(argv))
	for _, a := range argv {
		if isJSONFlag(a) {
			found = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, found
}

func isJSONFlag(arg string) bool {
	return arg == "--json" || strings.HasPrefix(arg, "--json=")
}
