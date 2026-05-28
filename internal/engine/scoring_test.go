package engine

import "testing"

func TestWeightedFieldsAffectRanking(t *testing.T) {
	se := NewSearchEngineWithFieldWeights(
		[]string{"title", "description"},
		map[string]int{"title": 5, "description": 1},
		nil,
		10,
	)

	docs := []map[string]interface{}{
		{
			"id":          "title-hit",
			"title":       "apple",
			"description": "quiet quiet quiet quiet quiet",
		},
		{
			"id":          "description-hit",
			"title":       "quiet",
			"description": "apple",
		},
	}
	se.Index(docs)

	res := se.SearchOneTerm("apple", nil)
	if len(res) != 2 {
		t.Fatalf("expected 2 apple results, got %d: %+v", len(res), res)
	}
	if res[0].ID != "title-hit" {
		t.Fatalf("expected title-weighted doc first, got %+v", res)
	}
	if res[0].Score <= res[1].Score {
		t.Fatalf("expected title hit score > description hit score, got %+v", res)
	}

	dir := t.TempDir()
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}
	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if loaded.FieldWeights["title"] != 5 || loaded.FieldWeights["description"] != 1 {
		t.Fatalf("field weights not restored: %+v", loaded.FieldWeights)
	}
	res = loaded.SearchOneTerm("apple", nil)
	if len(res) != 2 || res[0].ID != "title-hit" || res[0].Score <= res[1].Score {
		t.Fatalf("loaded weighted ranking mismatch: %+v", res)
	}
}

func TestNormalizeFieldWeightsDefaultsInvalidAndExtraWeights(t *testing.T) {
	got := normalizeFieldWeights(
		[]string{"title", "artist", "album"},
		map[string]int{
			"title":  4,
			"artist": 0,
			"extra":  9,
		},
	)

	if got["title"] != 4 {
		t.Fatalf("expected title weight 4, got %d", got["title"])
	}
	if got["artist"] != 1 {
		t.Fatalf("expected invalid artist weight to default to 1, got %d", got["artist"])
	}
	if got["album"] != 1 {
		t.Fatalf("expected missing album weight to default to 1, got %d", got["album"])
	}
	if _, ok := got["extra"]; ok {
		t.Fatalf("unexpected non-index field weight retained: %+v", got)
	}
}

func TestWeightedFieldsDefaultMatchesUnweightedScoring(t *testing.T) {
	doc := map[string]interface{}{
		"id":          "1",
		"title":       "apple",
		"description": "banana",
	}

	unweighted := weightedTokenScores(doc, []string{"title", "description"}, nil)
	defaulted := weightedTokenScores(
		doc,
		[]string{"title", "description"},
		normalizeFieldWeights([]string{"title", "description"}, nil),
	)

	if unweighted["apple"] != defaulted["apple"] || unweighted["banana"] != defaulted["banana"] {
		t.Fatalf("default field weights changed scoring: unweighted=%v defaulted=%v", unweighted, defaulted)
	}
}

func TestAddOrUpdateDocumentUsesFieldWeights(t *testing.T) {
	se := NewSearchEngineWithFieldWeights(
		[]string{"title", "description"},
		map[string]int{"title": 4, "description": 1},
		nil,
		10,
	)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":          "title-hit",
		"title":       "apple",
		"description": "quiet quiet quiet quiet",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument title-hit: %v", err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":          "description-hit",
		"title":       "quiet",
		"description": "apple",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument description-hit: %v", err)
	}

	res := se.SearchOneTerm("apple", nil)
	if len(res) != 2 {
		t.Fatalf("expected 2 apple results, got %d: %+v", len(res), res)
	}
	if res[0].ID != "title-hit" {
		t.Fatalf("expected title-weighted single-doc upsert first, got %+v", res)
	}
}

func TestAddOrUpdateDocumentReindexesWeightedScores(t *testing.T) {
	se := NewSearchEngineWithFieldWeights(
		[]string{"title", "description"},
		map[string]int{"title": 5, "description": 1},
		nil,
		10,
	)

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":          "moving-doc",
		"title":       "apple",
		"description": "quiet",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument initial moving-doc: %v", err)
	}
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":          "stable-doc",
		"title":       "quiet",
		"description": "apple",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument stable-doc: %v", err)
	}

	res := se.SearchOneTerm("apple", nil)
	if len(res) != 2 || res[0].ID != "moving-doc" {
		t.Fatalf("expected initially title-weighted moving-doc first, got %+v", res)
	}

	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":          "moving-doc",
		"title":       "banana",
		"description": "quiet",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument updated moving-doc: %v", err)
	}

	res = se.SearchOneTerm("apple", nil)
	if len(res) != 1 || res[0].ID != "stable-doc" {
		t.Fatalf("expected old weighted apple posting to be tombstoned, got %+v", res)
	}

	res = se.SearchOneTerm("banana", nil)
	if len(res) != 1 || res[0].ID != "moving-doc" {
		t.Fatalf("expected updated weighted banana posting, got %+v", res)
	}
}

func TestWeightedTokenScoresHandlesStringSlices(t *testing.T) {
	doc := map[string]interface{}{
		"id":     "1",
		"title":  []string{"apple"},
		"artist": []interface{}{"apple", "quiet"},
	}

	scores := weightedTokenScores(
		doc,
		[]string{"title", "artist"},
		map[string]int{"title": 4, "artist": 1},
	)

	if scores["apple"] <= scores["quiet"] {
		t.Fatalf("expected weighted title apple score to exceed artist-only quiet score: %+v", scores)
	}
}
