package engine

import (
	"context"
	"testing"
)

// TestStaticPopularity_SetOnIndex verifies that indexing a doc with a "popularity"
// field stores it in StaticPopularity keyed by externalID.
func TestStaticPopularity_SetOnIndex(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":         "1",
		"title":      "coffee",
		"popularity": 200,
	}); err != nil {
		t.Fatal(err)
	}

	se.mu.RLock()
	got := se.StaticPopularity["1"]
	se.mu.RUnlock()

	if got != 200 {
		t.Fatalf("StaticPopularity[1] = %d, want 200", got)
	}
}

// TestStaticPopularity_Float64 verifies that JSON-decoded float64 popularity values
// (the default numeric type from encoding/json) are accepted.
func TestStaticPopularity_Float64(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":         "1",
		"title":      "tea",
		"popularity": float64(150),
	}); err != nil {
		t.Fatal(err)
	}

	se.mu.RLock()
	got := se.StaticPopularity["1"]
	se.mu.RUnlock()

	if got != 150 {
		t.Fatalf("StaticPopularity[1] = %d, want 150", got)
	}
}

// TestStaticPopularity_MissingField verifies that a doc without a "popularity"
// field does not leave a stale entry in StaticPopularity.
func TestStaticPopularity_MissingField(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":    "1",
		"title": "espresso",
	}); err != nil {
		t.Fatal(err)
	}

	se.mu.RLock()
	_, exists := se.StaticPopularity["1"]
	se.mu.RUnlock()

	if exists {
		t.Fatal("StaticPopularity must not have an entry for a doc without popularity field")
	}
}

// TestStaticPopularity_UpdateRemovesField verifies that updating a doc to remove
// the popularity field clears its StaticPopularity entry.
func TestStaticPopularity_UpdateRemovesField(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "1", "title": "latte", "popularity": 99,
	}); err != nil {
		t.Fatal(err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "1", "title": "latte updated",
	}); err != nil {
		t.Fatal(err)
	}

	se.mu.RLock()
	_, exists := se.StaticPopularity["1"]
	se.mu.RUnlock()

	if exists {
		t.Fatal("StaticPopularity entry must be removed when popularity field is absent after update")
	}
}

// TestStaticPopularity_DeleteClearsEntry verifies that DeleteDocument removes
// the doc's entry from StaticPopularity.
func TestStaticPopularity_DeleteClearsEntry(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "1", "title": "mocha", "popularity": 42,
	}); err != nil {
		t.Fatal(err)
	}

	if ok := se.DeleteDocument("1"); !ok {
		t.Fatal("DeleteDocument(1) expected true")
	}

	se.mu.RLock()
	_, exists := se.StaticPopularity["1"]
	se.mu.RUnlock()

	if exists {
		t.Fatal("StaticPopularity must not have an entry after DeleteDocument")
	}
}

// TestStaticPopularity_BoostsSingleTermRanking verifies that two docs with the
// same search term are ranked by popularity when their TF scores are equal.
func TestStaticPopularity_BoostsSingleTermRanking(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "low", "title": "coffee", "popularity": 10,
	}); err != nil {
		t.Fatal(err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "high", "title": "coffee", "popularity": 1000,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := se.Search(context.Background(), "coffee", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Docs) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res.Docs))
	}
	if res.Docs[0].ID != "high" {
		t.Fatalf("expected high-popularity doc first, got %q (scores: %d, %d)",
			res.Docs[0].ID, res.Docs[0].Score, res.Docs[1].Score)
	}
}

// TestStaticPopularity_BoostsMultiTermRanking verifies popularity ranking in the
// multi-term search path.
func TestStaticPopularity_BoostsMultiTermRanking(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "low", "title": "cold brew", "popularity": 5,
	}); err != nil {
		t.Fatal(err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "high", "title": "cold brew", "popularity": 800,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := se.Search(context.Background(), "cold brew", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Docs) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res.Docs))
	}
	if res.Docs[0].ID != "high" {
		t.Fatalf("expected high-popularity doc first in multi-term, got %q", res.Docs[0].ID)
	}
}

// TestPopularity_SaveLoadPreservesStatic verifies that SaveAll/LoadAll round-trips
// StaticPopularity.
func TestPopularity_SaveLoadPreservesStatic(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "1", "title": "arabica", "popularity": 300,
	}); err != nil {
		t.Fatal(err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "2", "title": "robusta",
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}

	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	if loaded.StaticPopularity["1"] != 300 {
		t.Fatalf("StaticPopularity[1] = %d after load, want 300", loaded.StaticPopularity["1"])
	}
	if _, exists := loaded.StaticPopularity["2"]; exists {
		t.Fatal("StaticPopularity[2] must not exist after load (no popularity field)")
	}
}

// TestPopularity_SaveLoadExcludesDeletedDocs verifies that SaveAll excludes popularity
// entries for deleted documents, since we iterate only active docs.
func TestPopularity_SaveLoadExcludesDeletedDocs(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "keep", "title": "americano", "popularity": 100,
	}); err != nil {
		t.Fatal(err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "gone", "title": "lungo", "popularity": 50,
	}); err != nil {
		t.Fatal(err)
	}

	se.DeleteDocument("gone")

	dir := t.TempDir()
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}

	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	if loaded.StaticPopularity["keep"] != 100 {
		t.Fatalf("StaticPopularity[keep] = %d, want 100", loaded.StaticPopularity["keep"])
	}
	if _, exists := loaded.StaticPopularity["gone"]; exists {
		t.Fatal("StaticPopularity[gone] must not be saved for a deleted doc")
	}
}

// TestPopularity_CompactPreservesPopularity verifies that CompactDeleted leaves
// StaticPopularity intact for active docs and drops it for deleted docs.
func TestPopularity_CompactPreservesPopularity(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "1", "title": "macchiato", "popularity": 75,
	}); err != nil {
		t.Fatal(err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "2", "title": "lungo", "popularity": 40,
	}); err != nil {
		t.Fatal(err)
	}

	// Delete doc 2 — DeleteDocument already removes its StaticPopularity
	se.DeleteDocument("2")

	se.CompactDeleted()

	se.mu.RLock()
	static1, ok1 := se.StaticPopularity["1"]
	_, exists2Static := se.StaticPopularity["2"]
	se.mu.RUnlock()

	if !ok1 || static1 != 75 {
		t.Fatalf("StaticPopularity[1] = %d (exists=%v) after compact, want 75", static1, ok1)
	}
	if exists2Static {
		t.Fatal("StaticPopularity[2] must not exist after doc 2 was deleted")
	}
}

// TestPopularity_CompactDoesNotAffectSearchScores verifies that after compact,
// popularity still drives ranking correctly.
func TestPopularity_CompactDoesNotAffectSearchScores(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "a", "title": "flat white", "popularity": 5,
	}); err != nil {
		t.Fatal(err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "b", "title": "flat white", "popularity": 500,
	}); err != nil {
		t.Fatal(err)
	}
	// old version of "a" — create tombstone
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "a", "title": "flat white", "popularity": 5,
	}); err != nil {
		t.Fatal(err)
	}

	se.CompactDeleted()

	res, err := se.Search(context.Background(), "flat", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Docs) != 2 {
		t.Fatalf("expected 2 results after compact, got %d", len(res.Docs))
	}
	if res.Docs[0].ID != "b" {
		t.Fatalf("expected high-popularity doc 'b' first after compact, got %q", res.Docs[0].ID)
	}
}

// TestPopularity_BulkIndexPath verifies StaticPopularity is populated when docs
// are indexed via the bulk Index() path (InsertDocs + BuildDocumentIndex).
func TestPopularity_BulkIndexPath(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	se.Index([]map[string]interface{}{
		{"id": "1", "title": "pour over", "popularity": 120},
		{"id": "2", "title": "pour over"},
	})

	se.mu.RLock()
	pop1 := se.StaticPopularity["1"]
	_, exists2 := se.StaticPopularity["2"]
	se.mu.RUnlock()

	if pop1 != 120 {
		t.Fatalf("StaticPopularity[1] = %d via bulk index, want 120", pop1)
	}
	if exists2 {
		t.Fatal("StaticPopularity[2] must not exist when popularity field is absent")
	}

	res, err := se.Search(context.Background(), "pour", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Docs) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res.Docs))
	}
	if res.Docs[0].ID != "1" {
		t.Fatalf("expected doc '1' (higher popularity) first, got %q", res.Docs[0].ID)
	}
}
