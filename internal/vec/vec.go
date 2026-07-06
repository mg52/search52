// Package vec provides float32 vector math for the embedding hot path. float32
// halves memory traffic versus float64 — the dominant cost when scanning many
// stored vectors — and its ~7 significant digits are far more precision than
// embedding similarities carry.
package vec

import "math"

// Norm returns the Euclidean (L2) norm of v.
func Norm(v []float32) float32 {
	return float32(math.Sqrt(float64(Dot(v, v))))
}

// Dot returns the dot product of a and b. Returns 0 if their lengths differ.
//
// The loop is unrolled into eight independent accumulators: a single-accumulator
// loop serializes on the floating-point add latency chain, while parallel
// chains let the CPU pipeline the multiply-adds (measured ~15% over 4 chains on
// Apple Silicon). Reslicing b to len(a) lets the compiler drop per-element
// bounds checks.
func Dot(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	b = b[:len(a)]
	var s0, s1, s2, s3, s4, s5, s6, s7 float32
	i := 0
	for ; i+8 <= len(a); i += 8 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
		s4 += a[i+4] * b[i+4]
		s5 += a[i+5] * b[i+5]
		s6 += a[i+6] * b[i+6]
		s7 += a[i+7] * b[i+7]
	}
	d := (s0 + s1) + (s2 + s3) + (s4 + s5) + (s6 + s7)
	for ; i < len(a); i++ {
		d += a[i] * b[i]
	}
	return d
}

// Cosine returns the cosine similarity between a and b using their precomputed
// norms. Callers cache normA/normB to avoid recomputing them per comparison.
func Cosine(a, b []float32, normA, normB float32) float32 {
	if normA == 0 || normB == 0 {
		return 0
	}
	return Dot(a, b) / (normA * normB)
}
