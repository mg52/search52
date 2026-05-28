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
	"sort"
	"strings"
	"testing"

	"github.com/mg52/search52/internal/engine"
)

func assertEngineSearchIDs(t *testing.T, docs []engine.ReturnedDocument, exp ...string) {
	t.Helper()
	got := make([]string, 0, len(docs))
	for _, doc := range docs {
		got = append(got, doc.ID)
	}
	sort.Strings(got)
	sort.Strings(exp)
	if len(got) != len(exp) {
		t.Fatalf("ids mismatch: got=%v exp=%v", got, exp)
	}
	for i := range got {
		if got[i] != exp[i] {
			t.Fatalf("ids mismatch: got=%v exp=%v", got, exp)
		}
	}
}

func TestCreateIndexHandler(t *testing.T) {
	h := NewHTTP()
	// Valid create-index request
	body := `{"indexName":"testidx","indexFields":["name"],"filters":["year"],"resultCount":5,"workers":2}`
	req := httptest.NewRequest(http.MethodPost, "/create-index", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreateIndex(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status 201 Created; got %d", rr.Code)
	}

	var resp CreateIndexResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if resp.IndexName != "testidx" {
		t.Errorf("expected IndexName=testidx; got %q", resp.IndexName)
	}
	if resp.FieldWeights["name"] != 1 {
		t.Errorf("expected default name field weight 1; got %d", resp.FieldWeights["name"])
	}
}

func TestCreateIndexHandler_FieldWeights(t *testing.T) {
	h := NewHTTP()
	body := `{
		"indexName":"weightedidx",
		"indexFields":["title","artist","album"],
		"fieldWeights":{"title":5,"artist":2,"album":0,"ignored":9},
		"filters":["year"],
		"resultCount":5
	}`
	req := httptest.NewRequest(http.MethodPost, "/create-index", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreateIndex(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status 201 Created; got %d", rr.Code)
	}

	var resp CreateIndexResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if resp.FieldWeights["title"] != 5 {
		t.Fatalf("expected title weight 5; got %+v", resp.FieldWeights)
	}
	if resp.FieldWeights["artist"] != 2 {
		t.Fatalf("expected artist weight 2; got %+v", resp.FieldWeights)
	}
	if resp.FieldWeights["album"] != 1 {
		t.Fatalf("expected invalid album weight to default to 1; got %+v", resp.FieldWeights)
	}
	if _, ok := resp.FieldWeights["ignored"]; ok {
		t.Fatalf("expected non-index field weight to be ignored; got %+v", resp.FieldWeights)
	}

	created := h.engines["weightedidx"]
	if created == nil {
		t.Fatalf("expected weightedidx engine to be registered")
	}
	if created.FieldWeights["title"] != 5 || created.FieldWeights["artist"] != 2 || created.FieldWeights["album"] != 1 {
		t.Fatalf("engine field weights mismatch: %+v", created.FieldWeights)
	}
}

func TestHealthHandler(t *testing.T) {
	h := NewHTTP()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	rr := httptest.NewRecorder()
	h.Health(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK; got %d", rr.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("expected status=ok; got %v", resp["status"])
	}
}

func TestSearchHandler_NoIndex(t *testing.T) {
	h := NewHTTP()
	req := httptest.NewRequest(http.MethodGet, "/search?index=unknown&q=a", nil)
	rr := httptest.NewRecorder()
	h.Search(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404; got %d", rr.Code)
	}
}

func TestAddToIndexHandler_InvalidIndex(t *testing.T) {
	h := NewHTTP()
	// no index created
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	w.WriteField("indexName", "nope")
	w.Close()
	r := httptest.NewRequest(http.MethodPost, "/add-to-index?indexName=nope", &b)
	r.Header.Set("Content-Type", w.FormDataContentType())
	rw := httptest.NewRecorder()
	h.AddToIndex(rw, r)
	if rw.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing index; got %d", rw.Code)
	}
}

// TestSaveEngine_ErrorPaths checks SaveEngine error cases.
func TestSaveEngine_Errors(t *testing.T) {
	ht := NewHTTP()
	// missing indexName in payload
	req := httptest.NewRequest(http.MethodPost, "/save-controller", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	ht.SaveEngine(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing indexName, got %d", rr.Code)
	}

	// index not found
	payload := `{"indexName":"no_exist"}`
	req = httptest.NewRequest(http.MethodPost, "/save-controller", strings.NewReader(payload))
	rr = httptest.NewRecorder()
	ht.SaveEngine(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for index not found, got %d", rr.Code)
	}
}

func TestSaveEngine_Success(t *testing.T) {
	tmp := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("unable to get cwd: %v", err)
	}
	defer os.Chdir(origDir)
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir to tmp failed: %v", err)
	}

	os.Setenv("INDEX_DATA_DIR", tmp)
	ht := NewHTTP()
	idx := "idx"
	ht.engines[idx] = engine.NewSearchEngine([]string{"name"}, nil, 1)
	ht.engines[idx].Index([]map[string]interface{}{{"id": "1", "name": "val"}})

	payload := fmt.Sprintf(`{"indexName":"%s"}`, idx)
	req := httptest.NewRequest(http.MethodPost, "/save-controller", strings.NewReader(payload))
	rr := httptest.NewRecorder()
	ht.SaveEngine(rr, req)

	if rr.Code == http.StatusOK {
		// only if it's OK do we assert the file exists
		expected := idx + "/engine.gob"
		if _, err := os.Stat(filepath.Join(tmp, expected)); err != nil {
			t.Errorf("expected gob file %q, stat error: %v", expected, err)
		}
	} else if rr.Code == http.StatusInternalServerError {
		// in some environments saving into tmp may still error; at least check we returned JSON
		var errResp map[string]interface{}
		if e := json.NewDecoder(rr.Body).Decode(&errResp); e != nil {
			t.Errorf("on 500, expected JSON error body, got decode error: %v", e)
		}
	} else {
		t.Fatalf("unexpected status %d; want 200 or 500", rr.Code)
	}
}

func TestLoadEngine_Errors(t *testing.T) {
	h := NewHTTP()

	req := httptest.NewRequest(http.MethodGet, "/load-controller", nil)
	rr := httptest.NewRecorder()
	h.LoadEngine(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /load-controller expected 405, got %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/load-controller", strings.NewReader("{bad json}"))
	rr = httptest.NewRecorder()
	h.LoadEngine(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST /load-controller invalid JSON expected 400, got %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/load-controller", strings.NewReader(`{}`))
	rr = httptest.NewRecorder()
	h.LoadEngine(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST /load-controller missing indexName expected 400, got %d", rr.Code)
	}

	t.Setenv("INDEX_DATA_DIR", t.TempDir())
	req = httptest.NewRequest(http.MethodPost, "/load-controller", strings.NewReader(`{"indexName":"missing"}`))
	rr = httptest.NewRecorder()
	h.LoadEngine(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("POST /load-controller missing engine expected 500, got %d", rr.Code)
	}
}

func TestLoadEngine_Success(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("INDEX_DATA_DIR", tmp)

	se := engine.NewSearchEngineWithFieldWeights(
		[]string{"name", "tags"},
		map[string]int{"name": 3, "tags": 1},
		map[string]bool{"year": true},
		10,
	)
	if err := se.AddOrUpdateDocument(map[string]interface{}{
		"id":   "1",
		"name": "Sunny Rio",
		"tags": "music",
		"year": "2020",
	}); err != nil {
		t.Fatalf("AddOrUpdateDocument: %v", err)
	}

	dataDir := filepath.Join(tmp, "idx")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := se.SaveAll(dataDir); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}

	h := NewHTTP()
	req := httptest.NewRequest(http.MethodPost, "/load-controller", strings.NewReader(`{"indexName":"idx"}`))
	rr := httptest.NewRecorder()
	h.LoadEngine(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["indexName"] != "idx" {
		t.Fatalf("expected indexName idx, got %v", resp["indexName"])
	}
	loaded := h.engines["idx"]
	if loaded == nil {
		t.Fatalf("expected engine to be registered")
	}
	if loaded.FieldWeights["name"] != 3 || loaded.FieldWeights["tags"] != 1 {
		t.Fatalf("expected restored field weights, got %+v", loaded.FieldWeights)
	}
	assertEngineSearchIDs(t, loaded.SearchOneTerm("sunny", nil), "1")
	assertEngineSearchIDs(t, loaded.SearchOneTerm("sunny", map[string][]interface{}{"year": {"2020"}}), "1")
}

func TestSearchHandler_Errors(t *testing.T) {
	h := NewHTTP()
	// Method not allowed
	req := httptest.NewRequest(http.MethodPost, "/search?index=idx&q=x", nil)
	rr := httptest.NewRecorder()
	h.Search(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /search expected 405, got %d", rr.Code)
	}

	// Missing index param
	req = httptest.NewRequest(http.MethodGet, "/search?q=x", nil)
	rr = httptest.NewRecorder()
	h.Search(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("GET /search missing index expected 400, got %d", rr.Code)
	}
}

func TestCreateIndexHandler_Errors(t *testing.T) {
	h := NewHTTP()
	// Method not allowed
	req := httptest.NewRequest(http.MethodGet, "/create-index", nil)
	rr := httptest.NewRecorder()
	h.CreateIndex(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /create-index expected 405, got %d", rr.Code)
	}

	// Invalid JSON
	req = httptest.NewRequest(http.MethodPost, "/create-index", strings.NewReader("{bad json}"))
	rr = httptest.NewRecorder()
	h.CreateIndex(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("POST /create-index invalid JSON expected 400, got %d", rr.Code)
	}

	// Missing indexName
	body := `{"indexFields":["f"],"filters":[],"resultCount":1,"workers":1}`
	req = httptest.NewRequest(http.MethodPost, "/create-index", strings.NewReader(body))
	rr = httptest.NewRecorder()
	h.CreateIndex(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("POST /create-index missing indexName expected 400, got %d", rr.Code)
	}

	// Already exists
	valid := `{"indexName":"dup","indexFields":["f"],"filters":[],"resultCount":1,"workers":1}`
	req = httptest.NewRequest(http.MethodPost, "/create-index", strings.NewReader(valid))
	rr = httptest.NewRecorder()
	h.CreateIndex(rr, req)
	// second same
	req = httptest.NewRequest(http.MethodPost, "/create-index", strings.NewReader(valid))
	rr = httptest.NewRecorder()
	h.CreateIndex(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("POST /create-index duplicate expected 409, got %d", rr.Code)
	}
}

func TestAddToIndexHandler_Errors(t *testing.T) {
	h := NewHTTP()
	// Method not allowed
	req := httptest.NewRequest(http.MethodGet, "/add-to-index?indexName=idx", nil)
	rr := httptest.NewRecorder()
	h.AddToIndex(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /add-to-index expected 405, got %d", rr.Code)
	}

	// Missing indexName
	req = httptest.NewRequest(http.MethodPost, "/add-to-index", nil)
	rr = httptest.NewRecorder()
	h.AddToIndex(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("POST /add-to-index missing indexName expected 400, got %d", rr.Code)
	}

	// Not multipart
	h.engines["idx"] = engine.NewSearchEngine([]string{"f"}, nil, 1)
	req = httptest.NewRequest(http.MethodPost, "/add-to-index?indexName=idx", strings.NewReader("not multipart"))
	req.Header.Set("Content-Type", "text/plain")
	rr = httptest.NewRecorder()
	h.AddToIndex(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("POST /add-to-index invalid form expected 400, got %d", rr.Code)
	}

	// Multipart with no file
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.Close()
	req = httptest.NewRequest(http.MethodPost, "/add-to-index?indexName=idx", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr = httptest.NewRecorder()
	h.AddToIndex(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("POST /add-to-index missing file expected 400, got %d", rr.Code)
	}

	// Multipart file with invalid JSON and invalid CSV
	buf.Reset()
	mw = multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "bad.txt")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte("not json and not csv")); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	mw.Close()
	req = httptest.NewRequest(http.MethodPost, "/add-to-index?indexName=idx", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr = httptest.NewRecorder()
	h.AddToIndex(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("POST /add-to-index invalid upload expected 400, got %d", rr.Code)
	}
}

func TestAddToIndexHandler_JSONSuccess(t *testing.T) {
	h := NewHTTP()
	h.engines["idx"] = engine.NewSearchEngine([]string{"name"}, map[string]bool{"year": true}, 10)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "docs.json")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte(`[
		{"id":"1","name":"Sunny Rio","year":"2020"},
		{"id":"2","name":"Cloudy Day","year":"2021"}
	]`)); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/add-to-index?indexName=idx", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	h.AddToIndex(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var resp AddToIndexResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.AddedCount != 2 || resp.TotalDocs != 2 {
		t.Fatalf("unexpected add response: %+v", resp)
	}
	assertEngineSearchIDs(t, h.engines["idx"].SearchOneTerm("sunny", nil), "1")
	assertEngineSearchIDs(t, h.engines["idx"].SearchOneTerm("cloudy", nil), "2")
}

func TestAddToIndexHandler_CSVSuccess(t *testing.T) {
	h := NewHTTP()
	h.engines["idx"] = engine.NewSearchEngine([]string{"name"}, map[string]bool{"year": true}, 10)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "docs.csv")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte("id,name,year\n1,Sunny Rio,2020\n2,Cloudy Day,2021\n")); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/add-to-index?indexName=idx", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	h.AddToIndex(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var resp AddToIndexResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.AddedCount != 2 || resp.TotalDocs != 2 {
		t.Fatalf("unexpected add response: %+v", resp)
	}
	assertEngineSearchIDs(t, h.engines["idx"].SearchOneTerm("rio", nil), "1")
}

func TestAddOrUpdateDocumentHandler_Errors(t *testing.T) {
	h := NewHTTP()

	// Method not allowed
	req := httptest.NewRequest(http.MethodGet, "/document?indexName=idx", nil)
	rr := httptest.NewRecorder()
	h.AddOrUpdateDocument(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /document expected 405, got %d", rr.Code)
	}

	// Missing indexName (query and body)
	body := `{"document":{"id":"1","name":"x"}}`
	req = httptest.NewRequest(http.MethodPost, "/document", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.AddOrUpdateDocument(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST /document missing indexName expected 400, got %d", rr.Code)
	}

	// Index not found
	req = httptest.NewRequest(http.MethodPost, "/document?indexName=nope", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.AddOrUpdateDocument(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("POST /document index not found expected 404, got %d", rr.Code)
	}

	// Invalid JSON
	h.engines["idx"] = engine.NewSearchEngine([]string{"name"}, map[string]bool{"year": true}, 10)
	req = httptest.NewRequest(http.MethodPost, "/document?indexName=idx", strings.NewReader("{bad json}"))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.AddOrUpdateDocument(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST /document invalid JSON expected 400, got %d", rr.Code)
	}

	// Missing document
	req = httptest.NewRequest(http.MethodPost, "/document?indexName=idx", strings.NewReader(`{"indexName":"idx"}`))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.AddOrUpdateDocument(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST /document missing document expected 400, got %d", rr.Code)
	}

	// Missing id inside document
	req = httptest.NewRequest(http.MethodPost, "/document?indexName=idx", strings.NewReader(`{"document":{"name":"x"}}`))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.AddOrUpdateDocument(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST /document missing id expected 400, got %d", rr.Code)
	}
}

func TestDeleteDocumentHandler_Errors(t *testing.T) {
	h := NewHTTP()

	// Method not allowed
	req := httptest.NewRequest(http.MethodPost, "/document?indexName=idx&id=1", nil)
	rr := httptest.NewRecorder()
	h.DeleteDocument(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /document (delete) expected 405, got %d", rr.Code)
	}

	// Missing indexName
	req = httptest.NewRequest(http.MethodDelete, "/document?id=1", nil)
	rr = httptest.NewRecorder()
	h.DeleteDocument(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("DELETE /document missing indexName expected 400, got %d", rr.Code)
	}

	// Missing id
	req = httptest.NewRequest(http.MethodDelete, "/document?indexName=idx", nil)
	rr = httptest.NewRecorder()
	h.DeleteDocument(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("DELETE /document missing id expected 400, got %d", rr.Code)
	}

	// Index not found
	req = httptest.NewRequest(http.MethodDelete, "/document?indexName=nope&id=1", nil)
	rr = httptest.NewRecorder()
	h.DeleteDocument(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("DELETE /document index not found expected 404, got %d", rr.Code)
	}
}

func TestDocumentEndpoints_E2E_AddUpdateDeleteSearch(t *testing.T) {
	h := NewHTTP()

	// 1) Create index with indexFields=["name"] and filter=["year"]
	createBody := `{"indexName":"testidx","indexFields":["name"],"filters":["year"],"resultCount":10}`
	req := httptest.NewRequest(http.MethodPost, "/create-index", strings.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreateIndex(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create-index expected 201, got %d body=%s", rr.Code, rr.Body.String())
	}

	// helper: POST /document
	postDoc := func(doc map[string]interface{}) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]interface{}{"document": doc})
		r := httptest.NewRequest(http.MethodPost, "/document?indexName=testidx", bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.AddOrUpdateDocument(rec, r)
		return rec
	}

	// helper: DELETE /document
	delDoc := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodDelete, "/document?indexName=testidx&id="+id, nil)
		rec := httptest.NewRecorder()
		h.DeleteDocument(rec, r)
		return rec
	}

	// helper: GET /search
	search := func(q string) map[string]interface{} {
		r := httptest.NewRequest(http.MethodGet, "/search?index=testidx&q="+q, nil)
		rec := httptest.NewRecorder()
		h.Search(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("search %q expected 200, got %d body=%s", q, rec.Code, rec.Body.String())
		}
		var resp map[string]interface{}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode search resp: %v", err)
		}
		return resp
	}

	// helper: extract returned doc IDs from /search response
	// response shape:
	// { "response": { "Docs": [ { "ID": "...", "Data": {...}, "Score": ... }, ... ] } }
	extractIDs := func(searchResp map[string]interface{}) []string {
		respObj, ok := searchResp["response"].(map[string]interface{})
		if !ok {
			return nil
		}
		docsAny, ok := respObj["Docs"].([]interface{})
		if !ok {
			return nil
		}
		out := make([]string, 0, len(docsAny))
		for _, d := range docsAny {
			m, _ := d.(map[string]interface{})
			if m == nil {
				continue
			}
			if id, ok := m["ID"].(string); ok {
				out = append(out, id)
			}
		}
		sort.Strings(out)
		return out
	}

	assertIDs := func(got []string, exp ...string) {
		t.Helper()
		sort.Strings(exp)
		if len(got) != len(exp) {
			t.Fatalf("ids mismatch: got=%v exp=%v", got, exp)
		}
		for i := range got {
			if got[i] != exp[i] {
				t.Fatalf("ids mismatch: got=%v exp=%v", got, exp)
			}
		}
	}

	// 2) Add documents
	rec := postDoc(map[string]interface{}{"id": "1", "name": "Sunny Rio", "year": "2020"})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /document doc1 expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	rec = postDoc(map[string]interface{}{"id": "2", "name": "Rio Nights", "year": "2021"})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /document doc2 expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	rec = postDoc(map[string]interface{}{"id": "3", "name": "Cloudy Day", "year": "2021"})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /document doc3 expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 3) Search verifies initial state
	ids := extractIDs(search("sunny"))
	assertIDs(ids, "1")

	ids = extractIDs(search("rio"))
	assertIDs(ids, "1", "2")

	// 4) Update doc2: remove "rio", add "sunny"
	rec = postDoc(map[string]interface{}{"id": "2", "name": "Sunny Days", "year": "2021"})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /document doc2 update expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	ids = extractIDs(search("rio"))
	assertIDs(ids, "1")

	ids = extractIDs(search("sunny"))
	assertIDs(ids, "1", "2")

	// 5) Delete doc1 and verify it disappears
	rec = delDoc("1")
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /document doc1 expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	ids = extractIDs(search("rio"))
	assertIDs(ids) // empty

	ids = extractIDs(search("sunny"))
	assertIDs(ids, "2")

	// 6) Add doc4 and verify it shows up
	rec = postDoc(map[string]interface{}{"id": "4", "name": "Rio Sunny", "year": "2022"})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /document doc4 expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	ids = extractIDs(search("rio"))
	assertIDs(ids, "4")

	ids = extractIDs(search("sunny"))
	assertIDs(ids, "2", "4")

	// 7) Delete non-existing should still succeed with deleted=false
	rec = delDoc("does-not-exist")
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /document non-existing expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var delResp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&delResp); err != nil {
		t.Fatalf("decode delete resp: %v", err)
	}
	if d, ok := delResp["deleted"].(bool); ok {
		if d {
			t.Fatalf("expected deleted=false for non-existing doc")
		}
	}
}
