package symspell

import "sort"

// SymSpell implements the Symmetric-Delete spelling correction algorithm
// for edit-distance-1 fuzzy search. It precomputes all single-character
// deletions of each dictionary word and stores them in a map for O(m) lookup.
//
// Thread safety: SymSpell has no internal lock. Callers must ensure that
// AddWord/DeleteWord are not called concurrently with FuzzySearch. In the
// engine this is guaranteed by se.mu (writes under Lock, reads under RLock).

type SymSpell struct {
	DeleteMap map[string][]string
}

func NewSymSpell() *SymSpell {
	return &SymSpell{
		DeleteMap: make(map[string][]string),
	}
}

// AddWord indexes a new word by generating all its single-rune deletes.
func (s *SymSpell) AddWord(word string) {
	s.appendUnique(word, word)
	runes := []rune(word)
	if len(runes) < 2 {
		return
	}
	buf := make([]rune, len(runes)-1)
	for i := range runes {
		copy(buf, runes[:i])
		copy(buf[i:], runes[i+1:])
		s.appendUnique(string(buf), word)
	}
}

// appendUnique adds word to DeleteMap[key] only if not already present.
func (s *SymSpell) appendUnique(key, word string) {
	for _, w := range s.DeleteMap[key] {
		if w == word {
			return
		}
	}
	s.DeleteMap[key] = append(s.DeleteMap[key], word)
}

// SortByFrequency orders every delete bucket by descending word frequency (as
// reported by freqOf), with alphabetical tie-breaking for determinism. Because
// FuzzySearch walks each bucket front-to-back, keeping the buckets frequency-
// sorted lets the most common corrections surface first with zero per-query
// sorting. Callers invoke this once at an index finalize point (Index,
// CompactDeleted, LoadAll) after all words are present, so FuzzySearch stays
// allocation- and sort-free on the hot path.
func (s *SymSpell) SortByFrequency(freqOf func(word string) int) {
	for _, words := range s.DeleteMap {
		if len(words) < 2 {
			continue
		}
		// In-place sort mutates the slice's backing array, which the map entry
		// already references — no reassignment needed.
		sort.Slice(words, func(i, j int) bool {
			fi, fj := freqOf(words[i]), freqOf(words[j])
			if fi != fj {
				return fi > fj
			}
			return words[i] < words[j]
		})
	}
}

// LoadDictionary adds all words in the slice to the SymSpell index.
func (s *SymSpell) LoadDictionary(words []string) {
	for _, w := range words {
		s.AddWord(w)
	}
}

// DeleteWord removes a word from the SymSpell index.
func (s *SymSpell) DeleteWord(word string) {
	s.removeFrom(word, word)
	runes := []rune(word)
	if len(runes) < 2 {
		return
	}
	buf := make([]rune, len(runes)-1)
	for i := range runes {
		copy(buf, runes[:i])
		copy(buf[i:], runes[i+1:])
		s.removeFrom(string(buf), word)
	}
}

// removeFrom removes word from DeleteMap[key], deleting the key if empty.
func (s *SymSpell) removeFrom(key, word string) {
	words := s.DeleteMap[key]
	for i, w := range words {
		if w == word {
			last := len(words) - 1
			words[i] = words[last]
			s.DeleteMap[key] = words[:last]
			if last == 0 {
				delete(s.DeleteMap, key)
			}
			return
		}
	}
}

// FuzzySearch returns dictionary words within Levenshtein distance ≤ 1 of query,
// in insertion order. If maxReturnCount > 0 the result is capped at that many
// words; if maxReturnCount <= 0 the full ED1 candidate set is returned so the
// caller can rank it (e.g. by corpus frequency) before truncating. Caller must
// hold se.mu.RLock().
func (s *SymSpell) FuzzySearch(query string, maxReturnCount int) []string {
	results := []string{}

	// Bounded queries (the search hot path uses limits ≤ 50) dedupe with a
	// linear scan over the small result slice; only unbounded/large requests
	// pay for a map allocation.
	var seen map[string]struct{}
	if maxReturnCount <= 0 || maxReturnCount > 64 {
		seen = make(map[string]struct{})
	}
	isDup := func(w string) bool {
		if seen != nil {
			if _, dup := seen[w]; dup {
				return true
			}
			seen[w] = struct{}{}
			return false
		}
		for _, r := range results {
			if r == w {
				return true
			}
		}
		return false
	}

	add := func(key string) bool {
		for _, w := range s.DeleteMap[key] {
			if w == query {
				continue
			}
			if isDup(w) {
				continue
			}
			results = append(results, w)
			if maxReturnCount > 0 && len(results) >= maxReturnCount {
				return true
			}
		}
		return false
	}

	if add(query) {
		return results
	}

	runes := []rune(query)
	if len(runes) < 2 {
		return results
	}

	buf := make([]rune, len(runes)-1)
	for i := range runes {
		copy(buf, runes[:i])
		copy(buf[i:], runes[i+1:])
		if add(string(buf)) {
			break
		}
	}

	return results
}
