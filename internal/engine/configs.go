package engine

import (
	"os"
	"strconv"
)

var (
	// MaxPrefixTerms caps how many terms are stored per prefix key. Search reads
	// at most SingleTermMaxPrefixTokens (single-term) or multiTermPrefixLimit
	// (multi-term, ≤50) entries from a bucket, so anything stored beyond the
	// largest of those limits is unreachable and only costs memory. Raise this
	// (SEARCH52_MAX_PREFIX_TERMS) if you raise those consumer limits past 64.
	MaxPrefixTerms            = 64
	SkipUpdatePrefix          = false
	ExactMatchBoost           = 10
	SingleTermMaxPrefixTokens = 3
	SingleTermMaxFuzzyTokens  = 2
	SingleTermSkipWholeScan   = false
	MultiTermSkipWholeScan    = false
)

func init() {
	if v := os.Getenv("SEARCH52_MAX_PREFIX_TERMS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			MaxPrefixTerms = n
		}
	}
	if v := os.Getenv("SEARCH52_SKIP_UPDATE_PREFIX"); v != "" {
		SkipUpdatePrefix = v == "true" || v == "1"
	}
	if v := os.Getenv("SEARCH52_EXACT_MATCH_BOOST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ExactMatchBoost = n
		}
	}
	if v := os.Getenv("SEARCH52_SINGLE_TERM_MAX_PREFIX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			SingleTermMaxPrefixTokens = n
		}
	}
	if v := os.Getenv("SEARCH52_SINGLE_TERM_MAX_FUZZY_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			SingleTermMaxFuzzyTokens = n
		}
	}
	if v := os.Getenv("SEARCH52_SINGLE_TERM_SKIP_WHOLE_SCAN"); v != "" {
		SingleTermSkipWholeScan = v == "true" || v == "1"
	}
	if v := os.Getenv("SEARCH52_MULTI_TERM_SKIP_WHOLE_SCAN"); v != "" {
		MultiTermSkipWholeScan = v == "true" || v == "1"
	}
}

func multiTermPrefixLimit(prefix string) int {
	switch {
	case len(prefix) <= 2:
		return 50
	case len(prefix) <= 5:
		return 40
	default:
		return 30
	}
}

func fuzzyLimitForQuery(fuzzy string) int {
	switch {
	case len(fuzzy) <= 4:
		return 30
	case len(fuzzy) <= 6:
		return 20
	default:
		return 10
	}
}
