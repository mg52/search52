package engine

import (
	"bufio"
	"compress/gzip"
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// aiSnapshotFile is the per-index file holding embeddings + categories. It is
// written next to engine.gob and is intentionally separate so the (large,
// vector-heavy) AI state can be persisted/refreshed without rewriting the
// document snapshot.
const aiSnapshotFile = "category_embed.gob"

// aiPayload is the gob-serializable form of an AIIndex. Norms are persisted
// alongside vectors/centroids (not recomputed on load).
type aiPayload struct {
	Threshold      float64
	MaxPerDoc      int
	MaxCategories  int
	NextCategoryID int
	Documents      []aiDocSnapshot
	Categories     []aiCatSnapshot
	CatDocs        map[string][]string
}

type aiDocSnapshot struct {
	ID         string
	Categories []string
	Vector     []float32
	Norm       float32
	CreatedAt  time.Time
}

type aiCatSnapshot struct {
	Name      string
	Centroid  []float32
	Norm      float32
	Count     int
	CreatedAt time.Time
}

func init() {
	gob.Register(aiPayload{})
}

// PersistCategoryEmbed writes ONLY the AI state — per-document embeddings and
// the discovered categories — to path + "/category_embed.gob" via a temp file +
// atomic rename. The RLock is held only while copying state into the snapshot,
// not during encoding or disk I/O. Returns an error if AI is not enabled.
func (se *SearchEngine) PersistCategoryEmbed(path string) error {
	ai := se.AI
	if ai == nil {
		return fmt.Errorf("ai is not enabled on this engine")
	}

	ai.mu.RLock()
	payload := aiPayload{
		Threshold:      float64(ai.threshold),
		MaxPerDoc:      ai.maxPerDoc,
		MaxCategories:  ai.maxCategories,
		NextCategoryID: ai.nextCategoryID,
		Documents:      make([]aiDocSnapshot, 0, len(ai.docs)),
		Categories:     make([]aiCatSnapshot, 0, len(ai.categories)),
		CatDocs:        make(map[string][]string, len(ai.catDocs)),
	}
	for _, d := range ai.docs {
		payload.Documents = append(payload.Documents, aiDocSnapshot{
			ID:         d.ID,
			Categories: d.Categories,
			Vector:     d.Vector,
			Norm:       d.Norm,
			CreatedAt:  d.CreatedAt,
		})
	}
	for _, c := range ai.categories {
		payload.Categories = append(payload.Categories, aiCatSnapshot{
			Name:      c.Name,
			Centroid:  c.Centroid,
			Norm:      c.Norm,
			Count:     c.Count,
			CreatedAt: c.CreatedAt,
		})
	}
	for name, set := range ai.catDocs {
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		payload.CatDocs[name] = ids
	}
	ai.mu.RUnlock()

	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", path, err)
	}
	file := filepath.Join(path, aiSnapshotFile)
	tmp := file + ".tmp"

	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	bw := bufio.NewWriterSize(f, 4<<20)
	gz := gzip.NewWriter(bw)
	encErr := gob.NewEncoder(gz).Encode(payload)
	gzErr := gz.Close()
	flushErr := bw.Flush()
	closeErr := f.Close()
	for _, e := range []error{encErr, gzErr, flushErr, closeErr} {
		if e != nil {
			os.Remove(tmp)
			return fmt.Errorf("encode ai payload: %w", e)
		}
	}
	if err := os.Rename(tmp, file); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s to %s: %w", tmp, file, err)
	}
	return nil
}

// loadCategoryEmbed restores an AIIndex from path + "/category_embed.gob".
// Returns (nil, nil) when the snapshot does not exist — AI simply stays
// disabled for that engine. The embedder is NOT persisted; attach one with
// EnableAI after loading.
func loadCategoryEmbed(path string) (*AIIndex, error) {
	file := filepath.Join(path, aiSnapshotFile)
	f, err := os.Open(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", file, err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(bufio.NewReaderSize(f, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("gzip reader %s: %w", file, err)
	}
	defer gz.Close()

	var payload aiPayload
	if err := gob.NewDecoder(gz).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode %s: %w", file, err)
	}

	ai := &AIIndex{
		threshold:      float32(payload.Threshold),
		maxPerDoc:      payload.MaxPerDoc,
		maxCategories:  payload.MaxCategories,
		nextCategoryID: payload.NextCategoryID,
		docs:           make(map[string]AIDocument, len(payload.Documents)),
		categories:     make(map[string]*Category, len(payload.Categories)),
		catDocs:        make(map[string]map[string]struct{}, len(payload.CatDocs)),
	}
	if ai.maxPerDoc <= 0 {
		ai.maxPerDoc = AIMaxCategoriesPerDoc
	}
	if ai.maxCategories <= 0 {
		ai.maxCategories = AIMaxCategories
	}
	for _, d := range payload.Documents {
		ai.docs[d.ID] = AIDocument{
			ID:         d.ID,
			Categories: d.Categories,
			Vector:     d.Vector,
			Norm:       d.Norm,
			CreatedAt:  d.CreatedAt,
		}
	}
	for _, c := range payload.Categories {
		ai.categories[c.Name] = &Category{
			Name:      c.Name,
			Centroid:  c.Centroid,
			Norm:      c.Norm,
			Count:     c.Count,
			CreatedAt: c.CreatedAt,
		}
	}
	for name, ids := range payload.CatDocs {
		set := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			set[id] = struct{}{}
		}
		ai.catDocs[name] = set
	}
	return ai, nil
}
