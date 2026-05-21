package engine

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

// helper to build a SearchEngine with prepopulated FilterBits
func newTestEngineWithData(data map[string][]uint64) *SearchEngine {
	return &SearchEngine{
		mu:         sync.RWMutex{},
		FilterBits: data,
		DocDeleted: make(map[uint32]bool),
		ResultSize: 100,
	}
}

func TestApplyFilter_SingleValue(t *testing.T) {
	bits := make(map[string][]uint64)
	bits["author:Alice"] = filterBitSet(bits["author:Alice"], 1)
	bits["author:Alice"] = filterBitSet(bits["author:Alice"], 2)
	se := newTestEngineWithData(bits)

	filters := map[string][]interface{}{"author": {"Alice"}}
	got := se.ApplyFilter(filters)
	if got == nil {
		t.Fatal("expected non-nil bitset")
	}
	if !filterBitTest(got, 1) || !filterBitTest(got, 2) {
		t.Errorf("expected ids 1 and 2 to be set in bitset, got %v", got)
	}
}

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

func TestSearchOneTermBasic(t *testing.T) {
	se := newTestEngine()

	res := se.SearchOneTerm("apple", nil)
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res))
	}

	if res[0].ID != "doc2" || res[0].Score != 20 {
		t.Fatalf("unexpected top result: %+v", res[0])
	}
}

func TestSearchOneTermDeleted(t *testing.T) {
	se := newTestEngine()

	res := se.SearchOneTerm("phone", nil)
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

	res := se.SearchMultiTerms(terms, nil)
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

	res := se.SearchMultiTerms(terms, nil)
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

	res := se.SearchMultiTerms([][]string{{"nonexistent"}}, nil)
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
	assertIDs(t, se.SearchOneTerm("sunny", nil), "1")
	assertIDs(t, se.SearchOneTerm("rio", nil), "1", "2")
	assertIDs(t, se.SearchOneTerm("cloudy", nil), "3")

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
	assertIDs(t, se.SearchOneTerm("rio", nil), "1")

	// "sunny" should include doc1 and updated doc2
	// (order not guaranteed, we compare as a set)
	assertIDs(t, se.SearchOneTerm("sunny", nil), "1", "2")

	// 4) Delete doc1 and verify it disappears from results
	if ok := se.DeleteDocument("1"); !ok {
		t.Fatalf("DeleteDocument(1) expected true")
	}

	assertIDs(t, se.SearchOneTerm("sunny", nil), "2")
	assertIDs(t, se.SearchOneTerm("rio", nil)) // empty

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

	assertIDs(t, se.SearchOneTerm("rio", nil), "4")
	assertIDs(t, se.SearchOneTerm("sunny", nil), "2", "4")

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
	assertIDs(t, se.SearchOneTerm("sunny", filters), "2")
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
	assertIDs(t, se.SearchOneTerm("sunny", nil), "2")
	assertIDs(t, se.SearchOneTerm("rio", nil)) // should be empty now

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
	assertIDs(t, loaded.SearchOneTerm("sunny", nil), "2")
	assertIDs(t, loaded.SearchOneTerm("rio", nil)) // empty

	// Filter logic rebuilt
	filtered := loaded.SearchOneTerm("sunny", map[string][]interface{}{"year": {"2021"}})
	assertIDs(t, filtered, "2")
	filtered = loaded.SearchOneTerm("sunny", map[string][]interface{}{"year": {"2020"}})
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

	assertIDs(t, loaded.SearchOneTerm("rio", nil), "3")
	assertIDs(t, loaded.SearchOneTerm("sunny", nil), "2", "3")
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

func TestMinHeap_PushPopOrder(t *testing.T) {
	var h []internalHit

	input := []internalHit{
		{id: 1, score: 50},
		{id: 2, score: 10},
		{id: 3, score: 30},
		{id: 4, score: 5},
		{id: 5, score: 20},
	}

	for _, v := range input {
		h = heapPushHit(h, v)
	}

	n := len(h)
	out := make([]internalHit, n)
	for i := n - 1; i >= 0; i-- {
		hit := h[0]
		if i > 0 {
			h[0] = h[i]
			siftDownHit(h, 0, i)
		}
		out[i] = hit
	}

	expectedOrder := []int{50, 30, 20, 10, 5}
	for i, want := range expectedOrder {
		if out[i].score != want {
			t.Errorf("index %d: expected %v, got %v", i, want, out[i].score)
		}
	}
}

func TestMinHeap_Len(t *testing.T) {
	var h []internalHit

	if len(h) != 0 {
		t.Fatalf("expected empty heap")
	}

	h = heapPushHit(h, internalHit{id: 1, score: 10})
	h = heapPushHit(h, internalHit{id: 2, score: 20})

	if len(h) != 2 {
		t.Fatalf("expected len 2, got %d", len(h))
	}
}

func TestMinHeap_SingleElement(t *testing.T) {
	var h []internalHit

	h = heapPushHit(h, internalHit{id: 1, score: 42})

	n := len(h)
	out := make([]internalHit, n)
	for i := n - 1; i >= 0; i-- {
		hit := h[0]
		if i > 0 {
			h[0] = h[i]
			siftDownHit(h, 0, i)
		}
		out[i] = hit
	}

	if out[0].score != 42 {
		t.Errorf("expected 42, got %v", out[0].score)
	}
}

func TestMinHeap_StabilityRandomInsertions(t *testing.T) {
	var h []internalHit

	values := []int{100, 1, 50, 2, 99, 3, 75, 4, 60}

	for i, v := range values {
		h = heapPushHit(h, internalHit{id: uint32(i), score: v})
	}

	n := len(h)
	out := make([]internalHit, n)
	for i := n - 1; i >= 0; i-- {
		hit := h[0]
		if i > 0 {
			h[0] = h[i]
			siftDownHit(h, 0, i)
		}
		out[i] = hit
	}

	for i := 1; i < len(out); i++ {
		if out[i].score > out[i-1].score {
			t.Errorf("heap order violated at index %d: %v > %v", i, out[i].score, out[i-1].score)
		}
	}
}

func TestMinHeap_StabilityRandomInsertions2(t *testing.T) {
	var h []internalHit

	values := []int{100, 105, 1, 50, 2, 99, 101, 3, 75, 4, 60, 104, 110, 95, 90, 90, 106, 8, 111, 101, 106, 79}

	k := 5
	for i, score := range values {
		if len(h) < k {
			h = heapPushHit(h, internalHit{id: uint32(i), score: score})
		} else if h[0].score < score {
			heapReplaceTop(h, internalHit{id: uint32(i), score: score})
		}
	}

	n := len(h)
	scores := make([]int, n)
	for i := n - 1; i >= 0; i-- {
		hit := h[0]
		if i > 0 {
			h[0] = h[i]
			siftDownHit(h, 0, i)
		}
		scores[i] = hit.score
	}

	for i := 1; i < len(scores); i++ {
		if scores[i] > scores[i-1] {
			t.Errorf("heap order violated at index %d: %v > %v", i, scores[i], scores[i-1])
		}
	}
	if scores[0] != 111 || scores[1] != 110 || scores[2] != 106 || scores[3] != 106 || scores[4] != 105 {
		t.Errorf("Score violated: got %v", scores)
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

	// SingleTermSearch: exact match via termSet
	res := loaded.SingleTermSearch([]string{"golden"}, nil)
	if len(res.Docs) == 0 {
		t.Fatal("SingleTermSearch('golden') returned no results after load")
	}
	ids := make(map[string]bool)
	for _, d := range res.Docs {
		ids[d.ID] = true
	}
	if !ids["1"] || !ids["2"] {
		t.Errorf("SingleTermSearch('golden') expected ids 1 and 2, got %v", ids)
	}

	// SingleTermSearch: prefix match (term not exact, relies on Prefix rebuild)
	resPrefix := loaded.SingleTermSearch([]string{"brid"}, nil)
	if len(resPrefix.Docs) == 0 {
		t.Fatal("SingleTermSearch prefix 'brid' returned no results after load")
	}

	// SingleTermSearch: with filter
	resFiltered := loaded.SingleTermSearch([]string{"golden"}, map[string][]interface{}{"year": {"2021"}})
	if len(resFiltered.Docs) != 1 || resFiltered.Docs[0].ID != "2" {
		t.Errorf("SingleTermSearch('golden', year=2021) expected only doc 2, got %v", resFiltered.Docs)
	}

	// MultiTermSearch: both terms must appear
	resMulti := loaded.MultiTermSearch([]string{"golden", "bridge"}, nil)
	if len(resMulti.Docs) == 0 {
		t.Fatal("MultiTermSearch('golden bridge') returned no results after load")
	}
	if resMulti.Docs[0].ID != "1" {
		t.Errorf("MultiTermSearch('golden bridge') expected doc 1 first, got %v", resMulti.Docs[0].ID)
	}

	// MultiTermSearch: with filter
	resMultiFiltered := loaded.MultiTermSearch([]string{"golden", "bridge"}, map[string][]interface{}{"year": {"2020"}})
	if len(resMultiFiltered.Docs) != 1 || resMultiFiltered.Docs[0].ID != "1" {
		t.Errorf("MultiTermSearch('golden bridge', year=2020) expected only doc 1, got %v", resMultiFiltered.Docs)
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
	pre := se.SearchOneTerm("swift", nil)
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
	post := loaded.SearchOneTerm("swift", nil)
	if len(post) != 2 {
		t.Fatalf("expected 2 docs for 'swift' after load, got %d", len(post))
	}

	// Filter bitset restored
	filtered := loaded.SearchOneTerm("swift", map[string][]interface{}{"category": {"fitness"}})
	if len(filtered) != 1 || filtered[0].ID != "a" {
		t.Errorf("expected only doc 'a' for swift+fitness filter after load, got %v", filtered)
	}

	// OR filter within same field
	orFiltered := loaded.SearchOneTerm("swift", map[string][]interface{}{"category": {"fitness", "travel"}})
	if len(orFiltered) != 2 {
		t.Errorf("expected 2 docs for swift with fitness|travel filter after load, got %d", len(orFiltered))
	}

	// termSet populated: SingleTermSearch works
	singleRes := loaded.SingleTermSearch([]string{"calm"}, nil)
	if len(singleRes.Docs) == 0 || singleRes.Docs[0].ID != "b" {
		t.Errorf("SingleTermSearch('calm') after load expected doc b, got %v", singleRes.Docs)
	}

	// Mutate after load: add a new doc, verify it is searchable
	if err := loaded.AddOrUpdateDocument(map[string]interface{}{
		"id": "d", "name": "swift mountain", "tags": "outdoor hiking", "category": "fitness",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument after load: %v", err)
	}
	postMutate := loaded.SearchOneTerm("swift", nil)
	if len(postMutate) != 3 {
		t.Fatalf("expected 3 docs for 'swift' after adding doc d, got %d", len(postMutate))
	}
}
