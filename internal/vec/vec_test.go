package vec

import (
	"math"
	"testing"
)

func approxEqual(a, b float32) bool {
	return math.Abs(float64(a-b)) < 1e-4
}

func TestDot(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float32
	}{
		{"nil/nil", nil, nil, 0},
		{"empty/empty", []float32{}, []float32{}, 0},
		{"single", []float32{3}, []float32{4}, 12},
		{"len2", []float32{1, 2}, []float32{3, 4}, 11}, // 1*3+2*4
		// Lengths spanning the 8-wide unroll boundary: below, at, and above it,
		// plus one that leaves a remainder the tail loop must pick up.
		{"len7_belowUnroll", ones(7), ones(7), 7},
		{"len8_exactUnroll", ones(8), ones(8), 8},
		{"len9_unrollPlusRemainder", ones(9), ones(9), 9},
		{"len16_twoFullUnrolls", ones(16), ones(16), 16},
		{"len17_twoUnrollsPlusRemainder", ones(17), ones(17), 17},
		{"negativeComponents", []float32{-1, 2, -3}, []float32{1, 2, 3}, -1 + 4 - 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Dot(c.a, c.b); !approxEqual(got, c.want) {
				t.Errorf("Dot(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

func ones(n int) []float32 {
	v := make([]float32, n)
	for i := range v {
		v[i] = 1
	}
	return v
}

func TestDot_MismatchedLengthReturnsZero(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
	}{
		{"b longer", []float32{1, 2}, []float32{1, 2, 3}},
		{"b shorter", []float32{1, 2, 3}, []float32{1, 2}},
		{"a empty, b non-empty", nil, []float32{1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Dot(c.a, c.b); got != 0 {
				t.Errorf("Dot(%v, %v) = %v, want 0 for mismatched lengths", c.a, c.b, got)
			}
		})
	}
}

func TestDot_Commutative(t *testing.T) {
	a := []float32{1, -2, 3.5, 4, -5, 6, 7, 8, 9, 10}
	b := []float32{-1, 2, 0.5, -4, 5, -6, 7, -8, 9, -10}
	if got, want := Dot(a, b), Dot(b, a); got != want {
		t.Errorf("Dot not commutative: Dot(a,b)=%v, Dot(b,a)=%v", got, want)
	}
}

func TestNorm(t *testing.T) {
	cases := []struct {
		name string
		v    []float32
		want float32
	}{
		{"zero vector", []float32{0, 0, 0}, 0},
		{"nil", nil, 0},
		{"3-4-5 triangle", []float32{3, 4}, 5},
		{"single positive", []float32{5}, 5},
		{"single negative", []float32{-5}, 5}, // magnitude, sign-independent
		{"negative components", []float32{-3, 4}, 5},
		{"unit-ish", []float32{1, 1, 1, 1}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Norm(c.v); !approxEqual(got, c.want) {
				t.Errorf("Norm(%v) = %v, want %v", c.v, got, c.want)
			}
		})
	}
}

func TestCosine_ZeroNormReturnsZero(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{1, 0}
	if got := Cosine(a, b, 0, Norm(b)); got != 0 {
		t.Errorf("Cosine with normA=0 = %v, want 0", got)
	}
	if got := Cosine(a, b, Norm(a), 0); got != 0 {
		t.Errorf("Cosine with normB=0 = %v, want 0", got)
	}
	if got := Cosine(a, b, 0, 0); got != 0 {
		t.Errorf("Cosine with both norms 0 = %v, want 0", got)
	}
}

func TestCosine_KnownAngles(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float32
	}{
		{"identical vectors", []float32{1, 2, 3}, []float32{1, 2, 3}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite direction", []float32{1, 0}, []float32{-1, 0}, -1},
		{"scaled copy (scale-invariant)", []float32{1, 2}, []float32{2, 4}, 1},
		{"45 degrees", []float32{1, 0}, []float32{1, 1}, float32(1 / math.Sqrt2)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Cosine(c.a, c.b, Norm(c.a), Norm(c.b))
			if !approxEqual(got, c.want) {
				t.Errorf("Cosine(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

func TestCosine_Symmetric(t *testing.T) {
	a := []float32{1, 2, 3, -4}
	b := []float32{-1, 0, 2, 5}
	na, nb := Norm(a), Norm(b)
	if got, want := Cosine(a, b, na, nb), Cosine(b, a, nb, na); !approxEqual(got, want) {
		t.Errorf("Cosine not symmetric: Cosine(a,b)=%v, Cosine(b,a)=%v", got, want)
	}
}

func TestCosine_MismatchedLengthReturnsZero(t *testing.T) {
	a := []float32{1, 2}
	b := []float32{1, 2, 3}
	if got := Cosine(a, b, Norm(a), Norm(b)); got != 0 {
		t.Errorf("Cosine with mismatched lengths = %v, want 0 (Dot returns 0)", got)
	}
}
