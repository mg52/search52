package engine

import (
	"context"
	"fmt"
	"math/rand"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- vocabulary ----

// generateVocab returns n unique pseudo-random lowercase words (3–12 chars).
// The seed is fixed so corpus builds are reproducible across runs.
func generateVocab(n int) []string {
	const charset = "abcdefghijklmnopqrstuvwxyz"
	r := rand.New(rand.NewSource(42))
	seen := make(map[string]struct{}, n)
	words := make([]string, 0, n)
	buf := make([]byte, 12)
	for len(words) < n {
		length := 3 + r.Intn(10)
		for i := 0; i < length; i++ {
			buf[i] = charset[r.Intn(26)]
		}
		w := string(buf[:length])
		if _, exists := seen[w]; !exists {
			seen[w] = struct{}{}
			words = append(words, w)
		}
	}
	return words
}

var benchVocab = generateVocab(100_000)

// ---- helpers ----

func misspellWord(r *rand.Rand, word string) string {
	runes := []rune(word)
	n := len(runes)
	if n < 3 {
		return word
	}
	switch r.Intn(3) {
	case 0: // swap two adjacent chars
		i := r.Intn(n - 1)
		runes[i], runes[i+1] = runes[i+1], runes[i]
	case 1: // delete one char
		i := r.Intn(n)
		runes = append(runes[:i], runes[i+1:]...)
	case 2: // double one char (insert duplicate)
		i := r.Intn(n)
		runes = append(runes[:i+1], append([]rune{runes[i]}, runes[i+1:]...)...)
	}
	return string(runes)
}

func prefixOfWord(r *rand.Rand, word string) string {
	if len(word) <= 4 {
		return word
	}
	// keep 3 to len-2 characters so it's a genuine prefix, not the full word
	minLen := 3
	maxLen := len(word) - 1
	if maxLen <= minLen {
		return word[:minLen]
	}
	return word[:minLen+r.Intn(maxLen-minLen)]
}

// buildSingleQueries returns count single-term query strings.
// 10% misspellings, 25% prefix, rest exact.
func buildSingleQueries(r *rand.Rand, vocab []string, count int) []string {
	qs := make([]string, count)
	for i := range qs {
		word := vocab[r.Intn(len(vocab))]
		roll := r.Float64()
		switch {
		case roll < 0.10:
			word = misspellWord(r, word)
		case roll < 0.35:
			word = prefixOfWord(r, word)
		}
		qs[i] = word
	}
	return qs
}

// buildMultiQueries returns count multi-term query strings (2–4 terms).
// 10% of queries have one misspelled word; last word is prefix 25% of the time.
func buildMultiQueries(r *rand.Rand, vocab []string, count int) []string {
	qs := make([]string, count)
	for i := range qs {
		nTerms := 2 + r.Intn(3) // 2, 3, or 4
		terms := make([]string, nTerms)
		misspellIdx := -1
		if r.Float64() < 0.10 {
			misspellIdx = r.Intn(nTerms)
		}
		for j := 0; j < nTerms; j++ {
			w := vocab[r.Intn(len(vocab))]
			if j == misspellIdx {
				w = misspellWord(r, w)
			} else if j == nTerms-1 && r.Float64() < 0.25 {
				w = prefixOfWord(r, w)
			}
			terms[j] = w
		}
		qs[i] = strings.Join(terms, " ")
	}
	return qs
}

// buildYearFilters returns count year filter maps cycling through 2000–2024.
func buildYearFilters(r *rand.Rand, count int) []map[string][]interface{} {
	fs := make([]map[string][]interface{}, count)
	for i := range fs {
		year := 2000 + r.Intn(25)
		fs[i] = map[string][]interface{}{"year": {year}}
	}
	return fs
}

// ---- corpus ----

const queryPoolSize = 1000

type benchCorpus struct {
	engine      *SearchEngine
	heapMB      float64
	singleExact []string
	multiExact  []string
	yearFilters []map[string][]interface{}
}

var (
	corpus1M benchCorpus
	once1M   sync.Once
	corpus5M benchCorpus
	once5M   sync.Once
)

func getCorpus(n int) *benchCorpus {
	switch n {
	case 1_000_000:
		once1M.Do(func() { corpus1M = buildRealisticCorpus(1_000_000) })
		return &corpus1M
	case 5_000_000:
		once5M.Do(func() { corpus5M = buildRealisticCorpus(5_000_000) })
		return &corpus5M
	}
	panic(fmt.Sprintf("unknown corpus size: %d", n))
}

func buildRealisticCorpus(n int) benchCorpus {
	r := rand.New(rand.NewSource(42))

	se := NewSearchEngine(
		[]string{"title", "tags"},
		nil,
		map[string]bool{"year": true},
		100,
	)

	docs := make([]map[string]interface{}, n)
	for i := 0; i < n; i++ {
		nTitle := 3 + r.Intn(18) // 3–20 words
		titleParts := make([]string, nTitle)
		for j := range titleParts {
			titleParts[j] = benchVocab[r.Intn(len(benchVocab))]
		}

		nTags := 1 + r.Intn(10) // 1–10 words
		tagParts := make([]string, nTags)
		for j := range tagParts {
			tagParts[j] = benchVocab[r.Intn(len(benchVocab))]
		}

		docs[i] = map[string]interface{}{
			"id":    fmt.Sprintf("d%d", i),
			"title": strings.Join(titleParts, " "),
			"tags":  strings.Join(tagParts, " "),
			"year":  2000 + r.Intn(25),
		}
	}

	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)

	se.Index(docs)

	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	heapMB := float64(m1.HeapInuse-m0.HeapInuse) / (1024 * 1024)

	qr := rand.New(rand.NewSource(99))
	return benchCorpus{
		engine:      se,
		heapMB:      heapMB,
		singleExact: buildSingleQueries(qr, benchVocab, queryPoolSize),
		multiExact:  buildMultiQueries(qr, benchVocab, queryPoolSize),
		yearFilters: buildYearFilters(qr, queryPoolSize),
	}
}

// ---- latency helpers ----

func pct(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

// runAndMeasure runs b.N iterations of fn, reports p50/p99 via ReportMetric,
// and returns so testing.B can report ns/op, B/op, allocs/op normally.
func runAndMeasure(b *testing.B, fn func()) {
	b.Helper()
	lats := make([]int64, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t := time.Now()
		fn()
		lats = append(lats, time.Since(t).Nanoseconds())
	}
	b.StopTimer()
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	b.ReportMetric(float64(pct(lats, 0.50)), "p50_ns")
	b.ReportMetric(float64(pct(lats, 0.99)), "p99_ns")
}

// ---- benchmarks ----

func BenchmarkSearch1M(b *testing.B) { runSearchBench(b, 1_000_000) }
func BenchmarkSearch5M(b *testing.B) { runSearchBench(b, 5_000_000) }

func runSearchBench(b *testing.B, n int) {
	b.Helper()
	c := getCorpus(n)
	label := fmt.Sprintf("%dM_docs", n/1_000_000)

	b.Run(fmt.Sprintf("SingleTerm/NoFilter/%s", label), func(b *testing.B) {
		b.ReportAllocs()
		b.ReportMetric(c.heapMB, "heap_MB")
		idx := 0
		runAndMeasure(b, func() {
			_, _ = c.engine.Search(context.Background(), c.singleExact[idx%queryPoolSize], nil)
			idx++
		})
	})

	b.Run(fmt.Sprintf("SingleTerm/Filter/%s", label), func(b *testing.B) {
		b.ReportAllocs()
		b.ReportMetric(c.heapMB, "heap_MB")
		idx := 0
		runAndMeasure(b, func() {
			_, _ = c.engine.Search(context.Background(), c.singleExact[idx%queryPoolSize], c.yearFilters[idx%queryPoolSize])
			idx++
		})
	})

	b.Run(fmt.Sprintf("MultiTerm/NoFilter/%s", label), func(b *testing.B) {
		b.ReportAllocs()
		b.ReportMetric(c.heapMB, "heap_MB")
		idx := 0
		runAndMeasure(b, func() {
			_, _ = c.engine.Search(context.Background(), c.multiExact[idx%queryPoolSize], nil)
			idx++
		})
	})

	b.Run(fmt.Sprintf("MultiTerm/Filter/%s", label), func(b *testing.B) {
		b.ReportAllocs()
		b.ReportMetric(c.heapMB, "heap_MB")
		idx := 0
		runAndMeasure(b, func() {
			_, _ = c.engine.Search(context.Background(), c.multiExact[idx%queryPoolSize], c.yearFilters[idx%queryPoolSize])
			idx++
		})
	})
}
