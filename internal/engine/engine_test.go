package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func newTestEngine() *SearchEngine {
	se := &SearchEngine{
		DataMap: map[string]map[uint32]int{
			"apple": {
				1: 10,
				2: 20,
				3: 5,
			},
			"iphone": {
				1: 30,
				2: 15,
			},
			"phone": {
				2: 25,
				3: 40,
			},
		},
		DocDeleted: map[uint32]bool{
			3: true,
		},
		Documents: map[uint32]map[string]interface{}{
			1: {"id": "doc1"},
			2: {"id": "doc2"},
			3: {"id": "doc3"},
		},
		InternalToExternal: map[uint32]string{
			1: "doc1",
			2: "doc2",
			3: "doc3",
		},
		ResultSize: 100,
	}
	return se
}

func newTestEngineForMultiTerm() *SearchEngine {
	se := &SearchEngine{
		DataMap: map[string]map[uint32]int{
			"apple": {
				1: 10,
				2: 20,
				3: 5,
			},
			"mapple": {
				4: 7,
				2: 20,
				7: 5,
			},
			"iphone": {
				10: 30,
				2:  15,
			},
			"phone": {
				7:  25,
				12: 40,
			},
			"phona": {
				70: 25,
			},
		},
		DocDeleted: map[uint32]bool{
			3: true,
		},
		Documents: map[uint32]map[string]interface{}{
			1:  {"id": "doc1"},
			2:  {"id": "doc2"},
			3:  {"id": "doc3"},
			4:  {"id": "doc4"},
			7:  {"id": "doc7"},
			10: {"id": "doc10"},
			12: {"id": "doc12"},
			70: {"id": "doc70"},
		},
		InternalToExternal: map[uint32]string{
			1:  "doc1",
			2:  "doc2",
			3:  "doc3",
			4:  "doc4",
			7:  "doc7",
			10: "doc10",
			12: "doc12",
			70: "doc70",
		},
		ResultSize: 2,
	}
	return se
}

func TestSingleTermSearchLoopBasic(t *testing.T) {
	se := newTestEngine()

	res := mustSingleTermSearchLoop(t, se, "apple", nil)
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res))
	}

	if res[0].ID != "doc2" || res[0].Score != 20 {
		t.Fatalf("unexpected top result: %+v", res[0])
	}
}

func TestSingleTermSearchLoopDeleted(t *testing.T) {
	se := newTestEngine()

	res := mustSingleTermSearchLoop(t, se, "phone", nil)
	if len(res) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res))
	}

	if res[0].ID != "doc2" {
		t.Fatalf("deleted doc returned or wrong doc: %+v", res[0])
	}
}

func TestSearchMultiTermAND_OR(t *testing.T) {
	se := newTestEngineForMultiTerm()

	terms := [][]string{
		{"apple", "mapple"},
		{"iphone", "phone", "phona"},
	}

	res, err := se.MultiTermSearchLoop(context.Background(), terms, nil)
	if err != nil {
		t.Fatalf("MultiTermSearchLoop: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 result, got %d, res=%+v", len(res), res)
	}

	// Only doc2 matches: (apple OR mapple) AND (iphone OR phone OR phona)
	if res[0].ID != "doc2" {
		t.Fatalf("unexpected result: %+v", res[0])
	}
	if res[1].ID != "doc7" {
		t.Fatalf("unexpected result: %+v", res[1])
	}
}

func TestSearchMultiTermScoreAggregation(t *testing.T) {
	se := newTestEngine()

	terms := [][]string{
		{"apple"},
		{"iphone"},
	}

	res, err := se.MultiTermSearchLoop(context.Background(), terms, nil)
	if err != nil {
		t.Fatalf("MultiTermSearchLoop: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 result, got %d, res=%+v", len(res), res)
	}

	// doc1 score = apple(10) + iphone(30) = 40
	if res[0].ID != "doc1" || res[0].Score != 40 {
		t.Fatalf("unexpected score aggregation: %+v", res[0])
	}
	if res[1].ID != "doc2" || res[1].Score != 35 {
		t.Fatalf("unexpected score aggregation: %+v", res[1])
	}
}

func TestSearchMultiTermEmpty(t *testing.T) {
	se := newTestEngine()

	res, err := se.MultiTermSearchLoop(context.Background(), [][]string{{"nonexistent"}}, nil)
	if err != nil {
		t.Fatalf("MultiTermSearchLoop: %v", err)
	}
	if res != nil {
		t.Fatalf("expected nil result")
	}
}

func newTestEngineForE2E() *SearchEngine {
	// indexFields: which doc fields are tokenized into inverted index
	// filters: which doc fields are indexed into FilterDocs ("field:value")
	return NewSearchEngine(
		[]string{"title"},
		map[string]bool{"genre": true},
		10, // return size
	)
}

func idsFromDocs(docs []ReturnedDocument) []string {
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.ID)
	}
	sort.Strings(out)
	return out
}

func assertIDs(t *testing.T, got []ReturnedDocument, expIDs ...string) {
	t.Helper()

	gotIDs := idsFromDocs(got)
	sort.Strings(expIDs)

	if len(gotIDs) != len(expIDs) {
		t.Fatalf("unexpected result count: got=%v exp=%v", gotIDs, expIDs)
	}

	for i := range gotIDs {
		if gotIDs[i] != expIDs[i] {
			t.Fatalf("unexpected ids: got=%v exp=%v", gotIDs, expIDs)
		}
	}
}

func mustSingleTermSearchLoop(t *testing.T, se *SearchEngine, query string, filters map[string][]interface{}) []ReturnedDocument {
	t.Helper()
	docs, err := se.SingleTermSearchLoop(context.Background(), query, filters)
	if err != nil {
		t.Fatalf("SingleTermSearchLoop(%q): %v", query, err)
	}
	return docs
}

func TestAddOrUpdateAndDelete_E2E(t *testing.T) {
	se := newTestEngineForE2E()

	// 1) Add some documents (single-doc API)
	err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":    "1",
		"title": "Sunny Rio",
		"genre": "rock",
	})
	if err != nil {
		t.Fatalf("AddOrUpdateDocument doc1: %v", err)
	}

	err = se.AddOrUpdateDocument(map[string]interface{}{
		"id":    "2",
		"title": "Rio Nights",
		"genre": "pop",
	})
	if err != nil {
		t.Fatalf("AddOrUpdateDocument doc2: %v", err)
	}

	err = se.AddOrUpdateDocument(map[string]interface{}{
		"id":    "3",
		"title": "Cloudy Day",
		"genre": "jazz",
	})
	if err != nil {
		t.Fatalf("AddOrUpdateDocument doc3: %v", err)
	}

	// 2) Exact-term searches should find what we indexed
	assertIDs(t, mustSingleTermSearchLoop(t, se, "sunny", nil), "1")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "rio", nil), "1", "2")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "cloudy", nil), "3")

	// 3) Update doc2: remove "rio", add "sunny"
	// Old internal version should become tombstoned, new version indexed.
	err = se.AddOrUpdateDocument(map[string]interface{}{
		"id":    "2",
		"title": "Sunny Days",
		"genre": "pop",
	})
	if err != nil {
		t.Fatalf("AddOrUpdateDocument doc2 update: %v", err)
	}

	// Now "rio" should no longer include doc2 (old internal is deleted)
	assertIDs(t, mustSingleTermSearchLoop(t, se, "rio", nil), "1")

	// "sunny" should include doc1 and updated doc2
	// (order not guaranteed, we compare as a set)
	assertIDs(t, mustSingleTermSearchLoop(t, se, "sunny", nil), "1", "2")

	// 4) Delete doc1 and verify it disappears from results
	if ok := se.DeleteDocument("1"); !ok {
		t.Fatalf("DeleteDocument(1) expected true")
	}

	assertIDs(t, mustSingleTermSearchLoop(t, se, "sunny", nil), "2")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "rio", nil)) // empty

	// Deleting an unknown external ID should return false
	if ok := se.DeleteDocument("does-not-exist"); ok {
		t.Fatalf("DeleteDocument(does-not-exist) expected false")
	}

	// 5) Add a new doc4 and verify it shows up
	err = se.AddOrUpdateDocument(map[string]interface{}{
		"id":    "4",
		"title": "Rio Sunny",
		"genre": "rock",
	})
	if err != nil {
		t.Fatalf("AddOrUpdateDocument doc4: %v", err)
	}

	assertIDs(t, mustSingleTermSearchLoop(t, se, "rio", nil), "4")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "sunny", nil), "2", "4")

	// 6) E2E via Search() as well (single term path)
	// (Uses prefix/fuzzy expansion internally, but for these exact terms it should include the same docs.)
	res := se.Search("sunny", nil)
	if res == nil {
		t.Fatalf("Search returned nil")
	}
	assertIDs(t, res.Docs, "2", "4")

	// 7) Filtered search (if you wired filters end-to-end as discussed):
	// genre:pop should return only doc2 for "sunny"
	filters := map[string][]interface{}{
		"genre": {"pop"},
	}
	res = se.Search("sunny", filters)
	if res == nil {
		t.Fatalf("Search (filtered) returned nil")
	}
	assertIDs(t, res.Docs, "2")

	// Extra safety: leaf filtered function should always work if present.
	assertIDs(t, mustSingleTermSearchLoop(t, se, "sunny", filters), "2")
}

func containsString(arr []string, s string) bool {
	for _, x := range arr {
		if x == s {
			return true
		}
	}
	return false
}

func TestSaveLoad_RebuildsIndexesFromDocuments(t *testing.T) {
	// 1) Build engine + mutate state (add/update/delete)
	se := NewSearchEngine(
		[]string{"name"},
		map[string]bool{"year": true},
		10,
	)

	// Add docs
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":   "1",
		"name": "Sunny Rio",
		"year": "2020",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument doc1: %v", err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":   "2",
		"name": "Rio Nights",
		"year": "2021",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument doc2: %v", err)
	}

	// Update doc2: remove "rio", add "sunny"
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":   "2",
		"name": "Sunny Days",
		"year": "2021",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument doc2 update: %v", err)
	}

	// Delete doc1
	if ok := se.DeleteDocument("1"); !ok {
		t.Fatalf("DeleteDocument(1) expected true")
	}

	// Sanity before save
	assertIDs(t, mustSingleTermSearchLoop(t, se, "sunny", nil), "2")
	assertIDs(t, mustSingleTermSearchLoop(t, se, "rio", nil)) // should be empty now

	// 2) Save to temp dir
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir temp dir: %v", err)
	}

	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "engine.gob")); err != nil {
		t.Fatalf("expected engine.gob to exist: %v", err)
	}

	// 3) Load and ensure derived structures are rebuilt from Documents
	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	// Metadata restored
	if loaded.ResultSize != 10 {
		t.Fatalf("ResultSize mismatch: got=%d exp=%d", loaded.ResultSize, 10)
	}
	if len(loaded.IndexFields) != 1 || loaded.IndexFields[0] != "name" {
		t.Fatalf("IndexFields mismatch: got=%v", loaded.IndexFields)
	}
	if !loaded.Filters["year"] {
		t.Fatalf("Filters mismatch: expected Filters[year]=true")
	}

	// Searches work (meaning DataMap rebuilt and tombstones respected)
	assertIDs(t, mustSingleTermSearchLoop(t, loaded, "sunny", nil), "2")
	assertIDs(t, mustSingleTermSearchLoop(t, loaded, "rio", nil)) // empty

	// Filter logic rebuilt
	filtered := mustSingleTermSearchLoop(t, loaded, "sunny", map[string][]interface{}{"year": {"2021"}})
	assertIDs(t, filtered, "2")
	filtered = mustSingleTermSearchLoop(t, loaded, "sunny", map[string][]interface{}{"year": {"2020"}})
	assertIDs(t, filtered) // empty

	// Derived structures sanity checks (not exhaustive, but ensures rebuild happened)
	if loaded.DataMap["sunny"] == nil {
		t.Fatalf("expected DataMap to contain 'sunny' after rebuild")
	}
	yearBits := loaded.FilterBits["year:2021"]
	hasAny := false
	for _, w := range yearBits {
		if w != 0 {
			hasAny = true
			break
		}
	}
	if !hasAny {
		t.Fatalf("expected FilterBits['year:2021'] to have bits set after rebuild")
	}

	// Prefix map should have 'sunny' under prefix 'su' after rebuild
	if len(loaded.Prefix["su"]) == 0 {
		t.Fatalf("expected Prefix map to have terms under 'su' after rebuild")
	}

	// DataMap + Symspell should have the indexed term
	if _, ok := loaded.DataMap["sunny"]; !ok {
		t.Fatalf("expected DataMap to contain 'sunny' after rebuild")
	}
	if loaded.Symspell == nil {
		t.Fatalf("expected Symspell to be non-nil after rebuild")
	}
	// SymSpell often returns empty for an exact word; test a near-miss instead.
	fz := loaded.Symspell.FuzzySearch("suny", 10) // missing one 'n'
	if len(fz) == 0 {
		t.Fatalf("expected Symspell.FuzzySearch('suny') to return suggestions after rebuild, got=%v", fz)
	}
	// Optional: if your implementation usually suggests the correct word:
	if !containsString(fz, "sunny") {
		t.Logf("note: Symspell suggestions for 'suny' did not include 'sunny': %v", fz)
	}

	// 4) Verify nextInternalID is usable after load by inserting a new doc
	if err := loaded.AddOrUpdateDocument(map[string]interface{}{
		"id":   "3",
		"name": "Rio Sunny",
		"year": "2022",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument doc3 after load: %v", err)
	}

	assertIDs(t, mustSingleTermSearchLoop(t, loaded, "rio", nil), "3")
	assertIDs(t, mustSingleTermSearchLoop(t, loaded, "sunny", nil), "2", "3")
}

func TestMultiTermSearch_E2E(t *testing.T) {
	se := NewSearchEngine(
		[]string{"title", "tags"},      // indexed fields
		map[string]bool{"genre": true}, // filters
		10,
	)

	// 1) Insert documents
	docs := []map[string]interface{}{
		{
			"id":    "1",
			"title": "Apple iPhone Pro",
			"genre": "tech",
			"tags":  "phone mobile",
		},
		{
			"id":    "2",
			"title": "Apple Phone Basic",
			"genre": "tech",
			"tags":  "phone mobile",
		},
		{
			"id":    "3",
			"title": "Samsung Phone",
			"genre": "tech",
			"tags":  "phone mobile",
		},
		{
			"id":    "4",
			"title": "Apple Banana",
			"genre": "food",
			"tags":  "fruit",
		},
	}

	for _, d := range docs {
		if err := se.AddOrUpdateDocument(d); err != nil {
			t.Fatalf("AddOrUpdateDocument failed: %v", err)
		}
	}

	// 2) Multi-term search
	// Query: "apple phone"
	// Expected logic:
	// AND across terms:
	//   must match BOTH "apple" AND "phone"
	//
	// Matching docs:
	// - doc1: "Apple iPhone Pro" (iphone fuzzy/prefix match)
	// - doc2: "Apple Phone Basic"
	// - doc4: "Apple Banana" -> excluded (no phone)
	// - doc3: "Samsung Phone" -> excluded (no apple)

	res := se.Search("apple phone", nil)
	if res == nil {
		t.Fatalf("Search returned nil")
	}

	assertIDs(t, res.Docs, "1", "2")

	// 3) Filtered multi-term search
	// Only genre=tech → still doc1 + doc2
	filters := map[string][]interface{}{
		"genre": {"tech"},
	}

	res = se.Search("apple phone", filters)
	if res == nil {
		t.Fatalf("Filtered search returned nil")
	}

	assertIDs(t, res.Docs, "1", "2")

	// 4) Filter that excludes one result
	// genre=food → should remove both (since neither doc1/doc2 are food)
	filters = map[string][]interface{}{
		"genre": {"food"},
	}

	res = se.Search("apple phone", filters)
	if res == nil {
		t.Fatalf("Filtered search returned nil")
	}

	assertIDs(t, res.Docs) // expect empty
}

func TestMultiTermSearch_E2E_WithScoringOrder(t *testing.T) {
	se := NewSearchEngine(
		[]string{"title", "tags"},
		map[string]bool{"genre": true},
		10,
	)

	// 1) Insert documents with intentional scoring differences
	// We control score via:
	// - repeated tokens
	// - multiple fields
	// - token count normalization

	docs := []map[string]interface{}{
		{
			"id":    "1",
			"title": "apple phone phone", // phone twice
			"tags":  "phone mobile",      // +1 phone
			"genre": "tech",
		},
		{
			"id":    "2",
			"title": "apple phone", // phone once
			"tags":  "phone",       // +1 phone
			"genre": "tech",
		},
		{
			"id":    "3",
			"title": "apple device",
			"tags":  "phone", // phone only in tags
			"genre": "tech",
		},
		{
			"id":    "4",
			"title": "apple something else",
			"tags":  "other",
			"genre": "tech",
		},
	}

	for _, d := range docs {
		if err := se.AddOrUpdateDocument(d); err != nil {
			t.Fatalf("AddOrUpdateDocument failed: %v", err)
		}
	}

	// 2) Multi-term search
	// appre -> system will fuzzy fix it as apple
	// pho -> system will find the word phone for prefix pho
	res := se.Search("appre pho", nil)
	if res == nil {
		t.Fatalf("Search returned nil")
	}

	if len(res.Docs) != 3 {
		t.Fatalf("expected 3 results, got %d: %+v", len(res.Docs), res.Docs)
	}

	gotOrder := []string{
		res.Docs[0].ID,
		res.Docs[1].ID,
		res.Docs[2].ID,
	}

	expectedOrder := []string{"2", "1", "3"}

	for i := range expectedOrder {
		if gotOrder[i] != expectedOrder[i] {
			t.Fatalf("unexpected ranking order: got=%v expected=%v", gotOrder, expectedOrder)
		}
	}

	// 3) Extra: assert strictly descending scores (stronger check)
	if !(res.Docs[0].Score >= res.Docs[1].Score &&
		res.Docs[1].Score >= res.Docs[2].Score) {
		t.Fatalf("scores not sorted descending: %+v", res.Docs)
	}
}

func TestSingleTermSearch_E2E_WithScoringOrder(t *testing.T) {
	se := NewSearchEngine(
		[]string{"title", "description"},
		map[string]bool{"category": true},
		10,
	)

	docs := []map[string]interface{}{
		{
			"id":          "1",
			"title":       "another brilliant dark coffee",
			"description": "New coffee beans from around the world",
			"category":    "drink",
		},
		{
			"id":          "2",
			"title":       "a good mild coffee",
			"description": "Good coffee beans from America",
			"category":    "drink",
		},
		{
			"id":          "3",
			"title":       "decaffeinated coffee",
			"description": "small drink for you",
			"category":    "drink",
		},
	}

	for _, d := range docs {
		if err := se.AddOrUpdateDocument(d); err != nil {
			t.Fatalf("AddOrUpdateDocument failed: %v", err)
		}
	}

	res := se.Search("caffee", nil)
	if res == nil {
		t.Fatalf("Search returned nil")
	}

	if len(res.Docs) != 3 {
		t.Fatalf("expected 3 results, got %d: %+v", len(res.Docs), res.Docs)
	}

	gotOrder := []string{
		res.Docs[0].ID,
		res.Docs[1].ID,
		res.Docs[2].ID,
	}

	expectedOrder := []string{"2", "1", "3"}

	for i := range expectedOrder {
		if gotOrder[i] != expectedOrder[i] {
			t.Fatalf("unexpected ranking order: got=%v expected=%v", gotOrder, expectedOrder)
		}
	}

	// 3) Extra: assert strictly descending scores (stronger check)
	if !(res.Docs[0].Score >= res.Docs[1].Score &&
		res.Docs[1].Score >= res.Docs[2].Score) {
		t.Fatalf("scores not sorted descending: %+v", res.Docs)
	}
}

func Test_E2E(t *testing.T) {
	se := NewSearchEngine(
		[]string{"title", "description"},
		map[string]bool{"category": true},
		5,
	)

	docs := []map[string]interface{}{
		{
			"id":          "1",
			"title":       "another brilliant dark coffee",
			"description": "New coffee beans from around the world",
			"category":    "drink",
		},
		{
			"id":          "2",
			"title":       "a good mild coffee",
			"description": "Good coffee beans from America",
			"category":    "drink",
		},
		{
			"id":          "3",
			"title":       "decaffeinated coffee",
			"description": "small drink for you",
			"category":    "drink",
		},
		{
			"id":          "4",
			"title":       "strong espresso roast",
			"description": "Italian style espresso beans",
			"category":    "drink",
		},
		{
			"id":          "5",
			"title":       "cold brew coffee bottle",
			"description": "Refreshing chilled coffee beverage",
			"category":    "drink",
		},
		{
			"id":          "6",
			"title":       "vanilla flavored latte",
			"description": "Sweet creamy milk coffee",
			"category":    "drink",
		},
		{
			"id":          "7",
			"title":       "premium arabica beans",
			"description": "High quality beans from Colombia",
			"category":    "ingredient",
		},
		{
			"id":          "8",
			"title":       "caramel cappuccino mix",
			"description": "Instant creamy cappuccino drink",
			"category":    "drink",
		},
		{
			"id":          "9",
			"title":       "breakfast black tea",
			"description": "Strong tea leaves for mornings",
			"category":    "drink",
		},
		{
			"id":          "10",
			"title":       "organic green tea",
			"description": "Fresh antioxidant rich tea",
			"category":    "drink",
		},
		{
			"id":          "11",
			"title":       "hazelnut coffee creamer",
			"description": "Smooth nutty flavor for coffee",
			"category":    "ingredient",
		},
		{
			"id":          "12",
			"title":       "dark chocolate mocha",
			"description": "Chocolate flavored espresso drink",
			"category":    "drink",
		},
		{
			"id":          "13",
			"title":       "iced caramel macchiato",
			"description": "Cold espresso with caramel milk",
			"category":    "drink",
		},
		{
			"id":          "14",
			"title":       "fresh roasted decaf beans",
			"description": "Decaffeinated beans with smooth taste",
			"category":    "ingredient",
		},
		{
			"id":          "15",
			"title":       "coffee grinder machine",
			"description": "Electric grinder for coffee beans",
			"category":    "equipment",
		},
		{
			"id":          "16",
			"title":       "espresso brewing guide",
			"description": "Learn to brew rich espresso shots",
			"category":    "book",
		},
		{
			"id":          "17",
			"title":       "caffeine free herbal tea",
			"description": "Relaxing herbal evening drink",
			"category":    "drink",
		},
		{
			"id":          "18",
			"title":       "double shot americano",
			"description": "Bold espresso diluted with water",
			"category":    "drink",
		},
		{
			"id":          "19",
			"title":       "french press coffee maker",
			"description": "Classic manual brewing equipment",
			"category":    "equipment",
		},
		{
			"id":          "20",
			"title":       "toasted coconut latte",
			"description": "Tropical flavored milk coffee",
			"category":    "drink",
		},
		{
			"id":          "21",
			"title":       "Big coffee",
			"description": "Coffee, coffee and more coffee",
			"category":    "drink",
		},
	}

	for _, d := range docs {
		if err := se.AddOrUpdateDocument(d); err != nil {
			t.Fatalf("AddOrUpdateDocument failed: %v", err)
		}
	}

	res := se.Search("caffee", nil)
	if res == nil {
		t.Fatalf("Search returned nil")
	}

	if len(res.Docs) != 5 {
		t.Fatalf("expected 3 results, got %d: %+v", len(res.Docs), res.Docs)
	}

	if res.Docs[0].ID != "21" && res.Docs[0].Score != 66664 {
		t.Fatalf("expected first results is different, got ID: %s: %d", res.Docs[0].ID, res.Docs[0].Score)
	}
}

func TestWeightedFields_SearchUpdateDelete_E2E(t *testing.T) {
	se := NewSearchEngineWithFieldWeights(
		[]string{"title", "description"},
		map[string]int{"title": 5, "description": 1},
		map[string]bool{"category": true},
		10,
	)

	docs := []map[string]interface{}{
		{
			"id":          "title-hit",
			"title":       "blue train",
			"description": "quiet",
			"category":    "music",
		},
		{
			"id":          "description-hit",
			"title":       "quiet",
			"description": "blue train",
			"category":    "music",
		},
		{
			"id":          "filtered-title-hit",
			"title":       "blue train",
			"description": "quiet",
			"category":    "book",
		},
	}

	se.Index(docs)

	res := se.Search("blue", map[string][]interface{}{"category": {"music"}})
	if res == nil {
		t.Fatalf("Search returned nil")
	}
	if len(res.Docs) != 2 {
		t.Fatalf("expected 2 music docs for blue, got %d: %+v", len(res.Docs), res.Docs)
	}
	if res.Docs[0].ID != "title-hit" {
		t.Fatalf("expected title-weighted doc first, got %+v", res.Docs)
	}
	if res.Docs[0].Score <= res.Docs[1].Score {
		t.Fatalf("expected title hit score > description hit score, got %+v", res.Docs)
	}

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":          "description-hit",
		"title":       "blue",
		"description": "quiet",
		"category":    "music",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument weighted update: %v", err)
	}

	res = se.Search("blue", map[string][]interface{}{"category": {"music"}})
	if res == nil {
		t.Fatalf("Search after update returned nil")
	}
	if len(res.Docs) != 2 {
		t.Fatalf("expected 2 music docs after update, got %d: %+v", len(res.Docs), res.Docs)
	}
	if res.Docs[0].ID != "description-hit" {
		t.Fatalf("expected updated title-weighted doc first, got %+v", res.Docs)
	}

	if ok := se.DeleteDocument("description-hit"); !ok {
		t.Fatalf("DeleteDocument(description-hit) expected true")
	}

	res = se.Search("blue", map[string][]interface{}{"category": {"music"}})
	if res == nil {
		t.Fatalf("Search after delete returned nil")
	}
	assertIDs(t, res.Docs, "title-hit")

	res = se.Search("blue", map[string][]interface{}{"category": {"book"}})
	if res == nil {
		t.Fatalf("Search with book filter returned nil")
	}
	assertIDs(t, res.Docs, "filtered-title-hit")
}

func TestWeightedFields_SaveLoadAndMultiTerm_E2E(t *testing.T) {
	se := NewSearchEngineWithFieldWeights(
		[]string{"title", "artist", "album"},
		map[string]int{"title": 6, "artist": 3, "album": 1},
		map[string]bool{"kind": true},
		10,
	)

	docs := []map[string]interface{}{
		{
			"id":     "title-match",
			"title":  "iron maiden",
			"artist": "tribute act",
			"album":  "live archive",
			"kind":   "track",
		},
		{
			"id":     "artist-match",
			"title":  "number beast",
			"artist": "iron maiden",
			"album":  "classic metal",
			"kind":   "track",
		},
		{
			"id":     "album-match",
			"title":  "running free",
			"artist": "cover band",
			"album":  "iron maiden",
			"kind":   "track",
		},
		{
			"id":     "filtered-match",
			"title":  "iron maiden",
			"artist": "archive",
			"album":  "spoken word",
			"kind":   "podcast",
		},
	}

	se.Index(docs)

	res := se.Search("iron", map[string][]interface{}{"kind": {"track"}})
	if res == nil {
		t.Fatalf("Search returned nil")
	}
	if len(res.Docs) != 3 {
		t.Fatalf("expected 3 track docs for iron, got %d: %+v", len(res.Docs), res.Docs)
	}

	expectedOrder := []string{"title-match", "artist-match", "album-match"}
	for i, expectedID := range expectedOrder {
		if res.Docs[i].ID != expectedID {
			t.Fatalf("unexpected weighted order before load: got %+v expected=%v", res.Docs, expectedOrder)
		}
	}

	multi := se.Search("iron mai", map[string][]interface{}{"kind": {"track"}})
	if multi == nil {
		t.Fatalf("Multi-term Search returned nil")
	}
	if len(multi.Docs) != 3 {
		t.Fatalf("expected 3 track docs for iron mai, got %d: %+v", len(multi.Docs), multi.Docs)
	}
	for i, expectedID := range expectedOrder {
		if multi.Docs[i].ID != expectedID {
			t.Fatalf("unexpected weighted multi-term order before load: got %+v expected=%v", multi.Docs, expectedOrder)
		}
	}

	dir := t.TempDir()
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}

	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if loaded.FieldWeights["title"] != 6 || loaded.FieldWeights["artist"] != 3 || loaded.FieldWeights["album"] != 1 {
		t.Fatalf("field weights were not restored: %+v", loaded.FieldWeights)
	}

	res = loaded.Search("iron", map[string][]interface{}{"kind": {"track"}})
	if res == nil {
		t.Fatalf("loaded Search returned nil")
	}
	if len(res.Docs) != 3 {
		t.Fatalf("expected 3 loaded track docs for iron, got %d: %+v", len(res.Docs), res.Docs)
	}
	for i, expectedID := range expectedOrder {
		if res.Docs[i].ID != expectedID {
			t.Fatalf("unexpected weighted order after load: got %+v expected=%v", res.Docs, expectedOrder)
		}
	}

	filtered := loaded.Search("iron", map[string][]interface{}{"kind": {"podcast"}})
	if filtered == nil {
		t.Fatalf("loaded filtered Search returned nil")
	}
	assertIDs(t, filtered.Docs, "filtered-match")
}

func TestIndex_MultipleBatches(t *testing.T) {
	se := NewSearchEngine(
		[]string{"title", "tags"},
		map[string]bool{"genre": true},
		10,
	)

	// First batch
	batch1 := []map[string]interface{}{
		{
			"id":    "1",
			"title": "Apple Phone",
			"tags":  "mobile tech",
			"genre": "tech",
		},
		{
			"id":    "2",
			"title": "Samsung Tablet",
			"tags":  "device tech",
			"genre": "tech",
		},
	}

	// Second batch
	batch2 := []map[string]interface{}{
		{
			"id":    "3",
			"title": "Banana Fruit",
			"tags":  "food yellow",
			"genre": "food",
		},
		{
			"id":    "4",
			"title": "Gaming Laptop",
			"tags":  "computer performance",
			"genre": "gaming",
		},
	}

	// Index first batch
	se.Index(batch1)

	// Index second batch
	se.Index(batch2)

	// Verify all docs searchable
	tests := []struct {
		query      string
		expectedID string
	}{
		{"apple", "1"},
		{"samsung", "2"},
		{"banana", "3"},
		{"gaming", "4"},
	}

	for _, tt := range tests {
		res := se.Search(tt.query, nil)

		if res == nil {
			t.Fatalf("search returned nil for query %q", tt.query)
		}

		if len(res.Docs) == 0 {
			t.Fatalf("expected results for query %q", tt.query)
		}

		found := false
		for _, d := range res.Docs {
			if d.ID == tt.expectedID {
				found = true
				break
			}
		}

		if !found {
			t.Fatalf("expected doc %s for query %q, got %+v",
				tt.expectedID, tt.query, res.Docs)
		}
	}
}

// TestSaveLoad_SingleAndMultiTermSearchAfterLoad verifies that SingleTermSearch
// and MultiTermSearch (which use termSet) work correctly after a save/load cycle.
func TestSaveLoad_SingleAndMultiTermSearchAfterLoad(t *testing.T) {
	se := NewSearchEngine(
		[]string{"title"},
		map[string]bool{"year": true},
		10,
	)

	docs := []map[string]interface{}{
		{"id": "1", "title": "golden gate bridge", "year": "2020"},
		{"id": "2", "title": "golden sunrise vista", "year": "2021"},
		{"id": "3", "title": "bridge repairs ongoing", "year": "2020"},
	}
	se.Index(docs)

	dir := t.TempDir()
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}

	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	// SearchContext: exact single-term match via termSet
	res, err := loaded.SearchContext(context.Background(), "golden", nil)
	if err != nil {
		t.Fatalf("SearchContext('golden'): %v", err)
	}
	if len(res.Docs) == 0 {
		t.Fatal("SearchContext('golden') returned no results after load")
	}
	ids := make(map[string]bool)
	for _, d := range res.Docs {
		ids[d.ID] = true
	}
	if !ids["1"] || !ids["2"] {
		t.Errorf("SearchContext('golden') expected ids 1 and 2, got %v", ids)
	}

	// SearchContext: prefix match (term not exact, relies on Prefix rebuild)
	resPrefix, err := loaded.SearchContext(context.Background(), "brid", nil)
	if err != nil {
		t.Fatalf("SearchContext('brid'): %v", err)
	}
	if len(resPrefix.Docs) == 0 {
		t.Fatal("SearchContext prefix 'brid' returned no results after load")
	}

	// SearchContext: single-term with filter
	resFiltered, err := loaded.SearchContext(context.Background(), "golden", map[string][]interface{}{"year": {"2021"}})
	if err != nil {
		t.Fatalf("SearchContext('golden', filter): %v", err)
	}
	if len(resFiltered.Docs) != 1 || resFiltered.Docs[0].ID != "2" {
		t.Errorf("SearchContext('golden', year=2021) expected only doc 2, got %v", resFiltered.Docs)
	}

	// SearchContext: both terms must appear
	resMulti, err := loaded.SearchContext(context.Background(), "golden bridge", nil)
	if err != nil {
		t.Fatalf("SearchContext('golden bridge'): %v", err)
	}
	if len(resMulti.Docs) == 0 {
		t.Fatal("SearchContext('golden bridge') returned no results after load")
	}
	if resMulti.Docs[0].ID != "1" {
		t.Errorf("SearchContext('golden bridge') expected doc 1 first, got %v", resMulti.Docs[0].ID)
	}

	// SearchContext: multi-term with filter
	resMultiFiltered, err := loaded.SearchContext(context.Background(), "golden bridge", map[string][]interface{}{"year": {"2020"}})
	if err != nil {
		t.Fatalf("SearchContext('golden bridge', filter): %v", err)
	}
	if len(resMultiFiltered.Docs) != 1 || resMultiFiltered.Docs[0].ID != "1" {
		t.Errorf("SearchContext('golden bridge', year=2020) expected only doc 1, got %v", resMultiFiltered.Docs)
	}
}

// TestSaveLoad_BulkIndexPath verifies save/load correctness when indexing is
// done via the bulk Index() path (InsertDocs + BuildDocumentIndex).
func TestSaveLoad_BulkIndexPath(t *testing.T) {
	se := NewSearchEngine(
		[]string{"name", "tags"},
		map[string]bool{"category": true},
		10,
	)

	docs := []map[string]interface{}{
		{"id": "a", "name": "swift runner", "tags": "sports outdoor", "category": "fitness"},
		{"id": "b", "name": "calm waters", "tags": "nature peaceful", "category": "travel"},
		{"id": "c", "name": "swift breeze", "tags": "outdoor weather", "category": "travel"},
	}
	se.Index(docs)

	// Verify pre-save state
	pre := mustSingleTermSearchLoop(t, se, "swift", nil)
	if len(pre) != 2 {
		t.Fatalf("expected 2 docs for 'swift' before save, got %d", len(pre))
	}

	dir := t.TempDir()
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}

	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	// Exact search restored
	post := mustSingleTermSearchLoop(t, loaded, "swift", nil)
	if len(post) != 2 {
		t.Fatalf("expected 2 docs for 'swift' after load, got %d", len(post))
	}

	// Filter bitset restored
	filtered := mustSingleTermSearchLoop(t, loaded, "swift", map[string][]interface{}{"category": {"fitness"}})
	if len(filtered) != 1 || filtered[0].ID != "a" {
		t.Errorf("expected only doc 'a' for swift+fitness filter after load, got %v", filtered)
	}

	// OR filter within same field
	orFiltered := mustSingleTermSearchLoop(t, loaded, "swift", map[string][]interface{}{"category": {"fitness", "travel"}})
	if len(orFiltered) != 2 {
		t.Errorf("expected 2 docs for swift with fitness|travel filter after load, got %d", len(orFiltered))
	}

	// termSet populated: SearchContext single-term path works
	singleRes, err := loaded.SearchContext(context.Background(), "calm", nil)
	if err != nil {
		t.Fatalf("SearchContext('calm'): %v", err)
	}
	if len(singleRes.Docs) == 0 || singleRes.Docs[0].ID != "b" {
		t.Errorf("SearchContext('calm') after load expected doc b, got %v", singleRes.Docs)
	}

	// Mutate after load: add a new doc, verify it is searchable
	if err := loaded.AddOrUpdateDocument(map[string]interface{}{
		"id": "d", "name": "swift mountain", "tags": "outdoor hiking", "category": "fitness",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument after load: %v", err)
	}
	postMutate := mustSingleTermSearchLoop(t, loaded, "swift", nil)
	if len(postMutate) != 3 {
		t.Fatalf("expected 3 docs for 'swift' after adding doc d, got %d", len(postMutate))
	}
}

func TestCompactDeleted_RebuildsCurrentIndexOnly(t *testing.T) {
	se := NewSearchEngineWithFieldWeights(
		[]string{"title", "artist"},
		map[string]int{"title": 3, "artist": 1},
		map[string]bool{"year": true},
		10,
	)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "1", "title": "old iron", "artist": "archive", "year": "1980",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument old: %v", err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "1", "title": "current iron", "artist": "maiden", "year": "1981",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument current: %v", err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id": "2", "title": "deleted iron", "artist": "gone", "year": "1982",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument deleted: %v", err)
	}
	if ok := se.DeleteDocument("2"); !ok {
		t.Fatal("DeleteDocument(2) expected true")
	}

	before := se.Stats()
	if before.StoredDocuments != 3 || before.ActiveDocuments != 1 {
		t.Fatalf("unexpected pre-compact stats: %+v", before)
	}

	stats := se.CompactDeleted()
	if stats.BeforeStored != 3 || stats.BeforeActive != 1 || stats.AfterStored != 1 || stats.AfterActive != 1 || stats.RemovedVersions != 2 {
		t.Fatalf("unexpected compact stats: %+v", stats)
	}

	after := se.Stats()
	if after.StoredDocuments != 1 || after.ActiveDocuments != 1 {
		t.Fatalf("unexpected post-compact stats: %+v", after)
	}
	if got := mustSingleTermSearchLoop(t, se, "current", nil); len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("expected current doc after compact, got %v", got)
	}
	if got := mustSingleTermSearchLoop(t, se, "old", nil); len(got) != 0 {
		t.Fatalf("expected old postings removed, got %v", got)
	}
	if got := mustSingleTermSearchLoop(t, se, "deleted", nil); len(got) != 0 {
		t.Fatalf("expected deleted postings removed, got %v", got)
	}
	if got := mustSingleTermSearchLoop(t, se, "current", map[string][]interface{}{"year": {"1981"}}); len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("expected rebuilt filter bits after compact, got %v", got)
	}
}

func TestUpdatePrefixOrdersByActiveDocFrequencyAndDropsDeletedTerms(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, 10)

	docs := []map[string]interface{}{
		{"id": "1", "title": "pearl"},
		{"id": "2", "title": "pearl"},
		{"id": "3", "title": "pearl"},
		{"id": "4", "title": "peach"},
		{"id": "5", "title": "peach"},
		{"id": "6", "title": "peanut"},
		{"id": "7", "title": "pebble"},
		{"id": "8", "title": "pesto"},
	}
	for _, doc := range docs {
		if err := se.AddOrUpdateDocument(doc); err != nil {
			t.Fatalf("AddOrUpdateDocument: %v", err)
		}
	}
	if ok := se.DeleteDocument("7"); !ok {
		t.Fatal("DeleteDocument(7) expected true")
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{"id": "8", "title": "kiwi"}); err != nil {
		t.Fatalf("AddOrUpdateDocument update: %v", err)
	}

	se.UpdatePrefix()

	se.mu.RLock()
	got := append([]string(nil), se.Prefix["pe"]...)
	se.mu.RUnlock()

	wantPrefix := []string{"pearl", "peach", "peanut"}
	if len(got) < len(wantPrefix) {
		t.Fatalf("Prefix[pe] too short: got=%v want prefix=%v", got, wantPrefix)
	}
	for i, want := range wantPrefix {
		if got[i] != want {
			t.Fatalf("Prefix[pe] order mismatch: got=%v want prefix=%v", got, wantPrefix)
		}
	}
	for _, term := range got {
		if term == "pebble" || term == "pesto" {
			t.Fatalf("Prefix[pe] contains inactive term %q: %v", term, got)
		}
	}
}

func TestUpdatePrefixOrdersByActiveDocFrequencyAndDropsDeletedTerms2(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, 10)

	docs := []map[string]interface{}{
		{"id": "1", "title": "pearl"},
		{"id": "2", "title": "pearl"},
		{"id": "3", "title": "pearl"},
		{"id": "4", "title": "peach"},
		{"id": "5", "title": "peach"},
		{"id": "6", "title": "peanut"},
		{"id": "7", "title": "pebble"},
		{"id": "8", "title": "pesto"},
	}
	for _, doc := range docs {
		if err := se.AddOrUpdateDocument(doc); err != nil {
			t.Fatalf("AddOrUpdateDocument: %v", err)
		}
	}

	se.UpdatePrefix()

	se.mu.RLock()
	got := append([]string(nil), se.Prefix["pea"]...)
	se.mu.RUnlock()

	wantPrefix := []string{"pearl", "peach", "peanut"}
	if len(got) < len(wantPrefix) {
		t.Fatalf("Prefix[pe] too short: got=%v want prefix=%v", got, wantPrefix)
	}
	for i, want := range wantPrefix {
		if got[i] != want {
			t.Fatalf("Prefix[pe] order mismatch: got=%v want prefix=%v", got, wantPrefix)
		}
	}
	for _, term := range got {
		if term == "pebble" || term == "pesto" {
			t.Fatalf("Prefix[pe] contains inactive term %q: %v", term, got)
		}
	}

	if ok := se.DeleteDocument("1"); !ok {
		t.Fatal("DeleteDocument(1) expected true")
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{"id": "2", "title": "kiwi"}); err != nil {
		t.Fatalf("AddOrUpdateDocument update: %v", err)
	}

	se.UpdatePrefix()

	se.mu.RLock()
	got = append([]string(nil), se.Prefix["pea"]...)
	se.mu.RUnlock()

	if len(got) < 3 {
		t.Fatalf("Prefix[pe] too short: got=%v want prefix=3", got)
	}

	if got[0] != "peach" {
		t.Fatalf("Prefix[pe] contains wrong term, got: %s, want: peach", got[0])
	}

	if !(got[1] == "pearl" || got[1] == "peanut") {
		t.Fatalf("Prefix[pe] contains wrong term, got: %s, want: pearl or peanut", got[1])
	}

	if !(got[2] == "pearl" || got[2] == "peanut") {
		t.Fatalf("Prefix[pe] contains wrong term, got: %s, want: pearl or peanut", got[2])
	}
}

func TestSearchContextCanceled(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, 10)
	if err := se.AddOrUpdateDocument(map[string]interface{}{"id": "1", "title": "cancelable query"}); err != nil {
		t.Fatalf("AddOrUpdateDocument: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := se.SearchContext(ctx, "cancelable", nil)
	if !errors.Is(err, ErrSearchCanceled) {
		t.Fatalf("expected ErrSearchCanceled, got %v", err)
	}
}

func TestFilterFieldsReturnsCopy(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, map[string]bool{"year": true}, 10)

	filters := se.FilterFields()
	filters["year"] = false
	filters["category"] = true

	filters = se.FilterFields()
	if !filters["year"] {
		t.Fatalf("expected original year filter to remain true, got %+v", filters)
	}
	if filters["category"] {
		t.Fatalf("expected external mutation not to add category, got %+v", filters)
	}
}
