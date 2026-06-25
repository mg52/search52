package engine

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
)

// ---- shared helpers ----

func newCompactSE(t *testing.T) *SearchEngine {
	t.Helper()
	return NewSearchEngine(
		[]string{"title", "artist"},
		nil,
		map[string]bool{"genre": true, "year": true},
		10,
	)
}

func addDoc(t *testing.T, se *SearchEngine, doc map[string]interface{}) {
	t.Helper()
	if err := se.AddOrUpdateDocument(doc); err != nil {
		t.Fatalf("AddOrUpdateDocument %v: %v", doc["id"], err)
	}
}

func delDoc(t *testing.T, se *SearchEngine, id string) {
	t.Helper()
	if !se.DeleteDocument(id) {
		t.Fatalf("DeleteDocument(%q) returned false", id)
	}
}

// termInDataMap checks whether a term key exists in DataMap under RLock.
func termInDataMap(se *SearchEngine, term string) bool {
	se.mu.RLock()
	defer se.mu.RUnlock()
	_, ok := se.DataMap[term]
	return ok
}

// termInTermSet checks se.termSet (atomic.Pointer[sync.Map]).
func termInTermSet(se *SearchEngine, term string) bool {
	_, ok := se.termSet.Load().Load(term)
	return ok
}

// prefixList returns a copy of Prefix[pfx] under RLock.
func prefixList(se *SearchEngine, pfx string) []string {
	se.mu.RLock()
	defer se.mu.RUnlock()
	return append([]string(nil), se.Prefix[pfx]...)
}

// docDeletedLen returns len(DocDeleted) under RLock.
func docDeletedLen(se *SearchEngine) int {
	se.mu.RLock()
	defer se.mu.RUnlock()
	return len(se.DocDeleted)
}

// storedDocCount returns len(Documents) under RLock.
func storedDocCount(se *SearchEngine) int {
	se.mu.RLock()
	defer se.mu.RUnlock()
	return len(se.Documents)
}

// idMapsConsistent verifies ExternalToInternal and InternalToExternal are
// inverse of each other, under RLock.
func idMapsConsistent(t *testing.T, se *SearchEngine) {
	t.Helper()
	se.mu.RLock()
	defer se.mu.RUnlock()
	if len(se.ExternalToInternal) != len(se.InternalToExternal) {
		t.Fatalf("ID map size mismatch: ext→int=%d int→ext=%d",
			len(se.ExternalToInternal), len(se.InternalToExternal))
	}
	for ext, internalID := range se.ExternalToInternal {
		if se.InternalToExternal[internalID] != ext {
			t.Fatalf("ID maps inconsistent for extID=%q: InternalToExternal[%d]=%q",
				ext, internalID, se.InternalToExternal[internalID])
		}
	}
}

// allInternalIDsInDocuments checks that every internal ID in ExternalToInternal
// has a corresponding entry in Documents.
func allInternalIDsInDocuments(t *testing.T, se *SearchEngine) {
	t.Helper()
	se.mu.RLock()
	defer se.mu.RUnlock()
	for ext, internalID := range se.ExternalToInternal {
		if _, ok := se.Documents[internalID]; !ok {
			t.Fatalf("extID=%q internalID=%d not found in Documents", ext, internalID)
		}
	}
}

// ---- tests ----

func TestCompactDeleted_Stats(t *testing.T) {
	se := newCompactSE(t)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "iron maiden", "genre": "metal", "year": "1980"})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "black sabbath", "genre": "metal", "year": "1970"})
	// Update doc1 — creates a second internal version (tombstone).
	addDoc(t, se, map[string]interface{}{"id": "1", "title": "iron maiden updated", "genre": "metal", "year": "1981"})
	delDoc(t, se, "2")

	// 1 stored (only current version of doc1 — old version removed by AddOrUpdate, doc2 removed by DeleteDocument).
	stats := se.CompactDeleted()

	if stats.BeforeStored != 1 {
		t.Errorf("BeforeStored: got %d want 1", stats.BeforeStored)
	}
	if stats.BeforeActive != 1 {
		t.Errorf("BeforeActive: got %d want 1", stats.BeforeActive)
	}
	if stats.AfterStored != 1 {
		t.Errorf("AfterStored: got %d want 1", stats.AfterStored)
	}
	if stats.AfterActive != 1 {
		t.Errorf("AfterActive: got %d want 1", stats.AfterActive)
	}
	if stats.RemovedVersions != 0 {
		t.Errorf("RemovedVersions: got %d want 0", stats.RemovedVersions)
	}
}

func TestCompactDeleted_NoTombstonesRemain(t *testing.T) {
	se := newCompactSE(t)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "hello"})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "world"})
	addDoc(t, se, map[string]interface{}{"id": "1", "title": "hello updated"}) // creates tombstone
	delDoc(t, se, "2")

	se.CompactDeleted()

	if n := docDeletedLen(se); n != 0 {
		t.Errorf("DocDeleted should be empty after compact, got %d entries", n)
	}
}

func TestCompactDeleted_DocumentsMapClean(t *testing.T) {
	se := newCompactSE(t)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "keep"})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "remove"})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "remove updated"}) // tombstone on first v2
	delDoc(t, se, "2")

	se.CompactDeleted()

	if n := storedDocCount(se); n != 1 {
		t.Errorf("Documents should have 1 entry after compact, got %d", n)
	}
}

func TestCompactDeleted_DataMapDropsDeletedOnlyTerms(t *testing.T) {
	se := newCompactSE(t)

	// "vanish" appears only in deleted doc.
	addDoc(t, se, map[string]interface{}{"id": "1", "title": "vanish"})
	// "survive" appears only in active doc.
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "survive"})
	delDoc(t, se, "1")

	se.CompactDeleted()

	if termInDataMap(se, "vanish") {
		t.Error("term 'vanish' should be removed from DataMap after compact")
	}
	if !termInDataMap(se, "survive") {
		t.Error("term 'survive' should remain in DataMap after compact")
	}
}

func TestCompactDeleted_DataMapDropsOldVersionTerms(t *testing.T) {
	se := newCompactSE(t)

	// "oldterm" only in v1, "newterm" only in v2.
	addDoc(t, se, map[string]interface{}{"id": "1", "title": "oldterm"})
	addDoc(t, se, map[string]interface{}{"id": "1", "title": "newterm"}) // replaces v1

	se.CompactDeleted()

	if termInDataMap(se, "oldterm") {
		t.Error("'oldterm' from superseded version should be removed from DataMap")
	}
	if !termInDataMap(se, "newterm") {
		t.Error("'newterm' from active version should remain in DataMap")
	}
}

func TestCompactDeleted_IDMapsConsistentAfterCompact(t *testing.T) {
	se := newCompactSE(t)

	for i := 1; i <= 5; i++ {
		addDoc(t, se, map[string]interface{}{"id": fmt.Sprintf("%d", i), "title": fmt.Sprintf("doc%d", i)})
	}
	delDoc(t, se, "2")
	delDoc(t, se, "4")
	addDoc(t, se, map[string]interface{}{"id": "3", "title": "doc3 updated"})

	se.CompactDeleted()

	idMapsConsistent(t, se)
	allInternalIDsInDocuments(t, se)
}

func TestCompactDeleted_SingleTermSearchAfterCompact(t *testing.T) {
	se := newCompactSE(t)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "apple orange"})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "banana orange"})
	addDoc(t, se, map[string]interface{}{"id": "3", "title": "cherry"})
	delDoc(t, se, "3")
	addDoc(t, se, map[string]interface{}{"id": "1", "title": "apple grape"}) // removes "orange" from doc1

	se.CompactDeleted()

	assertIDs(t, mustSingleTermSearchLoop(t, se, "apple", nil), "1")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "orange", nil), "2") // doc1 update removed "orange"
	assertIDs(t, mustSingleTermSearchLoop(t, se, "cherry", nil))      // deleted
	assertIDs(t, mustSingleTermSearchLoop(t, se, "grape", nil), "1")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "banana", nil), "2")
}

func TestCompactDeleted_MultiTermSearchAfterCompact(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "apple phone"})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "apple tablet"})
	addDoc(t, se, map[string]interface{}{"id": "3", "title": "samsung phone"})
	addDoc(t, se, map[string]interface{}{"id": "4", "title": "apple phone case"}) // will be deleted
	delDoc(t, se, "4")

	se.CompactDeleted()

	res, _ := se.Search(context.Background(), "apple phone", nil)
	if res == nil {
		t.Fatal("Search returned nil")
	}
	assertIDs(t, res.Docs, "1")
}

func TestCompactDeleted_StringFilterAfterCompact(t *testing.T) {
	se := newCompactSE(t)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "track", "genre": "metal", "year": "1990"})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "track", "genre": "pop", "year": "2000"})
	addDoc(t, se, map[string]interface{}{"id": "3", "title": "track", "genre": "jazz", "year": "1985"})
	delDoc(t, se, "3")

	se.CompactDeleted()

	assertIDs(t, mustSingleTermSearchLoop(t, se, "track", map[string][]interface{}{"genre": {"metal"}}), "1")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "track", map[string][]interface{}{"genre": {"pop"}}), "2")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "track", map[string][]interface{}{"genre": {"jazz"}}))
}

func TestCompactDeleted_NumericFilterAfterCompact(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"year": true}, 10)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "song", "year": 2020})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "song", "year": 2021})
	addDoc(t, se, map[string]interface{}{"id": "3", "title": "song", "year": 2022})
	delDoc(t, se, "3")

	se.CompactDeleted()

	assertIDs(t, mustSingleTermSearchLoop(t, se, "song", map[string][]interface{}{"year": {"2020"}}), "1")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "song", map[string][]interface{}{"year": {"2021"}}), "2")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "song", map[string][]interface{}{"year": {"2022"}}))
}

func TestCompactDeleted_ArrayFilterAfterCompact(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"tags": true}, 10)

	addDoc(t, se, map[string]interface{}{
		"id":    "1",
		"title": "item",
		"tags":  []interface{}{"rock", "live"},
	})
	addDoc(t, se, map[string]interface{}{
		"id":    "2",
		"title": "item",
		"tags":  []interface{}{"pop"},
	})
	addDoc(t, se, map[string]interface{}{
		"id":    "3",
		"title": "item",
		"tags":  []interface{}{"jazz"},
	})
	delDoc(t, se, "3")

	se.CompactDeleted()

	assertIDs(t, mustSingleTermSearchLoop(t, se, "item", map[string][]interface{}{"tags": {"rock"}}), "1")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "item", map[string][]interface{}{"tags": {"live"}}), "1")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "item", map[string][]interface{}{"tags": {"pop"}}), "2")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "item", map[string][]interface{}{"tags": {"jazz"}}))
}

func TestCompactDeleted_PrefixOrderedByFrequency(t *testing.T) {
	if SkipUpdatePrefix {
		t.Skip("prefix ordering disabled")
	}
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	// "pearl" in 3 docs, "peach" in 2 docs, "peanut" in 1 doc.
	for i := 0; i < 3; i++ {
		addDoc(t, se, map[string]interface{}{"id": fmt.Sprintf("pearl-%d", i), "title": "pearl"})
	}
	for i := 0; i < 2; i++ {
		addDoc(t, se, map[string]interface{}{"id": fmt.Sprintf("peach-%d", i), "title": "peach"})
	}
	addDoc(t, se, map[string]interface{}{"id": "peanut-0", "title": "peanut"})

	// "pebble" only in deleted doc.
	addDoc(t, se, map[string]interface{}{"id": "pebble-0", "title": "pebble"})
	delDoc(t, se, "pebble-0")

	se.CompactDeleted()

	pea := prefixList(se, "pea")

	wantFirst3 := []string{"pearl", "peach", "peanut"}
	if len(pea) < len(wantFirst3) {
		t.Fatalf("Prefix['pea'] too short: %v", pea)
	}
	for i, want := range wantFirst3 {
		if pea[i] != want {
			t.Fatalf("Prefix['pea'][%d]: got %q want %q (full list: %v)", i, pea[i], want, pea)
		}
	}
	for _, term := range pea {
		if term == "pebble" {
			t.Errorf("Prefix['pea'] should not contain deleted-only term 'pebble': %v", pea)
		}
	}
}

func TestCompactDeleted_PrefixDropsDeletedOnlyTerms(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "keep"})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "kill"})
	delDoc(t, se, "2")

	se.CompactDeleted()

	// "k" prefix should contain "keep" but not "kill".
	list := prefixList(se, "k")
	hasKeep, hasKill := false, false
	for _, term := range list {
		if term == "keep" {
			hasKeep = true
		}
		if term == "kill" {
			hasKill = true
		}
	}
	if !hasKeep {
		t.Error("Prefix['k'] should contain 'keep'")
	}
	if hasKill {
		t.Error("Prefix['k'] should not contain 'kill' (deleted-only term)")
	}
}

func TestCompactDeleted_SymSpellRebuildAfterCompact(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "mountain"})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "fountain"})
	delDoc(t, se, "2")

	se.CompactDeleted()

	se.mu.RLock()
	defer se.mu.RUnlock()

	// "montain" is edit-distance-1 from "mountain" — should be found.
	hits := se.Symspell.FuzzySearch("montain", 10)
	found := false
	for _, h := range hits {
		if h == "mountain" {
			found = true
		}
	}
	if !found {
		t.Errorf("Symspell should suggest 'mountain' for 'montain', got %v", hits)
	}

	// "foutain" is edit-distance-1 from "fountain" — should NOT be found after deletion.
	hits = se.Symspell.FuzzySearch("foutain", 10)
	for _, h := range hits {
		if h == "fountain" {
			t.Errorf("Symspell should not suggest deleted term 'fountain', got %v", hits)
		}
	}
}

func TestCompactDeleted_TermSetAfterCompact(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "active"})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "gone"})
	delDoc(t, se, "2")

	se.CompactDeleted()

	if !termInTermSet(se, "active") {
		t.Error("termSet should contain 'active'")
	}
	if termInTermSet(se, "gone") {
		t.Error("termSet should not contain 'gone' (deleted-only term)")
	}
}

func TestCompactDeleted_NextIDUsableAfterCompact(t *testing.T) {
	se := newCompactSE(t)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "before"})
	delDoc(t, se, "1")

	se.CompactDeleted()

	// Insert new doc after compact — must not panic or collide.
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "after"})
	assertIDs(t, mustSingleTermSearchLoop(t, se, "after", nil), "2")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "before", nil))
}

func TestCompactDeleted_Idempotent(t *testing.T) {
	se := newCompactSE(t)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "alpha", "genre": "rock"})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "beta", "genre": "pop"})
	addDoc(t, se, map[string]interface{}{"id": "3", "title": "gamma", "genre": "jazz"})
	delDoc(t, se, "3")

	se.CompactDeleted()
	s1 := se.Stats()

	se.CompactDeleted()
	s2 := se.Stats()

	if s1.ActiveDocuments != s2.ActiveDocuments || s1.StoredDocuments != s2.StoredDocuments {
		t.Errorf("compact is not idempotent: after first=%+v after second=%+v", s1, s2)
	}
	assertIDs(t, mustSingleTermSearchLoop(t, se, "alpha", nil), "1")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "beta", nil), "2")
}

func TestCompactDeleted_EmptyEngine(t *testing.T) {
	se := newCompactSE(t)

	stats := se.CompactDeleted()

	if stats.BeforeStored != 0 || stats.AfterStored != 0 {
		t.Errorf("unexpected stats on empty engine: %+v", stats)
	}
	if docDeletedLen(se) != 0 {
		t.Error("DocDeleted should be empty after compact on empty engine")
	}
}

func TestCompactDeleted_AllDocsDeleted(t *testing.T) {
	se := newCompactSE(t)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "gone"})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "also gone"})
	delDoc(t, se, "1")
	delDoc(t, se, "2")

	stats := se.CompactDeleted()

	if stats.AfterStored != 0 || stats.AfterActive != 0 {
		t.Errorf("expected empty engine after deleting all docs: %+v", stats)
	}
	if storedDocCount(se) != 0 {
		t.Error("Documents should be empty after compacting all-deleted engine")
	}
	if docDeletedLen(se) != 0 {
		t.Error("DocDeleted should be empty")
	}

	se.mu.RLock()
	dm := len(se.DataMap)
	se.mu.RUnlock()
	if dm != 0 {
		t.Errorf("DataMap should be empty, got %d terms", dm)
	}
}

func TestCompactDeleted_NoDeletions(t *testing.T) {
	se := newCompactSE(t)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "hello", "genre": "rock"})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "world", "genre": "pop"})

	stats := se.CompactDeleted()

	if stats.RemovedVersions != 0 {
		t.Errorf("expected 0 RemovedVersions when nothing deleted, got %d", stats.RemovedVersions)
	}
	if stats.AfterStored != 2 {
		t.Errorf("AfterStored should be 2, got %d", stats.AfterStored)
	}

	assertIDs(t, mustSingleTermSearchLoop(t, se, "hello", nil), "1")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "world", nil), "2")
}

func TestCompactDeleted_MultipleVersionsOnlyLatestSurvives(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	// Update doc "1" three times.
	addDoc(t, se, map[string]interface{}{"id": "1", "title": "version one"})
	addDoc(t, se, map[string]interface{}{"id": "1", "title": "version two"})
	addDoc(t, se, map[string]interface{}{"id": "1", "title": "version three"})

	stats := se.CompactDeleted()

	if stats.BeforeStored != 1 || stats.AfterStored != 1 {
		t.Errorf("expected 1→1 after compact, got %+v", stats)
	}

	// Only "three" terms remain; "one" and "two" are gone.
	assertIDs(t, mustSingleTermSearchLoop(t, se, "three", nil), "1")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "version", nil), "1")
	if termInDataMap(se, "one") {
		t.Error("stale term 'one' should be purged from DataMap")
	}
	if termInDataMap(se, "two") {
		t.Error("stale term 'two' should be purged from DataMap")
	}
}

func TestCompactDeleted_WeightedFieldsPreservedAfterCompact(t *testing.T) {
	se := NewSearchEngine(
		[]string{"title", "description"},
		map[string]int{"title": 5, "description": 1},
		nil,
		10,
	)

	addDoc(t, se, map[string]interface{}{
		"id":          "1",
		"title":       "unique",
		"description": "generic text",
	})
	addDoc(t, se, map[string]interface{}{
		"id":          "2",
		"title":       "generic text",
		"description": "unique",
	})

	se.CompactDeleted()

	res, err := se.SingleTermSearchLoop(
		context.Background(),
		[]termCandidate{{term: "unique", boost: 1}},
		nil,
	)
	if err != nil {
		t.Fatalf("SingleTermSearchLoop: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res))
	}
	// doc1 has "unique" in title (weight 5) → higher score than doc2 (description weight 1).
	if res[0].ID != "1" {
		t.Errorf("expected doc1 first (title weight > description weight), got %v", res)
	}
}

func TestCompactDeleted_SaveLoadAfterCompact(t *testing.T) {
	se := newCompactSE(t)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "river", "genre": "folk", "year": "2001"})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "delta", "genre": "blues", "year": "1975"})
	addDoc(t, se, map[string]interface{}{"id": "3", "title": "ocean", "genre": "jazz", "year": "1988"})
	addDoc(t, se, map[string]interface{}{"id": "1", "title": "river revised", "genre": "folk", "year": "2005"})
	delDoc(t, se, "3")

	se.CompactDeleted()

	dir := t.TempDir()
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}
	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	assertIDs(t, mustSingleTermSearchLoop(t, loaded, "revised", nil), "1")
	assertIDs(t, mustSingleTermSearchLoop(t, loaded, "river", nil), "1")
	assertIDs(t, mustSingleTermSearchLoop(t, loaded, "delta", nil), "2")
	assertIDs(t, mustSingleTermSearchLoop(t, loaded, "ocean", nil))

	assertIDs(t, mustSingleTermSearchLoop(t, loaded, "river",
		map[string][]interface{}{"genre": {"folk"}}), "1")
	assertIDs(t, mustSingleTermSearchLoop(t, loaded, "delta",
		map[string][]interface{}{"genre": {"blues"}}), "2")
	assertIDs(t, mustSingleTermSearchLoop(t, loaded, "delta",
		map[string][]interface{}{"genre": {"jazz"}}))

	// Verify nextInternalID works after load.
	addDoc(t, loaded, map[string]interface{}{"id": "4", "title": "creek", "genre": "country"})
	assertIDs(t, mustSingleTermSearchLoop(t, loaded, "creek", nil), "4")
}

func TestCompactDeleted_ConcurrentSearchDuringCompact(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"genre": true}, 10)

	// Index a batch of docs so compact has real work to do.
	for i := 0; i < 200; i++ {
		addDoc(t, se, map[string]interface{}{
			"id":    fmt.Sprintf("doc%d", i),
			"title": fmt.Sprintf("song number %d lyrics", i),
			"genre": fmt.Sprintf("genre%d", i%5),
		})
	}
	// Delete half of them.
	for i := 0; i < 200; i += 2 {
		delDoc(t, se, fmt.Sprintf("doc%d", i))
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 32)

	// Launch concurrent searches during compact.
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				res, _ := se.Search(context.Background(), "song", nil)
				_ = res
			}
		}()
	}

	// Run compact in the background concurrently with searches.
	wg.Add(1)
	go func() {
		defer wg.Done()
		se.CompactDeleted()
	}()

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}

	// Post-compact sanity: surviving docs are searchable.
	res, _ := se.Search(context.Background(), "song", nil)
	if res == nil {
		t.Fatal("Search returned nil after concurrent compact")
	}
}

func TestCompactDeleted_LargeBatch(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"year": true}, 20)

	const total = 1000
	for i := 0; i < total; i++ {
		addDoc(t, se, map[string]interface{}{
			"id":    fmt.Sprintf("doc%d", i),
			"title": fmt.Sprintf("track number %d from album", i),
			"year":  fmt.Sprintf("%d", 2000+i%20),
		})
	}
	// Update every 3rd doc (creates tombstones).
	for i := 0; i < total; i += 3 {
		addDoc(t, se, map[string]interface{}{
			"id":    fmt.Sprintf("doc%d", i),
			"title": fmt.Sprintf("updated track %d", i),
			"year":  fmt.Sprintf("%d", 2000+i%20),
		})
	}
	// Delete every 7th doc.
	for i := 0; i < total; i += 7 {
		se.DeleteDocument(fmt.Sprintf("doc%d", i))
	}

	stats := se.CompactDeleted()

	if stats.AfterStored != stats.AfterActive {
		t.Errorf("after compact AfterStored(%d) != AfterActive(%d) — tombstones remain",
			stats.AfterStored, stats.AfterActive)
	}
	if docDeletedLen(se) != 0 {
		t.Errorf("DocDeleted should be empty after large compact, got %d", docDeletedLen(se))
	}


	idMapsConsistent(t, se)
	allInternalIDsInDocuments(t, se)
}

func TestCompactDeleted_PrefixSearchWorksThroughSearchFunc(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	addDoc(t, se, map[string]interface{}{"id": "1", "title": "strawberry"})
	addDoc(t, se, map[string]interface{}{"id": "2", "title": "strawman"})
	addDoc(t, se, map[string]interface{}{"id": "3", "title": "stranger"}) // will be deleted
	delDoc(t, se, "3")

	se.CompactDeleted()

	// Prefix "str" should expand to "strawberry" and "strawman" but not "stranger".
	res, _ := se.Search(context.Background(), "str", nil)
	if res == nil {
		t.Fatal("Search returned nil")
	}
	ids := make(map[string]bool)
	for _, d := range res.Docs {
		ids[d.ID] = true
	}
	if ids["3"] {
		t.Error("deleted doc '3' (stranger) should not appear in prefix search")
	}
	if !ids["1"] || !ids["2"] {
		t.Errorf("expected docs 1 and 2 in prefix search, got %v", res.Docs)
	}
}

func TestCompactDeleted_StatsConsistentWithEngineStats(t *testing.T) {
	se := newCompactSE(t)

	for i := 0; i < 5; i++ {
		addDoc(t, se, map[string]interface{}{
			"id":    fmt.Sprintf("%d", i),
			"title": fmt.Sprintf("track %d", i),
		})
	}
	delDoc(t, se, "1")
	delDoc(t, se, "3")

	statsBefore := se.Stats()
	compact := se.CompactDeleted()
	statsAfter := se.Stats()

	if compact.BeforeStored != statsBefore.StoredDocuments {
		t.Errorf("compact.BeforeStored=%d != Stats.StoredDocuments=%d",
			compact.BeforeStored, statsBefore.StoredDocuments)
	}
	if compact.BeforeActive != statsBefore.ActiveDocuments {
		t.Errorf("compact.BeforeActive=%d != Stats.ActiveDocuments=%d",
			compact.BeforeActive, statsBefore.ActiveDocuments)
	}
	if compact.AfterStored != statsAfter.StoredDocuments {
		t.Errorf("compact.AfterStored=%d != Stats.StoredDocuments=%d",
			compact.AfterStored, statsAfter.StoredDocuments)
	}
	if compact.AfterActive != statsAfter.ActiveDocuments {
		t.Errorf("compact.AfterActive=%d != Stats.ActiveDocuments=%d",
			compact.AfterActive, statsAfter.ActiveDocuments)
	}
}

func TestCompactDeleted_InternalIDsAreContiguous(t *testing.T) {
	se := newCompactSE(t)

	for i := 0; i < 10; i++ {
		addDoc(t, se, map[string]interface{}{"id": fmt.Sprintf("%d", i), "title": "x"})
	}
	for i := 0; i < 10; i += 3 {
		delDoc(t, se, fmt.Sprintf("%d", i))
	}

	se.CompactDeleted()

	se.mu.RLock()
	defer se.mu.RUnlock()

	n := len(se.InternalToExternal)
	ids := make([]int, 0, n)
	for id := range se.InternalToExternal {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)

	for i, id := range ids {
		if id != i+1 {
			t.Fatalf("internal IDs not contiguous after compact: got %v", ids)
		}
	}
	if se.nextInternalID != uint32(n+1) {
		t.Errorf("nextInternalID=%d want %d", se.nextInternalID, n+1)
	}
}
