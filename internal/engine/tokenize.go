package engine

import (
	"regexp"
	"strings"
)

// nonAlphaNumeric is compiled once and reused by Tokenize.
var nonAlphaNumeric = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// TODO: make it a variable
// stopWords lists very common words excluded from indexing.
var stopWords = map[string]bool{
	"a":   true,
	"the": true,
	"and": true,
}

// Tokenize splits text into tokens by lowercasing, stripping non-alphanumeric,
// and dropping stopwords.
func Tokenize(content string) []string {
	words := strings.Fields(content)
	var tokens []string
	for _, word := range words {
		word = nonAlphaNumeric.ReplaceAllString(strings.ToLower(word), "")
		if word != "" && !stopWords[word] {
			tokens = append(tokens, word)
		}
	}
	return tokens
}
