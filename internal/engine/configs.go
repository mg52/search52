package engine

const MaxPrefixTerms = 5_000
const SkipUpdatePrefix = false
const ExactMatchBoost = 10
const SingleTermMaxPrefixTokens = 3
const SingleTermMaxFuzzyTokens = 2
const SingleTermSkipWholeScan = true
const MultiTermSkipWholeScan = false

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
