package engine

import "strings"

// stopWords lists very common words excluded from indexing.
var stopWords = map[string]bool{
	"a":   true,
	"the": true,
	"and": true,
}

// Tokenize splits text into lowercase alphanumeric tokens, dropping stopwords.
func Tokenize(content string) []string {
	words := strings.Fields(content)
	tokens := make([]string, 0, len(words))
	for _, word := range words {
		word = alphaNumericLower(word)
		if word != "" && !stopWords[word] {
			tokens = append(tokens, word)
		}
	}
	return tokens
}

// alphaNumericLower strips non-ASCII-alphanumeric bytes and lowercases A-Z.
// For strings that are already clean lowercase ASCII it returns s unchanged (zero allocation).
func alphaNumericLower(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			continue
		}
		// First byte that needs work: copy the clean prefix and transform the rest.
		b := make([]byte, i, len(s))
		copy(b, s[:i])
		for j := i; j < len(s); j++ {
			c = s[j]
			if c >= 'A' && c <= 'Z' {
				b = append(b, c+32)
			} else if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
				b = append(b, c)
			}
		}
		return string(b)
	}
	return s
}
