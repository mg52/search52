package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type loadTestMode struct{ multi, filter bool }

type loadTestResult struct {
	lat    time.Duration
	mode   loadTestMode
	err    bool
	status int
}

type loadTestQuery struct {
	query      string
	kind       string
	misspelled bool
	prefix     bool
	terms      int
}

func runLoadtest(args []string) {
	fs := flag.NewFlagSet("loadtest", flag.ExitOnError)
	baseURL := fs.String("url", "http://localhost:8080/search", "Search endpoint URL")
	indexName := fs.String("index", "bench", "Index name (?index=)")
	vocabFile := fs.String("vocab", "vocab.txt", "Vocabulary file (produced by vocab command)")
	workers := fs.Int("workers", 8, "Number of parallel workers")
	requests := fs.Int("requests", 10_000, "Total requests to send")
	timeout := fs.Duration("timeout", 10*time.Second, "Per-request HTTP timeout")
	seed := fs.Int64("seed", time.Now().UnixNano(), "Random seed")
	keepAlive := fs.Bool("keepalive", true, "Use HTTP keep-alive")
	filterPct := fs.Int("filter-pct", 50, "Percentage of requests with a year filter (0-100)")
	multiPct := fs.Int("multi-pct", 50, "Percentage of multi-term queries (0-100)")
	modeMix := fs.String("mode-mix", "random", "Mode mix: random or balanced. Balanced matches benchmark.go's four modes evenly.")
	_ = fs.Parse(args)

	vocab, err := loadVocabFile(*vocabFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vocab: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Vocabulary: %d words from %s\n", len(vocab), *vocabFile)

	tr := &http.Transport{
		DisableKeepAlives:   !*keepAlive,
		MaxIdleConns:        1000,
		MaxConnsPerHost:     0,
		MaxIdleConnsPerHost: 1000,
		IdleConnTimeout:     90 * time.Second,
	}
	client := &http.Client{Timeout: *timeout, Transport: tr}

	var (
		sent      int64
		errCount  int64
		reqErrors int64
		resultsMu sync.Mutex
		results   = make([]loadTestResult, 0, *requests)
		statusMu  sync.Mutex
		statuses  = make(map[int]int)
	)

	fmt.Printf("Target:  %s  index=%s\n", *baseURL, *indexName)
	fmt.Printf("Workers: %d  |  Requests: %d  |  mode-mix: %s  |  filter-pct: %d%%  |  multi-pct: %d%%\n",
		*workers, *requests, *modeMix, *filterPct, *multiPct)
	fmt.Println("Starting...")

	querySeed := rand.New(rand.NewSource(*seed))
	queryPool := *requests
	if queryPool < queryPoolSize {
		queryPool = queryPoolSize
	}
	singleQs := buildLoadtestSingleQueries(querySeed, vocab, queryPool)
	multiQs := buildLoadtestMultiQueries(querySeed, vocab, queryPool)
	yearFs := buildYearFilters(querySeed, queryPool)
	yearFilterValue := func(i int) int {
		values := yearFs[i%len(yearFs)]["year"]
		if len(values) == 0 {
			return 2000
		}
		year, ok := values[0].(int)
		if !ok {
			return 2000
		}
		return year
	}

	startAll := time.Now()
	var wg sync.WaitGroup
	wg.Add(*workers)

	for w := 0; w < *workers; w++ {
		go func(workerID int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(*seed + int64(workerID)*1_000_003))

			for {
				reqNo := atomic.AddInt64(&sent, 1)
				if reqNo > int64(*requests) {
					return
				}
				idx := int(reqNo - 1)
				var isMulti, isFilter bool
				var q string

				if *modeMix == "balanced" {
					switch idx % 4 {
					case 0:
						isMulti, isFilter = false, false
					case 1:
						isMulti, isFilter = false, true
					case 2:
						isMulti, isFilter = true, false
					default:
						isMulti, isFilter = true, true
					}
				} else {
					isMulti = r.Intn(100) < *multiPct
					isFilter = r.Intn(100) < *filterPct
				}

				if isMulti {
					q = multiQs[idx%len(multiQs)].query
				} else {
					q = singleQs[idx%len(singleQs)].query
				}
				reqURL := buildSearchURL(*baseURL, *indexName, q, isFilter, yearFilterValue(idx))

				t0 := time.Now()
				req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, reqURL, nil)
				resp, reqErr := client.Do(req)

				res := loadTestResult{mode: loadTestMode{isMulti, isFilter}, err: reqErr != nil}
				if reqErr != nil {
					atomic.AddInt64(&reqErrors, 1)
					atomic.AddInt64(&errCount, 1)
				} else {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
					res.lat = time.Since(t0)
					res.status = resp.StatusCode
					statusMu.Lock()
					statuses[resp.StatusCode]++
					statusMu.Unlock()
					if resp.StatusCode < 200 || resp.StatusCode >= 300 {
						atomic.AddInt64(&errCount, 1)
						res.err = true
					}
				}
				if reqErr != nil {
					res.lat = time.Since(t0)
				}
				resultsMu.Lock()
				results = append(results, res)
				resultsMu.Unlock()
			}
		}(w)
	}

	wg.Wait()
	elapsed := time.Since(startAll)

	if len(results) == 0 {
		fmt.Println("No results collected.")
		os.Exit(1)
	}

	allLats := make([]time.Duration, len(results))
	for i, r := range results {
		allLats[i] = r.lat
	}
	sort.Slice(allLats, func(i, j int) bool { return allLats[i] < allLats[j] })
	rps := float64(len(results)) / elapsed.Seconds()

	byMode := make(map[loadTestMode][]time.Duration)
	resultsByMode := make(map[loadTestMode][]loadTestResult)
	for _, r := range results {
		byMode[r.mode] = append(byMode[r.mode], r.lat)
		resultsByMode[r.mode] = append(resultsByMode[r.mode], r)
	}
	orderedModes := []loadTestMode{{false, false}, {false, true}, {true, false}, {true, true}}

	fmt.Printf("\nLoad test totals\n\n")
	fmt.Printf("| Metric | Value |\n")
	fmt.Printf("|---|---:|\n")
	fmt.Printf("| Requested total requests | %d |\n", *requests)
	fmt.Printf("| Processed total requests | %d |\n", len(results))
	fmt.Printf("| Load test duration | %s |\n", elapsed.Round(time.Millisecond))
	fmt.Printf("| Requests per second | %.1f |\n", rps)

	fmt.Printf("\nLoad test summary\n\n")
	fmt.Printf("| Scope | Workers | Queries | Errors | Client errors | HTTP statuses | Wall time | RPS | avg | p50 | p95 | p99 |\n")
	fmt.Printf("|---|---:|---:|---:|---:|---|---:|---:|---:|---:|---:|---:|\n")
	fmt.Printf("| Overall | %d | %d | %d | %d | %s | %s | %.1f | %s | %s | %s | %s |\n",
		*workers,
		len(results),
		atomic.LoadInt64(&errCount),
		atomic.LoadInt64(&reqErrors),
		formatStatusCounts(statuses),
		elapsed.Round(time.Millisecond),
		rps,
		fmtDurMs(avgDur(allLats)),
		fmtDurMs(pctDur(allLats, 50)),
		fmtDurMs(pctDur(allLats, 95)),
		fmtDurMs(pctDur(allLats, 99)),
	)
	for _, m := range orderedModes {
		lats := byMode[m]
		if len(lats) == 0 {
			continue
		}
		sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
		modeResults := resultsByMode[m]
		fmt.Printf("| %s | %d | %d | %d | %d | %s | %s | %.1f | %s | %s | %s | %s |\n",
			modeLabel(m),
			*workers,
			len(lats),
			countErrors(modeResults),
			countClientErrors(modeResults),
			formatStatusCounts(statusCountsFromResults(modeResults)),
			elapsed.Round(time.Millisecond),
			float64(len(lats))/elapsed.Seconds(),
			fmtDurMs(avgDur(lats)),
			fmtDurMs(pctDur(lats, 50)),
			fmtDurMs(pctDur(lats, 95)),
			fmtDurMs(pctDur(lats, 99)),
		)
	}

	fmt.Printf("\nLoad test configuration\n\n")
	fmt.Printf("| Setting | Value |\n")
	fmt.Printf("|---|---:|\n")
	fmt.Printf("| Target URL | %s |\n", *baseURL)
	fmt.Printf("| Index | %s |\n", *indexName)
	fmt.Printf("| Vocabulary file | %s |\n", *vocabFile)
	fmt.Printf("| Vocabulary size | %d |\n", len(vocab))
	fmt.Printf("| Query pool size | %d |\n", queryPool)
	fmt.Printf("| Requested total queries | %d |\n", *requests)
	fmt.Printf("| Workers | %d |\n", *workers)
	fmt.Printf("| Seed | %d |\n", *seed)
	fmt.Printf("| Mode mix | %s |\n", *modeMix)
	fmt.Printf("| Filter pct | %d%% |\n", *filterPct)
	fmt.Printf("| Multi pct | %d%% |\n", *multiPct)
	fmt.Printf("| Timeout | %s |\n", *timeout)
	fmt.Printf("| Keep-alive | %t |\n", *keepAlive)
	fmt.Printf("| GOMAXPROCS | %d |\n", runtime.GOMAXPROCS(0))

	printQueryStrategyTable(singleQs, multiQs)
}

func formatStatusCounts(statuses map[int]int) string {
	if len(statuses) == 0 {
		return "{}"
	}
	codes := make([]int, 0, len(statuses))
	for code := range statuses {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	parts := make([]string, 0, len(codes))
	for _, code := range codes {
		parts = append(parts, fmt.Sprintf("%d:%d", code, statuses[code]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func modeLabel(m loadTestMode) string {
	query := "Single"
	filter := "NoFilter"
	if m.multi {
		query = "Multi"
	}
	if m.filter {
		filter = "Filter"
	}
	return query + " / " + filter
}

func countErrors(results []loadTestResult) int {
	n := 0
	for _, result := range results {
		if result.err {
			n++
		}
	}
	return n
}

func countClientErrors(results []loadTestResult) int {
	n := 0
	for _, result := range results {
		if result.err && result.status == 0 {
			n++
		}
	}
	return n
}

func statusCountsFromResults(results []loadTestResult) map[int]int {
	statuses := make(map[int]int)
	for _, result := range results {
		if result.status != 0 {
			statuses[result.status]++
		}
	}
	return statuses
}

func printQueryStrategyTable(singleQs, multiQs []loadTestQuery) {
	singleExact, singlePrefix, singleMisspelled := 0, 0, 0
	for _, q := range singleQs {
		switch q.kind {
		case "exact":
			singleExact++
		case "prefix":
			singlePrefix++
		case "misspelled":
			singleMisspelled++
		}
	}

	multiMisspelled, multiPrefix := 0, 0
	termCounts := make(map[int]int)
	for _, q := range multiQs {
		if q.misspelled {
			multiMisspelled++
		}
		if q.prefix {
			multiPrefix++
		}
		termCounts[q.terms]++
	}

	fmt.Printf("\nQuery generation details\n\n")
	fmt.Printf("| Query set | Pool size | Exact | Prefix | Misspelled | 2 terms | 3 terms | 4 terms | Strategy |\n")
	fmt.Printf("|---|---:|---:|---:|---:|---:|---:|---:|---|\n")
	fmt.Printf("| Single-term | %d | %d | %d | %d | - | - | - | 65%% exact, 25%% prefix, 10%% misspelled |\n",
		len(singleQs),
		singleExact,
		singlePrefix,
		singleMisspelled,
	)
	fmt.Printf("| Multi-term | %d | - | %d | %d | %d | %d | %d | 2-4 terms; 10%% one misspelled token; last token prefix-truncated 25%% when not misspelled |\n",
		len(multiQs),
		multiPrefix,
		multiMisspelled,
		termCounts[2],
		termCounts[3],
		termCounts[4],
	)
	fmt.Printf("| Balanced mode | - | - | - | - | - | - | - | Cycles evenly through Single/NoFilter, Single/Filter, Multi/NoFilter, Multi/Filter |\n")
}

// ---- query helpers ----

func buildLoadtestSingleQueries(r *rand.Rand, vocab []string, count int) []loadTestQuery {
	qs := make([]loadTestQuery, count)
	for i := range qs {
		word := vocab[r.Intn(len(vocab))]
		roll := r.Float64()
		kind := "exact"
		switch {
		case roll < 0.10:
			word = misspellWord(r, word)
			kind = "misspelled"
		case roll < 0.35:
			word = prefixWord(r, word)
			kind = "prefix"
		}
		qs[i] = loadTestQuery{query: word, kind: kind, misspelled: kind == "misspelled", prefix: kind == "prefix", terms: 1}
	}
	return qs
}

func buildLoadtestMultiQueries(r *rand.Rand, vocab []string, count int) []loadTestQuery {
	qs := make([]loadTestQuery, count)
	for i := range qs {
		nTerms := 2 + r.Intn(3)
		terms := make([]string, nTerms)
		misspellIdx := -1
		misspelled := false
		prefix := false
		if r.Float64() < 0.10 {
			misspellIdx = r.Intn(nTerms)
		}
		for j := 0; j < nTerms; j++ {
			w := vocab[r.Intn(len(vocab))]
			switch {
			case j == misspellIdx:
				w = misspellWord(r, w)
				misspelled = true
			case j == nTerms-1 && r.Float64() < 0.25:
				w = prefixWord(r, w)
				prefix = true
			}
			terms[j] = w
		}
		qs[i] = loadTestQuery{query: joinTerms(terms), misspelled: misspelled, prefix: prefix, terms: nTerms}
	}
	return qs
}

func ltSingleQuery(r *rand.Rand, vocab []string) string {
	word := vocab[r.Intn(len(vocab))]
	roll := r.Float64()
	switch {
	case roll < 0.10:
		return misspellWord(r, word)
	case roll < 0.35:
		return prefixWord(r, word)
	default:
		return word
	}
}

func ltMultiQuery(r *rand.Rand, vocab []string) string {
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
	return joinTerms(terms)
}

func joinTerms(terms []string) string {
	n := 0
	for _, t := range terms {
		n += len(t) + 1
	}
	if n == 0 {
		return ""
	}
	b := make([]byte, 0, n-1)
	for i, t := range terms {
		if i > 0 {
			b = append(b, ' ')
		}
		b = append(b, t...)
	}
	return string(b)
}

func buildSearchURL(base, index, q string, addFilter bool, year int) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	params := u.Query()
	params.Set("index", index)
	params.Set("q", q)
	if addFilter {
		params.Set("filter", fmt.Sprintf("year:%d", year))
	}
	u.RawQuery = params.Encode()
	return u.String()
}

// ---- duration stats ----

func printLTRow(label string, sorted []time.Duration) {
	fmt.Printf("  %-8s  avg=%-10s  p50=%-10s  p95=%-10s  p99=%s\n",
		label,
		fmtDurMs(avgDur(sorted)),
		fmtDurMs(pctDur(sorted, 50)),
		fmtDurMs(pctDur(sorted, 95)),
		fmtDurMs(pctDur(sorted, 99)),
	)
}

func fmtDurMs(d time.Duration) string {
	return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
}

func avgDur(lats []time.Duration) time.Duration {
	if len(lats) == 0 {
		return 0
	}
	var sum int64
	for _, d := range lats {
		sum += int64(d)
	}
	return time.Duration(sum / int64(len(lats)))
}

func pctDur(sorted []time.Duration, p int) time.Duration {
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
