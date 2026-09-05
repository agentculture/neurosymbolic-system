package mgmt

import (
	"regexp"
	"strings"
)

// schemaVersionLine matches ONE line that is the whole of a `schema_version =
// 1` assignment: the key, an `=`, the literal 1, and nothing after it but
// whitespace or a comment. It is matched per line, never against the whole
// document, so the caller decides which lines are eligible at all.
var schemaVersionLine = regexp.MustCompile(`^(\s*schema_version\s*=\s*)1(\s*(#.*)?)$`)

// rewriteSchemaVersion returns raw with the file's TOP-LEVEL `schema_version
// = 1` rewritten to 2, and whether it found one. Every other byte — comments,
// blank lines, rule bodies, line endings — survives untouched.
//
// It scans line by line rather than running the regexp over the whole
// document, because the whole-document form had no idea what a string was: a
// react rule carrying
//
//	say = """
//	schema_version = 1
//	"""
//
// had that line silently rewritten to 2, producing a twin whose speech text
// differs from the input's. A migration that edits a rule's payload is worse
// than one that refuses, and this one did it invisibly.
//
// Two conditions make a line eligible, and both come from what TOML actually
// guarantees:
//
//   - it is not inside a multi-line string (tracked by tomlScan), so no
//     value's own text can ever be mistaken for the key;
//   - it comes before the first table header, because a top-level key must:
//     after `[modes.calm]` a `schema_version = 1` line is that mode's
//     parameter, not the document's version.
//
// The scanner is deliberately CONSERVATIVE about the string forms it does not
// model exactly (a multi-line basic string ending in an escaped quote, say):
// its failure mode is to consider a line ineligible, which makes migrate
// report that it found no line to rewrite rather than edit the wrong one. And
// the caller re-loads and compares the result before it replaces anything, so
// a rewrite this function gets wrong is refused, never written.
func rewriteSchemaVersion(raw string) (string, bool) {
	var out strings.Builder
	out.Grow(len(raw))

	var scan tomlScan
	rewritten := false

	for _, line := range strings.SplitAfter(raw, "\n") {
		content, terminator := splitLineTerminator(line)
		eligible := !rewritten && !scan.inMulti && !scan.seenTable

		// The state machine always sees the ORIGINAL line: what it tracks is
		// the input document's shape, not the output's.
		next := content
		if eligible {
			if m := schemaVersionLine.FindStringSubmatch(content); m != nil {
				next = m[1] + "2" + m[2]
				rewritten = true
			}
		}
		scan.advance(content)

		out.WriteString(next)
		out.WriteString(terminator)
	}
	return out.String(), rewritten
}

// splitLineTerminator splits a line produced by strings.SplitAfter into its
// content and its "\n", if it has one. A "\r" stays part of the content,
// where schemaVersionLine's trailing \s* absorbs it — so a CRLF file
// round-trips with its line endings intact.
func splitLineTerminator(line string) (content, terminator string) {
	if strings.HasSuffix(line, "\n") {
		return line[:len(line)-1], "\n"
	}
	return line, ""
}

// tomlScan is the minimum TOML lexical state rewriteSchemaVersion needs: am I
// inside a multi-line string, and have I passed the first table header. It is
// not a TOML parser and does not try to be one — it only has to answer "could
// a `schema_version` on this line possibly be the document's own key".
type tomlScan struct {
	inMulti bool
	// delim is the multi-line delimiter that opened the current string,
	// `"""` or `'''`. A `'''` inside a `"""` string is text, not a close, so
	// the opener has to be remembered.
	delim     string
	seenTable bool
}

func (s *tomlScan) advance(line string) {
	if !s.inMulti && strings.HasPrefix(strings.TrimLeft(line, " \t"), "[") {
		s.seenTable = true
	}
	for i := 0; i < len(line); {
		if s.inMulti {
			idx := strings.Index(line[i:], s.delim)
			if idx < 0 {
				return
			}
			i += idx + len(s.delim)
			s.inMulti = false
			continue
		}
		switch {
		case line[i] == '#':
			return // the rest of the line is a comment
		case strings.HasPrefix(line[i:], `"""`):
			s.inMulti, s.delim = true, `"""`
			i += 3
		case strings.HasPrefix(line[i:], `'''`):
			s.inMulti, s.delim = true, `'''`
			i += 3
		case line[i] == '"':
			i = skipBasicString(line, i+1)
		case line[i] == '\'':
			i = skipLiteralString(line, i+1)
		default:
			i++
		}
	}
}

// skipBasicString returns the index just past the closing quote of a
// single-line basic string starting at i, honoring backslash escapes.
func skipBasicString(line string, i int) int {
	for ; i < len(line); i++ {
		switch line[i] {
		case '\\':
			i++ // the escaped character, whatever it is, is not a delimiter
		case '"':
			return i + 1
		}
	}
	return len(line)
}

// skipLiteralString returns the index just past the closing quote of a
// single-line literal string starting at i. A literal string has no escapes:
// the first "'" ends it.
func skipLiteralString(line string, i int) int {
	if idx := strings.IndexByte(line[i:], '\''); idx >= 0 {
		return i + idx + 1
	}
	return len(line)
}
