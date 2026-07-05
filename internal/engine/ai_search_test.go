package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// funcEmbedder resolves a vector by exact input string via an explicit rule.
// Unlike stubEmbedder's token-containment matching, this lets a test give a
// document's indexed content and a query string independent vectors even when
// they share literal words — needed to prove classic and AI results can come
// from two different documents in the very same query.
type funcEmbedder struct {
	fn func(text string) ([]float32, error)

	mu    sync.Mutex
	calls int
}

func (f *funcEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.fn(text)
}

// withAIParallelSearchMinTokens temporarily overrides the package-level knob
// and restores it at test cleanup.
func withAIParallelSearchMinTokens(t *testing.T, n int) {
	t.Helper()
	orig := AIParallelSearchMinTokens
	AIParallelSearchMinTokens = n
	t.Cleanup(func() { AIParallelSearchMinTokens = orig })
}

func withAISearchTopNCategories(t *testing.T, n int) {
	t.Helper()
	orig := AISearchTopNCategories
	AISearchTopNCategories = n
	t.Cleanup(func() { AISearchTopNCategories = orig })
}

// carBikeEngine builds an AI-enabled engine with two disjoint categories (car,
// bicycle) and a query-only keyword ("vehicle") that embeds to the same vector
// as "car" without appearing in any indexed document — so a query containing
// it can only be found through the AI vector path, never through keyword
// matching. This isolates "classic found nothing, AI did" from any dedup path.
func carBikeEngine(t *testing.T) (*SearchEngine, *stubEmbedder) {
	t.Helper()
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)
	emb := &stubEmbedder{vectors: map[string][]float32{
		"car":     {1, 0, 0, 0},
		"bicycle": {0, 1, 0, 0},
		"vehicle": {1, 0, 0, 0}, // synonym for car, never indexed
	}}
	se.EnableAI(emb)
	se.Index([]map[string]interface{}{
		{"id": "1", "name": "red car"},
		{"id": "2", "name": "blue bicycle"},
	})
	return se, emb
}

func TestSearch_AIFindsSemanticMatchAbsentFromClassic(t *testing.T) {
	se, _ := carBikeEngine(t)

	// "fast vehicle": neither token is indexed anywhere, so the classic
	// multi-term search has nothing to match and would normally return nil.
	res, err := se.Search(context.Background(), "fast vehicle", nil)
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if res == nil {
		t.Fatal("Search returned nil; want AI-only result wrapped in a SearchResult")
	}
	if len(res.Docs) != 1 {
		t.Fatalf("Docs = %v, want exactly 1 AI hit", res.Docs)
	}
	got := res.Docs[0]
	if got.ID != "1" || !got.AI {
		t.Fatalf("got %+v, want AI hit for doc 1 (car)", got)
	}
	if got.Score <= 0 {
		t.Fatalf("AI hit Score = %d, want > 0", got.Score)
	}
	if got.Data == nil || got.Data["name"] != "red car" {
		t.Fatalf("AI hit Data not populated correctly: %+v", got.Data)
	}
}

// TestSearch_ClassicAndAIBothPresent proves classic and AI can each contribute
// a hit for a *different* document from the same query. MultiTermSearch is
// AND-across-tokens, so the classic-matching doc ("hit") must literally
// contain every query token; funcEmbedder then independently routes the exact
// query string to a different document's ("other") category, something a
// literal-content-derived embedding could never do on its own.
func TestSearch_ClassicAndAIBothPresent(t *testing.T) {
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)
	emb := &funcEmbedder{fn: func(text string) ([]float32, error) {
		switch text {
		case "wireless mouse gadget": // doc "hit" content, embedded at index time
			return []float32{1, 0, 0, 0}, nil
		case "banana bread": // doc "other" content, embedded at index time
			return []float32{0, 1, 0, 0}, nil
		case "wireless gadget": // the query: routed to doc "other"'s category
			return []float32{0, 1, 0, 0}, nil
		}
		return nil, fmt.Errorf("no rule for %q", text)
	}}
	se.EnableAI(emb)
	se.Index([]map[string]interface{}{
		{"id": "hit", "name": "wireless mouse gadget"},
		{"id": "other", "name": "banana bread"},
	})
	if got := se.AI.CategoryCount(); got != 2 {
		t.Fatalf("setup: CategoryCount = %d, want 2", got)
	}

	res, err := se.Search(context.Background(), "wireless gadget", nil)
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(res.Docs) != 2 {
		t.Fatalf("Docs = %+v, want 2 (one classic, one AI)", res.Docs)
	}

	var classicHit, aiHit *ReturnedDocument
	for i := range res.Docs {
		d := &res.Docs[i]
		if d.AI {
			aiHit = d
		} else {
			classicHit = d
		}
	}
	if classicHit == nil || classicHit.ID != "hit" {
		t.Fatalf("classic hit = %+v, want doc %q with AI=false", classicHit, "hit")
	}
	if aiHit == nil || aiHit.ID != "other" {
		t.Fatalf("AI hit = %+v, want doc %q with AI=true", aiHit, "other")
	}
}

func TestSearch_SingleTermQuerySkipsAIByDefault(t *testing.T) {
	se, emb := carBikeEngine(t)

	before := emb.callCount()
	res, err := se.Search(context.Background(), "vehicle", nil)
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if got := emb.callCount(); got != before {
		t.Fatalf("embedder calls = %d, want %d (single-token query must not trigger AI search)", got, before)
	}
	for _, d := range res.Docs {
		if d.AI {
			t.Fatalf("unexpected AI hit for single-token query below AIParallelSearchMinTokens: %+v", d)
		}
	}
}

func TestSearch_AIParallelSearchMinTokensConfigurable(t *testing.T) {
	se, emb := carBikeEngine(t)
	withAIParallelSearchMinTokens(t, 1)

	before := emb.callCount()
	res, err := se.Search(context.Background(), "vehicle", nil)
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if got := emb.callCount(); got != before+1 {
		t.Fatalf("embedder calls = %d, want %d (lowering the threshold to 1 must trigger AI on a single-token query)", got, before+1)
	}
	found := false
	for _, d := range res.Docs {
		if d.AI && d.ID == "1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected AI hit for doc 1 with MinTokens=1, got %+v", res.Docs)
	}
}

func TestSearch_AIDisabledEngineNeverSetsAIFlag(t *testing.T) {
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)
	se.Index([]map[string]interface{}{
		{"id": "1", "name": "red car"},
		{"id": "2", "name": "blue bicycle"},
	})

	res, err := se.Search(context.Background(), "red car", nil)
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(res.Docs) == 0 {
		t.Fatal("expected classic hits")
	}
	for _, d := range res.Docs {
		if d.AI {
			t.Fatalf("AI-disabled engine must never set AI=true: %+v", d)
		}
	}
}

func TestSearch_AINoMatchLeavesClassicResultUnchanged(t *testing.T) {
	se, _ := carBikeEngine(t)

	// Both tokens are indexed keywords with no AI-only synonym involved, and
	// no stub vector matches "red car" as a whole new concept outside the car
	// category — but "red" and "car" are both known keywords, so classic and
	// AI should agree on doc 1. Assert the classic doc is present and no
	// spurious AI duplicate corrupts the non-AI case when AI simply confirms
	// the same document (both may legitimately appear per current no-dedup design).
	res, err := se.Search(context.Background(), "red car", nil)
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	classicCount, aiCount := 0, 0
	for _, d := range res.Docs {
		if d.ID != "1" {
			t.Fatalf("unexpected doc in results: %+v", d)
		}
		if d.AI {
			aiCount++
		} else {
			classicCount++
		}
	}
	if classicCount != 1 {
		t.Fatalf("classic hits for doc 1 = %d, want 1", classicCount)
	}
	if aiCount != 1 {
		t.Fatalf("AI hits for doc 1 = %d, want 1 (no dedup by design)", aiCount)
	}
}

func TestAIVectorSearch_DisabledOrNoEmbedder(t *testing.T) {
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)
	if got := se.aiVectorSearch(context.Background(), "car", nil); got != nil {
		t.Fatalf("AI-disabled engine: got %v, want nil", got)
	}

	se.AI = newAIIndex(nil)
	if got := se.aiVectorSearch(context.Background(), "car", nil); got != nil {
		t.Fatalf("no-embedder AI index: got %v, want nil", got)
	}
}

func TestAIVectorSearch_EmbedErrorAndZeroVector(t *testing.T) {
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)
	se.EnableAI(&stubEmbedder{vectors: map[string][]float32{
		"car":  {1, 0, 0, 0},
		"void": {0, 0, 0, 0},
	}})
	se.Index([]map[string]interface{}{{"id": "1", "name": "red car"}})

	// Embed error (no stub vector matches the query at all).
	if got := se.aiVectorSearch(context.Background(), "xyzzy", nil); got != nil {
		t.Fatalf("embed error: got %v, want nil", got)
	}
	// Zero-norm query embedding.
	if got := se.aiVectorSearch(context.Background(), "void", nil); got != nil {
		t.Fatalf("zero-norm query vector: got %v, want nil", got)
	}
}

func TestAIVectorSearch_ResultSizeCapsResults(t *testing.T) {
	se := NewSearchEngine([]string{"name"}, nil, nil, 1) // ResultSize=1
	se.EnableAI(&stubEmbedder{vectors: map[string][]float32{
		"phone": {1, 0, 0, 0},
	}})
	se.AI.maxPerDoc = 1
	se.Index([]map[string]interface{}{
		{"id": "1", "name": "apple phone"},
		{"id": "2", "name": "pixel phone"},
		{"id": "3", "name": "galaxy phone"},
	})

	got := se.aiVectorSearch(context.Background(), "phone", nil)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (capped by ResultSize)", len(got))
	}
}

func TestAIVectorSearch_ZeroResultSizeReturnsNil(t *testing.T) {
	se := NewSearchEngine([]string{"name"}, nil, nil, 0)
	se.EnableAI(&stubEmbedder{vectors: map[string][]float32{"phone": {1, 0, 0, 0}}})
	se.Index([]map[string]interface{}{{"id": "1", "name": "apple phone"}})

	if got := se.aiVectorSearch(context.Background(), "phone", nil); got != nil {
		t.Fatalf("ResultSize=0: got %v, want nil", got)
	}
}

func TestAIVectorSearch_TopNCategoriesLimitsScan(t *testing.T) {
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)
	se.EnableAI(&stubEmbedder{vectors: map[string][]float32{
		"phone":  {1, 0, 0, 0},
		"tablet": {0, 1, 0, 0},
		"watch":  {0, 0, 1, 0},
		// Equidistant-ish query that is somewhat aligned with all three axes;
		// with topN=1 only the single nearest category can contribute docs.
		"query": {0.9, 0.3, 0.3, 0},
	}})
	se.Index([]map[string]interface{}{
		{"id": "p", "name": "apple phone"},
		{"id": "t", "name": "android tablet"},
		{"id": "w", "name": "smart watch"},
	})
	withAISearchTopNCategories(t, 1)

	got := se.aiVectorSearch(context.Background(), "query", nil)
	if len(got) != 1 || got[0].ID != "p" {
		t.Fatalf("got %+v, want exactly doc p (phone, nearest category) with topN=1", got)
	}
}

func TestAIVectorSearch_NegativeSimilarityExcluded(t *testing.T) {
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)
	se.EnableAI(&stubEmbedder{vectors: map[string][]float32{
		"phone":   {1, 0, 0, 0},
		"reverse": {-1, 0, 0, 0}, // opposite direction: cosine < 0 vs the phone category
	}})
	se.Index([]map[string]interface{}{{"id": "1", "name": "apple phone"}})

	got := se.aiVectorSearch(context.Background(), "reverse", nil)
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty (negative cosine similarity must be excluded)", got)
	}
}

// TestAIVectorSearch_SkipsStaleCandidate simulates the narrow race window in
// DeleteDocument where se.ExternalToInternal/DocDeleted have already been
// updated but ai.RemoveDocument has not yet run (they are separate locks by
// design — see CategorizeDocument). aiVectorSearch must skip such a candidate
// instead of returning stale/zero-value document data.
func TestAIVectorSearch_SkipsStaleCandidate(t *testing.T) {
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)
	se.EnableAI(&stubEmbedder{vectors: map[string][]float32{"phone": {1, 0, 0, 0}}})
	se.Index([]map[string]interface{}{
		{"id": "1", "name": "apple phone"},
		{"id": "2", "name": "pixel phone"},
	})

	se.mu.Lock()
	delete(se.ExternalToInternal, "1") // simulates: removed from ExternalToInternal, not yet from ai.docs
	internalID2 := se.ExternalToInternal["2"]
	se.DocDeleted[internalID2] = true // simulates: tombstoned, not yet removed from ai.docs
	se.mu.Unlock()

	got := se.aiVectorSearch(context.Background(), "phone", nil)
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty: both candidates are stale (one missing from ExternalToInternal, one tombstoned)", got)
	}
}

// TestAIVectorSearch_ResultsDescendingScore pins the output ordering: highest
// cosine similarity first, matching the classic search loops. (An earlier
// draft drained the min-heap by appending, which silently produced ASCENDING
// order — this test exists so that regression cannot come back.)
func TestAIVectorSearch_ResultsDescendingScore(t *testing.T) {
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)
	emb := &funcEmbedder{fn: func(text string) ([]float32, error) {
		switch text {
		case "alpha item":
			return []float32{1, 0, 0, 0}, nil // cos vs query = 1.00
		case "bravo item":
			return []float32{0.8, 0.6, 0, 0}, nil // cos vs query = 0.80
		case "charlie item":
			return []float32{0.6, 0.8, 0, 0}, nil // cos vs query = 0.60
		case "probe":
			return []float32{1, 0, 0, 0}, nil
		}
		return nil, fmt.Errorf("no rule for %q", text)
	}}
	se.EnableAI(emb)
	se.Index([]map[string]interface{}{
		{"id": "c", "name": "charlie item"},
		{"id": "a", "name": "alpha item"},
		{"id": "b", "name": "bravo item"},
	})

	got := se.aiVectorSearch(context.Background(), "probe", nil)
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3: %+v", len(got), got)
	}
	wantOrder := []string{"a", "b", "c"}
	for i, want := range wantOrder {
		if got[i].ID != want {
			t.Fatalf("got order %v, want %v (descending similarity)", []string{got[0].ID, got[1].ID, got[2].ID}, wantOrder)
		}
	}
	if !(got[0].Score > got[1].Score && got[1].Score > got[2].Score) {
		t.Fatalf("scores not strictly descending: %d, %d, %d", got[0].Score, got[1].Score, got[2].Score)
	}
	// Cross-check the absolute scale: cos=1.0 → aiScoreScale.
	if got[0].Score != int(1.0*aiScoreScale) {
		t.Fatalf("top score = %d, want %d", got[0].Score, int(1.0*aiScoreScale))
	}
}

// TestSearch_FiltersApplyToAIHits pins that the AI vector path honors the same
// filters as the classic path: a document excluded by the active filter must
// not surface as an AI hit, and must come back once the filter matches it.
func TestSearch_FiltersApplyToAIHits(t *testing.T) {
	se := NewSearchEngine([]string{"name"}, nil, map[string]bool{"year": true}, 10)
	se.EnableAI(&stubEmbedder{vectors: map[string][]float32{
		"car":     {1, 0, 0, 0},
		"bicycle": {0, 1, 0, 0},
		"vehicle": {1, 0, 0, 0}, // query-only synonym for car
	}})
	se.Index([]map[string]interface{}{
		{"id": "1", "name": "red car", "year": "2020"},
		{"id": "2", "name": "blue bicycle", "year": "2021"},
	})

	// The query only matches through AI (neither token indexed). Filter that
	// excludes the car doc → no results at all.
	res, err := se.Search(context.Background(), "fast vehicle", map[string][]interface{}{"year": {"2021"}})
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if res != nil && len(res.Docs) != 0 {
		t.Fatalf("filter year=2021 must exclude the AI car hit, got %+v", res.Docs)
	}

	// Same query with the matching filter → the AI hit comes through.
	res, err = se.Search(context.Background(), "fast vehicle", map[string][]interface{}{"year": {"2020"}})
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if res == nil || len(res.Docs) != 1 || res.Docs[0].ID != "1" || !res.Docs[0].AI {
		t.Fatalf("filter year=2020: got %+v, want the AI hit for doc 1", res)
	}

	// Filter matching nothing at all → nil short-circuit, no embed panic.
	res, err = se.Search(context.Background(), "fast vehicle", map[string][]interface{}{"year": {"1999"}})
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if res != nil && len(res.Docs) != 0 {
		t.Fatalf("filter year=1999 must match nothing, got %+v", res.Docs)
	}
}

// TestAIVectorSearch_FilteredCandidatesDoNotConsumeSlots pins the reason the
// filter check runs during the scan rather than after the heap: with
// ResultSize=1 and the best-scoring candidate filtered out, the runner-up must
// still fill the single slot.
func TestAIVectorSearch_FilteredCandidatesDoNotConsumeSlots(t *testing.T) {
	se := NewSearchEngine([]string{"name"}, nil, map[string]bool{"year": true}, 1)
	emb := &funcEmbedder{fn: func(text string) ([]float32, error) {
		switch text {
		case "alpha item":
			return []float32{1, 0, 0, 0}, nil // best match, but filtered out
		case "bravo item":
			return []float32{0.8, 0.6, 0, 0}, nil // runner-up, passes the filter
		case "probe":
			return []float32{1, 0, 0, 0}, nil
		}
		return nil, fmt.Errorf("no rule for %q", text)
	}}
	se.EnableAI(emb)
	se.Index([]map[string]interface{}{
		{"id": "a", "name": "alpha item", "year": "2020"},
		{"id": "b", "name": "bravo item", "year": "2021"},
	})

	got := se.aiVectorSearch(context.Background(), "probe", map[string][]interface{}{"year": {"2021"}})
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("got %+v, want doc b: the filtered-out best match must not consume the only result slot", got)
	}
}

func TestAIVectorSearch_ContextCancellationStillSafe(t *testing.T) {
	se, _ := carBikeEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// aiVectorSearch has no ctx.Err() checkpoint (unlike the classic loops);
	// it must still run to completion without panicking on an already-canceled
	// context, since the embedder mock ignores ctx.
	got := se.aiVectorSearch(ctx, "vehicle", nil)
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("got %+v, want doc 1", got)
	}
}
