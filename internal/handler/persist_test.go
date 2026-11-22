package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mg52/search52/internal/engine"
)

func newSavedHTTP(t *testing.T) *HTTP {
	t.Helper()
	h := NewHTTP()
	h.DataDir = t.TempDir()
	se := engine.NewSearchEngine([]string{"name"}, map[string]int{"name": 3}, map[string]bool{"year": true}, 7)
	for _, d := range []map[string]interface{}{
		{"id": "1", "name": "apple pie", "year": "2020"},
		{"id": "2", "name": "banana split", "year": "2021"},
		{"id": "3", "name": "cherry tart", "year": "2021"},
	} {
		if err := se.AddOrUpdateDocument(d); err != nil {
			t.Fatal(err)
		}
	}
	se.DeleteDocument("3")
	h.engines["products"] = se
	return h
}

func post(h http.HandlerFunc, target string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodPost, target, nil))
	return rr
}

func TestSaveAndLoadIndexRoundTrip(t *testing.T) {
	h := newSavedHTTP(t)

	rr := post(h.SaveIndex, "/save-index?indexName=products")
	if rr.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rr.Code, rr.Body.String())
	}
	files, _ := filepath.Glob(filepath.Join(h.DataDir, "*"))
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %v", files)
	}
	for _, re := range []string{`^index_products_\d{14}\.json$`, `^documents_products_\d{14}\.json$`} {
		found := false
		for _, f := range files {
			found = found || regexp.MustCompile(re).MatchString(filepath.Base(f))
		}
		if !found {
			t.Errorf("no file matching %s in %v", re, files)
		}
	}

	// Load into a fresh server that shares the data dir.
	h2 := NewHTTP()
	h2.DataDir = h.DataDir
	rr = post(h2.LoadIndex, "/load-index?indexName=products")
	if rr.Code != http.StatusOK {
		t.Fatalf("load: %d %s", rr.Code, rr.Body.String())
	}
	got := h2.engines["products"]
	if got == nil {
		t.Fatal("index not registered")
	}
	stats := got.Stats()
	if stats.ActiveDocuments != 2 || stats.ResultSize != 7 || stats.FieldWeights["name"] != 3 || !stats.Filters["year"] {
		t.Fatalf("unexpected loaded stats: %+v", stats)
	}
	res, err := got.Search(t.Context(), "banana", map[string][]interface{}{"year": {"2021"}})
	if err != nil || len(res.Docs) != 1 || res.Docs[0].ID != "2" {
		t.Fatalf("search after load: %+v err=%v", res, err)
	}
	if res, _ := got.Search(t.Context(), "cherry", nil); len(res.Docs) != 0 {
		t.Fatal("deleted doc must not be saved")
	}

	// Loading again conflicts.
	if rr = post(h2.LoadIndex, "/load-index?indexName=products"); rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rr.Code)
	}
}

func TestSaveIndexKeepsSearchesRunning(t *testing.T) {
	h := newSavedHTTP(t)
	se := h.engines["products"]

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if _, err := se.Search(t.Context(), "apple", nil); err != nil {
						t.Error(err)
						return
					}
					_ = se.AddOrUpdateDocument(map[string]interface{}{"id": "9", "name": "apple"})
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		if rr := post(h.SaveIndex, "/save-index?indexName=products"); rr.Code != http.StatusOK {
			t.Fatalf("save: %d %s", rr.Code, rr.Body.String())
		}
	}
	close(stop)
	wg.Wait()
}

func TestLoadIndexPicksLatestOrGivenTimestamp(t *testing.T) {
	h := newSavedHTTP(t)
	write := func(ts, name string) {
		meta := `{"indexName":"products","indexFields":["name"],"resultCount":5}`
		docs := `[{"id":"1","name":"` + name + `"}]`
		_ = os.WriteFile(filepath.Join(h.DataDir, "index_products_"+ts+".json"), []byte(meta), 0o644)
		_ = os.WriteFile(filepath.Join(h.DataDir, "documents_products_"+ts+".json"), []byte(docs), 0o644)
	}
	write("20260101000000", "old")
	write("20260202000000", "new")

	for query, want := range map[string]string{"": "new", "&timestamp=20260101000000": "old"} {
		h2 := NewHTTP()
		h2.DataDir = h.DataDir
		if rr := post(h2.LoadIndex, "/load-index?indexName=products"+query); rr.Code != http.StatusOK {
			t.Fatalf("%q: %d %s", query, rr.Code, rr.Body.String())
		}
		if res, _ := h2.engines["products"].Search(t.Context(), want, nil); len(res.Docs) != 1 {
			t.Errorf("%q: expected a hit for %q", query, want)
		}
	}
}

func TestSaveLoadIndexErrors(t *testing.T) {
	h := newSavedHTTP(t)

	cases := []struct {
		name   string
		fn     http.HandlerFunc
		target string
		want   int
	}{
		{"save missing name", h.SaveIndex, "/save-index", 400},
		{"save dot name", h.SaveIndex, "/save-index?indexName=..", 400},
		{"save unknown index", h.SaveIndex, "/save-index?indexName=nope", 404},
		{"load dot name", h.LoadIndex, "/load-index?indexName=..", 400},
		{"load bad timestamp", h.LoadIndex, "/load-index?indexName=x&timestamp=../..", 400},
		{"load no snapshot", h.LoadIndex, "/load-index?indexName=nope", 404},
		{"load missing timestamp file", h.LoadIndex, "/load-index?indexName=nope&timestamp=20260101000000", 404},
	}
	for _, tc := range cases {
		if rr := post(tc.fn, tc.target); rr.Code != tc.want {
			t.Errorf("%s: got %d want %d (%s)", tc.name, rr.Code, tc.want, rr.Body.String())
		}
	}

	for name, fn := range map[string]http.HandlerFunc{"save": h.SaveIndex, "load": h.LoadIndex} {
		rr := httptest.NewRecorder()
		fn(rr, httptest.NewRequest(http.MethodGet, "/x?indexName=products", nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s GET: got %d want 405", name, rr.Code)
		}
	}
}

func TestLoadIndexRejectsTamperedFiles(t *testing.T) {
	for name, meta := range map[string]string{
		"wrong index name": `{"indexName":"other","indexFields":["name"]}`,
		"no fields":        `{"indexName":"products","indexFields":[]}`,
		"bad json":         `{`,
	} {
		h := NewHTTP()
		h.DataDir = t.TempDir()
		ts := "20260101000000"
		_ = os.WriteFile(filepath.Join(h.DataDir, "index_products_"+ts+".json"), []byte(meta), 0o644)
		_ = os.WriteFile(filepath.Join(h.DataDir, "documents_products_"+ts+".json"), []byte(`[]`), 0o644)
		rr := post(h.LoadIndex, "/load-index?indexName=products")
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d want 422 (%s)", name, rr.Code, rr.Body.String())
		}
		if len(h.engines) != 0 {
			t.Errorf("%s: index must not be registered", name)
		}
	}

	// Documents file missing.
	h := NewHTTP()
	h.DataDir = t.TempDir()
	_ = os.WriteFile(filepath.Join(h.DataDir, "index_products_20260101000000.json"), []byte(`{"indexName":"products","indexFields":["name"]}`), 0o644)
	if rr := post(h.LoadIndex, "/load-index?indexName=products"); rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "documents_products_") {
		t.Errorf("missing documents file: %d %s", rr.Code, rr.Body.String())
	}
}

func TestDeleteIndex(t *testing.T) {
	h := newSavedHTTP(t)
	h.engines["other"] = engine.NewSearchEngine([]string{"name"}, nil, nil, 10)

	del := func(target string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.DeleteIndex(rr, httptest.NewRequest(http.MethodDelete, target, nil))
		return rr
	}
	for target, want := range map[string]int{
		"/delete-index":                    400,
		"/delete-index?indexName=..":       400,
		"/delete-index?indexName=nope":     404,
		"/delete-index?indexName=products": 200,
	} {
		if rr := del(target); rr.Code != want {
			t.Errorf("%s: got %d want %d (%s)", target, rr.Code, want, rr.Body.String())
		}
	}
	if _, ok := h.engines["products"]; ok {
		t.Fatal("index still registered")
	}
	if _, ok := h.engines["other"]; !ok {
		t.Fatal("other index must be untouched")
	}
	if rr := del("/delete-index?indexName=products"); rr.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d", rr.Code)
	}

	rr := httptest.NewRecorder()
	h.DeleteIndex(rr, httptest.NewRequest(http.MethodPost, "/delete-index?indexName=other", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: got %d want 405", rr.Code)
	}
}

// Requests in flight on a deleted index finish; the index can be re-created afterwards.
func TestDeleteIndexWhileBusy(t *testing.T) {
	h := newSavedHTTP(t)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					rr := httptest.NewRecorder()
					h.Search(rr, httptest.NewRequest(http.MethodGet, "/search?index=products&q=apple", nil))
					if rr.Code != http.StatusOK && rr.Code != http.StatusNotFound {
						t.Errorf("search: %d %s", rr.Code, rr.Body.String())
						return
					}
					h.mu.RLock()
					sec := h.engines["products"]
					h.mu.RUnlock()
					if sec != nil {
						_ = sec.AddOrUpdateDocument(map[string]interface{}{"id": "9", "name": "apple"})
					}
				}
			}
		}()
	}
	rr := httptest.NewRecorder()
	h.DeleteIndex(rr, httptest.NewRequest(http.MethodDelete, "/delete-index?indexName=products", nil))
	close(stop)
	wg.Wait()
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
}

func TestSaveLoadEmptyIndex(t *testing.T) {
	h := NewHTTP()
	h.DataDir = t.TempDir()
	h.engines["empty"] = engine.NewSearchEngine([]string{"name"}, nil, nil, 10)
	if rr := post(h.SaveIndex, "/save-index?indexName=empty"); rr.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rr.Code, rr.Body.String())
	}
	h2 := NewHTTP()
	h2.DataDir = h.DataDir
	if rr := post(h2.LoadIndex, "/load-index?indexName=empty"); rr.Code != http.StatusOK {
		t.Fatalf("load: %d %s", rr.Code, rr.Body.String())
	}
	if n := h2.engines["empty"].StoredDocumentCount(); n != 0 {
		t.Fatalf("expected 0 docs, got %d", n)
	}
}

// Documents must come back with the same values, including numbers, arrays,
// nested objects and unicode.
func TestSaveLoadPreservesDocumentContent(t *testing.T) {
	h := NewHTTP()
	h.DataDir = t.TempDir()
	se := engine.NewSearchEngine([]string{"name"}, nil, map[string]bool{"tags": true}, 10)
	want := map[string]interface{}{
		"id": "7", "name": "Çiğ köfte \"quoted\"", "year": float64(2026), "big": float64(1500000),
		"tags": []interface{}{"a", "b"}, "nested": map[string]interface{}{"k": "v"}, "flag": true, "nothing": nil,
	}
	if err := se.AddOrUpdateDocument(want); err != nil {
		t.Fatal(err)
	}
	h.engines["x"] = se
	post(h.SaveIndex, "/save-index?indexName=x")

	h2 := NewHTTP()
	h2.DataDir = h.DataDir
	if rr := post(h2.LoadIndex, "/load-index?indexName=x"); rr.Code != http.StatusOK {
		t.Fatalf("load: %d %s", rr.Code, rr.Body.String())
	}
	got := h2.engines["x"].ExportDocuments()
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("document changed:\n got %v\nwant %v", got, want)
	}
}

func TestSaveIndexFileNameUsesUTC(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("far", 9*3600)
	defer func() { time.Local = old }()

	h := newSavedHTTP(t)
	before := time.Now().UTC().Add(-2 * time.Second)
	rr := post(h.SaveIndex, "/save-index?indexName=products")
	after := time.Now().UTC().Add(2 * time.Second)
	var resp SaveIndexResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	ts := strings.TrimSuffix(strings.TrimPrefix(resp.IndexFile, "index_products_"), ".json")
	got, err := time.Parse(snapshotTimeFormat, ts)
	if err != nil || got.Before(before.Truncate(time.Second)) || got.After(after) {
		t.Fatalf("timestamp %q (%v, err=%v) is not current UTC", ts, got, err)
	}
	if resp.DocumentsFile != "documents_products_"+ts+".json" || resp.SavedDocs != 2 {
		t.Fatalf("unexpected response %+v", resp)
	}
}

func TestSaveIndexWriteFailureReturns500(t *testing.T) {
	h := newSavedHTTP(t)
	blocker := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(blocker, []byte("x"), 0o644)
	h.DataDir = blocker // a file, so MkdirAll / create fails
	if rr := post(h.SaveIndex, "/save-index?indexName=products"); rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestWriteFileAtomicLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	boom := errors.New("boom")
	err := writeFileAtomic(path, func(w io.Writer) error { _, _ = w.Write([]byte("partial")); return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("failed write left files behind: %v", left)
	}

	if err := writeFileAtomic(path, func(w io.Writer) error { _, err := w.Write([]byte("ok")); return err }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "ok" {
		t.Fatalf("got %q", b)
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Fatalf("temp file not cleaned up: %v", left)
	}
}

func TestSaveLoadWorksWithGlobCharsInDataDir(t *testing.T) {
	h := newSavedHTTP(t)
	h.DataDir = filepath.Join(t.TempDir(), "data[1]*?")
	if rr := post(h.SaveIndex, "/save-index?indexName=products"); rr.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rr.Code, rr.Body.String())
	}
	h2 := NewHTTP()
	h2.DataDir = h.DataDir
	if rr := post(h2.LoadIndex, "/load-index?indexName=products"); rr.Code != http.StatusOK {
		t.Fatalf("load: %d %s", rr.Code, rr.Body.String())
	}
}

// Snapshots of "a" must not be picked up when loading "a_b" and vice versa.
func TestLoadIndexIgnoresSimilarlyNamedIndexes(t *testing.T) {
	h := NewHTTP()
	h.DataDir = t.TempDir()
	for _, name := range []string{"a", "a_b", "a.b"} {
		h.engines[name] = engine.NewSearchEngine([]string{"name"}, nil, nil, 10)
		_ = h.engines[name].AddOrUpdateDocument(map[string]interface{}{"id": "1", "name": "doc " + name})
		post(h.SaveIndex, "/save-index?indexName="+name)
	}
	for _, name := range []string{"a", "a_b", "a.b"} {
		h2 := NewHTTP()
		h2.DataDir = h.DataDir
		if rr := post(h2.LoadIndex, "/load-index?indexName="+name); rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, rr.Code, rr.Body.String())
		}
		docs := h2.engines[name].ExportDocuments()
		if len(docs) != 1 || docs[0]["name"] != "doc "+name {
			t.Fatalf("%s loaded the wrong snapshot: %v", name, docs)
		}
	}

	// Only a similarly named snapshot exists: "a" must not fall back to "a_b".
	only := NewHTTP()
	only.DataDir = t.TempDir()
	only.engines["a_b"] = engine.NewSearchEngine([]string{"name"}, nil, nil, 10)
	post(only.SaveIndex, "/save-index?indexName=a_b")
	if rr := post(only.LoadIndex, "/load-index?indexName=a"); rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestLoadIndexRejectsBadDocumentsFile(t *testing.T) {
	h := NewHTTP()
	h.DataDir = t.TempDir()
	ts := "20260101000000"
	_ = os.WriteFile(filepath.Join(h.DataDir, "index_p_"+ts+".json"), []byte(`{"indexName":"p","indexFields":["name"]}`), 0o644)
	_ = os.WriteFile(filepath.Join(h.DataDir, "documents_p_"+ts+".json"), []byte(`{"not":"an array"}`), 0o644)
	if rr := post(h.LoadIndex, "/load-index?indexName=p"); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d %s", rr.Code, rr.Body.String())
	}
	if len(h.engines) != 0 {
		t.Fatal("index must not be registered")
	}
}

// Two loads of the same name race: exactly one wins, the other gets 409.
func TestConcurrentLoadsOfSameIndex(t *testing.T) {
	h := newSavedHTTP(t)
	post(h.SaveIndex, "/save-index?indexName=products")

	h2 := NewHTTP()
	h2.DataDir = h.DataDir
	codes := make(chan int, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- post(h2.LoadIndex, "/load-index?indexName=products").Code }()
	}
	wg.Wait()
	close(codes)
	ok, conflict := 0, 0
	for c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if ok != 1 || conflict != 7 {
		t.Fatalf("ok=%d conflict=%d", ok, conflict)
	}
}

// While a big index loads, other indexes keep answering and the loading one is not visible yet.
func TestLoadIndexDoesNotBlockOtherIndexes(t *testing.T) {
	h := NewHTTP()
	h.DataDir = t.TempDir()
	big := engine.NewSearchEngine([]string{"name"}, nil, nil, 10)
	docs := make([]map[string]interface{}, 20000)
	for i := range docs {
		docs[i] = map[string]interface{}{"id": i, "name": "word" + strings.Repeat("x", i%7) + " common"}
	}
	big.Index(docs)
	h.engines["big"] = big
	post(h.SaveIndex, "/save-index?indexName=big")
	delete(h.engines, "big")
	h.engines["live"] = newSavedHTTP(t).engines["products"]

	done := make(chan struct{})
	go func() { defer close(done); post(h.LoadIndex, "/load-index?indexName=big") }()

	served := 0
loop:
	for {
		select {
		case <-done:
			break loop
		default:
			rr := httptest.NewRecorder()
			h.Search(rr, httptest.NewRequest(http.MethodGet, "/search?index=live&q=apple", nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("live index search failed during load: %d", rr.Code)
			}
			// "big" is either absent (404) or fully loaded: never partial.
			rr = httptest.NewRecorder()
			h.Search(rr, httptest.NewRequest(http.MethodGet, "/search?index=big&q=common", nil))
			if rr.Code == http.StatusOK && !strings.Contains(rr.Body.String(), `"Docs":[{`) {
				t.Fatalf("big index visible but empty: %s", rr.Body.String())
			}
			served++
		}
	}
	if served == 0 {
		t.Log("load finished before any search ran (fast machine); race check still ran under -race")
	}
	if n := h.engines["big"].StoredDocumentCount(); n != 20000 {
		t.Fatalf("loaded %d docs, want 20000", n)
	}
}

func TestDeleteThenRecreateOrReload(t *testing.T) {
	h := newSavedHTTP(t)
	post(h.SaveIndex, "/save-index?indexName=products")

	rr := httptest.NewRecorder()
	h.DeleteIndex(rr, httptest.NewRequest(http.MethodDelete, "/delete-index?indexName=products", nil))
	if rr.Code != http.StatusOK {
		t.Fatal(rr.Body.String())
	}
	var resp DeleteIndexResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.DeletedDocs != 2 || resp.IndexName != "products" {
		t.Fatalf("unexpected response %+v", resp)
	}

	// Name is free again: create works, then delete, then load from disk works.
	rr = httptest.NewRecorder()
	h.CreateIndex(rr, httptest.NewRequest(http.MethodPost, "/create-index", strings.NewReader(`{"indexName":"products","indexFields":["name"]}`)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("recreate: %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.DeleteIndex(rr, httptest.NewRequest(http.MethodDelete, "/delete-index?indexName=products", nil))
	if rr := post(h.LoadIndex, "/load-index?indexName=products"); rr.Code != http.StatusOK {
		t.Fatalf("reload: %d %s", rr.Code, rr.Body.String())
	}
	if h.engines["products"].StoredDocumentCount() != 2 {
		t.Fatal("reloaded index lost documents")
	}
}
