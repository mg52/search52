package handler

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/mg52/search52/internal/engine"
)

const snapshotTimeFormat = "20060102150405" // UTC

var snapshotTimestampPattern = regexp.MustCompile(`^[0-9]{14}$`)

// SaveIndexResponse is returned after an index snapshot was written to disk.
type SaveIndexResponse struct {
	Status        string `json:"status"`
	StatusCode    int    `json:"statusCode"`
	IndexName     string `json:"indexName"`
	IndexFile     string `json:"indexFile"`
	DocumentsFile string `json:"documentsFile"`
	SavedDocs     int    `json:"savedDocs"`
	Duration      string `json:"duration"`
	DurationMs    int64  `json:"durationMs"`
}

// LoadIndexResponse is returned after an index was rebuilt from a snapshot.
type LoadIndexResponse struct {
	Status        string `json:"status"`
	StatusCode    int    `json:"statusCode"`
	IndexName     string `json:"indexName"`
	IndexFile     string `json:"indexFile"`
	DocumentsFile string `json:"documentsFile"`
	LoadedDocs    int    `json:"loadedDocs"`
	Duration      string `json:"duration"`
	DurationMs    int64  `json:"durationMs"`
}

func snapshotFileNames(indexName, timestamp string) (indexFile, documentsFile string) {
	return fmt.Sprintf("index_%s_%s.json", indexName, timestamp),
		fmt.Sprintf("documents_%s_%s.json", indexName, timestamp)
}

// writeFileAtomic writes via a temp file + rename so a crash never leaves a
// half-written snapshot under the final name.
func writeFileAtomic(path string, write func(io.Writer) error) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	bw := bufio.NewWriter(tmp)
	if err = write(bw); err != nil {
		return err
	}
	if err = bw.Flush(); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil { // data on disk before the rename publishes it
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// writeDocsJSON streams docs as a JSON array without building one big buffer.
func writeDocsJSON(w io.Writer, docs []map[string]interface{}) error {
	if _, err := io.WriteString(w, "["); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	for i, doc := range docs {
		if i > 0 {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		if err := enc.Encode(doc); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "]")
	return err
}

// SaveIndex writes the index definition and its active documents to DataDir.
// POST /save-index?indexName=...
//
// Only a short read lock is held (to copy document references); encoding and
// disk I/O run without any engine lock, so searches and writes keep going.
func (ht *HTTP) SaveIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		errJSON(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
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

	start := time.Now()
	stats := sec.Stats()
	docs := sec.ExportDocuments()

	filters := make([]string, 0, len(stats.Filters))
	for f := range stats.Filters {
		filters = append(filters, f)
	}
	sort.Strings(filters)
	meta := CreateIndexRequest{
		IndexName:    indexName,
		IndexFields:  stats.IndexFields,
		FieldWeights: stats.FieldWeights,
		Filters:      filters,
		ResultCount:  stats.ResultSize,
	}

	if err := os.MkdirAll(ht.DataDir, 0o755); err != nil {
		errJSON(w, http.StatusInternalServerError, fmt.Errorf("unable to create data dir: %w", err))
		return
	}
	indexFile, documentsFile := snapshotFileNames(indexName, start.UTC().Format(snapshotTimeFormat))

	// Documents first: an index file on disk always has its documents next to it.
	if err := writeFileAtomic(filepath.Join(ht.DataDir, documentsFile), func(w io.Writer) error {
		return writeDocsJSON(w, docs)
	}); err != nil {
		errJSON(w, http.StatusInternalServerError, fmt.Errorf("unable to save documents: %w", err))
		return
	}
	if err := writeFileAtomic(filepath.Join(ht.DataDir, indexFile), func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(meta)
	}); err != nil {
		errJSON(w, http.StatusInternalServerError, fmt.Errorf("unable to save index: %w", err))
		return
	}

	elapsed := time.Since(start)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(SaveIndexResponse{
		Status:        "success",
		StatusCode:    http.StatusOK,
		IndexName:     indexName,
		IndexFile:     indexFile,
		DocumentsFile: documentsFile,
		SavedDocs:     len(docs),
		Duration:      elapsed.String(),
		DurationMs:    elapsed.Milliseconds(),
	})
}

// latestSnapshotTimestamp returns the newest saved timestamp for indexName,
// or os.ErrNotExist. It lists the directory instead of using filepath.Glob so a
// DataDir containing glob characters such as `[` still works.
func (ht *HTTP) latestSnapshotTimestamp(indexName string) (string, error) {
	entries, err := os.ReadDir(ht.DataDir)
	if err != nil {
		return "", err
	}
	re := regexp.MustCompile(`^index_` + regexp.QuoteMeta(indexName) + `_([0-9]{14})\.json$`)
	latest := ""
	for _, e := range entries {
		if m := re.FindStringSubmatch(e.Name()); m != nil && m[1] > latest { // fixed-width: string order is time order
			latest = m[1]
		}
	}
	if latest == "" {
		return "", os.ErrNotExist
	}
	return latest, nil
}

// LoadIndex rebuilds an index from a saved snapshot.
// POST /load-index?indexName=...[&timestamp=YYYYMMDDHHMMSS] (latest when omitted)
//
// The engine is built off to the side and only registered once complete, so
// other indexes keep serving and no one ever sees a half-loaded index.
func (ht *HTTP) LoadIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		errJSON(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	indexName := r.URL.Query().Get("indexName")
	if err := validateIdentifier("indexName", indexName, maxIndexNameLength); err != nil {
		errJSON(w, http.StatusBadRequest, err)
		return
	}
	timestamp := r.URL.Query().Get("timestamp")
	if timestamp != "" && !snapshotTimestampPattern.MatchString(timestamp) {
		errJSON(w, http.StatusBadRequest, errors.New("`timestamp` must look like YYYYMMDDHHMMSS"))
		return
	}

	ht.mu.RLock()
	_, exists := ht.engines[indexName]
	ht.mu.RUnlock()
	if exists {
		errJSON(w, http.StatusConflict, fmt.Errorf("index %q already exists", indexName))
		return
	}

	start := time.Now()
	if timestamp == "" {
		var err error
		if timestamp, err = ht.latestSnapshotTimestamp(indexName); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				errJSON(w, http.StatusNotFound, fmt.Errorf("no saved snapshot for index %q", indexName))
			} else {
				errJSON(w, http.StatusInternalServerError, fmt.Errorf("unable to list snapshots: %w", err))
			}
			return
		}
	}
	indexFile, documentsFile := snapshotFileNames(indexName, timestamp)

	var meta CreateIndexRequest
	if err := readJSONFile(filepath.Join(ht.DataDir, indexFile), &meta); err != nil {
		errJSON(w, statusForFileErr(err), fmt.Errorf("unable to read %s: %w", indexFile, err))
		return
	}
	// The file is outside the trust boundary: apply the same rules as CreateIndex.
	if meta.IndexName != indexName {
		errJSON(w, http.StatusUnprocessableEntity, fmt.Errorf("%s belongs to index %q", indexFile, meta.IndexName))
		return
	}
	if err := validateCreateIndexRequest(&meta); err != nil {
		errJSON(w, http.StatusUnprocessableEntity, fmt.Errorf("invalid %s: %w", indexFile, err))
		return
	}
	var docs []map[string]interface{}
	if err := readJSONFile(filepath.Join(ht.DataDir, documentsFile), &docs); err != nil {
		errJSON(w, statusForFileErr(err), fmt.Errorf("unable to read %s: %w", documentsFile, err))
		return
	}

	filterMap := make(map[string]bool, len(meta.Filters))
	for _, f := range meta.Filters {
		filterMap[f] = true
	}
	sec := engine.NewSearchEngine(meta.IndexFields, meta.FieldWeights, filterMap, meta.ResultCount)
	sec.Index(docs)

	ht.mu.Lock()
	_, exists = ht.engines[indexName]
	if !exists {
		ht.engines[indexName] = sec
	}
	ht.mu.Unlock()
	if exists {
		errJSON(w, http.StatusConflict, fmt.Errorf("index %q already exists", indexName))
		return
	}

	elapsed := time.Since(start)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(LoadIndexResponse{
		Status:        "success",
		StatusCode:    http.StatusOK,
		IndexName:     indexName,
		IndexFile:     indexFile,
		DocumentsFile: documentsFile,
		LoadedDocs:    sec.StoredDocumentCount(),
		Duration:      elapsed.String(),
		DurationMs:    elapsed.Milliseconds(),
	})
}

func readJSONFile(path string, dst interface{}) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(bufio.NewReader(f)).Decode(dst)
}

func statusForFileErr(err error) int {
	if errors.Is(err, os.ErrNotExist) {
		return http.StatusNotFound
	}
	return http.StatusUnprocessableEntity
}
