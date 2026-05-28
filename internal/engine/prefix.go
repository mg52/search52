package engine

const MaxPrefixTerms = 5_000

func prefixLimitForQuery(prefix string) int {
	switch {
	case len(prefix) <= 2:
		return 100
	case len(prefix) <= 5:
		return 60
	default:
		return 50
	}
}
