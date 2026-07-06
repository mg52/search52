package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mg52/search52/internal/vec"
)

// Embedder turns text into an embedding vector.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// aiScoreScale converts a cosine similarity in [-1,1] to the integer score the
// min-heap orders on. 1e6 keeps ~6 significant digits, far more than embedding
// similarities can meaningfully distinguish.
const aiScoreScale = 1e6

// AIDocument is a document's embedding plus the categories it clustered into.
// It is keyed by the EXTERNAL doc ID: updates of a document reuse the same
// external ID (while the engine's internal uint32 ID churns), and the embedding
// belongs to the document, not to one indexed version of it.
type AIDocument struct {
	ID         string    // external doc ID
	Categories []string  // category names this doc belongs to
	Vector     []float32 // embedding
	Norm       float32   // cached L2 norm of Vector
	CreatedAt  time.Time
}

// Category is an incrementally-discovered cluster. Centroid holds the running
// SUM of member document vectors; because cosine is scale-invariant the sum
// ranks identically to the mean, and member removal stays exact. Norm caches
// its L2 norm; Count is the number of member documents. Categories are
// auto-named ("category1", …).
type Category struct {
	Name      string
	Centroid  []float32
	Norm      float32
	Count     int
	CreatedAt time.Time
}

// AIIndex owns the embedding/categorization state of a SearchEngine. It has its
// own RWMutex so embedding (a network call) and clustering never contend with
// the inverted-index lock. A document is embedded once, then assigned to every
// existing category whose cosine similarity is >= threshold (highest first,
// capped at maxPerDoc), or seeds a new category when none are close enough
// (capped by maxCategories).
//
// LOCK ORDER INVARIANT: when both locks are needed, acquire se.mu BEFORE
// ai.mu (DeleteDocument and aiVectorSearch nest this way). Never acquire
// se.mu while holding ai.mu — that would deadlock against them. Writes that
// need engine state first (CategorizeDocument, clusterDoc) read it under
// se.mu, RELEASE it, then take ai.mu; the resulting staleness window is
// closed on the read side, where aiVectorSearch re-validates every candidate
// against ExternalToInternal/DocDeleted before returning it.
type AIIndex struct {
	embedder Embedder

	threshold     float32
	maxPerDoc     int
	maxCategories int

	mu             sync.RWMutex
	docs           map[string]AIDocument          // doc ID -> document
	categories     map[string]*Category           // name -> category
	catDocs        map[string]map[string]struct{} // category name -> set of doc IDs
	nextCategoryID int                            // monotonic; never reuses a pruned name
}

// newAIIndex constructs an empty AI index tuned by the package-level AI knobs.
func newAIIndex(embedder Embedder) *AIIndex {
	return &AIIndex{
		embedder:      embedder,
		threshold:     float32(AICategoryThreshold),
		maxPerDoc:     AIMaxCategoriesPerDoc,
		maxCategories: AIMaxCategories,
		docs:          make(map[string]AIDocument),
		categories:    make(map[string]*Category),
		catDocs:       make(map[string]map[string]struct{}),
	}
}

// EnableAI turns on embedding+categorization for this engine. Safe to call on a
// fresh engine before it is shared; existing AI state (e.g. restored by
// LoadAll) is preserved and only the embedder is attached.
func (se *SearchEngine) EnableAI(embedder Embedder) {
	if se.AI == nil {
		se.AI = newAIIndex(embedder)
		return
	}
	se.AI.embedder = embedder
}

// AIEnabled reports whether this engine categorizes documents.
func (se *SearchEngine) AIEnabled() bool { return se.AI != nil }

// AIDocCount returns the number of embedded documents.
func (ai *AIIndex) AIDocCount() int {
	ai.mu.RLock()
	defer ai.mu.RUnlock()
	return len(ai.docs)
}

// CategoryCount returns the number of discovered categories.
func (ai *AIIndex) CategoryCount() int {
	ai.mu.RLock()
	defer ai.mu.RUnlock()
	return len(ai.categories)
}

// ListCategories returns every category (Centroid omitted) in unspecified order.
func (ai *AIIndex) ListCategories() []Category {
	ai.mu.RLock()
	defer ai.mu.RUnlock()
	out := make([]Category, 0, len(ai.categories))
	for _, c := range ai.categories {
		out = append(out, Category{Name: c.Name, Norm: c.Norm, Count: c.Count, CreatedAt: c.CreatedAt})
	}
	return out
}

// GetAIDocument returns the embedded document with the given external ID.
func (ai *AIIndex) GetAIDocument(extID string) (AIDocument, bool) {
	ai.mu.RLock()
	defer ai.mu.RUnlock()
	d, ok := ai.docs[extID]
	return d, ok
}

// embedContent builds the text that represents a document for embedding: the
// values of the engine's index fields joined in declaration order.
func embedContent(doc map[string]interface{}, indexFields []string) string {
	var sb strings.Builder
	for _, f := range indexFields {
		v, ok := doc[f]
		if !ok || v == nil {
			continue
		}
		s := fmt.Sprintf("%v", v)
		if s == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(s)
	}
	return sb.String()
}

// CategorizeDocument embeds one document and clusters it under its external ID,
// replacing any existing embedding for that ID (stale category memberships are
// detached first). No-op when AI is disabled.
func (se *SearchEngine) CategorizeDocument(ctx context.Context, extID string, doc map[string]interface{}) error {
	ai := se.AI
	if ai == nil {
		return nil
	}
	if ai.embedder == nil {
		return fmt.Errorf("ai index has no embedder attached")
	}

	se.mu.RLock()
	indexFields := append([]string(nil), se.IndexFields...)
	se.mu.RUnlock()

	content := embedContent(doc, indexFields)
	if content == "" {
		return fmt.Errorf("document %q has no content in index fields", extID)
	}

	v, err := ai.embedder.Embed(ctx, content)
	if err != nil {
		return fmt.Errorf("embedding document %q: %w", extID, err)
	}
	return ai.clusterDoc(extID, v)
}

// clusterDoc folds an already-computed embedding into the category structure.
func (ai *AIIndex) clusterDoc(extID string, v []float32) error {
	norm := vec.Norm(v)
	if norm == 0 {
		return fmt.Errorf("document %q embedding is a zero vector", extID)
	}

	ai.mu.Lock()
	defer ai.mu.Unlock()

	doc := AIDocument{ID: extID, Vector: v, Norm: norm, CreatedAt: time.Now()}
	if old, ok := ai.docs[extID]; ok {
		doc.CreatedAt = old.CreatedAt // preserve original creation time on update
		ai.detachLocked(old)
	}

	doc.Categories = ai.assignLocked(v, norm)
	ai.docs[extID] = doc
	for _, name := range doc.Categories {
		ai.addToCentroidLocked(name, v)
		ai.addDocToCategoryLocked(extID, name)
	}
	return nil
}

// aiCategorizeBatch embeds a batch of documents in parallel (bounded by
// AIEmbedConcurrency), then clusters them sequentially in input order so the
// incremental category discovery stays deterministic for a given input order.
// Per-document failures are logged and skipped; the batch keeps going.
func (se *SearchEngine) aiCategorizeBatch(ctx context.Context, extIDs []string, contents []string) (ok, failed int) {
	ai := se.AI
	vecs := make([][]float32, len(extIDs))
	errs := make([]error, len(extIDs))

	workers := AIEmbedConcurrency
	if workers > len(extIDs) {
		workers = len(extIDs)
	}
	jobCh := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobCh {
				vecs[i], errs[i] = ai.embedder.Embed(ctx, contents[i])
			}
		}()
	}
	for i := range extIDs {
		jobCh <- i
	}
	close(jobCh)
	wg.Wait()

	for i, extID := range extIDs {
		err := errs[i]
		if err == nil {
			err = ai.clusterDoc(extID, vecs[i])
		}
		if err != nil {
			failed++
			slog.Error("ai categorize failed", "doc", extID, "error", err)
			continue
		}
		ok++
	}
	return ok, failed
}

// CategorizeDocs embeds and categorizes a slice of raw documents (bulk index
// path). Documents without a usable id or with empty index-field content are
// skipped. Returns how many documents were categorized and how many failed.
func (se *SearchEngine) CategorizeDocs(ctx context.Context, docs []map[string]interface{}) (ok, failed int) {
	ai := se.AI
	if ai == nil {
		return 0, 0
	}
	if ai.embedder == nil {
		slog.Error("ai categorize skipped: no embedder attached")
		return 0, len(docs)
	}

	se.mu.RLock()
	indexFields := append([]string(nil), se.IndexFields...)
	se.mu.RUnlock()

	const batchSize = 256
	extIDs := make([]string, 0, batchSize)
	contents := make([]string, 0, batchSize)
	flush := func() {
		if len(extIDs) == 0 {
			return
		}
		bOK, bFail := se.aiCategorizeBatch(ctx, extIDs, contents)
		ok += bOK
		failed += bFail
		extIDs = extIDs[:0]
		contents = contents[:0]
	}

	for i, doc := range docs {
		if i%10_000 == 0 || i == len(docs)-1 {
			slog.Info("CategorizeDocs", "doc", i, "categorized", ok, "failed", failed)
		}
		rawID, okID := doc["id"]
		if !okID || rawID == nil {
			continue
		}
		extID := fmt.Sprintf("%v", rawID)
		if extID == "" || extID == "<nil>" {
			continue
		}
		content := embedContent(doc, indexFields)
		if content == "" {
			continue
		}
		extIDs = append(extIDs, extID)
		contents = append(contents, content)
		if len(extIDs) == batchSize {
			flush()
		}
	}
	flush()
	return ok, failed
}

// aiVectorSearch embeds query, selects the AISearchTopNCategories nearest
// categories, and ranks their member documents by cosine similarity to the
// query.
//
// Locking: the embed call (network) happens with no lock held; the scan then
// holds se.mu.RLock with ai.mu.RLock nested inside. That nesting order matches
// DeleteDocument (se.mu → ai.mu), so it cannot deadlock.
func (se *SearchEngine) aiVectorSearch(ctx context.Context, query string, filters map[string][]interface{}) []ReturnedDocument {
	ai := se.AI
	if ai == nil || ai.embedder == nil {
		return nil
	}

	qv, err := ai.embedder.Embed(ctx, query)
	if err != nil {
		slog.Error("ai vector search: embed query failed", "error", err)
		return nil
	}
	qn := vec.Norm(qv)
	if qn == 0 {
		return nil
	}

	se.mu.RLock()
	defer se.mu.RUnlock()

	k := se.ResultSize
	if k <= 0 {
		return nil
	}

	var allowed []uint64
	if len(filters) > 0 {
		allowed = se.ApplyFilterLocked(filters)
		if allowed == nil {
			return nil
		}
	}

	ai.mu.RLock()
	defer ai.mu.RUnlock()

	topN := AISearchTopNCategories
	catNames := make([]string, 0, topN)
	ch := make([]internalHit, 0, topN)
	for name, c := range ai.categories {
		if c.Norm == 0 {
			continue
		}
		sim := vec.Cosine(qv, c.Centroid, qn, c.Norm)
		score := int(sim * aiScoreScale)
		if len(ch) < topN {
			catNames = append(catNames, name)
			ch = heapPushHit(ch, internalHit{id: uint32(len(catNames) - 1), score: score})
		} else if ch[0].score < score {
			slot := ch[0].id
			catNames[slot] = name
			heapReplaceTop(ch, internalHit{id: slot, score: score})
		}
	}
	if len(ch) == 0 {
		return nil
	}

	type docHit struct {
		extID      string
		internalID uint32
		sim        float32
	}
	docCands := make([]docHit, 0, k)
	dh := make([]internalHit, 0, k)
	seen := make(map[string]struct{})
	for _, hit := range ch {
		for extID := range ai.catDocs[catNames[hit.id]] {
			if _, dup := seen[extID]; dup {
				continue
			}
			seen[extID] = struct{}{}
			doc, ok := ai.docs[extID]
			if !ok || doc.Norm == 0 {
				continue
			}
			// Reject dead and filtered-out documents here, not after the heap:
			// they must not consume one of the k result slots.
			internalID, live := se.ExternalToInternal[extID]
			if !live || se.DocDeleted[internalID] {
				continue // deleted/updated since it was embedded
			}
			if allowed != nil && !filterBitTest(allowed, internalID) {
				continue
			}
			sim := vec.Cosine(qv, doc.Vector, qn, doc.Norm)
			if sim <= 0 {
				continue // irrelevant to the query
			}
			score := int(sim * aiScoreScale)
			if len(dh) < k {
				docCands = append(docCands, docHit{extID, internalID, sim})
				dh = heapPushHit(dh, internalHit{id: uint32(len(docCands) - 1), score: score})
			} else if dh[0].score < score {
				slot := dh[0].id
				docCands[slot] = docHit{extID, internalID, sim}
				heapReplaceTop(dh, internalHit{id: slot, score: score})
			}
		}
	}

	n := len(dh)
	if n == 0 {
		return nil
	}

	// Drain the min-heap back-to-front so out is ordered highest score first,
	// matching the classic search loops.
	out := make([]ReturnedDocument, n)
	for i := n - 1; i >= 0; i-- {
		hit := dh[0]
		if i > 0 {
			dh[0] = dh[i]
			siftDownHit(dh, 0, i)
		}
		cand := docCands[hit.id]
		out[i] = ReturnedDocument{
			ID:    cand.extID,
			Data:  se.Documents[cand.internalID],
			Score: int(cand.sim * aiScoreScale),
			AI:    true,
		}
	}
	return out
}

// RemoveDocument deletes a document's embedding and detaches it from every
// category (pruning any left empty).
func (ai *AIIndex) RemoveDocument(extID string) bool {
	ai.mu.Lock()
	defer ai.mu.Unlock()
	doc, ok := ai.docs[extID]
	if !ok {
		return false
	}
	ai.detachLocked(doc)
	delete(ai.docs, extID)
	return true
}

// -------------------- clustering (caller holds ai.mu write lock) --------------------

// assignLocked picks the categories for a vector: every existing category whose
// cosine similarity is >= threshold (up to maxPerDoc, highest first); otherwise
// a new category, or — when the maxCategories cap is hit — the single nearest.
func (ai *AIIndex) assignLocked(v []float32, norm float32) []string {
	type catSim struct {
		name string
		sim  float32
	}
	sims := make([]catSim, 0, len(ai.categories))
	for name, c := range ai.categories {
		if c.Norm == 0 {
			continue
		}
		sims = append(sims, catSim{name, vec.Cosine(v, c.Centroid, norm, c.Norm)})
	}
	sort.Slice(sims, func(i, j int) bool { return sims[i].sim > sims[j].sim })

	var assigned []string
	for _, sc := range sims {
		if sc.sim < ai.threshold || len(assigned) >= ai.maxPerDoc {
			break
		}
		assigned = append(assigned, sc.name)
	}
	if len(assigned) == 0 {
		switch {
		case len(ai.categories) < ai.maxCategories:
			assigned = append(assigned, ai.newCategoryLocked())
		case len(sims) > 0:
			assigned = append(assigned, sims[0].name) // cap reached: nearest wins
		}
	}
	return assigned
}

func (ai *AIIndex) newCategoryLocked() string {
	ai.nextCategoryID++
	name := fmt.Sprintf("category%d", ai.nextCategoryID)
	ai.categories[name] = &Category{Name: name, CreatedAt: time.Now()}
	return name
}

// addToCentroidLocked folds v into a category's running centroid sum.
func (ai *AIIndex) addToCentroidLocked(name string, v []float32) {
	c, ok := ai.categories[name]
	if !ok {
		return
	}
	if c.Centroid == nil {
		c.Centroid = make([]float32, len(v))
	}
	for i := range v {
		c.Centroid[i] += v[i]
	}
	c.Count++
	c.Norm = vec.Norm(c.Centroid)
}

// removeFromCentroidLocked subtracts v from a category's centroid sum, pruning
// the category when its last member leaves.
func (ai *AIIndex) removeFromCentroidLocked(name string, v []float32) {
	c, ok := ai.categories[name]
	if !ok {
		return
	}
	c.Count--
	if c.Count <= 0 {
		delete(ai.categories, name)
		delete(ai.catDocs, name)
		return
	}
	for i := range v {
		if i < len(c.Centroid) {
			c.Centroid[i] -= v[i]
		}
	}
	c.Norm = vec.Norm(c.Centroid)
}

func (ai *AIIndex) addDocToCategoryLocked(id, name string) {
	set := ai.catDocs[name]
	if set == nil {
		set = make(map[string]struct{})
		ai.catDocs[name] = set
	}
	set[id] = struct{}{}
}

// detachLocked removes a document's contribution to every category it belonged to.
func (ai *AIIndex) detachLocked(doc AIDocument) {
	for _, name := range doc.Categories {
		ai.removeFromCentroidLocked(name, doc.Vector)
		if set := ai.catDocs[name]; set != nil {
			delete(set, doc.ID)
		}
	}
}
