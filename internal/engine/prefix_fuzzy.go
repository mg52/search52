package engine

const MaxPrefixTerms = 5_000

func prefixLimitForQuery(prefix string) int {
	switch {
	case len(prefix) <= 2:
		return 200
	case len(prefix) <= 5:
		return 100
	default:
		return 80
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
