package symspell

import (
	"reflect"
	"sort"
	"testing"
)

func TestSymSpell_FuzzySearch(t *testing.T) {
	words := []string{"black", "block", "back", "blacks", "slack", "flack"}
	ss := NewSymSpell()
	ss.LoadDictionary(words)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "Substitution",
			query: "blacj",
			want:  []string{"black"},
		},
		{
			name:  "Deletion",
			query: "blac",
			want:  []string{"black", "back"},
		},
		{
			name:  "Insertion",
			query: "blackso",
			want:  []string{"blacks"},
		},
		{
			name:  "MultipleMatches",
			query: "lack",
			want:  []string{"black", "flack", "slack", "back"},
		},
		{
			name:  "NoMatch",
			query: "xyz",
			want:  []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ss.FuzzySearch(tc.query, 5)
			sort.Strings(got)
			sort.Strings(tc.want)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("FuzzySearch(%q) = %v; want %v", tc.query, got, tc.want)
			}
		})
	}
}

func TestAddWord_Idempotent(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("test")
	ss.AddWord("test")
	got := ss.FuzzySearch("tes", 5)
	if len(got) != 1 || got[0] != "test" {
		t.Errorf("Duplicate AddWord should not create multiple entries, got %v", got)
	}
}

func TestEmptyDictionary(t *testing.T) {
	ss := NewSymSpell()
	got := ss.FuzzySearch("anything", 5)
	if len(got) != 0 {
		t.Errorf("Empty dictionary: FuzzySearch should return empty slice, got %v", got)
	}
}

func TestDeleteWord_RemovesWordAndVariants(t *testing.T) {
	words := []string{"black", "block", "back", "blacks", "slack", "flack"}
	ss := NewSymSpell()
	ss.LoadDictionary(words)

	got := ss.FuzzySearch("lack", 5)
	want := []string{"black", "flack", "slack", "back"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FuzzySearch(\"lack\") = %v; want %v", got, want)
	}

	ss.DeleteWord("slack")

	got = ss.FuzzySearch("lack", 5)
	want = []string{"black", "flack", "back"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FuzzySearch(\"lack\") = %v; want %v", got, want)
	}
}

func TestSymSpell_FuzzySearch_FirstLastChar(t *testing.T) {
	words := []string{"iphone", "phane", "phone1"}
	ss := NewSymSpell()
	ss.LoadDictionary(words)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "Substitution",
			query: "phone",
			want:  []string{"iphone", "phane", "phone1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ss.FuzzySearch(tc.query, 5)
			sort.Strings(got)
			sort.Strings(tc.want)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("FuzzySearch(%q) = %v; want %v", tc.query, got, tc.want)
			}
		})
	}
}

// TestFuzzySearch_MaxReturnCount verifies results are capped at maxReturnCount.
func TestFuzzySearch_MaxReturnCount(t *testing.T) {
	ss := NewSymSpell()
	ss.LoadDictionary([]string{"black", "flack", "slack", "back", "block"})
	got := ss.FuzzySearch("lack", 2)
	if len(got) != 2 {
		t.Errorf("expected 2 results, got %d: %v", len(got), got)
	}
}

// TestFuzzySearch_MaxReturnCountOne verifies maxReturnCount=1 returns exactly one result.
func TestFuzzySearch_MaxReturnCountOne(t *testing.T) {
	ss := NewSymSpell()
	ss.LoadDictionary([]string{"cat", "car", "bar", "bat"})
	got := ss.FuzzySearch("ca", 1)
	if len(got) != 1 {
		t.Errorf("expected exactly 1 result, got %d: %v", len(got), got)
	}
}

// TestFuzzySearch_QueryInDictionary checks that the query itself is not returned.
func TestFuzzySearch_QueryInDictionary(t *testing.T) {
	ss := NewSymSpell()
	ss.LoadDictionary([]string{"test", "text", "tent", "rest"})
	got := ss.FuzzySearch("test", 10)
	for _, w := range got {
		if w == "test" {
			t.Errorf("FuzzySearch returned the query word itself: %v", got)
		}
	}
}

// TestFuzzySearch_ShortQuery_LenOne covers the single-character query path.
func TestFuzzySearch_ShortQuery_LenOne(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("a")
	ss.AddWord("b")
	// "a" exact match returns nothing (query skipped); "b" is edit-distance 1 via substitution
	// but SymSpell only does deletes, so "b" won't be found via "a"'s deletions.
	// An empty query produces no deletions so we just ensure no panic.
	got := ss.FuzzySearch("a", 5)
	// "a" should not return itself; len(query)<2 so no deletions generated
	for _, w := range got {
		if w == "a" {
			t.Errorf("FuzzySearch returned the query word itself")
		}
	}
}

// TestFuzzySearch_ShortQuery_LenTwo covers the two-character edge case.
func TestFuzzySearch_ShortQuery_LenTwo(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("ab")
	ss.AddWord("ac")
	ss.AddWord("abc")
	// query "ab": exact match skipped, deletion "a" and "b" checked
	got := ss.FuzzySearch("ab", 5)
	for _, w := range got {
		if w == "ab" {
			t.Errorf("FuzzySearch returned the query word itself")
		}
	}
	// "abc" is edit-distance 1 (insertion) and should appear
	found := false
	for _, w := range got {
		if w == "abc" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'abc' (edit-distance 1) in results, got %v", got)
	}
}

// TestDeleteWord_NonExistent ensures no panic when deleting a word not in the index.
func TestDeleteWord_NonExistent(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("hello")
	ss.DeleteWord("world") // should not panic
	got := ss.FuzzySearch("hell", 5)
	if len(got) != 1 || got[0] != "hello" {
		t.Errorf("expected [hello], got %v", got)
	}
}

// TestDeleteWord_AllWords deletes every word and expects an empty DeleteMap.
func TestDeleteWord_AllWords(t *testing.T) {
	ss := NewSymSpell()
	words := []string{"cat", "dog", "fish"}
	ss.LoadDictionary(words)
	for _, w := range words {
		ss.DeleteWord(w)
	}
	if len(ss.DeleteMap) != 0 {
		t.Errorf("DeleteMap should be empty after deleting all words, got %d keys", len(ss.DeleteMap))
	}
}

// TestDeleteWord_MiddleElement checks swap-with-last correctness when removing a middle entry.
func TestDeleteWord_MiddleElement(t *testing.T) {
	ss := NewSymSpell()
	// "lack" is a shared deletion key for black/slack/flack; removing one must not disturb others
	ss.LoadDictionary([]string{"black", "slack", "flack"})
	ss.DeleteWord("slack")
	got := ss.FuzzySearch("lack", 5)
	sort.Strings(got)
	want := []string{"black", "flack"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after deleting slack: got %v, want %v", got, want)
	}
}

// TestDeleteWord_LastElement checks removing the only/last entry in a deletion bucket.
func TestDeleteWord_LastElement(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("only")
	ss.DeleteWord("only")
	// All deletion keys for "only" should be gone
	got := ss.FuzzySearch("onl", 5)
	if len(got) != 0 {
		t.Errorf("expected no results after deleting only word, got %v", got)
	}
}

// TestFuzzySearch_NoDuplicates verifies no word appears twice even when reachable via multiple deletion keys.
func TestFuzzySearch_NoDuplicates(t *testing.T) {
	ss := NewSymSpell()
	// "abcd" generates deletions "bcd","acd","abd","abc" — querying "abcd" hits the exact key
	// and deletions. The word must appear at most once.
	ss.AddWord("abcd")
	ss.AddWord("abce")
	got := ss.FuzzySearch("abce", 10)
	seen := map[string]int{}
	for _, w := range got {
		seen[w]++
	}
	for w, count := range seen {
		if count > 1 {
			t.Errorf("word %q appears %d times in results", w, count)
		}
	}
}

// TestLoadDictionary verifies all words are searchable after a bulk load.
func TestLoadDictionary(t *testing.T) {
	ss := NewSymSpell()
	words := []string{"alpha", "beta", "gamma", "delta"}
	ss.LoadDictionary(words)
	// "alph" → edit-distance 1 deletion of "alpha"
	got := ss.FuzzySearch("alph", 5)
	found := false
	for _, w := range got {
		if w == "alpha" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'alpha' after LoadDictionary, got %v", got)
	}
}

// TestFuzzySearch_RepeatedChars checks words with repeated characters don't cause issues.
func TestFuzzySearch_RepeatedChars(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("aabb")
	ss.AddWord("abb")
	// "aab" is edit-distance 1 deletion of "aabb"
	got := ss.FuzzySearch("aab", 5)
	found := false
	for _, w := range got {
		if w == "aabb" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'aabb' in results for query 'aab', got %v", got)
	}
}

// TestAddWord_SharedDeletionKeys ensures words sharing deletion variants don't contaminate each other.
func TestAddWord_SharedDeletionKeys(t *testing.T) {
	ss := NewSymSpell()
	// both "care" and "core" share the deletion key "ore" (delete first char of "core", delete 'c' of "care"→"are" no...
	// simpler: "cat" and "bat" both produce "at" as a deletion key
	ss.AddWord("cat")
	ss.AddWord("bat")
	ss.DeleteWord("cat")
	// "at" key should still carry "bat"
	got := ss.FuzzySearch("at", 5)
	for _, w := range got {
		if w == "cat" {
			t.Errorf("deleted word 'cat' still returned: %v", got)
		}
	}
}

// TestFuzzySearch_ExactMatchNotReturned verifies a query word in dict is excluded from results.
func TestFuzzySearch_ExactMatchNotReturned(t *testing.T) {
	ss := NewSymSpell()
	ss.LoadDictionary([]string{"hello", "helo", "hell", "jello"})
	got := ss.FuzzySearch("hello", 10)
	for _, w := range got {
		if w == "hello" {
			t.Errorf("query word 'hello' should not appear in results, got %v", got)
		}
	}
	// "helo", "hell", "jello" are all edit-distance 1 and should appear
	want := []string{"helo", "hell", "jello"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v; want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Unicode / multi-script tests
// ---------------------------------------------------------------------------

// TestSymSpell_CJK verifies that rune-based deletions work for CJK characters.
// Byte-based deletion would produce invalid UTF-8 keys.
func TestSymSpell_CJK(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("日本語") // Japanese
	ss.AddWord("音楽")   // music
	ss.AddWord("日本")   // Japan (shares prefix)

	tests := []struct {
		query string
		want  string
	}{
		// "日本" is a 1-rune deletion of "日本語" (drop last rune)
		{"日本", "日本語"},
		// "本語" is a 1-rune deletion of "日本語" (drop first rune)
		{"本語", "日本語"},
		// "日語" is a 1-rune deletion of "日本語" (drop middle rune)
		{"日語", "日本語"},
	}
	for _, tc := range tests {
		found := false
		for _, w := range ss.FuzzySearch(tc.query, 10) {
			if w == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("FuzzySearch(%q): want %q in results, got %v", tc.query, tc.want, ss.FuzzySearch(tc.query, 10))
		}
	}
}

// TestSymSpell_Cyrillic verifies fuzzy matching for Cyrillic words.
func TestSymSpell_Cyrillic(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("привет") // hello
	ss.AddWord("привет") // duplicate — idempotent

	// "приве" = delete last rune → should find "привет"
	got := ss.FuzzySearch("приве", 5)
	found := false
	for _, w := range got {
		if w == "привет" {
			found = true
		}
	}
	if !found {
		t.Errorf("FuzzySearch(\"приве\") expected \"привет\", got %v", got)
	}
}

// TestSymSpell_Greek verifies fuzzy matching for Greek words including accented letters.
func TestSymSpell_Greek(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("ωμέγα")
	ss.AddWord("ωμεγα")

	// "ωμέγ" = delete last rune of "ωμέγα"
	got := ss.FuzzySearch("ωμέγ", 5)
	found := false
	for _, w := range got {
		if w == "ωμέγα" {
			found = true
		}
	}
	if !found {
		t.Errorf("FuzzySearch(\"ωμέγ\") expected \"ωμέγα\", got %v", got)
	}
}

// TestSymSpell_Arabic verifies that Arabic letters (multi-byte, no case) are handled correctly.
func TestSymSpell_Arabic(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("موسيقى") // music in Arabic

	// "موسيق" = delete last rune (ى)
	got := ss.FuzzySearch("موسيق", 5)
	found := false
	for _, w := range got {
		if w == "موسيقى" {
			found = true
		}
	}
	if !found {
		t.Errorf("FuzzySearch(\"موسيق\") expected \"موسيقى\", got %v", got)
	}
}

// TestSymSpell_Thai verifies Thai words with vowel marks (IsMark runes) work end-to-end.
func TestSymSpell_Thai(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("ดนตรี") // music in Thai (5 runes: ด น ต ร ี)

	// "ดนตร" = delete last rune (ี, a combining vowel mark)
	got := ss.FuzzySearch("ดนตร", 5)
	found := false
	for _, w := range got {
		if w == "ดนตรี" {
			found = true
		}
	}
	if !found {
		t.Errorf("FuzzySearch(\"ดนตร\") expected \"ดนตรี\", got %v", got)
	}
}

// TestSymSpell_Devanagari verifies Devanagari words (with virama/vowel mark runes).
func TestSymSpell_Devanagari(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("संगीत") // music in Hindi (7 runes including marks)

	// Delete the last rune (त)
	runes := []rune("संगीत")
	queryRunes := runes[:len(runes)-1]
	query := string(queryRunes)

	got := ss.FuzzySearch(query, 5)
	found := false
	for _, w := range got {
		if w == "संगीत" {
			found = true
		}
	}
	if !found {
		t.Errorf("FuzzySearch(%q) expected \"संगीत\", got %v", query, got)
	}
}

// TestSymSpell_DeleteWord_Unicode verifies DeleteWord works for multi-byte rune words.
func TestSymSpell_DeleteWord_Unicode(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("日本語")
	ss.AddWord("日本")

	ss.DeleteWord("日本語")

	// "本語" was a deletion key for "日本語"; after delete it should no longer return it
	for _, w := range ss.FuzzySearch("本語", 10) {
		if w == "日本語" {
			t.Errorf("deleted word \"日本語\" still appears in FuzzySearch results")
		}
	}
	// "日本" itself must still be findable (it was not deleted)
	got := ss.FuzzySearch("日", 10) // "日" is a deletion of "日本" (drop 本)
	found := false
	for _, w := range got {
		if w == "日本" {
			found = true
		}
	}
	if !found {
		t.Errorf("\"日本\" should still be in index after deleting \"日本語\", got %v", got)
	}
}

// TestSymSpell_MultiScript verifies a mixed-language dictionary works correctly.
func TestSymSpell_MultiScript(t *testing.T) {
	ss := NewSymSpell()
	words := []string{"hello", "привет", "مرحبا", "שלום", "こんにちは"}
	ss.LoadDictionary(words)

	cases := []struct {
		query string // one rune deleted from the front of each word
		want  string
	}{
		{"ello", "hello"},
		{"ривет", "привет"},
		{"رحبا", "مرحبا"},
		{"לום", "שלום"},
		{"んにちは", "こんにちは"},
	}
	for _, c := range cases {
		got := ss.FuzzySearch(c.query, 10)
		found := false
		for _, w := range got {
			if w == c.want {
				found = true
			}
		}
		if !found {
			t.Errorf("FuzzySearch(%q): want %q in results, got %v", c.query, c.want, got)
		}
	}
}

// TestSymSpell_NoByteSplitOnUnicode ensures delete variants are always valid UTF-8.
// A byte-based implementation would produce invalid UTF-8 keys for multi-byte runes.
func TestSymSpell_NoByteSplitOnUnicode(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("中文歌曲") // Chinese: 4 runes, each 3 bytes

	for key := range ss.DeleteMap {
		if !isValidUTF8(key) {
			t.Errorf("DeleteMap contains invalid UTF-8 key: %x", []byte(key))
		}
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// TestDeleteWord_ThenReAdd verifies a word can be re-added after deletion.
func TestDeleteWord_ThenReAdd(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("rocket")
	ss.DeleteWord("rocket")
	ss.AddWord("rocket")
	got := ss.FuzzySearch("rocke", 5)
	found := false
	for _, w := range got {
		if w == "rocket" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'rocket' after re-adding, got %v", got)
	}
}

// TestSortByFrequency_OrdersBucketByFreqDesc verifies that a shared delete
// bucket is reordered by descending frequency, with alphabetical tie-breaking,
// regardless of the order in which the words were added.
func TestSortByFrequency_OrdersBucketByFreqDesc(t *testing.T) {
	ss := NewSymSpell()
	// Added rare-first so insertion order does NOT match frequency.
	for _, w := range []string{"irok", "irop", "iron"} {
		ss.AddWord(w)
	}
	freq := map[string]int{"iron": 100, "irok": 1, "irop": 1}

	ss.SortByFrequency(func(w string) int { return freq[w] })

	// All three delete (last rune) to "iro".
	got := ss.DeleteMap["iro"]
	want := []string{"iron", "irok", "irop"} // freq desc, then alpha for the ties
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeleteMap[\"iro\"] = %v; want %v", got, want)
	}
}

// TestSortByFrequency_FuzzySearchReturnsHighFreqFirst verifies that after
// sorting, FuzzySearch surfaces the highest-frequency correction first even
// under a cap smaller than the number of candidates.
func TestSortByFrequency_FuzzySearchReturnsHighFreqFirst(t *testing.T) {
	ss := NewSymSpell()
	for _, w := range []string{"irok", "irop", "iroq", "iros", "iron"} {
		ss.AddWord(w)
	}
	freq := map[string]int{"iron": 100, "irok": 1, "irop": 1, "iroq": 1, "iros": 1}

	ss.SortByFrequency(func(w string) int { return freq[w] })

	got := ss.FuzzySearch("irom", 1) // cap of 1 keeps only the best correction
	if len(got) != 1 || got[0] != "iron" {
		t.Fatalf("FuzzySearch(\"irom\", 1) = %v; want [iron]", got)
	}
}

// TestSortByFrequency_TieBreakAlphabetical verifies deterministic alphabetical
// ordering when frequencies are equal.
func TestSortByFrequency_TieBreakAlphabetical(t *testing.T) {
	ss := NewSymSpell()
	for _, w := range []string{"irob", "iroa", "iroc"} {
		ss.AddWord(w)
	}
	ss.SortByFrequency(func(string) int { return 5 }) // all equal frequency

	got := ss.DeleteMap["iro"]
	want := []string{"iroa", "irob", "iroc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeleteMap[\"iro\"] = %v; want %v", got, want)
	}
}

// TestSortByFrequency_SingletonBucketNoPanic verifies that buckets with fewer
// than two entries are left untouched and do not invoke freqOf.
func TestSortByFrequency_SingletonBucketNoPanic(t *testing.T) {
	ss := NewSymSpell()
	ss.AddWord("hello")

	called := false
	ss.SortByFrequency(func(string) int { called = true; return 0 })

	if called {
		t.Fatalf("freqOf should not be called for singleton buckets")
	}
	got := ss.FuzzySearch("hallo", 5)
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("FuzzySearch(\"hallo\") = %v; want [hello]", got)
	}
}
