package engine

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// stubEmbedder returns deterministic unit-ish vectors per keyword so documents
// about the same topic cluster together and different topics stay apart. It is
// safe for concurrent use: CategorizeDocs calls Embed from parallel workers.
type stubEmbedder struct {
	vectors map[string][]float32

	mu    sync.Mutex
	calls int
}

func (s *stubEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	for kw, v := range s.vectors {
		if containsWord(text, kw) {
			return append([]float32(nil), v...), nil
		}
	}
	return nil, fmt.Errorf("no stub vector for %q", text)
}

func (s *stubEmbedder) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func containsWord(text, kw string) bool {
	for _, tok := range Tokenize(text) {
		if tok == kw {
			return true
		}
	}
	return false
}

func newAITestEngine(t *testing.T) (*SearchEngine, *stubEmbedder) {
	t.Helper()
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)
	emb := &stubEmbedder{vectors: map[string][]float32{
		"phone":  {1, 0, 0, 0},
		"laptop": {0.9, 0.1, 0, 0}, // close to phone -> same category
		"banana": {0, 0, 1, 0},     // far -> own category
	}}
	se.EnableAI(emb)
	return se, emb
}

func TestAICategorizeOnIndex(t *testing.T) {
	se, emb := newAITestEngine(t)

	se.Index([]map[string]interface{}{
		{"id": "1", "name": "apple phone"},
		{"id": "2", "name": "dell laptop"},
		{"id": "3", "name": "fresh banana"},
	})

	if got := se.AI.AIDocCount(); got != 3 {
		t.Fatalf("AIDocCount = %d, want 3", got)
	}
	if got := se.AI.CategoryCount(); got != 2 {
		t.Fatalf("CategoryCount = %d, want 2 (electronics + fruit)", got)
	}
	if got := emb.callCount(); got != 3 {
		t.Fatalf("embedder calls = %d, want 3", got)
	}

	d1, ok := se.AI.GetAIDocument("1")
	if !ok || len(d1.Categories) == 0 {
		t.Fatalf("doc 1 not categorized: %+v", d1)
	}
	d2, _ := se.AI.GetAIDocument("2")
	if d1.Categories[0] != d2.Categories[0] {
		t.Fatalf("phone and laptop should share a category: %v vs %v", d1.Categories, d2.Categories)
	}
	d3, _ := se.AI.GetAIDocument("3")
	if d3.Categories[0] == d1.Categories[0] {
		t.Fatalf("banana should not share the phone category: %v", d3.Categories)
	}
}

func TestAIUpdateAndDelete(t *testing.T) {
	se, _ := newAITestEngine(t)

	if err := se.AddOrUpdateDocument(map[string]interface{}{"id": "1", "name": "apple phone"}); err != nil {
		t.Fatal(err)
	}
	d1, _ := se.AI.GetAIDocument("1")
	phoneCat := d1.Categories[0]

	// Update the same external ID with different content: it must move category,
	// and the emptied category must be pruned.
	if err := se.AddOrUpdateDocument(map[string]interface{}{"id": "1", "name": "fresh banana"}); err != nil {
		t.Fatal(err)
	}
	d1, _ = se.AI.GetAIDocument("1")
	if d1.Categories[0] == phoneCat {
		t.Fatalf("doc 1 should have moved out of %s", phoneCat)
	}
	if got := se.AI.CategoryCount(); got != 1 {
		t.Fatalf("CategoryCount = %d, want 1 (old category pruned)", got)
	}

	if !se.DeleteDocument("1") {
		t.Fatal("DeleteDocument returned false")
	}
	if _, ok := se.AI.GetAIDocument("1"); ok {
		t.Fatal("doc 1 still in AI index after delete")
	}
	if got := se.AI.CategoryCount(); got != 0 {
		t.Fatalf("CategoryCount = %d, want 0 after delete", got)
	}
}

func TestAIPersistAndLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	se, _ := newAITestEngine(t)

	se.Index([]map[string]interface{}{
		{"id": "1", "name": "apple phone"},
		{"id": "2", "name": "fresh banana"},
	})

	// Standalone AI-only persist must work without SaveAll.
	if err := se.PersistCategoryEmbed(dir); err != nil {
		t.Fatalf("PersistCategoryEmbed: %v", err)
	}
	// SaveAll must also write both snapshots.
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}

	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if !loaded.AIEnabled() {
		t.Fatal("loaded engine should have AI state restored")
	}
	if got := loaded.AI.AIDocCount(); got != 2 {
		t.Fatalf("loaded AIDocCount = %d, want 2", got)
	}
	if got, want := loaded.AI.CategoryCount(), se.AI.CategoryCount(); got != want {
		t.Fatalf("loaded CategoryCount = %d, want %d", got, want)
	}

	orig, _ := se.AI.GetAIDocument("1")
	rest, ok := loaded.AI.GetAIDocument("1")
	if !ok {
		t.Fatal("doc 1 missing after load")
	}
	if fmt.Sprint(rest.Categories) != fmt.Sprint(orig.Categories) {
		t.Fatalf("categories differ after roundtrip: %v vs %v", rest.Categories, orig.Categories)
	}
	if len(rest.Vector) != len(orig.Vector) || rest.Norm != orig.Norm {
		t.Fatalf("vector/norm differ after roundtrip")
	}
	if rest.Norm == 0 {
		t.Fatal("restored norm is zero")
	}

	// Embedder is not persisted: categorizing without one must fail cleanly...
	if err := loaded.CategorizeDocument(context.Background(), "9", map[string]interface{}{"id": "9", "name": "apple phone"}); err == nil {
		t.Fatal("expected error when categorizing without an embedder")
	}
	// ...and re-attaching one must preserve the existing state.
	loaded.EnableAI(&stubEmbedder{vectors: map[string][]float32{"phone": {1, 0, 0, 0}}})
	if got := loaded.AI.AIDocCount(); got != 2 {
		t.Fatalf("EnableAI reset restored state: AIDocCount = %d, want 2", got)
	}
	if err := loaded.CategorizeDocument(context.Background(), "9", map[string]interface{}{"id": "9", "name": "apple phone"}); err != nil {
		t.Fatalf("categorize after re-attach: %v", err)
	}
}

// checkAIConsistency verifies the structural invariants between docs,
// categories, and catDocs:
//   - every category a doc lists exists, and catDocs for it contains the doc
//   - every catDocs member exists and lists that category back
//   - every category's Count equals its catDocs set size, and no category is empty
func checkAIConsistency(t *testing.T, ai *AIIndex) {
	t.Helper()
	ai.mu.RLock()
	defer ai.mu.RUnlock()

	for id, doc := range ai.docs {
		if doc.ID != id {
			t.Fatalf("doc key %q != doc.ID %q", id, doc.ID)
		}
		if len(doc.Categories) == 0 {
			t.Fatalf("doc %q has no categories", id)
		}
		if doc.Norm == 0 || len(doc.Vector) == 0 {
			t.Fatalf("doc %q has empty vector/norm", id)
		}
		for _, name := range doc.Categories {
			if _, ok := ai.categories[name]; !ok {
				t.Fatalf("doc %q lists missing category %q", id, name)
			}
			if _, ok := ai.catDocs[name][id]; !ok {
				t.Fatalf("catDocs[%q] missing doc %q", name, id)
			}
		}
	}
	for name, set := range ai.catDocs {
		c, ok := ai.categories[name]
		if !ok {
			t.Fatalf("catDocs has entry for missing category %q", name)
		}
		if c.Count != len(set) {
			t.Fatalf("category %q Count=%d but catDocs size=%d", name, c.Count, len(set))
		}
		for id := range set {
			doc, ok := ai.docs[id]
			if !ok {
				t.Fatalf("catDocs[%q] references missing doc %q", name, id)
			}
			found := false
			for _, dc := range doc.Categories {
				if dc == name {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("doc %q in catDocs[%q] but does not list it", id, name)
			}
		}
	}
	for name := range ai.categories {
		if len(ai.catDocs[name]) == 0 {
			t.Fatalf("category %q exists with no members (should have been pruned)", name)
		}
	}
}

func TestCategorizeDocsBatching(t *testing.T) {
	// 600 docs > 2×batchSize(256) forces three flushes; three topics rotate so
	// each batch mixes categories and later batches must join categories
	// discovered by earlier ones.
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)
	emb := &stubEmbedder{vectors: map[string][]float32{
		"phone":  {1, 0, 0, 0},
		"banana": {0, 0, 1, 0},
		"car":    {0, 0, 0, 1},
	}}
	se.EnableAI(emb)

	topics := []string{"phone", "banana", "car"}
	const n = 600
	docs := make([]map[string]interface{}, 0, n)
	for i := 0; i < n; i++ {
		docs = append(docs, map[string]interface{}{
			"id":   fmt.Sprintf("d%d", i),
			"name": fmt.Sprintf("%s model%d", topics[i%3], i),
		})
	}

	ok, failed := se.CategorizeDocs(context.Background(), docs)
	if ok != n || failed != 0 {
		t.Fatalf("CategorizeDocs = (%d ok, %d failed), want (%d, 0)", ok, failed, n)
	}
	if got := emb.callCount(); got != n {
		t.Fatalf("embedder calls = %d, want %d", got, n)
	}
	if got := se.AI.AIDocCount(); got != n {
		t.Fatalf("AIDocCount = %d, want %d", got, n)
	}
	// Orthogonal topics with threshold 0.60 must yield exactly 3 categories.
	if got := se.AI.CategoryCount(); got != 3 {
		t.Fatalf("CategoryCount = %d, want 3", got)
	}
	// Every category must hold exactly one topic's 200 documents.
	for _, c := range se.AI.ListCategories() {
		if c.Count != n/3 {
			t.Fatalf("category %s Count = %d, want %d", c.Name, c.Count, n/3)
		}
	}
	// All docs of the same topic must share one category; the batch boundary
	// (doc 0 in batch 1, doc 597 in batch 3) must not split a topic.
	for _, pair := range [][2]string{{"d0", "d597"}, {"d1", "d598"}, {"d2", "d599"}} {
		a, _ := se.AI.GetAIDocument(pair[0])
		b, okB := se.AI.GetAIDocument(pair[1])
		if !okB || len(a.Categories) != 1 || len(b.Categories) != 1 || a.Categories[0] != b.Categories[0] {
			t.Fatalf("docs %s and %s should share one category: %v vs %v", pair[0], pair[1], a.Categories, b.Categories)
		}
	}
	checkAIConsistency(t, se.AI)
}

func TestCategorizeDocsSkipsAndFailures(t *testing.T) {
	se, emb := newAITestEngine(t)

	docs := []map[string]interface{}{
		{"id": "1", "name": "apple phone"},   // ok
		{"name": "no id at all"},             // skipped: missing id
		{"id": nil, "name": "nil id"},        // skipped: nil id
		{"id": "", "name": "empty id"},       // skipped: empty id
		{"id": "4"},                          // skipped: no content in index fields
		{"id": "5", "name": ""},              // skipped: empty content
		{"id": "6", "name": "mystery gizmo"}, // failed: embedder has no vector
		{"id": "7", "name": "fresh banana"},  // ok
	}

	ok, failed := se.CategorizeDocs(context.Background(), docs)
	if ok != 2 || failed != 1 {
		t.Fatalf("CategorizeDocs = (%d ok, %d failed), want (2, 1)", ok, failed)
	}
	// Skipped docs must not reach the embedder: 2 ok + 1 failed = 3 calls.
	if got := emb.callCount(); got != 3 {
		t.Fatalf("embedder calls = %d, want 3", got)
	}
	if got := se.AI.AIDocCount(); got != 2 {
		t.Fatalf("AIDocCount = %d, want 2", got)
	}
	if _, found := se.AI.GetAIDocument("6"); found {
		t.Fatal("failed doc 6 must not be stored")
	}
	checkAIConsistency(t, se.AI)
}

func TestCategorizeDocsZeroVectorFails(t *testing.T) {
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)
	se.EnableAI(&stubEmbedder{vectors: map[string][]float32{
		"phone": {1, 0, 0, 0},
		"void":  {0, 0, 0, 0}, // zero vector must be rejected by clusterDoc
	}})

	ok, failed := se.CategorizeDocs(context.Background(), []map[string]interface{}{
		{"id": "1", "name": "apple phone"},
		{"id": "2", "name": "void thing"},
	})
	if ok != 1 || failed != 1 {
		t.Fatalf("CategorizeDocs = (%d ok, %d failed), want (1, 1)", ok, failed)
	}
	if _, found := se.AI.GetAIDocument("2"); found {
		t.Fatal("zero-vector doc must not be stored")
	}
}

func TestCategorizeDocsDisabledAndNoEmbedder(t *testing.T) {
	docs := []map[string]interface{}{{"id": "1", "name": "apple phone"}}

	// AI disabled: silent no-op.
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)
	if ok, failed := se.CategorizeDocs(context.Background(), docs); ok != 0 || failed != 0 {
		t.Fatalf("disabled engine = (%d, %d), want (0, 0)", ok, failed)
	}

	// AI state present but no embedder (e.g. loaded snapshot without env
	// config): everything fails, nothing panics.
	se.AI = newAIIndex(nil)
	if ok, failed := se.CategorizeDocs(context.Background(), docs); ok != 0 || failed != len(docs) {
		t.Fatalf("no-embedder engine = (%d, %d), want (0, %d)", ok, failed, len(docs))
	}
}

func TestAssignRespectsThresholdAndMaxPerDoc(t *testing.T) {
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)
	emb := &stubEmbedder{vectors: map[string][]float32{
		"phone":  {1, 0, 0, 0},
		"tablet": {0, 1, 0, 0},
		"banana": {0, 0, 1, 0},
		// cos(hybrid, phone)=0.74, cos(hybrid, tablet)=0.64, cos(hybrid, banana)=0.20
		// → joins phone AND tablet (both ≥ 0.60), not banana; highest first.
		"hybrid": {0.75, 0.65, 0.2, 0},
	}}
	se.EnableAI(emb)

	seed := []map[string]interface{}{
		{"id": "p", "name": "apple phone"},
		{"id": "t", "name": "android tablet"},
		{"id": "b", "name": "fresh banana"},
	}
	if ok, failed := se.CategorizeDocs(context.Background(), seed); ok != 3 || failed != 0 {
		t.Fatalf("seed = (%d, %d), want (3, 0)", ok, failed)
	}
	if got := se.AI.CategoryCount(); got != 3 {
		t.Fatalf("CategoryCount = %d, want 3", got)
	}
	pDoc, _ := se.AI.GetAIDocument("p")
	tDoc, _ := se.AI.GetAIDocument("t")
	bDoc, _ := se.AI.GetAIDocument("b")

	if err := se.CategorizeDocument(context.Background(), "h", map[string]interface{}{"id": "h", "name": "hybrid device"}); err != nil {
		t.Fatal(err)
	}
	hDoc, _ := se.AI.GetAIDocument("h")
	want := []string{pDoc.Categories[0], tDoc.Categories[0]} // highest similarity first
	if fmt.Sprint(hDoc.Categories) != fmt.Sprint(want) {
		t.Fatalf("hybrid categories = %v, want %v", hDoc.Categories, want)
	}
	for _, c := range hDoc.Categories {
		if c == bDoc.Categories[0] {
			t.Fatalf("hybrid must not join the banana category (cos < threshold): %v", hDoc.Categories)
		}
	}
	// No new category was created for the hybrid doc.
	if got := se.AI.CategoryCount(); got != 3 {
		t.Fatalf("CategoryCount after hybrid = %d, want 3", got)
	}

	// With maxPerDoc=1 only the single best category may be joined.
	se.AI.maxPerDoc = 1
	if err := se.CategorizeDocument(context.Background(), "h2", map[string]interface{}{"id": "h2", "name": "hybrid gadget"}); err != nil {
		t.Fatal(err)
	}
	h2, _ := se.AI.GetAIDocument("h2")
	if len(h2.Categories) != 1 || h2.Categories[0] != pDoc.Categories[0] {
		t.Fatalf("maxPerDoc=1: categories = %v, want [%s]", h2.Categories, pDoc.Categories[0])
	}
	checkAIConsistency(t, se.AI)
}

func TestAssignMaxCategoriesCapNearestWins(t *testing.T) {
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)
	emb := &stubEmbedder{vectors: map[string][]float32{
		"phone":  {1, 0, 0, 0},
		"banana": {0, 1, 0, 0},
		// Below threshold vs both, but leaning toward phone: with the category
		// cap reached it must join phone (nearest) instead of seeding a new one.
		"drone": {0.5, 0.1, 0.85, 0},
	}}
	se.EnableAI(emb)
	se.AI.maxCategories = 2

	seed := []map[string]interface{}{
		{"id": "p", "name": "apple phone"},
		{"id": "b", "name": "fresh banana"},
	}
	if ok, _ := se.CategorizeDocs(context.Background(), seed); ok != 2 {
		t.Fatalf("seed failed")
	}
	if got := se.AI.CategoryCount(); got != 2 {
		t.Fatalf("CategoryCount = %d, want 2", got)
	}

	if err := se.CategorizeDocument(context.Background(), "d", map[string]interface{}{"id": "d", "name": "flying drone"}); err != nil {
		t.Fatal(err)
	}
	if got := se.AI.CategoryCount(); got != 2 {
		t.Fatalf("cap breached: CategoryCount = %d, want 2", got)
	}
	dDoc, _ := se.AI.GetAIDocument("d")
	pDoc, _ := se.AI.GetAIDocument("p")
	if len(dDoc.Categories) != 1 || dDoc.Categories[0] != pDoc.Categories[0] {
		t.Fatalf("drone categories = %v, want nearest (phone) %v", dDoc.Categories, pDoc.Categories)
	}
	checkAIConsistency(t, se.AI)
}

func TestCentroidSumAndExactRemoval(t *testing.T) {
	se, _ := newAITestEngine(t)

	// phone {1,0,0,0} and laptop {0.9,0.1,0,0} land in the same category.
	se.Index([]map[string]interface{}{
		{"id": "1", "name": "apple phone"},
		{"id": "2", "name": "dell laptop"},
	})
	d1, _ := se.AI.GetAIDocument("1")
	catName := d1.Categories[0]

	approx := func(got, want float32) bool { return math.Abs(float64(got-want)) < 1e-5 }
	assertCentroid := func(wantCentroid []float32, wantCount int) {
		t.Helper()
		se.AI.mu.RLock()
		defer se.AI.mu.RUnlock()
		c := se.AI.categories[catName]
		if c == nil {
			t.Fatalf("category %s missing", catName)
		}
		if c.Count != wantCount {
			t.Fatalf("Count = %d, want %d", c.Count, wantCount)
		}
		var normSq float32
		for i, w := range wantCentroid {
			if !approx(c.Centroid[i], w) {
				t.Fatalf("Centroid[%d] = %f, want %f", i, c.Centroid[i], w)
			}
			normSq += w * w
		}
		if !approx(c.Norm, float32(math.Sqrt(float64(normSq)))) {
			t.Fatalf("Norm = %f, want %f", c.Norm, math.Sqrt(float64(normSq)))
		}
	}

	// Centroid is the running SUM of member vectors.
	assertCentroid([]float32{1.9, 0.1, 0, 0}, 2)

	// Removal subtracts the member's exact vector.
	if !se.AI.RemoveDocument("2") {
		t.Fatal("RemoveDocument(2) = false")
	}
	assertCentroid([]float32{1, 0, 0, 0}, 1)
	checkAIConsistency(t, se.AI)
}

func TestAIVectorAndNormAssignedToDoc(t *testing.T) {
	se, emb := newAITestEngine(t)
	se.Index([]map[string]interface{}{{"id": "2", "name": "dell laptop"}})

	d, ok := se.AI.GetAIDocument("2")
	if !ok {
		t.Fatal("doc 2 missing")
	}
	want := emb.vectors["laptop"]
	if len(d.Vector) != len(want) {
		t.Fatalf("vector length = %d, want %d", len(d.Vector), len(want))
	}
	var normSq float64
	for i := range want {
		if d.Vector[i] != want[i] {
			t.Fatalf("Vector[%d] = %f, want %f", i, d.Vector[i], want[i])
		}
		normSq += float64(want[i]) * float64(want[i])
	}
	if math.Abs(float64(d.Norm)-math.Sqrt(normSq)) > 1e-6 {
		t.Fatalf("Norm = %f, want %f", d.Norm, math.Sqrt(normSq))
	}
	if d.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not set")
	}
}

func TestAIUpdatePreservesCreatedAt(t *testing.T) {
	se, _ := newAITestEngine(t)
	if err := se.AddOrUpdateDocument(map[string]interface{}{"id": "1", "name": "apple phone"}); err != nil {
		t.Fatal(err)
	}
	first, _ := se.AI.GetAIDocument("1")
	if err := se.AddOrUpdateDocument(map[string]interface{}{"id": "1", "name": "fresh banana"}); err != nil {
		t.Fatal(err)
	}
	second, _ := se.AI.GetAIDocument("1")
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("CreatedAt changed on update: %v -> %v", first.CreatedAt, second.CreatedAt)
	}
}

func TestEmbedContent(t *testing.T) {
	doc := map[string]interface{}{
		"name":  "apple phone",
		"brand": "apple",
		"price": 42,
		"empty": "",
		"nilf":  nil,
	}
	cases := []struct {
		fields []string
		want   string
	}{
		{[]string{"name"}, "apple phone"},
		{[]string{"name", "brand"}, "apple phone apple"},
		{[]string{"brand", "name"}, "apple apple phone"}, // fields join in declaration order
		{[]string{"name", "missing", "empty", "nilf", "price"}, "apple phone 42"},
		{[]string{"missing", "empty", "nilf"}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := embedContent(doc, c.fields); got != c.want {
			t.Errorf("embedContent(%v) = %q, want %q", c.fields, got, c.want)
		}
	}
}

func TestLoadCategoryEmbedMissingAndCorrupt(t *testing.T) {
	// Missing snapshot: (nil, nil) — AI simply stays disabled.
	ai, err := loadCategoryEmbed(t.TempDir())
	if err != nil || ai != nil {
		t.Fatalf("missing snapshot: got (%v, %v), want (nil, nil)", ai, err)
	}

	// Corrupt snapshot: an error, not a silent empty index.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, aiSnapshotFile), []byte("not a gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCategoryEmbed(dir); err == nil {
		t.Fatal("corrupt snapshot: expected error")
	}
}

func TestAIPersistRestoresCatDocs(t *testing.T) {
	dir := t.TempDir()
	se, _ := newAITestEngine(t)
	se.Index([]map[string]interface{}{
		{"id": "1", "name": "apple phone"},
		{"id": "2", "name": "dell laptop"},
		{"id": "3", "name": "fresh banana"},
	})
	if err := se.PersistCategoryEmbed(dir); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadCategoryEmbed(dir)
	if err != nil {
		t.Fatal(err)
	}
	checkAIConsistency(t, loaded)

	se.AI.mu.RLock()
	defer se.AI.mu.RUnlock()
	loaded.mu.RLock()
	defer loaded.mu.RUnlock()
	if loaded.nextCategoryID != se.AI.nextCategoryID {
		t.Fatalf("nextCategoryID = %d, want %d", loaded.nextCategoryID, se.AI.nextCategoryID)
	}
	for name, set := range se.AI.catDocs {
		lset, ok := loaded.catDocs[name]
		if !ok || len(lset) != len(set) {
			t.Fatalf("catDocs[%q] size = %d, want %d", name, len(lset), len(set))
		}
		for id := range set {
			if _, ok := lset[id]; !ok {
				t.Fatalf("catDocs[%q] missing doc %q after roundtrip", name, id)
			}
		}
	}
}

func TestAIDisabledIsNoop(t *testing.T) {
	dir := t.TempDir()
	se := NewSearchEngine([]string{"name"}, nil, nil, 10)

	se.Index([]map[string]interface{}{{"id": "1", "name": "apple phone"}})
	if se.AIEnabled() {
		t.Fatal("AI should be disabled by default")
	}
	if err := se.PersistCategoryEmbed(dir); err == nil {
		t.Fatal("PersistCategoryEmbed should error when AI is disabled")
	}
	if err := se.SaveAll(dir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}
	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if loaded.AIEnabled() {
		t.Fatal("loaded engine should not have AI state")
	}
}
