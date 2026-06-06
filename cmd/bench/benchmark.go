package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/mg52/search52/internal/engine"
)

const queryPoolSize = 1000

func runBenchmark(args []string) {
	fs := flag.NewFlagSet("benchmark", flag.ExitOnError)
	dataFile := fs.String("data", "data.json", "JSON data file (output of datagen)")
	vocabFile := fs.String("vocab", "vocab.txt", "Vocabulary file (output of vocab)")
	queries := fs.Int("queries", 5_000, "Number of queries to measure per mode")
	resultSize := fs.Int("result-size", 100, "Engine result size (top-k)")
	warmup := fs.Int("warmup", 500, "Warmup iterations before measuring")
	seed := fs.Int64("seed", 99, "Random seed for query generation")
	fields := fs.String("fields", "title,tags", "Comma-separated fields to index")
	filterField := fs.String("filter", "year", "Filter field")
	_ = fs.Parse(args)

	vocab, err := loadVocabFile(*vocabFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load vocab: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Vocabulary: %d words from %s\n", len(vocab), *vocabFile)

	fmt.Printf("Loading %s...\n", *dataFile)
	docs, err := loadJSONDocs(*dataFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load data: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Loaded %d documents\n", len(docs))

	indexFields := splitCSV(*fields)
	filters := map[string]bool{}
	if *filterField != "" {
		filters[*filterField] = true
	}

	fmt.Printf("Building engine (fields: %s, filter: %s, result-size: %d)...\n", strings.Join(indexFields, "+"), *filterField, *resultSize)
	se := engine.NewSearchEngine(
		indexFields,
		filters,
		*resultSize,
	)

	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)

	se.Index(docs)

	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	heapMB := float64(m1.HeapInuse-m0.HeapInuse) / (1024 * 1024)
	fmt.Printf("Engine ready — heap delta: %.1f MB\n\n", heapMB)

	r := rand.New(rand.NewSource(*seed))
	singleQs := buildSingleQueries(r, vocab, queryPoolSize)
	multiQs := buildMultiQueries(r, vocab, queryPoolSize)
	yearFs := buildYearFilters(r, queryPoolSize)

	type modeSpec struct {
		label  string
		multi  bool
		filter bool
	}
	modes := []modeSpec{
		{"SingleTerm / NoFilter", false, false},
		{"SingleTerm / Filter  ", false, true},
		{"MultiTerm  / NoFilter", true, false},
		{"MultiTerm  / Filter  ", true, true},
	}

	type modeStats struct {
		label       string
		lats        []int64
		bytesPerOp  uint64
		allocsPerOp uint64
	}
	results := make([]modeStats, len(modes))

	for i, m := range modes {
		results[i].label = m.label

		for w := 0; w < *warmup; w++ {
			q := singleQs[w%queryPoolSize]
			if m.multi {
				q = multiQs[w%queryPoolSize]
			}
			var f map[string][]interface{}
			if m.filter {
				f = yearFs[w%queryPoolSize]
			}
			se.Search(q, f)
		}

		runtime.GC()
		var mBefore runtime.MemStats
		runtime.ReadMemStats(&mBefore)

		lats := make([]int64, 0, *queries)
		for n := 0; n < *queries; n++ {
			q := singleQs[n%queryPoolSize]
			if m.multi {
				q = multiQs[n%queryPoolSize]
			}
			var f map[string][]interface{}
			if m.filter {
				f = yearFs[n%queryPoolSize]
			}
			t0 := time.Now()
			se.Search(q, f)
			lats = append(lats, time.Since(t0).Nanoseconds())
		}

		var mAfter runtime.MemStats
		runtime.ReadMemStats(&mAfter)

		sort.Slice(lats, func(a, b int) bool { return lats[a] < lats[b] })
		results[i].lats = lats
		results[i].bytesPerOp = (mAfter.TotalAlloc - mBefore.TotalAlloc) / uint64(*queries)
		results[i].allocsPerOp = (mAfter.Mallocs - mBefore.Mallocs) / uint64(*queries)
	}

	fmt.Printf("Benchmark results: %d docs | %d queries/mode | heap delta %.1f MB | GOMAXPROCS %d\n",
		len(docs), *queries, heapMB, runtime.GOMAXPROCS(0))
	fmt.Printf("────────────────────────────────────────────────────────────────────────────\n")
	fmt.Printf("  %-26s  %9s  %9s  %9s  %8s  %10s\n", "mode", "avg", "p50", "p99", "B/op", "allocs/op")
	fmt.Printf("  %-26s  %9s  %9s  %9s  %8s  %10s\n", "----", "---", "---", "---", "----", "---------")
	for _, res := range results {
		fmt.Printf("  %-26s  %9s  %9s  %9s  %8d  %10d\n",
			res.label,
			fmtNs(avgNs(res.lats)),
			fmtNs(pctNs(res.lats, 50)),
			fmtNs(pctNs(res.lats, 99)),
			res.bytesPerOp,
			res.allocsPerOp,
		)
	}
	fmt.Printf("────────────────────────────────────────────────────────────────────────────\n")
}

// ---- query generation (shared with loadtest.go) ----

func buildSingleQueries(r *rand.Rand, vocab []string, count int) []string {
	qs := make([]string, count)
	for i := range qs {
		word := vocab[r.Intn(len(vocab))]
		roll := r.Float64()
		switch {
		case roll < 0.10:
			word = misspellWord(r, word)
		case roll < 0.35:
			word = prefixWord(r, word)
		}
		qs[i] = word
	}
	return qs
}

func buildMultiQueries(r *rand.Rand, vocab []string, count int) []string {
	qs := make([]string, count)
	for i := range qs {
		nTerms := 2 + r.Intn(3)
		terms := make([]string, nTerms)
		misspellIdx := -1
		if r.Float64() < 0.10 {
			misspellIdx = r.Intn(nTerms)
		}
		for j := 0; j < nTerms; j++ {
			w := vocab[r.Intn(len(vocab))]
			switch {
			case j == misspellIdx:
				w = misspellWord(r, w)
			case j == nTerms-1 && r.Float64() < 0.25:
				w = prefixWord(r, w)
			}
			terms[j] = w
		}
		qs[i] = strings.Join(terms, " ")
	}
	return qs
}

func buildYearFilters(r *rand.Rand, count int) []map[string][]interface{} {
	fs := make([]map[string][]interface{}, count)
	for i := range fs {
		fs[i] = map[string][]interface{}{"year": {2000 + r.Intn(25)}}
	}
	return fs
}

func misspellWord(r *rand.Rand, word string) string {
	runes := []rune(word)
	n := len(runes)
	if n < 3 {
		return word
	}
	switch r.Intn(3) {
	case 0:
		i := r.Intn(n - 1)
		runes[i], runes[i+1] = runes[i+1], runes[i]
	case 1:
		i := r.Intn(n)
		runes = append(runes[:i], runes[i+1:]...)
	case 2:
		i := r.Intn(n)
		runes = append(runes[:i+1], append([]rune{runes[i]}, runes[i+1:]...)...)
	}
	return string(runes)
}

func prefixWord(r *rand.Rand, word string) string {
	if len(word) <= 4 {
		return word
	}
	minLen := 3
	maxLen := len(word) - 1
	if maxLen <= minLen {
		return word[:minLen]
	}
	return word[:minLen+r.Intn(maxLen-minLen)]
}

// ---- stats ----

func avgNs(lats []int64) int64 {
	if len(lats) == 0 {
		return 0
	}
	var sum int64
	for _, v := range lats {
		sum += v
	}
	return sum / int64(len(lats))
}

func pctNs(sorted []int64, p int) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	k := (p*n + 99) / 100
	idx := k - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

func fmtNs(ns int64) string {
	switch {
	case ns < 1_000:
		return fmt.Sprintf("%dns", ns)
	case ns < 1_000_000:
		return fmt.Sprintf("%.1fµs", float64(ns)/1_000)
	default:
		return fmt.Sprintf("%.2fms", float64(ns)/1_000_000)
	}
}

// ---- file helpers ----

func loadJSONDocs(path string) ([]map[string]interface{}, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	var docs []map[string]interface{}
	if err := json.NewDecoder(bufio.NewReaderSize(f, 16*1024*1024)).Decode(&docs); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return docs, nil
}
