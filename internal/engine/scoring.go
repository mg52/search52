package engine

func normalizeFieldWeights(indexFields []string, weights map[string]int) map[string]int {
	out := make(map[string]int, len(indexFields))
	for _, field := range indexFields {
		weight := 1
		if weights != nil && weights[field] > 0 {
			weight = weights[field]
		}
		out[field] = weight
	}
	return out
}

func copyBoolMap(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyIntMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func weightedTokenScores(doc map[string]interface{}, indexFields []string, fieldWeights map[string]int) map[string]int {
	type fieldTokenSet struct {
		tokens []string
		weight int
	}

	var (
		sets              []fieldTokenSet
		totalWeightedSize int
		totalTokens       int
	)

	for _, field := range indexFields {
		value, exists := doc[field]
		if !exists {
			continue
		}

		var tokens []string
		switch v := value.(type) {
		case string:
			tokens = Tokenize(v)
		case []string:
			for _, item := range v {
				tokens = append(tokens, Tokenize(item)...)
			}
		case []interface{}:
			for _, item := range v {
				if str, ok := item.(string); ok {
					tokens = append(tokens, Tokenize(str)...)
				}
			}
		}

		if len(tokens) == 0 {
			continue
		}

		weight := fieldWeights[field]
		if weight <= 0 {
			weight = 1
		}
		sets = append(sets, fieldTokenSet{tokens: tokens, weight: weight})
		totalWeightedSize += len(tokens) * weight
		totalTokens += len(tokens)
	}

	if totalWeightedSize == 0 {
		return nil
	}

	localScores := make(map[string]int, totalTokens)
	for _, set := range sets {
		normalizedScore := 100_000 * set.weight / totalWeightedSize
		if normalizedScore == 0 {
			normalizedScore = 1
		}
		for _, token := range set.tokens {
			localScores[token] += normalizedScore
		}
	}
	return localScores
}
