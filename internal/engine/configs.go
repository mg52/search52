package engine

import (
	"os"
	"strconv"
)

var (
	MaxPrefixTerms            = 5_000
	SkipUpdatePrefix          = false
	ExactMatchBoost           = 10
	SingleTermMaxPrefixTokens = 3
	SingleTermMaxFuzzyTokens  = 2
	SingleTermSkipWholeScan   = true
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

// multiTermPrefixLimit caps prefix expansion for the last token of a multi-term
// query. Lower than prefixLimitForQuery because each extra candidate multiplies
// the inner-loop cost by the size of every other group's anchor posting list.
// The prefix map is sorted by term frequency so top-N still covers the most
// relevant completions.
func multiTermPrefixLimit(prefix string) int {
	switch {
	case len(prefix) <= 2:
		return 30
	case len(prefix) <= 5:
		return 80
	default:
		return 100
	}
}

func fuzzyLimitForQuery(fuzzy string) int {
	switch {
	case len(fuzzy) <= 4:
		return 80
	case len(fuzzy) <= 6:
		return 50
	default:
		return 30
	}
}
