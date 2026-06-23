package engine

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// gzip magic bytes: 1f 8b
func isGzipFile(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var magic [2]byte
	if _, err := f.Read(magic[:]); err != nil {
		t.Fatalf("read magic bytes: %v", err)
	}
	return binary.BigEndian.Uint16(magic[:]) == 0x1f8b
}

// TestSaveAll_FileIsGzip verifies that the saved file is gzip-compressed.
func TestSaveAll_FileIsGzip(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)
	_ = se.AddOrUpdateDocument(map[string]interface{}{"id": "1", "title": "hello"})

	dir := t.TempDir()
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}
	if !isGzipFile(t, filepath.Join(dir, "engine.gob")) {
		t.Fatal("engine.gob is not gzip-compressed")
	}
}

// TestSaveAll_TombstonesExcluded verifies that deleted and superseded internal
// IDs are not written to disk: only active documents appear in the loaded engine.
func TestSaveAll_TombstonesExcluded(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	// Add three docs, then update one and delete another.
	_ = se.AddOrUpdateDocument(map[string]interface{}{"id": "keep", "title": "alpha"})
	_ = se.AddOrUpdateDocument(map[string]interface{}{"id": "update", "title": "beta old"})
	_ = se.AddOrUpdateDocument(map[string]interface{}{"id": "delete", "title": "gamma"})
	_ = se.AddOrUpdateDocument(map[string]interface{}{"id": "update", "title": "beta new"}) // supersedes previous
	se.DeleteDocument("delete")

	// Before save: 4 internal IDs stored (keep=1, update-old=2, delete=3, update-new=4),
	// but only 2 active (keep, update-new).
	if len(se.Documents) != 4 {
		t.Fatalf("expected 4 stored docs before save, got %d", len(se.Documents))
	}

	dir := t.TempDir()
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}

	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	// Only 2 active docs should be in the loaded engine.
	if len(loaded.Documents) != 2 {
		t.Fatalf("expected 2 docs after load (tombstones excluded), got %d", len(loaded.Documents))
	}
	if len(loaded.DocDeleted) != 0 {
		t.Fatalf("expected DocDeleted to be empty after load, got %v", loaded.DocDeleted)
	}

	// "keep" and current "update" must be searchable.
	res, _ := loaded.Search(context.Background(), "alpha", nil)
	if len(res.Docs) != 1 || res.Docs[0].ID != "keep" {
		t.Errorf("expected 'keep' for 'alpha', got %v", res.Docs)
	}
	res, _ = loaded.Search(context.Background(), "beta", nil)
	if len(res.Docs) != 1 || res.Docs[0].ID != "update" {
		t.Errorf("expected 'update' for 'beta', got %v", res.Docs)
	}

	// Old and deleted versions must not appear.
	res, _ = loaded.Search(context.Background(), "gamma", nil)
	if len(res.Docs) != 0 {
		t.Errorf("deleted doc 'delete' must not appear, got %v", res.Docs)
	}
	res, _ = loaded.Search(context.Background(), "old", nil)
	if len(res.Docs) != 0 {
		t.Errorf("old version of 'update' must not appear, got %v", res.Docs)
	}
}

// TestLoadAll_IDMapsRebuilt verifies that ExternalToInternal and
// InternalToExternal are correctly derived from document "id" fields.
func TestLoadAll_IDMapsRebuilt(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)
	_ = se.AddOrUpdateDocument(map[string]interface{}{"id": "abc", "title": "foo"})
	_ = se.AddOrUpdateDocument(map[string]interface{}{"id": "xyz", "title": "bar"})

	dir := t.TempDir()
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}
	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	// ExternalToInternal must contain both external IDs.
	abcInternal, ok := loaded.ExternalToInternal["abc"]
	if !ok {
		t.Fatal("ExternalToInternal missing 'abc'")
	}
	xyzInternal, ok := loaded.ExternalToInternal["xyz"]
	if !ok {
		t.Fatal("ExternalToInternal missing 'xyz'")
	}

	// InternalToExternal must be the inverse.
	if loaded.InternalToExternal[abcInternal] != "abc" {
		t.Errorf("InternalToExternal[%d] = %q, want 'abc'", abcInternal, loaded.InternalToExternal[abcInternal])
	}
	if loaded.InternalToExternal[xyzInternal] != "xyz" {
		t.Errorf("InternalToExternal[%d] = %q, want 'xyz'", xyzInternal, loaded.InternalToExternal[xyzInternal])
	}

	// The two internal IDs must be distinct.
	if abcInternal == xyzInternal {
		t.Fatal("ExternalToInternal maps different external IDs to the same internal ID")
	}
}

// TestLoadAll_NextInternalID verifies that AddOrUpdateDocument after a load
// cycle does not reuse internal IDs or overwrite existing documents.
func TestLoadAll_NextInternalID(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)
	_ = se.AddOrUpdateDocument(map[string]interface{}{"id": "a", "title": "alpha"})
	_ = se.AddOrUpdateDocument(map[string]interface{}{"id": "b", "title": "beta"})

	dir := t.TempDir()
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}
	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	// Adding a new doc after load must not collide with existing internal IDs.
	if err := loaded.AddOrUpdateDocument(map[string]interface{}{"id": "c", "title": "gamma"}); err != nil {
		t.Fatalf("AddOrUpdateDocument after load: %v", err)
	}

	// All three docs must be searchable.
	for _, tc := range []struct{ query, id string }{
		{"alpha", "a"},
		{"beta", "b"},
		{"gamma", "c"},
	} {
		res, _ := loaded.Search(context.Background(), tc.query, nil)
		if len(res.Docs) != 1 || res.Docs[0].ID != tc.id {
			t.Errorf("query %q: expected doc %q, got %v", tc.query, tc.id, res.Docs)
		}
	}

	// Updating an existing doc after load must work correctly too.
	if err := loaded.AddOrUpdateDocument(map[string]interface{}{"id": "a", "title": "alpha updated"}); err != nil {
		t.Fatalf("UpdateDocument after load: %v", err)
	}
	res, _ := loaded.Search(context.Background(), "alpha", nil)
	if len(res.Docs) != 1 || res.Docs[0].ID != "a" {
		t.Errorf("updated doc 'a' not found, got %v", res.Docs)
	}
	res, _ = loaded.Search(context.Background(), "updated", nil)
	if len(res.Docs) != 1 || res.Docs[0].ID != "a" {
		t.Errorf("updated term 'updated' not found in doc 'a', got %v", res.Docs)
	}
}

// TestSaveLoad_EmptyEngine verifies that an engine with no documents can be
// saved and loaded without error.
func TestSaveLoad_EmptyEngine(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"year": true}, 10)

	dir := t.TempDir()
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll empty engine: %v", err)
	}

	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll empty engine: %v", err)
	}

	if len(loaded.Documents) != 0 {
		t.Fatalf("expected 0 docs, got %d", len(loaded.Documents))
	}
	if loaded.ResultSize != 10 {
		t.Fatalf("ResultSize mismatch: got %d", loaded.ResultSize)
	}

	// Engine must be usable after loading empty state.
	if err := loaded.AddOrUpdateDocument(map[string]interface{}{"id": "1", "title": "hello"}); err != nil {
		t.Fatalf("AddOrUpdateDocument after empty load: %v", err)
	}
	res, _ := loaded.Search(context.Background(), "hello", nil)
	if len(res.Docs) != 1 {
		t.Errorf("expected 1 result after adding to empty-loaded engine, got %d", len(res.Docs))
	}
}

// TestSaveLoad_TermSetRebuilt verifies that the lock-free termSet is populated
// after a load so that the exact-match fast path in Search works correctly.
func TestSaveLoad_TermSetRebuilt(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)
	_ = se.AddOrUpdateDocument(map[string]interface{}{"id": "1", "title": "elephant"})

	dir := t.TempDir()
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}
	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	if !inTermSet(loaded, "elephant") {
		t.Fatal("termSet does not contain 'elephant' after load")
	}
}

// TestSaveLoad_PrefixMapSortedByFrequency verifies that the prefix map after
// load orders terms by descending document frequency (same as CompactDeleted).
func TestSaveLoad_PrefixMapSortedByFrequency(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)

	// "alpha" appears in 3 docs, "always" in 1 — both share prefix "al".
	// After load, "alpha" must come first under "al".
	_ = se.AddOrUpdateDocument(map[string]interface{}{"id": "1", "title": "alpha one"})
	_ = se.AddOrUpdateDocument(map[string]interface{}{"id": "2", "title": "alpha two"})
	_ = se.AddOrUpdateDocument(map[string]interface{}{"id": "3", "title": "alpha three"})
	_ = se.AddOrUpdateDocument(map[string]interface{}{"id": "4", "title": "always rare"})

	dir := t.TempDir()
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}
	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	alTerms := loaded.Prefix["al"]
	if len(alTerms) < 2 {
		t.Fatalf("expected at least 2 terms under prefix 'al', got %v", alTerms)
	}
	if alTerms[0] != "alpha" {
		t.Errorf("expected 'alpha' first under 'al' (highest freq), got %q", alTerms[0])
	}
}
