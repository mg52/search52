package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newMultipartUploadRequest builds a multipart POST with one "file" part.
func newMultipartUploadRequest(t *testing.T, url, filename, content string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, url, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// newEmbeddingTestServer serves an OpenAI-compatible /embeddings endpoint that
// returns a deterministic vector per keyword, mirroring how the engine tests
// stub the embedder. Unknown inputs get a 500 so failure paths are exercisable.
func newEmbeddingTestServer(t *testing.T, vectors map[string][]float32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Input string `json:"input"`
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for kw, v := range vectors {
			if strings.Contains(strings.ToLower(req.Input), kw) {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"data": []map[string]interface{}{{"embedding": v}},
				})
				return
			}
		}
		http.Error(w, fmt.Sprintf("no vector for %q", req.Input), http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setEmbeddingEnv(t *testing.T, baseURL string) {
	t.Helper()
	t.Setenv("SEARCH52_EMBEDDING_BASE_URL", baseURL)
	t.Setenv("SEARCH52_EMBEDDING_MODEL", "test-model")
	t.Setenv("SEARCH52_EMBEDDING_API_KEY", "")
}

func createAIIndex(t *testing.T, ht *HTTP, name string) {
	t.Helper()
	body := fmt.Sprintf(`{"indexName":%q,"indexFields":["name"],"resultCount":5,"aiEnabled":true}`, name)
	req := httptest.NewRequest(http.MethodPost, "/create-index", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ht.CreateIndex(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create-index status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestCreateIndex_AIEnabled_MissingEnv(t *testing.T) {
	// Explicitly blank the embedding env so the test is hermetic.
	t.Setenv("SEARCH52_EMBEDDING_BASE_URL", "")
	t.Setenv("SEARCH52_EMBEDDING_MODEL", "")

	ht := NewHTTP()
	body := `{"indexName":"aidx","indexFields":["name"],"resultCount":5,"aiEnabled":true}`
	req := httptest.NewRequest(http.MethodPost, "/create-index", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ht.CreateIndex(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when embedding env is missing", rr.Code)
	}
	// A failed AI create must not leave a half-registered index behind.
	ht.mu.RLock()
	_, exists := ht.engines["aidx"]
	ht.mu.RUnlock()
	if exists {
		t.Fatal("index must not be registered when embedder construction fails")
	}
}

func TestCreateIndex_AIEnabled_Success(t *testing.T) {
	srv := newEmbeddingTestServer(t, map[string][]float32{"phone": {1, 0, 0, 0}})
	setEmbeddingEnv(t, srv.URL)

	ht := NewHTTP()
	createAIIndex(t, ht, "aidx")

	ht.mu.RLock()
	sec := ht.engines["aidx"]
	ht.mu.RUnlock()
	if sec == nil || !sec.AIEnabled() {
		t.Fatal("created index must have AI enabled")
	}

	// aiEnabled defaults to false: a plain index must NOT get an AI state.
	body := `{"indexName":"plain","indexFields":["name"],"resultCount":5}`
	req := httptest.NewRequest(http.MethodPost, "/create-index", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ht.CreateIndex(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("plain create-index status = %d", rr.Code)
	}
	ht.mu.RLock()
	plain := ht.engines["plain"]
	ht.mu.RUnlock()
	if plain.AIEnabled() {
		t.Fatal("plain index must not have AI enabled")
	}
}

func TestPersistCategoryEmbedHandler_Errors(t *testing.T) {
	ht := NewHTTP()

	// Wrong method.
	req := httptest.NewRequest(http.MethodGet, "/persist-category-embed", nil)
	rr := httptest.NewRecorder()
	ht.PersistCategoryEmbed(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status = %d, want 405", rr.Code)
	}

	// Missing indexName.
	req = httptest.NewRequest(http.MethodPost, "/persist-category-embed", strings.NewReader(`{}`))
	rr = httptest.NewRecorder()
	ht.PersistCategoryEmbed(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing indexName: status = %d, want 400", rr.Code)
	}

	// Unknown index.
	req = httptest.NewRequest(http.MethodPost, "/persist-category-embed", strings.NewReader(`{"indexName":"nope"}`))
	rr = httptest.NewRecorder()
	ht.PersistCategoryEmbed(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown index: status = %d, want 404", rr.Code)
	}

	// Index without AI.
	body := `{"indexName":"plain","indexFields":["name"],"resultCount":5}`
	req = httptest.NewRequest(http.MethodPost, "/create-index", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	ht.CreateIndex(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create plain index: %d", rr.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/persist-category-embed", strings.NewReader(`{"indexName":"plain"}`))
	rr = httptest.NewRecorder()
	ht.PersistCategoryEmbed(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("AI-disabled index: status = %d, want 400", rr.Code)
	}
}

// TestAIEndToEnd drives the full HTTP flow: create an AI index against a fake
// embedding server, upsert documents, persist only the AI state, save the full
// engine, then load it back and verify embeddings + categories survived.
func TestAIEndToEnd_DocumentPersistLoad(t *testing.T) {
	srv := newEmbeddingTestServer(t, map[string][]float32{
		"phone":  {1, 0, 0, 0},
		"laptop": {0.9, 0.1, 0, 0},
		"banana": {0, 0, 1, 0},
	})
	setEmbeddingEnv(t, srv.URL)
	tmp := t.TempDir()
	t.Setenv("SEARCH52_INDEX_DATA_DIR", tmp)

	ht := NewHTTP()
	const idx = "aidx"
	createAIIndex(t, ht, idx)

	// Upsert three docs through the /document handler.
	for _, doc := range []string{
		`{"document":{"id":"1","name":"apple phone"}}`,
		`{"document":{"id":"2","name":"dell laptop"}}`,
		`{"document":{"id":"3","name":"fresh banana"}}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/document?indexName="+idx, strings.NewReader(doc))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		ht.AddOrUpdateDocument(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("document upsert status = %d, body = %s", rr.Code, rr.Body.String())
		}
	}

	ht.mu.RLock()
	sec := ht.engines[idx]
	ht.mu.RUnlock()
	if got := sec.AI.AIDocCount(); got != 3 {
		t.Fatalf("AIDocCount = %d, want 3", got)
	}
	if got := sec.AI.CategoryCount(); got != 2 {
		t.Fatalf("CategoryCount = %d, want 2", got)
	}

	// Persist only the AI state: category_embed.gob appears, engine.gob does not.
	req := httptest.NewRequest(http.MethodPost, "/persist-category-embed", strings.NewReader(fmt.Sprintf(`{"indexName":%q}`, idx)))
	rr := httptest.NewRecorder()
	ht.PersistCategoryEmbed(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("persist-category-embed status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(tmp, idx, "category_embed.gob")); err != nil {
		t.Fatalf("category_embed.gob missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, idx, "engine.gob")); !os.IsNotExist(err) {
		t.Fatalf("engine.gob must not be written by persist-category-embed (err=%v)", err)
	}

	// Full save writes the document snapshot too.
	req = httptest.NewRequest(http.MethodPost, "/save-controller", strings.NewReader(fmt.Sprintf(`{"indexName":%q}`, idx)))
	rr = httptest.NewRecorder()
	ht.SaveEngine(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("save-controller status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(tmp, idx, "engine.gob")); err != nil {
		t.Fatalf("engine.gob missing after save-controller: %v", err)
	}

	// Load into a fresh handler: AI state must be restored and the embedder
	// re-attached from env, so new upserts keep categorizing.
	ht2 := NewHTTP()
	req = httptest.NewRequest(http.MethodPost, "/load-controller", strings.NewReader(fmt.Sprintf(`{"indexName":%q}`, idx)))
	rr = httptest.NewRecorder()
	ht2.LoadEngine(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("load-controller status = %d, body = %s", rr.Code, rr.Body.String())
	}
	ht2.mu.RLock()
	loaded := ht2.engines[idx]
	ht2.mu.RUnlock()
	if !loaded.AIEnabled() {
		t.Fatal("loaded engine must have AI state")
	}
	if got := loaded.AI.AIDocCount(); got != 3 {
		t.Fatalf("loaded AIDocCount = %d, want 3", got)
	}
	if got := loaded.AI.CategoryCount(); got != 2 {
		t.Fatalf("loaded CategoryCount = %d, want 2", got)
	}
	d1, ok := loaded.AI.GetAIDocument("1")
	if !ok || len(d1.Vector) != 4 || d1.Norm == 0 || len(d1.Categories) != 1 {
		t.Fatalf("doc 1 not restored correctly: %+v", d1)
	}

	// The re-attached embedder works: a new doc lands in the phone category.
	req = httptest.NewRequest(http.MethodPost, "/document?indexName="+idx, strings.NewReader(`{"document":{"id":"4","name":"pixel phone"}}`))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	ht2.AddOrUpdateDocument(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("post-load upsert status = %d", rr.Code)
	}
	d4, ok := loaded.AI.GetAIDocument("4")
	if !ok {
		t.Fatal("doc 4 not categorized after load")
	}
	if d4.Categories[0] != d1.Categories[0] {
		t.Fatalf("doc 4 category = %v, want same as doc 1 %v", d4.Categories, d1.Categories)
	}

	// Delete through the handler removes the doc from the AI index too.
	req = httptest.NewRequest(http.MethodDelete, "/document?indexName="+idx+"&id=4", nil)
	rr = httptest.NewRecorder()
	ht2.DeleteDocument(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rr.Code)
	}
	if _, ok := loaded.AI.GetAIDocument("4"); ok {
		t.Fatal("doc 4 still in AI index after delete")
	}
}

// TestSearchHandler_MergesAIHits drives a real HTTP /search request against an
// AI-enabled index and confirms the JSON response carries the AI-sourced hit
// (with "AI":true) alongside the classic hit, proving the merge in
// engine.Search reaches all the way through the HTTP layer.
func TestSearchHandler_MergesAIHits(t *testing.T) {
	srv := newEmbeddingTestServer(t, map[string][]float32{
		"car":     {1, 0, 0, 0},
		"bicycle": {0, 1, 0, 0},
		"vehicle": {1, 0, 0, 0}, // query-only synonym for car
	})
	setEmbeddingEnv(t, srv.URL)

	ht := NewHTTP()
	const idx = "aidx"
	createAIIndex(t, ht, idx)

	for _, doc := range []string{
		`{"document":{"id":"1","name":"red car"}}`,
		`{"document":{"id":"2","name":"blue bicycle"}}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/document?indexName="+idx, strings.NewReader(doc))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		ht.AddOrUpdateDocument(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("document upsert status = %d, body = %s", rr.Code, rr.Body.String())
		}
	}

	// "fast vehicle": neither token is indexed, so classic search alone would
	// return nothing; "vehicle" embeds to the car vector, so AI must surface
	// doc 1 on its own.
	req := httptest.NewRequest(http.MethodGet, "/search?index="+idx+"&q=fast+vehicle", nil)
	rr := httptest.NewRecorder()
	ht.Search(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("search status = %d, body = %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	var resp SearchResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Response == nil || len(resp.Response.Docs) != 1 {
		t.Fatalf("Docs = %+v, want exactly 1 AI hit", resp.Response)
	}
	if got := resp.Response.Docs[0]; got.ID != "1" || !got.AI {
		t.Fatalf("got %+v, want AI hit for doc 1", got)
	}

	// Confirm the raw JSON actually carries the literal "AI":true key (not
	// silently dropped or renamed by an unexpected json tag).
	if !strings.Contains(body, `"AI":true`) {
		t.Fatalf("response JSON missing \"AI\":true: %s", body)
	}
}

// TestAddToIndex_AICategorizes verifies the bulk upload path (multipart file →
// Index → CategorizeDocs) also embeds and categorizes.
func TestAddToIndex_AICategorizes(t *testing.T) {
	srv := newEmbeddingTestServer(t, map[string][]float32{
		"phone":  {1, 0, 0, 0},
		"banana": {0, 0, 1, 0},
	})
	setEmbeddingEnv(t, srv.URL)

	ht := NewHTTP()
	const idx = "aidx"
	createAIIndex(t, ht, idx)

	docs := `[{"id":"1","name":"apple phone"},{"id":"2","name":"fresh banana"}]`
	req := newMultipartUploadRequest(t, "/add-to-index?indexName="+idx, "docs.json", docs)
	rr := httptest.NewRecorder()
	ht.AddToIndex(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("add-to-index status = %d, body = %s", rr.Code, rr.Body.String())
	}

	ht.mu.RLock()
	sec := ht.engines[idx]
	ht.mu.RUnlock()
	if got := sec.AI.AIDocCount(); got != 2 {
		t.Fatalf("AIDocCount = %d, want 2", got)
	}
	if got := sec.AI.CategoryCount(); got != 2 {
		t.Fatalf("CategoryCount = %d, want 2", got)
	}
}
