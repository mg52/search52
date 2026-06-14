package engine

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

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

// alphaNumericLower lowercases ASCII letters, passes through Unicode letters and
// digits, and strips everything else. For strings that are already clean lowercase
// ASCII alphanumeric it returns s unchanged (zero allocation).
func alphaNumericLower(s string) string {
	// Fast path: scan for the first byte that needs work.
	i := 0
	for i < len(s) {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			i++
			continue
		}
		break
	}
	if i == len(s) {
		return s
	}

	b := make([]byte, i, len(s))
	copy(b, s[:i])

	for i < len(s) {
		c := s[i]
		if c < 0x80 {
			if c >= 'A' && c <= 'Z' {
				b = append(b, c+32)
			} else if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
				b = append(b, c)
			}
			// else: non-alphanumeric ASCII, strip
			i++
		} else {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r != utf8.RuneError && (unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)) {
				b = utf8.AppendRune(b, unicode.ToLower(r))
			}
			i += size
		}
	}
	return string(b)
}
