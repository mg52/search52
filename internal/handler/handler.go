package handler

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mg52/search52/internal/engine"
)

const (
	searchTimeout        = 10 * time.Second
	maxJSONBodyBytes     = 1 << 20
	maxDocumentBodyBytes = 5 << 20
	maxUploadBytes       = 1 << 30
	maxIndexNameLength   = 128
	maxFieldNameLength   = 64
	maxIndexFields       = 32
	maxFilters           = 32
	maxFilterValues      = 64
	maxFilterValueLength = 256
	maxFilterStringLen   = 2048
	maxQueryLength       = 512
	maxResultCount       = 10_000
	maxFieldWeight       = 1_000
	maxDocumentFields    = 256
	maxDocumentIDLength  = 256
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

type ErrorResponse struct {
	Status     string `json:"status"`
	StatusCode int    `json:"statusCode"`
	Error      string `json:"error"`
}

// AddToIndexRequest is the payload for adding documents to an existing index.
type AddToIndexRequest struct {
	IndexName string `json:"indexName"`
}

// AddToIndexResponse is returned on successful addition.
type AddToIndexResponse struct {
	Status     string `json:"status"`
	StatusCode int    `json:"statusCode"`
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
	Status       string         `json:"status"`
	StatusCode   int            `json:"statusCode"`
	IndexName    string         `json:"indexName"`
	ResultCount  int            `json:"resultCount"`
	FieldWeights map[string]int `json:"fieldWeights"`
	Duration     string         `json:"duration"`
}

type SearchResponse struct {
	Status     string               `json:"status"`
	StatusCode int                  `json:"statusCode"`
	Index      string               `json:"index"`
	Query      string               `json:"query"`
	Response   *engine.SearchResult `json:"response"`
	Duration   string               `json:"duration"`
	DurationMs int64                `json:"durationInMs"`
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

type IndexInfo struct {
	Name            string         `json:"name"`
	ActiveDocs      int            `json:"activeDocs"`
	StoredDocs      int            `json:"storedDocs"`
	IndexFields     []string       `json:"indexFields"`
	FieldWeights    map[string]int `json:"fieldWeights"`
	Filters         []string       `json:"filters"`
	ResultCount     int            `json:"resultCount"`
	DeletedVersions int            `json:"deletedVersions"`
}

type ListIndexesResponse struct {
	Status     string      `json:"status"`
	StatusCode int         `json:"statusCode"`
	Total      int         `json:"total"`
	Indexes    []IndexInfo `json:"indexes"`
}

type SaveEngineResponse struct {
	Status     string `json:"status"`
	StatusCode int    `json:"statusCode"`
	IndexName  string `json:"indexName"`
}

type LoadEngineResponse struct {
	Status     string `json:"status"`
	StatusCode int    `json:"statusCode"`
	IndexName  string `json:"indexName"`
	Duration   string `json:"duration"`
	DurationMs int64  `json:"durationMs"`
}

type CompactIndexRequest struct {
	IndexName string `json:"indexName"`
}

type CompactIndexResponse struct {
	Status     string              `json:"status"`
	StatusCode int                 `json:"statusCode"`
	IndexName  string              `json:"indexName"`
	Stats      engine.CompactStats `json:"stats"`
	Duration   string              `json:"duration"`
	DurationMs int64               `json:"durationMs"`
}

type HealthResponse struct {
	Status     string `json:"status"`
	StatusCode int    `json:"statusCode"`
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
	body, jsonErr := json.Marshal(ErrorResponse{
		Status:     "error",
		StatusCode: statusCode,
		Error:      fmt.Sprintf("%v", err),
	})
	if jsonErr != nil {
		body = []byte(fmt.Sprintf(`{"status":"error","statusCode":%d,"error":%q}`, statusCode, err.Error()))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	w.Write(body)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, dst interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON payload: %w", err)
	}
	var extra struct{}
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("invalid JSON payload: multiple JSON values")
	}
	return nil
}

func validateIdentifier(kind, value string, maxLen int) error {
	if value == "" {
		return fmt.Errorf("`%s` is required", kind)
	}
	if len(value) > maxLen {
		return fmt.Errorf("`%s` must be at most %d characters", kind, maxLen)
	}
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("`%s` may only contain letters, numbers, underscore, dash and dot", kind)
	}
	return nil
}

func validateFieldList(name string, fields []string, required bool, maxCount int) error {
	if required && len(fields) == 0 {
		return fmt.Errorf("`%s` must contain at least one field", name)
	}
	if len(fields) > maxCount {
		return fmt.Errorf("`%s` can contain at most %d fields", name, maxCount)
	}
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if err := validateIdentifier(name, field, maxFieldNameLength); err != nil {
			return err
		}
		if _, ok := seen[field]; ok {
			return fmt.Errorf("`%s` contains duplicate field %q", name, field)
		}
		seen[field] = struct{}{}
	}
	return nil
}

func validateCreateIndexRequest(req *CreateIndexRequest) error {
	if err := validateIdentifier("indexName", req.IndexName, maxIndexNameLength); err != nil {
		return err
	}
	if err := validateFieldList("indexFields", req.IndexFields, true, maxIndexFields); err != nil {
		return err
	}
	if err := validateFieldList("filters", req.Filters, false, maxFilters); err != nil {
		return err
	}
	if req.ResultCount == 0 {
		req.ResultCount = 100
	}
	if req.ResultCount < 1 || req.ResultCount > maxResultCount {
		return fmt.Errorf("`resultCount` must be between 1 and %d", maxResultCount)
	}
	indexFields := make(map[string]struct{}, len(req.IndexFields))
	for _, field := range req.IndexFields {
		indexFields[field] = struct{}{}
	}
	for field, weight := range req.FieldWeights {
		if err := validateIdentifier("fieldWeights", field, maxFieldNameLength); err != nil {
			return err
		}
		if _, ok := indexFields[field]; !ok {
			return fmt.Errorf("`fieldWeights.%s` must refer to an index field", field)
		}
		if weight < 1 || weight > maxFieldWeight {
			return fmt.Errorf("`fieldWeights.%s` must be between 1 and %d", field, maxFieldWeight)
		}
	}
	return nil
}

func parseAndValidateSearchFilters(raw string, allowed map[string]bool) (map[string][]interface{}, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxFilterStringLen {
		return nil, fmt.Errorf("`filter` must be at most %d characters", maxFilterStringLen)
	}
	filters := make(map[string][]interface{})
	items := strings.Split(raw, ",")
	if len(items) > maxFilterValues {
		return nil, fmt.Errorf("`filter` can contain at most %d values", maxFilterValues)
	}
	for _, item := range items {
		parts := strings.SplitN(strings.TrimSpace(item), ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("invalid filter %q, expected field:value", item)
		}
		field := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if len(value) > maxFilterValueLength {
			return nil, fmt.Errorf("filter value for %q must be at most %d characters", field, maxFilterValueLength)
		}
		if err := validateIdentifier("filter", field, maxFieldNameLength); err != nil {
			return nil, err
		}
		if !allowed[field] {
			return nil, fmt.Errorf("filter field %q is not configured for this index", field)
		}
		if len(filters[field]) >= maxFilterValues {
			return nil, fmt.Errorf("filter field %q has too many values", field)
		}
		filters[field] = append(filters[field], value)
	}
	return filters, nil
}

func validateDocument(doc map[string]interface{}) error {
	if len(doc) > maxDocumentFields {
		return fmt.Errorf("document can contain at most %d fields", maxDocumentFields)
	}
	rawID, ok := doc["id"]
	if !ok || rawID == nil {
		return errors.New("document missing `id` field")
	}
	id := fmt.Sprintf("%v", rawID)
	if id == "" || id == "<nil>" {
		return errors.New("invalid document `id`")
	}
	if len(id) > maxDocumentIDLength {
		return fmt.Errorf("document `id` must be at most %d characters", maxDocumentIDLength)
	}
	return nil
}

func (ht *HTTP) ListIndexes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		errJSON(w, http.StatusMethodNotAllowed, errors.New("unsupported method"))
		return
	}

	ht.mu.RLock()
	names := make([]string, 0, len(ht.engines))
	engines := make(map[string]*engine.SearchEngine, len(ht.engines))
	for name, sec := range ht.engines {
		names = append(names, name)
		engines[name] = sec
	}
	ht.mu.RUnlock()
	sort.Strings(names)

	indexes := make([]IndexInfo, 0, len(names))
	for _, name := range names {
		stats := engines[name].Stats()
		filters := make([]string, 0, len(stats.Filters))
		for filter := range stats.Filters {
			filters = append(filters, filter)
		}
		sort.Strings(filters)

		indexes = append(indexes, IndexInfo{
			Name:            name,
			ActiveDocs:      stats.ActiveDocuments,
			StoredDocs:      stats.StoredDocuments,
			IndexFields:     stats.IndexFields,
			FieldWeights:    stats.FieldWeights,
			Filters:         filters,
			ResultCount:     stats.ResultSize,
			DeletedVersions: stats.StoredDocuments - stats.ActiveDocuments,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(ListIndexesResponse{
		Status:     "success",
		StatusCode: http.StatusOK,
		Total:      len(indexes),
		Indexes:    indexes,
	})
}

func (ht *HTTP) Search(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, errors.New("unsupported method"))
		return
	}

	indexName := r.URL.Query().Get("index")
	if err := validateIdentifier("index", indexName, maxIndexNameLength); err != nil {
		errJSON(w, http.StatusBadRequest, err)
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
	if len(query) > maxQueryLength {
		errJSON(w, http.StatusBadRequest, fmt.Errorf("`q` must be at most %d characters", maxQueryLength))
		return
	}

	filters, err := parseAndValidateSearchFilters(r.URL.Query().Get("filter"), sec.FilterFields())
	if err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}

	startTime := time.Now()

	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()
	result, err := sec.Search(ctx, query, filters)
	if err != nil {
		if errors.Is(err, engine.ErrSearchCanceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			errJSON(w, http.StatusGatewayTimeout, fmt.Errorf("search canceled after %s", searchTimeout))
			return
		}
		errJSON(w, http.StatusInternalServerError, err)
		return
	}
	if result == nil {
		result = &engine.SearchResult{}
	}

	duration := time.Since(startTime)

	resp := SearchResponse{
		Status:     "success",
		StatusCode: http.StatusOK,
		Index:      indexName,
		Query:      query,
		Response:   result,
		Duration:   duration.String(),
		DurationMs: duration.Milliseconds(),
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
	if err := decodeJSON(w, r, maxJSONBodyBytes, &req); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}
	if err := validateCreateIndexRequest(&req); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}

	filterMap := make(map[string]bool, len(req.Filters))
	for _, f := range req.Filters {
		filterMap[f] = true
	}

	start := time.Now()
	sec := engine.NewSearchEngine(
		req.IndexFields,
		req.FieldWeights,
		filterMap,
		req.ResultCount,
	)
	elapsed := time.Since(start)

	ht.mu.Lock()
	_, exists := ht.engines[req.IndexName]
	if !exists {
		ht.engines[req.IndexName] = sec
	}
	ht.mu.Unlock()

	if exists {
		errJSON(w, http.StatusConflict, fmt.Errorf("index %q already exists", req.IndexName))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(CreateIndexResponse{
		Status:       "success",
		StatusCode:   http.StatusCreated,
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
	if err := validateIdentifier("indexName", indexName, maxIndexNameLength); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}

	ht.mu.RLock()
	sec, ok := ht.engines[indexName]
	ht.mu.RUnlock()
	if !ok {
		errJSON(w, http.StatusNotFound, fmt.Errorf("index %q not found", indexName))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
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
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(AddToIndexResponse{
		Status:     "success",
		StatusCode: http.StatusOK,
		IndexName:  indexName,
		AddedCount: len(docs),
		Duration:   elapsed.String(),
		DurationMs: elapsed.Milliseconds(),
		TotalDocs:  int64(sec.StoredDocumentCount()),
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
	if err := decodeJSON(w, r, maxJSONBodyBytes, &req); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}
	if err := validateIdentifier("indexName", req.IndexName, maxIndexNameLength); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}
	ht.mu.RLock()
	sec, ok := ht.engines[req.IndexName]
	ht.mu.RUnlock()
	if !ok {
		errJSON(w, http.StatusNotFound, fmt.Errorf("index %q not found", req.IndexName))
		return
	}

	baseDir := os.Getenv("SEARCH52_INDEX_DATA_DIR")
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
	resp := SaveEngineResponse{
		Status:     "success",
		StatusCode: http.StatusOK,
		IndexName:  req.IndexName,
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
	if err := decodeJSON(w, r, maxJSONBodyBytes, &req); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}
	if err := validateIdentifier("indexName", req.IndexName, maxIndexNameLength); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}

	baseDir := os.Getenv("SEARCH52_INDEX_DATA_DIR")
	if baseDir == "" {
		baseDir = "./data"
	}

	dataDir := filepath.Join(baseDir, req.IndexName)
	start := time.Now()
	eng, err := engine.LoadAll(dataDir)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, fmt.Errorf("failed to load index %q; existing in-memory index was left unchanged: %w", req.IndexName, err))
		return
	}

	ht.mu.Lock()
	ht.engines[req.IndexName] = eng
	ht.mu.Unlock()

	duration := time.Since(start)

	resp := LoadEngineResponse{
		Status:     "success",
		StatusCode: http.StatusOK,
		IndexName:  req.IndexName,
		Duration:   duration.String(),
		DurationMs: duration.Milliseconds(),
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
	if err := decodeJSON(w, r, maxDocumentBodyBytes, &req); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}
	if indexName == "" {
		indexName = req.IndexName
	}
	if err := validateIdentifier("indexName", indexName, maxIndexNameLength); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}
	if req.Document == nil {
		errJSON(w, http.StatusBadRequest, errors.New("`document` is required"))
		return
	}
	if err := validateDocument(req.Document); err != nil {
		errJSON(w, http.StatusBadRequest, err)
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
	docID := fmt.Sprintf("%v", req.Document["id"])

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
		TotalDocs:  int64(sec.StoredDocumentCount()),
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
	if err := validateIdentifier("indexName", indexName, maxIndexNameLength); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		errJSON(w, http.StatusBadRequest, errors.New("`id` query parameter is required"))
		return
	}
	if len(id) > maxDocumentIDLength {
		errJSON(w, http.StatusBadRequest, fmt.Errorf("`id` must be at most %d characters", maxDocumentIDLength))
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

func (ht *HTTP) CompactIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		errJSON(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}

	var req CompactIndexRequest
	if err := decodeJSON(w, r, maxJSONBodyBytes, &req); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}
	if err := validateIdentifier("indexName", req.IndexName, maxIndexNameLength); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}

	ht.mu.RLock()
	sec, ok := ht.engines[req.IndexName]
	ht.mu.RUnlock()
	if !ok {
		errJSON(w, http.StatusNotFound, fmt.Errorf("index %q not found", req.IndexName))
		return
	}

	start := time.Now()
	stats := sec.CompactDeleted()
	duration := time.Since(start)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(CompactIndexResponse{
		Status:     "success",
		StatusCode: http.StatusOK,
		IndexName:  req.IndexName,
		Stats:      stats,
		Duration:   duration.String(),
		DurationMs: duration.Milliseconds(),
	})
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
	resp := HealthResponse{
		Status:     "ok",
		StatusCode: http.StatusOK,
		Duration:   duration.String(),
		DurationMs: duration.Milliseconds(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		errJSON(w, http.StatusInternalServerError, err)
	}
}
