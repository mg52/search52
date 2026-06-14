package engine

const MaxPrefixTerms = 5_000
const SkipWholeScan = false
const SkipUpdatePrefix = false
const ExactMatchBoost = 10

func prefixLimitForQuery(prefix string) int {
	switch {
	case len(prefix) <= 2:
		return 200
	case len(prefix) <= 5:
		return 150
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
