package handler

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mg52/search52/internal/engine"
)

// AddToIndexRequest is the payload for adding documents to an existing index.
type AddToIndexRequest struct {
	IndexName string `json:"indexName"`
}

// AddToIndexResponse is returned on successful addition.
type AddToIndexResponse struct {
	IndexName  string `json:"indexName"`
	AddedCount int    `json:"addedCount"`
	Duration   string `json:"duration"`
	DurationMs int64  `json:"durationMs"`
	TotalDocs  int64  `json:"totalDocs"`
}

// CreateIndexRequest is the payload for creating index.
type CreateIndexRequest struct {
	IndexName    string         `json:"indexName"`
	IndexFields  []string       `json:"indexFields"`
	FieldWeights map[string]int `json:"fieldWeights"`
	Filters      []string       `json:"filters"`
	ResultCount  int            `json:"resultCount"`
}

// CreateIndexResponse is returned on succressful index creation.
type CreateIndexResponse struct {
	IndexName    string         `json:"indexName"`
	ResultCount  int            `json:"resultCount"`
	FieldWeights map[string]int `json:"fieldWeights"`
	Duration     string         `json:"duration"`
}

// AddOrUpdateDocumentRequest is the payload for inserting/updating a single document.
type AddOrUpdateDocumentRequest struct {
	IndexName string                 `json:"indexName,omitempty"` // optional, can also come from query param
	Document  map[string]interface{} `json:"document"`
}

// AddOrUpdateDocumentResponse is returned on successful upsert.
type AddOrUpdateDocumentResponse struct {
	Status     string `json:"status"`
	StatusCode int    `json:"statusCode"`
	IndexName  string `json:"indexName"`
	ID         string `json:"id"`
	Duration   string `json:"duration"`
	DurationMs int64  `json:"durationMs"`
	TotalDocs  int64  `json:"totalDocs"`
}

// DeleteDocumentResponse is returned on successful delete.
type DeleteDocumentResponse struct {
	Status     string `json:"status"`
	StatusCode int    `json:"statusCode"`
	IndexName  string `json:"indexName"`
	ID         string `json:"id"`
	Deleted    bool   `json:"deleted"`
	Duration   string `json:"duration"`
	DurationMs int64  `json:"durationMs"`
}

type HTTP struct {
	mu      sync.RWMutex
	engines map[string]*engine.SearchEngine
}

// NewHTTP initializes the handler with an empty map.
func NewHTTP() *HTTP {
	return &HTTP{
		engines: make(map[string]*engine.SearchEngine),
	}
}

func errJSON(w http.ResponseWriter, statusCode int, err error) {
	body, jsonErr := json.Marshal(map[string]interface{}{
		"err": fmt.Sprintf("%v", err),
	})
	if jsonErr != nil {
		body = []byte(fmt.Sprintf(`{"err":%q}`, err.Error()))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	w.Write(body)
}

func (ht *HTTP) Search(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, errors.New("unsupported method"))
		return
	}

	indexName := r.URL.Query().Get("index")
	if indexName == "" {
		errJSON(w, http.StatusBadRequest, errors.New("`index` query parameter is required"))
		return
	}

	ht.mu.RLock()
	sec, ok := ht.engines[indexName]
	ht.mu.RUnlock()
	if !ok {
		errJSON(w, http.StatusNotFound, fmt.Errorf("index %q not found", indexName))
		return
	}

	query := r.URL.Query().Get("q")

	// Parse filters
	filters := make(map[string][]interface{})
	filterStr := r.URL.Query().Get("filter")
	if filterStr != "" {
		for _, item := range strings.Split(filterStr, ",") {
			parts := strings.SplitN(item, ":", 2)
			if len(parts) != 2 {
				continue
			}
			filters[parts[0]] = append(filters[parts[0]], parts[1])
		}
	}

	startTime := time.Now()

	result := sec.Search(query, filters)

	duration := time.Since(startTime)

	resp := map[string]interface{}{
		"status":       "success",
		"statusCode":   200,
		"index":        indexName,
		"query":        query,
		"response":     result,
		"duration":     duration,
		"durationInMs": duration.Milliseconds(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (ht *HTTP) CreateIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		errJSON(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}

	var req CreateIndexRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, fmt.Errorf("invalid JSON payload: %w", err))
		return
	}

	if req.IndexName == "" {
		errJSON(w, http.StatusBadRequest, errors.New("`indexName` is required"))
		return
	}
	ht.mu.RLock()
	_, exists := ht.engines[req.IndexName]
	ht.mu.RUnlock()
	if exists {
		errJSON(w, http.StatusConflict, fmt.Errorf("index %q already exists", req.IndexName))
		return
	}

	if req.ResultCount <= 0 {
		req.ResultCount = 100
	}

	filterMap := make(map[string]bool, len(req.Filters))
	for _, f := range req.Filters {
		filterMap[f] = true
	}

	start := time.Now()
	sec := engine.NewSearchEngineWithFieldWeights(
		req.IndexFields,
		req.FieldWeights,
		filterMap,
		req.ResultCount,
	)
	elapsed := time.Since(start)

	ht.mu.Lock()
	ht.engines[req.IndexName] = sec
	ht.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(CreateIndexResponse{
		IndexName:    req.IndexName,
		ResultCount:  req.ResultCount,
		FieldWeights: sec.FieldWeights,
		Duration:     elapsed.String(),
	})
}

// AddToIndex appends the documents from the given JSON file into an existing index.
func (ht *HTTP) AddToIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		errJSON(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}

	indexName := r.URL.Query().Get("indexName")
	if indexName == "" {
		errJSON(w, http.StatusBadRequest, errors.New("`indexName` query parameter is required"))
		return
	}

	ht.mu.RLock()
	sec, ok := ht.engines[indexName]
	ht.mu.RUnlock()
	if !ok {
		errJSON(w, http.StatusNotFound, fmt.Errorf("index %q not found", indexName))
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		errJSON(w, http.StatusBadRequest, fmt.Errorf("invalid multipart form: %w", err))
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		errJSON(w, http.StatusBadRequest, fmt.Errorf("file upload required: %w", err))
		return
	}
	defer file.Close()

	raw, err := io.ReadAll(file)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, fmt.Errorf("unable to read uploaded file: %w", err))
		return
	}

	// try JSON first
	var docs []map[string]interface{}
	if err := json.Unmarshal(raw, &docs); err != nil {
		// not JSON → try CSV
		rdr := csv.NewReader(bytes.NewReader(raw))
		rows, err2 := rdr.ReadAll()
		if err2 != nil || len(rows) < 2 {
			errJSON(w, http.StatusBadRequest, fmt.Errorf("invalid JSON or CSV in file: %v / %v", err, err2))
			return
		}
		headers := rows[0]
		docs = make([]map[string]interface{}, 0, len(rows)-1)
		for _, row := range rows[1:] {
			doc := make(map[string]interface{}, len(headers))
			for i, h := range headers {
				if i < len(row) {
					doc[h] = row[i]
				} else {
					doc[h] = ""
				}
			}
			docs = append(docs, doc)
		}
	}

	start := time.Now()
	sec.Index(docs)
	elapsed := time.Since(start)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AddToIndexResponse{
		IndexName:  indexName,
		AddedCount: len(docs),
		Duration:   elapsed.String(),
		DurationMs: elapsed.Milliseconds(),
		TotalDocs:  int64(len(sec.Documents)),
	})
}

// SaveEngineRequest is the payload for saving an engine to disk.
type SaveEngineRequest struct {
	IndexName string `json:"indexName"`
}

// LoadEngineRequest is the payload for loading an engine from disk.
type LoadEngineRequest struct {
	IndexName string `json:"indexName"`
}

// SaveEngine persists all files for the named engine.
func (ht *HTTP) SaveEngine(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		errJSON(w, http.StatusMethodNotAllowed, fmt.Errorf("unsupported method"))
		return
	}
	var req SaveEngineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, fmt.Errorf("invalid JSON payload: %w", err))
		return
	}
	if req.IndexName == "" {
		errJSON(w, http.StatusBadRequest, fmt.Errorf("`indexName` is required"))
		return
	}
	ht.mu.RLock()
	sec, ok := ht.engines[req.IndexName]
	ht.mu.RUnlock()
	if !ok {
		errJSON(w, http.StatusNotFound, fmt.Errorf("index %q not found", req.IndexName))
		return
	}

	baseDir := os.Getenv("INDEX_DATA_DIR")
	if baseDir == "" {
		baseDir = "./data"
	}
	dataDir := filepath.Join(baseDir, req.IndexName)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		errJSON(w, http.StatusInternalServerError, fmt.Errorf("failed to create data dir: %w", err))
		return
	}
	if err := sec.SaveAll(dataDir); err != nil {
		errJSON(w, http.StatusInternalServerError, fmt.Errorf("failed to save engine: %w", err))
		return
	}
	resp := map[string]interface{}{
		"status":     "success",
		"statusCode": 200,
		"indexName":  req.IndexName,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func (ht *HTTP) LoadEngine(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		errJSON(w, http.StatusMethodNotAllowed, fmt.Errorf("unsupported method"))
		return
	}

	var req LoadEngineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, fmt.Errorf("invalid JSON payload: %w", err))
		return
	}
	if req.IndexName == "" {
		errJSON(w, http.StatusBadRequest, fmt.Errorf("`indexName` is required"))
		return
	}

	baseDir := os.Getenv("INDEX_DATA_DIR")
	if baseDir == "" {
		baseDir = "./data"
	}

	dataDir := filepath.Join(baseDir, req.IndexName)
	start := time.Now()
	eng, err := engine.LoadAll(dataDir)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, err)
		return
	}

	ht.mu.Lock()
	ht.engines[req.IndexName] = eng
	ht.mu.Unlock()

	duration := time.Since(start)

	resp := map[string]interface{}{
		"status":     "success",
		"statusCode": 200,
		"indexName":  req.IndexName,
		"duration":   duration.String(),
		"durationMs": duration.Milliseconds(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// AddOrUpdateDocument upserts a single document into an existing index.
// Endpoint suggestion: POST /document?indexName=...
// Body: either { "document": {...} } or { "indexName": "...", "document": {...} }
func (ht *HTTP) AddOrUpdateDocument(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		errJSON(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}

	// indexName can be in query or body
	indexName := r.URL.Query().Get("indexName")

	var req AddOrUpdateDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, fmt.Errorf("invalid JSON payload: %w", err))
		return
	}
	if indexName == "" {
		indexName = req.IndexName
	}
	if indexName == "" {
		errJSON(w, http.StatusBadRequest, errors.New("`indexName` is required (query param or JSON body)"))
		return
	}
	if req.Document == nil {
		errJSON(w, http.StatusBadRequest, errors.New("`document` is required"))
		return
	}

	// Find engine
	ht.mu.RLock()
	sec, ok := ht.engines[indexName]
	ht.mu.RUnlock()
	if !ok {
		errJSON(w, http.StatusNotFound, fmt.Errorf("index %q not found", indexName))
		return
	}

	// Validate id early for response fields
	rawID, ok := req.Document["id"]
	if !ok || rawID == nil {
		errJSON(w, http.StatusBadRequest, errors.New("document missing `id` field"))
		return
	}
	docID := fmt.Sprintf("%v", rawID)
	if docID == "" || docID == "<nil>" {
		errJSON(w, http.StatusBadRequest, errors.New("invalid document `id`"))
		return
	}

	start := time.Now()
	if err := sec.AddOrUpdateDocument(req.Document); err != nil {
		errJSON(w, http.StatusInternalServerError, err)
		return
	}
	duration := time.Since(start)

	resp := AddOrUpdateDocumentResponse{
		Status:     "success",
		StatusCode: 200,
		IndexName:  indexName,
		ID:         docID,
		Duration:   duration.String(),
		DurationMs: duration.Milliseconds(),
		TotalDocs:  int64(len(sec.Documents)),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// DeleteDocument deletes (tombstones) a single document by external ID.
// Endpoint suggestion: DELETE /document?indexName=...&id=...
func (ht *HTTP) DeleteDocument(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		errJSON(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}

	indexName := r.URL.Query().Get("indexName")
	if indexName == "" {
		errJSON(w, http.StatusBadRequest, errors.New("`indexName` query parameter is required"))
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		errJSON(w, http.StatusBadRequest, errors.New("`id` query parameter is required"))
		return
	}

	ht.mu.RLock()
	sec, ok := ht.engines[indexName]
	ht.mu.RUnlock()
	if !ok {
		errJSON(w, http.StatusNotFound, fmt.Errorf("index %q not found", indexName))
		return
	}

	start := time.Now()
	deleted := sec.DeleteDocument(id)
	duration := time.Since(start)

	resp := DeleteDocumentResponse{
		Status:     "success",
		StatusCode: 200,
		IndexName:  indexName,
		ID:         id,
		Deleted:    deleted,
		Duration:   duration.String(),
		DurationMs: duration.Milliseconds(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// Health is a simple health-check endpoint.
func (ht *HTTP) Health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		errJSON(w, http.StatusMethodNotAllowed, fmt.Errorf("unsupported method"))
		return
	}

	start := time.Now()
	duration := time.Since(start)
	resp := map[string]interface{}{
		"status":     "ok",
		"duration":   duration.String(),
		"durationMs": duration.Milliseconds(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		errJSON(w, http.StatusInternalServerError, err)
	}
}
