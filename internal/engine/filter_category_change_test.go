package engine

import (
	"context"
	"testing"
)

// TestFilterCategoryChange_OldCategoryExcludesDoc verifies that after a document's
// category changes, the old category no longer matches it in a filtered search.
func TestFilterCategoryChange_OldCategoryExcludesDoc(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"category": true}, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "running shoes",
		"category": "sports",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument: %v", err)
	}

	// Change category from "sports" to "fashion"
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "running shoes",
		"category": "fashion",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument update: %v", err)
	}

	// Filtering by old category must not return the doc
	res, _ := se.Search(context.Background(), "running", map[string][]interface{}{"category": {"sports"}})
	if res == nil {
		t.Fatal("Search returned nil")
	}
	if len(res.Docs) != 0 {
		t.Fatalf("expected no results for old category 'sports', got %+v", res.Docs)
	}
}

// TestFilterCategoryChange_NewCategoryIncludesDoc verifies that after a document's
// category changes, the new category correctly matches it in a filtered search.
func TestFilterCategoryChange_NewCategoryIncludesDoc(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"category": true}, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "running shoes",
		"category": "sports",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument: %v", err)
	}

	// Change category from "sports" to "fashion"
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "running shoes",
		"category": "fashion",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument update: %v", err)
	}

	// Filtering by new category must return the doc
	res, _ := se.Search(context.Background(), "running", map[string][]interface{}{"category": {"fashion"}})
	if res == nil {
		t.Fatal("Search returned nil")
	}
	assertIDs(t, res.Docs, "1")
}

// TestFilterCategoryChange_NoFilterStillReturnsDoc verifies that an unfiltered
// search still returns the doc after its category changes.
func TestFilterCategoryChange_NoFilterStillReturnsDoc(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"category": true}, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "hiking boots",
		"category": "outdoors",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument: %v", err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "hiking boots",
		"category": "fashion",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument update: %v", err)
	}

	res, _ := se.Search(context.Background(), "hiking", nil)
	if res == nil {
		t.Fatal("Search returned nil")
	}
	assertIDs(t, res.Docs, "1")
}

// TestFilterCategoryChange_DoesNotAffectSiblingDocs verifies that changing one
// document's category does not affect the filter results of other documents.
func TestFilterCategoryChange_DoesNotAffectSiblingDocs(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"category": true}, 10)

	docs := []map[string]interface{}{
		{"id": "1", "title": "trail running", "category": "sports"},
		{"id": "2", "title": "trail mix",    "category": "food"},
		{"id": "3", "title": "trail blazer", "category": "sports"},
	}
	for _, d := range docs {
		if err := se.AddOrUpdateDocument(d); err != nil {
			t.Fatalf("AddOrUpdateDocument: %v", err)
		}
	}

	// Move doc1 from sports → food
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "trail running",
		"category": "food",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument update: %v", err)
	}

	// sports filter should only return doc3
	assertIDs(t, mustSingleTermSearchLoop(t, se, "trail", map[string][]interface{}{"category": {"sports"}}), "3")

	// food filter should return doc1 and doc2
	assertIDs(t, mustSingleTermSearchLoop(t, se, "trail", map[string][]interface{}{"category": {"food"}}), "1", "2")
}

// TestFilterCategoryChange_MultiTerm verifies category-change filter correctness
// on the multi-term search path.
func TestFilterCategoryChange_MultiTerm(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"category": true}, 10)

	docs := []map[string]interface{}{
		{"id": "1", "title": "apple phone", "category": "tech"},
		{"id": "2", "title": "apple phone", "category": "clearance"},
	}
	for _, d := range docs {
		if err := se.AddOrUpdateDocument(d); err != nil {
			t.Fatalf("AddOrUpdateDocument: %v", err)
		}
	}

	// Move doc1 from tech → clearance
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "apple phone",
		"category": "clearance",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument update: %v", err)
	}

	res, _ := se.Search(context.Background(), "apple phone", map[string][]interface{}{"category": {"tech"}})
	if res == nil {
		t.Fatal("Search returned nil")
	}
	if len(res.Docs) != 0 {
		t.Fatalf("expected no tech results after category change, got %+v", res.Docs)
	}

	res, _ = se.Search(context.Background(), "apple phone", map[string][]interface{}{"category": {"clearance"}})
	if res == nil {
		t.Fatal("Search returned nil")
	}
	assertIDs(t, res.Docs, "1", "2")
}

// TestFilterCategoryChange_WithCompact verifies that after CompactDeleted, the
// old category's filter bits are fully pruned and the new category's bits survive.
func TestFilterCategoryChange_WithCompact(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"category": true}, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "camping gear",
		"category": "outdoors",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument: %v", err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "camping gear",
		"category": "sports",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument update: %v", err)
	}

	se.CompactDeleted()

	// Old category must produce no results
	res, _ := se.Search(context.Background(), "camping", map[string][]interface{}{"category": {"outdoors"}})
	if res == nil {
		t.Fatal("Search returned nil")
	}
	if len(res.Docs) != 0 {
		t.Fatalf("expected no results for old category after compact, got %+v", res.Docs)
	}

	// New category must still return the doc
	res, _ = se.Search(context.Background(), "camping", map[string][]interface{}{"category": {"sports"}})
	if res == nil {
		t.Fatal("Search returned nil")
	}
	assertIDs(t, res.Docs, "1")

	// Verify old filter bits are absent from FilterBits after compact
	se.mu.RLock()
	oldBits := se.FilterBits["category:outdoors"]
	se.mu.RUnlock()
	for _, w := range oldBits {
		if w != 0 {
			t.Fatal("expected FilterBits['category:outdoors'] to be empty after compact")
		}
	}
}

// TestFilterCategoryChange_ArrayCategory verifies that array-valued filter fields
// are correctly updated when a document is re-indexed with a different set.
func TestFilterCategoryChange_ArrayCategory(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"category": true}, 10)

	// doc1 belongs to music and party
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "summer festival",
		"category": []interface{}{"music", "party"},
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument: %v", err)
	}

	// Update to remove music, add outdoor
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "summer festival",
		"category": []interface{}{"party", "outdoor"},
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument update: %v", err)
	}

	// music category must now return nothing for doc1
	res, _ := se.Search(context.Background(), "summer", map[string][]interface{}{"category": {"music"}})
	if res == nil {
		t.Fatal("Search returned nil")
	}
	if len(res.Docs) != 0 {
		t.Fatalf("expected no results for removed 'music' category, got %+v", res.Docs)
	}

	// party category must still return doc1
	res, _ = se.Search(context.Background(), "summer", map[string][]interface{}{"category": {"party"}})
	if res == nil {
		t.Fatal("Search returned nil")
	}
	assertIDs(t, res.Docs, "1")

	// outdoor category must return doc1 (newly added)
	res, _ = se.Search(context.Background(), "summer", map[string][]interface{}{"category": {"outdoor"}})
	if res == nil {
		t.Fatal("Search returned nil")
	}
	assertIDs(t, res.Docs, "1")
}

// TestFilterCategoryChange_MultipleFilterFields verifies that changing one filter
// field does not corrupt the other filter field's bits.
func TestFilterCategoryChange_MultipleFilterFields(t *testing.T) {
	se := NewSearchEngine(
		[]string{"title"},
		nil,
		map[string]bool{"category": true, "year": true},
		10,
	)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "jazz album",
		"category": "music",
		"year":     "2020",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument: %v", err)
	}

	// Change category; year stays the same
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "jazz album",
		"category": "vinyl",
		"year":     "2020",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument update: %v", err)
	}

	// Old category filter → no results
	assertIDs(t, mustSingleTermSearchLoop(t, se, "jazz",
		map[string][]interface{}{"category": {"music"}}))

	// New category filter → doc returned
	assertIDs(t, mustSingleTermSearchLoop(t, se, "jazz",
		map[string][]interface{}{"category": {"vinyl"}}), "1")

	// Year filter (unchanged) → doc still returned
	assertIDs(t, mustSingleTermSearchLoop(t, se, "jazz",
		map[string][]interface{}{"year": {"2020"}}), "1")

	// Combined new category + year → doc returned
	assertIDs(t, mustSingleTermSearchLoop(t, se, "jazz",
		map[string][]interface{}{"category": {"vinyl"}, "year": {"2020"}}), "1")

	// Combined old category + year → no results
	assertIDs(t, mustSingleTermSearchLoop(t, se, "jazz",
		map[string][]interface{}{"category": {"music"}, "year": {"2020"}}))
}

// TestFilterCategoryChange_SaveLoad verifies that a category-changed document is
// correctly re-indexed after a save/load cycle.
func TestFilterCategoryChange_SaveLoad(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"category": true}, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "electric guitar",
		"category": "music",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument: %v", err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":       "1",
		"title":    "electric guitar",
		"category": "equipment",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument update: %v", err)
	}

	dir := t.TempDir()
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}
	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	// Old category must not match after load
	res, _ := loaded.Search(context.Background(), "electric", map[string][]interface{}{"category": {"music"}})
	if res == nil {
		t.Fatal("Search returned nil")
	}
	if len(res.Docs) != 0 {
		t.Fatalf("expected no results for old category 'music' after load, got %+v", res.Docs)
	}

	// New category must match after load
	res, _ = loaded.Search(context.Background(), "electric", map[string][]interface{}{"category": {"equipment"}})
	if res == nil {
		t.Fatal("Search returned nil")
	}
	assertIDs(t, res.Docs, "1")
}

// TestFilterCategoryChange_Sequential verifies that multiple sequential category
// changes all resolve correctly to the latest value.
func TestFilterCategoryChange_Sequential(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"category": true}, 10)

	categories := []string{"alpha", "beta", "gamma", "delta"}
	for _, cat := range categories {
		if err := se.AddOrUpdateDocument(map[string]interface{}{
			"id":       "1",
			"title":    "shape shifter",
			"category": cat,
		}); err != nil {
			t.Fatalf("AddOrUpdateDocument (category=%s): %v", cat, err)
		}
	}

	// Only the final category should match
	final := categories[len(categories)-1]
	res, _ := se.Search(context.Background(), "shape", map[string][]interface{}{"category": {final}})
	if res == nil {
		t.Fatal("Search returned nil")
	}
	assertIDs(t, res.Docs, "1")

	// All previous categories must not match
	for _, old := range categories[:len(categories)-1] {
		res, _ = se.Search(context.Background(), "shape", map[string][]interface{}{"category": {old}})
		if res == nil {
			t.Fatal("Search returned nil")
		}
		if len(res.Docs) != 0 {
			t.Fatalf("expected no results for old category %q, got %+v", old, res.Docs)
		}
	}
}

// TestFilterCategoryChange_BulkIndexPath verifies category-change filter
// correctness when documents are re-indexed via the bulk Index() path.
func TestFilterCategoryChange_BulkIndexPath(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"category": true}, 10)

	batch1 := []map[string]interface{}{
		{"id": "1", "title": "acoustic guitar", "category": "music"},
		{"id": "2", "title": "acoustic tile",   "category": "construction"},
	}
	se.Index(batch1)

	// Re-index doc1 with a different category
	batch2 := []map[string]interface{}{
		{"id": "1", "title": "acoustic guitar", "category": "equipment"},
	}
	se.Index(batch2)

	// doc1 left music; doc2 was never music → music filter returns nothing
	assertIDs(t, mustSingleTermSearchLoop(t, se, "acoustic",
		map[string][]interface{}{"category": {"music"}}))

	// doc2 was never moved → construction still returns only doc2
	assertIDs(t, mustSingleTermSearchLoop(t, se, "acoustic",
		map[string][]interface{}{"category": {"construction"}}), "2")

	// doc1 must appear in equipment
	assertIDs(t, mustSingleTermSearchLoop(t, se, "acoustic",
		map[string][]interface{}{"category": {"equipment"}}), "1")
}
