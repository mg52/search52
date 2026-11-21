package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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
		nextInternalID: 4,
		ResultSize:     100,
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
		nextInternalID: 71,
		ResultSize:     2,
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

func TestSingleTermSearchBoostsExactMatchOverPrefixCandidate(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)
	se.Index([]map[string]interface{}{
		{"id": "exact", "title": "pro"},
		{"id": "prefix", "title": "project"},
	})

	res, _ := se.Search(context.Background(), "pro", nil)
	if res == nil {
		t.Fatalf("Search returned nil")
	}
	if len(res.Docs) != 2 {
		t.Fatalf("expected exact and prefix results, got %d: %+v", len(res.Docs), res.Docs)
	}
	if res.Docs[0].ID != "exact" {
		t.Fatalf("expected exact match first, got %+v", res.Docs)
	}
	if res.Docs[0].Score != 1_000_000 {
		t.Fatalf("expected exact score to be boosted to 1000000 (ExactMatchBoost=%d), got %+v", ExactMatchBoost, res.Docs[0])
	}
	if res.Docs[1].ID != "prefix" || res.Docs[1].Score != 100000 {
		t.Fatalf("expected unboosted prefix candidate second, got %+v", res.Docs[1])
	}
}

// TestFuzzyRanksByFrequency verifies that a misspelled query resolves to the
// highest-frequency edit-distance-1 correction even when rarer neighbours were
// indexed first. The fuzzy cap (SingleTermMaxFuzzyTokens) is smaller than the
// number of rare neighbours, so under the old insertion-order truncation the cap
// would be consumed by the rare neighbours and the common correction ("iron")
// would never reach the search. Build-time frequency sorting of the SymSpell
// buckets floats it to the front, so it survives the cap.
func TestFuzzyRanksByFrequency(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 100)

	docs := []map[string]interface{}{
		// Rare ED1 neighbours of "irom", indexed first so they head the
		// SymSpell insertion order.
		{"id": "r1", "title": "irok"},
		{"id": "r2", "title": "irop"},
		{"id": "r3", "title": "iroq"},
		{"id": "r4", "title": "iros"},
	}
	// "iron" appears in many documents -> highest corpus frequency, but indexed last.
	for i := 0; i < 5; i++ {
		docs = append(docs, map[string]interface{}{"id": fmt.Sprintf("n%d", i), "title": "iron"})
	}
	se.Index(docs)

	res, err := se.Search(context.Background(), "irom", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res == nil || len(res.Docs) == 0 {
		t.Fatalf("expected fuzzy results for %q, got none", "irom")
	}
	// The high-frequency correction "iron" must have been selected as a fuzzy
	// candidate despite being indexed after the rare neighbours, so its docs
	// must appear in the results.
	ironDocs := 0
	for _, d := range res.Docs {
		if d.ID[0] == 'n' {
			ironDocs++
		}
	}
	if ironDocs == 0 {
		t.Fatalf("expected 'iron' docs (n*) in results, got %+v", res.Docs)
	}
}

// resultHasDocWithIDPrefix reports whether any returned doc has an ID starting
// with the given byte.
func resultHasDocWithIDPrefix(res *SearchResult, prefix byte) bool {
	if res == nil {
		return false
	}
	for _, d := range res.Docs {
		if len(d.ID) > 0 && d.ID[0] == prefix {
			return true
		}
	}
	return false
}

func TestSearchMultiTermAND_OR(t *testing.T) {
	se := newTestEngineForMultiTerm()

	terms := [][]string{
		{"apple", "mapple"},
		{"iphone", "phone", "phona"},
	}

	res, err := se.MultiTermSearchLoop(context.Background(), termCandidateGroups(terms), nil)
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

func TestMultiTermSearchBoostsExactMatchOverPrefixCandidate(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)
	se.Index([]map[string]interface{}{
		{"id": "exact", "title": "apple phone"},
		{"id": "prefix", "title": "apple phonecase"},
	})

	res, _ := se.Search(context.Background(), "apple phone", nil)
	if res == nil {
		t.Fatalf("Search returned nil")
	}
	if len(res.Docs) != 2 {
		t.Fatalf("expected exact and prefix results, got %d: %+v", len(res.Docs), res.Docs)
	}
	if res.Docs[0].ID != "exact" {
		t.Fatalf("expected exact last-term match first, got %+v", res.Docs)
	}
	if res.Docs[0].Score != 1_000_000 {
		t.Fatalf("expected exact multi-term score to be boosted to 1000000 (ExactMatchBoost=%d), got %+v", ExactMatchBoost, res.Docs[0])
	}
	if res.Docs[1].ID != "prefix" || res.Docs[1].Score != 550000 {
		t.Fatalf("expected prefix candidate to keep only first-term exact boost, got %+v", res.Docs[1])
	}
}

func TestSearchMultiTermScoreAggregation(t *testing.T) {
	se := newTestEngine()

	terms := [][]string{
		{"apple"},
		{"iphone"},
	}

	res, err := se.MultiTermSearchLoop(context.Background(), termCandidateGroups(terms), nil)
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

	res, err := se.MultiTermSearchLoop(context.Background(), termCandidateGroups([][]string{{"nonexistent"}}), nil)
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
		nil,
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
	docs, err := se.SingleTermSearchLoop(context.Background(), []termCandidate{{term: query, boost: 1}}, filters)
	if err != nil {
		t.Fatalf("SingleTermSearchLoop(%q): %v", query, err)
	}
	return docs
}

func termCandidateGroups(groups [][]string) [][]termCandidate {
	out := make([][]termCandidate, len(groups))
	for i, group := range groups {
		out[i] = make([]termCandidate, len(group))
		for j, term := range group {
			out[i][j] = termCandidate{term: term, boost: 1}
		}
	}
	return out
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
	res, _ := se.Search(context.Background(), "sunny", nil)
	if res == nil {
		t.Fatalf("Search returned nil")
	}
	assertIDs(t, res.Docs, "2", "4")

	// 7) Filtered search (if you wired filters end-to-end as discussed):
	// genre:pop should return only doc2 for "sunny"
	filters := map[string][]interface{}{
		"genre": {"pop"},
	}
	res, _ = se.Search(context.Background(), "sunny", filters)
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

func TestMultiTermSearch_E2E(t *testing.T) {
	se := NewSearchEngine(
		[]string{"title", "tags"},      // indexed fields
		nil,
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

	res, _ := se.Search(context.Background(), "apple phone", nil)
	if res == nil {
		t.Fatalf("Search returned nil")
	}

	assertIDs(t, res.Docs, "1", "2")

	// 3) Filtered multi-term search
	// Only genre=tech → still doc1 + doc2
	filters := map[string][]interface{}{
		"genre": {"tech"},
	}

	res, _ = se.Search(context.Background(), "apple phone", filters)
	if res == nil {
		t.Fatalf("Filtered search returned nil")
	}

	assertIDs(t, res.Docs, "1", "2")

	// 4) Filter that excludes one result
	// genre=food → should remove both (since neither doc1/doc2 are food)
	filters = map[string][]interface{}{
		"genre": {"food"},
	}

	res, _ = se.Search(context.Background(), "apple phone", filters)
	if res == nil {
		t.Fatalf("Filtered search returned nil")
	}

	assertIDs(t, res.Docs) // expect empty
}

func TestMultiTermSearch_E2E_WithScoringOrder(t *testing.T) {
	se := NewSearchEngine(
		[]string{"title", "tags"},
		nil,
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
	res, _ := se.Search(context.Background(), "appre pho", nil)
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
		nil,
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

	res, _ := se.Search(context.Background(), "caffee", nil)
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
		nil,
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

	res, _ := se.Search(context.Background(), "caffee", nil)
	if res == nil {
		t.Fatalf("Search returned nil")
	}

	if len(res.Docs) != 5 {
		t.Fatalf("expected 5 results (ResultSize cap), got %d: %+v", len(res.Docs), res.Docs)
	}

	if !SingleTermSkipWholeScan {
		if res.Docs[0].ID != "21" || res.Docs[0].Score != 66664 {
			t.Fatalf("expected first result ID=21 score=66664, got ID=%s score=%d", res.Docs[0].ID, res.Docs[0].Score)
		}
	}
}

func TestWeightedFields_SearchUpdateDelete_E2E(t *testing.T) {
	se := NewSearchEngine(
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

	res, _ := se.Search(context.Background(), "blue", map[string][]interface{}{"category": {"music"}})
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

	res, _ = se.Search(context.Background(), "blue", map[string][]interface{}{"category": {"music"}})
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

	res, _ = se.Search(context.Background(), "blue", map[string][]interface{}{"category": {"music"}})
	if res == nil {
		t.Fatalf("Search after delete returned nil")
	}
	assertIDs(t, res.Docs, "title-hit")

	res, _ = se.Search(context.Background(), "blue", map[string][]interface{}{"category": {"book"}})
	if res == nil {
		t.Fatalf("Search with book filter returned nil")
	}
	assertIDs(t, res.Docs, "filtered-title-hit")
}

func TestWeightedFields_MultiTerm_E2E(t *testing.T) {
	se := NewSearchEngine(
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

	res, _ := se.Search(context.Background(), "iron", map[string][]interface{}{"kind": {"track"}})
	if res == nil {
		t.Fatalf("Search returned nil")
	}
	if len(res.Docs) != 3 {
		t.Fatalf("expected 3 track docs for iron, got %d: %+v", len(res.Docs), res.Docs)
	}

	expectedOrder := []string{"title-match", "artist-match", "album-match"}
	for i, expectedID := range expectedOrder {
		if res.Docs[i].ID != expectedID {
			t.Fatalf("unexpected weighted order: got %+v expected=%v", res.Docs, expectedOrder)
		}
	}

	multi, _ := se.Search(context.Background(), "iron mai", map[string][]interface{}{"kind": {"track"}})
	if multi == nil {
		t.Fatalf("Multi-term Search returned nil")
	}
	if len(multi.Docs) != 3 {
		t.Fatalf("expected 3 track docs for iron mai, got %d: %+v", len(multi.Docs), multi.Docs)
	}
	for i, expectedID := range expectedOrder {
		if multi.Docs[i].ID != expectedID {
			t.Fatalf("unexpected weighted multi-term order: got %+v expected=%v", multi.Docs, expectedOrder)
		}
	}
}

func TestIndex_MultipleBatches(t *testing.T) {
	se := NewSearchEngine(
		[]string{"title", "tags"},
		nil,
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
		res, _ := se.Search(context.Background(), tt.query, nil)

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

func TestSearchContextCanceled(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)
	if err := se.AddOrUpdateDocument(map[string]interface{}{"id": "1", "title": "cancelable query"}); err != nil {
		t.Fatalf("AddOrUpdateDocument: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := se.Search(ctx, "cancelable", nil)
	if !errors.Is(err, ErrSearchCanceled) {
		t.Fatalf("expected ErrSearchCanceled, got %v", err)
	}
}

func TestFilterFieldsReturnsCopy(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"year": true}, 10)

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

func TestArrayFilterValuesAreIndexed(t *testing.T) {
	se := NewSearchEngine(
		[]string{"name", "category", "description"},
		nil,
		map[string]bool{"category": true},
		10,
	)

	se.Index([]map[string]interface{}{
		{
			"id":          "p24",
			"name":        "greenbackism hyrax ars venturers speiring dogsbodyings wems ringbarks",
			"category":    []interface{}{"music", "yard", "party", "collectibles", "tea"},
			"description": "papa caricature airlines peremptoriness storages",
		},
		{
			"id":          "p25",
			"name":        "ringbarks alternate",
			"category":    []interface{}{"coffee"},
			"description": "another product",
		},
	})

	res, _ := se.Search(context.Background(), "ringbarks", map[string][]interface{}{"category": {"tea"}})
	if len(res.Docs) == 0 || res.Docs[0].ID != "p24" {
		t.Fatalf("expected p24 for category:tea filter, got %+v", res.Docs)
	}

	res, _ = se.Search(context.Background(), "ringbarks", map[string][]interface{}{"category": {"coffee"}})
	if len(res.Docs) == 0 || res.Docs[0].ID != "p25" {
		t.Fatalf("expected p25 for category:coffee filter, got %+v", res.Docs)
	}
}

func TestNumericYearFilterValuesAreIndexed(t *testing.T) {
	se := NewSearchEngine(
		[]string{"title", "description"},
		nil,
		map[string]bool{"year": true},
		10,
	)

	se.Index([]map[string]interface{}{
		{
			"id":          "int-year",
			"title":       "ceramic travel mug",
			"description": "insulated cup for morning tea",
			"year":        2024,
		},
		{
			"id":          "float-year",
			"title":       "ceramic desk lamp",
			"description": "warm light for evening reading",
			"year":        float64(2025),
		},
		{
			"id":          "other-year",
			"title":       "ceramic garden pot",
			"description": "large planter for flowers",
			"year":        2023,
		},
	})

	res, _ := se.Search(context.Background(), "ceramic", map[string][]interface{}{"year": {"2024"}})
	if len(res.Docs) != 1 || res.Docs[0].ID != "int-year" {
		t.Fatalf("expected int-year for year:2024 filter, got %+v", res.Docs)
	}

	res, _ = se.Search(context.Background(), "ceramic", map[string][]interface{}{"year": {"2025"}})
	if len(res.Docs) != 1 || res.Docs[0].ID != "float-year" {
		t.Fatalf("expected float-year for year:2025 filter, got %+v", res.Docs)
	}

	res, _ = se.Search(context.Background(), "ceramic", map[string][]interface{}{"year": {"2024", "2025"}})
	if len(res.Docs) != 2 {
		t.Fatalf("expected two docs for year:2024 OR year:2025 filter, got %+v", res.Docs)
	}
	got := map[string]bool{res.Docs[0].ID: true, res.Docs[1].ID: true}
	if !got["int-year"] || !got["float-year"] {
		t.Fatalf("expected int-year and float-year, got %+v", res.Docs)
	}

	res, _ = se.Search(context.Background(), "ceramic", map[string][]interface{}{"year": {"2022"}})
	if len(res.Docs) != 0 {
		t.Fatalf("expected no docs for year:2022 filter, got %+v", res.Docs)
	}
}

func TestIndexBatchingMatchesSingleBatch(t *testing.T) {
	docs := make([]map[string]interface{}, 100)
	for i := range docs {
		docs[i] = map[string]interface{}{
			"id":          fmt.Sprintf("p%03d", i+1),
			"name":        fmt.Sprintf("product %03d shared term%d", i+1, i%7),
			"category":    []interface{}{fmt.Sprintf("category-%02d", i%5), fmt.Sprintf("group-%02d", i%3)},
			"description": fmt.Sprintf("description words batch term%d marker%d", i%11, i%13),
			"year":        2020 + i%4,
		}
	}

	oneShot := NewSearchEngine(
		[]string{"name", "category", "description"},
		nil,
		map[string]bool{"category": true, "year": true},
		25,
	)
	batched := NewSearchEngine(
		[]string{"name", "category", "description"},
		nil,
		map[string]bool{"category": true, "year": true},
		25,
	)

	oneShot.Index(docs)
	batched.Index(docs[:33])
	batched.Index(docs[33:66])
	batched.Index(docs[66:])

	assertEnginesEquivalent(t, oneShot, batched)
}

// ---------------------------------------------------------------------------
// termSet tests
// ---------------------------------------------------------------------------

// inTermSet is a white-box helper that reads the engine's lock-free termSet.
func inTermSet(se *SearchEngine, term string) bool {
	_, ok := se.termSet.Load().Load(term)
	return ok
}

// termSetSnapshot returns all terms currently in the termSet as a sorted slice.
func termSetSnapshot(se *SearchEngine) []string {
	var terms []string
	se.termSet.Load().Range(func(k, _ any) bool {
		terms = append(terms, k.(string))
		return true
	})
	sort.Strings(terms)
	return terms
}

func newTermSetEngine() *SearchEngine {
	return NewSearchEngine(
		[]string{"title", "artist"},
		nil,
		map[string]bool{"genre": true},
		10,
	)
}

// TestTermSet_NonNilOnFreshEngine verifies a new engine has an initialized
// (non-nil) termSet so the first Load().Load() call cannot panic.
func TestTermSet_NonNilOnFreshEngine(t *testing.T) {
	se := newTermSetEngine()
	if se.termSet.Load() == nil {
		t.Fatal("termSet pointer is nil on fresh engine")
	}
	if inTermSet(se, "anything") {
		t.Fatal("fresh engine should have empty termSet")
	}
}

// TestTermSet_ContainsExactTokensAfterIndex verifies that after indexing a
// document the termSet holds exactly the lowercased tokens from the index fields.
func TestTermSet_ContainsExactTokensAfterIndex(t *testing.T) {
	se := newTermSetEngine()
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":     "1",
		"title":  "Blue Ocean",
		"artist": "Sun Kil Moon",
		"genre":  "folk", // filter field — must NOT be in termSet
	}); err != nil {
		t.Fatal(err)
	}

	// Index fields tokenize to: "blue", "ocean", "sun", "kil", "moon"
	for _, want := range []string{"blue", "ocean", "sun", "kil", "moon"} {
		if !inTermSet(se, want) {
			t.Errorf("expected %q in termSet", want)
		}
	}
	// Filter field value must not be indexed as a term
	if inTermSet(se, "folk") {
		t.Error("filter field value \"folk\" must not appear in termSet")
	}
	// The external id field must not be indexed as a term
	if inTermSet(se, "1") {
		t.Error("document id must not appear in termSet")
	}
}

// TestTermSet_StopwordsExcluded verifies "a", "the", "and" are never in termSet.
func TestTermSet_StopwordsExcluded(t *testing.T) {
	se := newTermSetEngine()
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":    "1",
		"title": "a song and the band",
	}); err != nil {
		t.Fatal(err)
	}

	for _, sw := range []string{"a", "the", "and"} {
		if inTermSet(se, sw) {
			t.Errorf("stopword %q must not be in termSet", sw)
		}
	}
	if !inTermSet(se, "song") || !inTermSet(se, "band") {
		t.Error("non-stopword tokens must be in termSet")
	}
}

// TestTermSet_CaseNormalized verifies tokens are stored lowercase regardless
// of the original document field casing.
func TestTermSet_CaseNormalized(t *testing.T) {
	se := newTermSetEngine()
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":    "1",
		"title": "LOUD THUNDER",
	}); err != nil {
		t.Fatal(err)
	}

	if !inTermSet(se, "loud") || !inTermSet(se, "thunder") {
		t.Error("tokens must be stored lowercase")
	}
	if inTermSet(se, "LOUD") || inTermSet(se, "THUNDER") {
		t.Error("uppercase form must not be in termSet")
	}
}

// TestTermSet_MultipleDocsSameTermBothPresent verifies that a term shared by
// two documents appears exactly once in the termSet.
func TestTermSet_MultipleDocsSameTermBothPresent(t *testing.T) {
	se := newTermSetEngine()
	docs := []map[string]interface{}{
		{"id": "1", "title": "River Song"},
		{"id": "2", "title": "River Delta"},
	}
	for _, d := range docs {
		if err := se.AddOrUpdateDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	if !inTermSet(se, "river") {
		t.Error("shared term \"river\" must be in termSet")
	}
	if !inTermSet(se, "song") || !inTermSet(se, "delta") {
		t.Error("unique terms must also be in termSet")
	}
}

// TestTermSet_DeletedDocTermsRemain verifies that deleting a doc
// does NOT remove its terms from the termSet — they persist.
func TestTermSet_DeletedDocTermsRemain(t *testing.T) {
	se := newTermSetEngine()
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":    "1",
		"title": "Phantom Signal",
	}); err != nil {
		t.Fatal(err)
	}

	se.DeleteDocument("1")

	// Terms must still be in termSet (DataMap still has them).
	if !inTermSet(se, "phantom") || !inTermSet(se, "signal") {
		t.Error("terms of a deleted doc must remain in termSet")
	}
}

// TestTermSet_UpdateAddsNewTerms verifies that updating a doc with new content
// adds the new terms to termSet while old terms also remain.
func TestTermSet_UpdateAddsNewTerms(t *testing.T) {
	se := newTermSetEngine()
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":    "1",
		"title": "Crimson Wave",
	}); err != nil {
		t.Fatal(err)
	}
	// Update with completely different title
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":    "1",
		"title": "Golden Storm",
	}); err != nil {
		t.Fatal(err)
	}

	// New terms must be present
	if !inTermSet(se, "golden") || !inTermSet(se, "storm") {
		t.Error("new terms from update must be in termSet")
	}
	// Old terms also remain (DataMap still holds them)
	if !inTermSet(se, "crimson") || !inTermSet(se, "wave") {
		t.Error("old terms must remain in termSet")
	}
}

// TestTermSet_Unicode verifies that Unicode tokens (CJK, Cyrillic, Arabic) are
// stored and looked up correctly in the termSet.
func TestTermSet_Unicode(t *testing.T) {
	se := newTermSetEngine()
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":    "1",
		"title": "日本語 音楽", // Japanese: "Japanese language" and "music"
	}); err != nil {
		t.Fatal(err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":    "2",
		"title": "ПРИВЕТ МИР", // Russian: "hello world"
	}); err != nil {
		t.Fatal(err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":    "3",
		"title": "موسيقى عربية", // Arabic: "Arabic music"
	}); err != nil {
		t.Fatal(err)
	}

	for _, term := range []string{"日本語", "音楽", "привет", "мир", "موسيقى", "عربية"} {
		if !inTermSet(se, term) {
			t.Errorf("Unicode term %q must be in termSet", term)
		}
	}
	// Uppercase Cyrillic must not appear — stored lowercase
	if inTermSet(se, "ПРИВЕТ") {
		t.Error("uppercase Cyrillic must not be in termSet")
	}
}

// TestTermSet_BuildDocumentIndex verifies that batch indexing via
// BuildDocumentIndex populates the termSet identically to per-doc indexing.
func TestTermSet_BuildDocumentIndex(t *testing.T) {
	se := newTermSetEngine()

	// Pre-register external IDs so BuildDocumentIndex can resolve them.
	se.mu.Lock()
	se.Documents[1] = map[string]interface{}{"id": "1", "title": "Midnight Rain", "artist": "Clara"}
	se.Documents[2] = map[string]interface{}{"id": "2", "title": "Desert Wind", "artist": "Omar"}
	se.ExternalToInternal["1"] = 1
	se.ExternalToInternal["2"] = 2
	se.InternalToExternal[1] = "1"
	se.InternalToExternal[2] = "2"
	se.nextInternalID = 3
	se.mu.Unlock()

	se.BuildDocumentIndex([]map[string]interface{}{
		{"id": "1", "title": "Midnight Rain", "artist": "Clara"},
		{"id": "2", "title": "Desert Wind", "artist": "Omar"},
	})

	for _, term := range []string{"midnight", "rain", "clara", "desert", "wind", "omar"} {
		if !inTermSet(se, term) {
			t.Errorf("term %q missing from termSet after BuildDocumentIndex", term)
		}
	}
}

// TestTermSet_UnindexedTermAbsent verifies that arbitrary strings not derived
// from indexed documents are never present in termSet.
func TestTermSet_UnindexedTermAbsent(t *testing.T) {
	se := newTermSetEngine()
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":    "1",
		"title": "Bright Star",
	}); err != nil {
		t.Fatal(err)
	}

	for _, absent := range []string{"dark", "moon", "planet", "galaxy", "nebula"} {
		if inTermSet(se, absent) {
			t.Errorf("term %q was never indexed but appears in termSet", absent)
		}
	}
}

func assertEnginesEquivalent(t *testing.T, want, got *SearchEngine) {
	t.Helper()

	want.mu.RLock()
	got.mu.RLock()
	defer got.mu.RUnlock()
	defer want.mu.RUnlock()

	if !reflect.DeepEqual(want.DataMap, got.DataMap) {
		t.Fatalf("DataMap mismatch")
	}
	if !reflect.DeepEqual(want.Prefix, got.Prefix) {
		t.Fatalf("Prefix mismatch")
	}
	if !reflect.DeepEqual(want.FilterBits, got.FilterBits) {
		t.Fatalf("FilterBits mismatch")
	}
	if !reflect.DeepEqual(want.Documents, got.Documents) {
		t.Fatalf("Documents mismatch")
	}
	if !reflect.DeepEqual(want.DocDeleted, got.DocDeleted) {
		t.Fatalf("DocDeleted mismatch")
	}
	if !reflect.DeepEqual(want.ExternalToInternal, got.ExternalToInternal) {
		t.Fatalf("ExternalToInternal mismatch")
	}
	if !reflect.DeepEqual(want.InternalToExternal, got.InternalToExternal) {
		t.Fatalf("InternalToExternal mismatch")
	}
	if want.nextInternalID != got.nextInternalID {
		t.Fatalf("nextInternalID mismatch: got %d want %d", got.nextInternalID, want.nextInternalID)
	}
}

func TestStatsAndStoredDocumentCount_AfterUpdateAndDelete(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, map[string]bool{"year": true}, 10)
	add := func(id, title string) {
		t.Helper()
		if err := se.AddOrUpdateDocument(map[string]interface{}{"id": id, "title": title}); err != nil {
			t.Fatalf("AddOrUpdateDocument(%q): %v", id, err)
		}
	}

	add("a", "apple")
	add("b", "banana")
	add("a", "apricot") // update: old version of "a" becomes a tombstone
	if !se.DeleteDocument("b") {
		t.Fatal("DeleteDocument(b) = false")
	}
	if se.DeleteDocument("b") {
		t.Fatal("deleting b twice must return false")
	}

	st := se.Stats()
	if st.ActiveDocuments != 1 || st.StoredDocuments != 1 || st.TombstonedVersions != 2 {
		t.Fatalf("unexpected stats: %+v", st)
	}
	if !reflect.DeepEqual(st.IndexFields, []string{"title"}) || !st.Filters["year"] || st.ResultSize != 10 {
		t.Fatalf("unexpected config in stats: %+v", st)
	}
	if got := se.StoredDocumentCount(); got != 1 {
		t.Fatalf("StoredDocumentCount = %d, want 1", got)
	}

	// Stats must hand out copies: mutating them must not change the engine.
	st.IndexFields[0] = "changed"
	st.Filters["other"] = true
	if again := se.Stats(); again.IndexFields[0] != "title" || again.Filters["other"] {
		t.Fatalf("Stats leaked internal state: %+v", again)
	}
}

// Bulk Index finalizes by sorting SymSpell buckets by term frequency. With the
// fuzzy cap pinned to 1, the misspelled "irom" only resolves to the common
// "iron" if that sort ran.
func TestIndex_SortsFuzzyByFrequency(t *testing.T) {
	old := SingleTermMaxFuzzyTokens
	SingleTermMaxFuzzyTokens = 1
	t.Cleanup(func() { SingleTermMaxFuzzyTokens = old })

	se := NewSearchEngine([]string{"title"}, nil, nil, 20)
	var docs []map[string]interface{}
	for _, w := range []string{"irok", "irop", "iroq", "iros"} {
		docs = append(docs, map[string]interface{}{"id": w, "title": w})
	}
	for _, id := range []string{"n0", "n1", "n2", "n3", "n4"} {
		docs = append(docs, map[string]interface{}{"id": id, "title": "iron"})
	}
	se.Index(docs)

	res, err := se.Search(context.Background(), "irom", nil)
	if err != nil || res == nil {
		t.Fatalf("Search: res=%v err=%v", res, err)
	}
	if len(res.Docs) != 5 {
		t.Fatalf("expected the 5 'iron' docs, got %+v", res.Docs)
	}
	for _, d := range res.Docs {
		if d.ID[0] != 'n' {
			t.Fatalf("expected only 'iron' docs, got %+v", res.Docs)
		}
	}
}

// Popularity was removed: a "popularity" field is just another document field
// and must not change the score.
func TestPopularityFieldDoesNotAffectScore(t *testing.T) {
	se := NewSearchEngine([]string{"title"}, nil, nil, 10)
	for _, doc := range []map[string]interface{}{
		{"id": "popular", "title": "apple", "popularity": 1000000},
		{"id": "plain", "title": "apple"},
	} {
		if err := se.AddOrUpdateDocument(doc); err != nil {
			t.Fatal(err)
		}
	}

	res, err := se.Search(context.Background(), "apple", nil)
	if err != nil || res == nil || len(res.Docs) != 2 {
		t.Fatalf("Search: res=%v err=%v", res, err)
	}
	if res.Docs[0].Score != res.Docs[1].Score {
		t.Fatalf("popularity must not change scores, got %+v", res.Docs)
	}
	for _, d := range res.Docs {
		if d.ID == "popular" {
			if _, ok := d.Data["popularity"]; !ok {
				t.Fatalf("popularity should stay a plain stored field, got %+v", d.Data)
			}
		}
	}
}

func TestFilterKeys(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
		want  []string
	}{
		{"string", "2020", []string{"year:2020"}},
		{"empty string", "", nil},
		{"int", 2020, []string{"year:2020"}},
		{"float64", float64(2020), []string{"year:2020"}},
		{"string slice skips empties", []string{"a", "", "b"}, []string{"year:a", "year:b"}},
		{"interface slice", []interface{}{"a", float64(2), nil, true}, []string{"year:a", "year:2"}},
		{"unsupported type", true, nil},
		{"nil", nil, nil},
	}
	for _, tt := range tests {
		got := filterKeys("year", tt.value)
		if len(got) == 0 && len(tt.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: filterKeys = %v, want %v", tt.name, got, tt.want)
		}
	}
}
