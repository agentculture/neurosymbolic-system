package provider

import "math"

// cosineSimilarity is the standard cosine of the angle between a and b.
// Mismatched lengths or a zero vector return 0 rather than panicking or
// dividing by zero — a malformed embedding degrades to "no similarity"
// rather than crashing a worker goroutine.
func cosineSimilarity(a, b []float64) float64 {
	n := len(a)
	if n > len(b) {
		n = len(b)
	}
	if n == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := 0; i < n; i++ {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// bestLabel returns the label whose reference embedding is most similar to
// vector, and that similarity. refs is never empty by construction: warm-up
// refuses to mark a KindEmbedding provider configured with none.
func bestLabel(refs map[string][]float64, vector []float64) (label string, score float64) {
	first := true
	for name, ref := range refs {
		s := cosineSimilarity(vector, ref)
		if first || s > score {
			label, score = name, s
			first = false
		}
	}
	return label, score
}
