package engine

import (
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
// It walks the string once, so no intermediate word slice is allocated.
func Tokenize(content string) []string {
	var tokens []string
	i := 0
	for i < len(content) {
		// Skip whitespace.
		for i < len(content) {
			if c := content[i]; c < utf8.RuneSelf {
				if asciiSpace[c] {
					i++
					continue
				}
			} else if r, size := utf8.DecodeRuneInString(content[i:]); unicode.IsSpace(r) {
				i += size
				continue
			}
			break
		}
		if i >= len(content) {
			break
		}
		// Consume one word.
		start := i
		for i < len(content) {
			if c := content[i]; c < utf8.RuneSelf {
				if asciiSpace[c] {
					break
				}
				i++
			} else {
				r, size := utf8.DecodeRuneInString(content[i:])
				if unicode.IsSpace(r) {
					break
				}
				i += size
			}
		}
		word := alphaNumericLower(content[start:i])
		if word != "" && !stopWords[word] {
			tokens = append(tokens, word)
		}
	}
	return tokens
}

// asciiSpace mirrors the whitespace set used by strings.Fields for ASCII bytes.
var asciiSpace = [utf8.RuneSelf]bool{'\t': true, '\n': true, '\v': true, '\f': true, '\r': true, ' ': true}

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
