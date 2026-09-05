package provider

import (
	"fmt"
	"sort"
	"strings"
)

// renderInputs turns this tick's declared input fields into one line of text
// for a KindCompletion user message: "<field>: <value>" pairs, field order
// fixed by cfg.Inputs so the rendering is stable across ticks.
func renderInputs(fields []string, view map[string]any) string {
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		value, ok := view[field]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %v", field, value))
	}
	return strings.Join(parts, "; ")
}

// renderEmbeddingInput is the same idea for a KindEmbedding request, whose
// wire shape carries a single input string. Field order is sorted for a
// deterministic embedding independent of map iteration order — an embedding
// call, unlike a completion prompt, has no reader for whom field order
// matters, so determinism wins over "the config's own order".
func renderEmbeddingInput(fields []string, view map[string]any) string {
	sorted := append([]string(nil), fields...)
	sort.Strings(sorted)
	parts := make([]string, 0, len(sorted))
	for _, field := range sorted {
		value, ok := view[field]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%v", value))
	}
	return strings.Join(parts, " ")
}

// firstWordLower is a KindCompletion reply reduced to its first token: the
// first run of non-space runes, lowercased. An empty reply (already refused
// upstream as malformed) never reaches here.
func firstWordLower(reply string) string {
	fields := strings.Fields(reply)
	if len(fields) == 0 {
		return ""
	}
	word := strings.ToLower(fields[0])
	word = strings.Trim(word, ".,!?;:\"'")
	return word
}
