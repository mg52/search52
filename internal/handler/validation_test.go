package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mg52/search52/internal/engine"
)

func TestValidateIdentifier(t *testing.T) {
	tests := []struct {
		value   string
		wantErr bool
	}{
		{"products", false},
		{"my.index", false},
		{"v1.0", false},
		{"..a", false},
		{"a_b-c", false},
		{"", true},
		{".", true},
		{"..", true},
		{"...", true},
		{"a/b", true},
		{"../x", true},
		{"a b", true},
		{"ü", true},
		{strings.Repeat("a", maxIndexNameLength), false},
		{strings.Repeat("a", maxIndexNameLength+1), true},
	}
	for _, tt := range tests {
		err := validateIdentifier("indexName", tt.value, maxIndexNameLength)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateIdentifier(%q) err=%v, wantErr=%v", tt.value, err, tt.wantErr)
		}
	}
}

// Dot-only index names must be rejected by every endpoint that takes one.
func TestDotOnlyIndexNamesRejectedEverywhere(t *testing.T) {
	for _, name := range []string{".", ".."} {
		h := NewHTTP()

		t.Run("create-index "+name, func(t *testing.T) {
			body := `{"indexName":"` + name + `","indexFields":["name"]}`
			rr := httptest.NewRecorder()
			h.CreateIndex(rr, httptest.NewRequest(http.MethodPost, "/create-index", strings.NewReader(body)))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
			}
			if len(h.engines) != 0 {
				t.Fatalf("index %q must not be created", name)
			}
		})

		t.Run("search "+name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.Search(rr, httptest.NewRequest(http.MethodGet, "/search?q=x&index="+name, nil))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rr.Code)
			}
		})

		t.Run("add-to-index "+name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.AddToIndex(rr, httptest.NewRequest(http.MethodPost, "/add-to-index?indexName="+name, nil))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rr.Code)
			}
		})

		t.Run("add document "+name, func(t *testing.T) {
			body := `{"document":{"id":"1"}}`
			rr := httptest.NewRecorder()
			h.AddOrUpdateDocument(rr, httptest.NewRequest(http.MethodPost, "/document?indexName="+name, strings.NewReader(body)))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rr.Code)
			}
		})

		t.Run("delete document "+name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.DeleteDocument(rr, httptest.NewRequest(http.MethodDelete, "/document?id=1&indexName="+name, nil))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rr.Code)
			}
		})
	}
}

func TestCreateIndexRejectsDotOnlyFieldNames(t *testing.T) {
	h := NewHTTP()
	for _, body := range []string{
		`{"indexName":"idx","indexFields":[".."]}`,
		`{"indexName":"idx","indexFields":["name"],"filters":[".."]}`,
	} {
		rr := httptest.NewRecorder()
		h.CreateIndex(rr, httptest.NewRequest(http.MethodPost, "/create-index", strings.NewReader(body)))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body %s: expected 400, got %d", body, rr.Code)
		}
	}
}

func TestValidateFieldList(t *testing.T) {
	tooMany := make([]string, maxFilters+1)
	for i := range tooMany {
		tooMany[i] = "f" + strings.Repeat("x", i)
	}
	tests := []struct {
		name     string
		fields   []string
		required bool
		wantErr  bool
	}{
		{"ok", []string{"a", "b"}, true, false},
		{"empty not required", nil, false, false},
		{"empty required", nil, true, true},
		{"duplicate", []string{"a", "a"}, false, true},
		{"bad identifier", []string{"a b"}, false, true},
		{"too many", tooMany, false, true},
	}
	for _, tt := range tests {
		err := validateFieldList("filters", tt.fields, tt.required, maxFilters)
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: err=%v, wantErr=%v", tt.name, err, tt.wantErr)
		}
	}
}

func TestValidateCreateIndexRequest(t *testing.T) {
	base := func() CreateIndexRequest {
		return CreateIndexRequest{IndexName: "idx", IndexFields: []string{"name", "tags"}}
	}

	req := base()
	if err := validateCreateIndexRequest(&req); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if req.ResultCount != 100 {
		t.Fatalf("resultCount should default to 100, got %d", req.ResultCount)
	}

	bad := map[string]func(*CreateIndexRequest){
		"missing name":          func(r *CreateIndexRequest) { r.IndexName = "" },
		"no index fields":       func(r *CreateIndexRequest) { r.IndexFields = nil },
		"negative resultCount":  func(r *CreateIndexRequest) { r.ResultCount = -1 },
		"resultCount too large": func(r *CreateIndexRequest) { r.ResultCount = maxResultCount + 1 },
		"weight for non-field":  func(r *CreateIndexRequest) { r.FieldWeights = map[string]int{"other": 2} },
		"zero weight":           func(r *CreateIndexRequest) { r.FieldWeights = map[string]int{"name": 0} },
		"weight too large":      func(r *CreateIndexRequest) { r.FieldWeights = map[string]int{"name": maxFieldWeight + 1} },
		"bad weight field name": func(r *CreateIndexRequest) { r.FieldWeights = map[string]int{"..": 1} },
	}
	for name, mutate := range bad {
		r := base()
		mutate(&r)
		if err := validateCreateIndexRequest(&r); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseAndValidateSearchFilters(t *testing.T) {
	allowed := map[string]bool{"year": true, "tag": true}

	got, err := parseAndValidateSearchFilters("", allowed)
	if err != nil || got != nil {
		t.Fatalf("empty filter: got=%v err=%v", got, err)
	}

	got, err = parseAndValidateSearchFilters("year:2020, year:2021 ,tag:a", allowed)
	if err != nil {
		t.Fatalf("valid filter rejected: %v", err)
	}
	if len(got["year"]) != 2 || got["year"][0] != "2020" || got["year"][1] != "2021" || len(got["tag"]) != 1 || got["tag"][0] != "a" {
		t.Fatalf("unexpected parse result: %v", got)
	}

	tooManyValues := strings.TrimSuffix(strings.Repeat("year:1,", maxFilterValues+1), ",")
	bad := map[string]string{
		"no colon":        "year",
		"empty value":     "year:",
		"empty field":     ":2020",
		"unknown field":   "category:books",
		"dot-only field":  "..:1",
		"value too long":  "year:" + strings.Repeat("9", maxFilterValueLength+1),
		"string too long": "year:" + strings.Repeat("9", maxFilterStringLen),
		"too many values": tooManyValues,
	}
	for name, raw := range bad {
		if _, err := parseAndValidateSearchFilters(raw, allowed); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestValidateDocument(t *testing.T) {
	tooManyFields := map[string]interface{}{"id": "1"}
	for i := 0; i <= maxDocumentFields; i++ {
		tooManyFields["f"+strings.Repeat("x", i)] = 1
	}

	ok := []map[string]interface{}{
		{"id": "1"},
		{"id": float64(7)}, // JSON numbers decode as float64
	}
	for _, doc := range ok {
		if err := validateDocument(doc); err != nil {
			t.Errorf("validateDocument(%v) rejected: %v", doc, err)
		}
	}

	bad := map[string]map[string]interface{}{
		"missing id":    {"name": "x"},
		"nil id":        {"id": nil},
		"empty id":      {"id": ""},
		"id too long":   {"id": strings.Repeat("a", maxDocumentIDLength+1)},
		"too many keys": tooManyFields,
	}
	for name, doc := range bad {
		if err := validateDocument(doc); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestHealthHandler_MethodNotAllowed(t *testing.T) {
	rr := httptest.NewRecorder()
	NewHTTP().Health(rr, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
	if rr.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("expected Allow: GET, got %q", rr.Header().Get("Allow"))
	}
}

func TestSearchHandler_CanceledRequestReturns504(t *testing.T) {
	h := NewHTTP()
	se := engine.NewSearchEngine([]string{"name"}, nil, nil, 10)
	if err := se.AddOrUpdateDocument(map[string]interface{}{"id": "1", "name": "apple"}); err != nil {
		t.Fatal(err)
	}
	h.engines["idx"] = se

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/search?index=idx&q=apple", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Search(rr, req)
	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d body=%s", rr.Code, rr.Body.String())
	}
}
