package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mg52/search52/internal/handler"
)

func TestAdminMuxRequiresAdminKeyForMutations(t *testing.T) {
	t.Setenv("ADMINKEY", "secret")
	mux := newAdminMux(handler.NewHTTP())

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name:   "create index",
			method: http.MethodPost,
			path:   "/api/create-index",
			body:   `{}`,
		},
		{
			name:   "add document",
			method: http.MethodPost,
			path:   "/api/document?indexName=products",
			body:   `{}`,
		},
		{
			name:   "delete document",
			method: http.MethodDelete,
			path:   "/api/document?indexName=products&id=1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" missing key", func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})

		t.Run(tt.name+" wrong key", func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Admin-Key", "wrong")
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestAdminMuxAllowsPublicRoutesWithoutAdminKey(t *testing.T) {
	t.Setenv("ADMINKEY", "secret")
	mux := newAdminMux(handler.NewHTTP())

	for _, path := range []string{"/api/health", "/api/list-indexes"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
		})
	}
}

func TestAdminMuxRejectsAllWhenAdminKeyUnset(t *testing.T) {
	t.Setenv("ADMINKEY", "")
	mux := newAdminMux(handler.NewHTTP())

	req := httptest.NewRequest(http.MethodPost, "/api/create-index", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	// Provide a key anyway — server must still reject because ADMINKEY is unset.
	req.Header.Set("X-Admin-Key", "anything")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when ADMINKEY env var is unset, got %d", rec.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "ADMINKEY") {
		t.Fatalf("expected error message to mention ADMINKEY, got %q", errMsg)
	}
}

func TestAdminMuxAcceptsCorrectAdminKey(t *testing.T) {
	t.Setenv("ADMINKEY", "secret")
	mux := newAdminMux(handler.NewHTTP())

	req := httptest.NewRequest(http.MethodPost, "/api/create-index", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Key", "secret")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("status = %d, expected request to pass admin key check", rec.Code)
	}
}

// Persistence, compaction and prefix-maintenance endpoints were removed; they
// must not come back by accident.
func TestRemovedRoutesReturn404(t *testing.T) {
	t.Setenv("ADMINKEY", "secret")
	ht := handler.NewHTTP()
	muxes := map[string]http.Handler{"api": newAPIMux(ht), "admin": newAdminMux(ht)}
	prefixes := map[string]string{"api": "", "admin": "/api"}

	for name, mux := range muxes {
		for _, route := range []string{"/save-controller", "/load-controller", "/compact-index", "/update-prefix", "/update-prefix-and-fuzzy"} {
			req := httptest.NewRequest(http.MethodPost, prefixes[name]+route, strings.NewReader(`{"indexName":"x"}`))
			req.Header.Set("X-Admin-Key", "secret")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s mux %s: status = %d, want 404", name, route, rec.Code)
			}
		}
	}
}

func TestAPIMuxRequiresAdminKeyAndAllowsPublicRoutes(t *testing.T) {
	t.Setenv("ADMINKEY", "secret")
	mux := newAPIMux(handler.NewHTTP())

	for _, path := range []string{"/create-index", "/add-to-index?indexName=x", "/document?indexName=x"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s without key: status = %d, want 401", path, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/create-index", strings.NewReader(`{}`))
	req.Header.Set("X-Admin-Key", "wrong")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong key: status = %d, want 401", rec.Code)
	}

	for _, path := range []string{"/health", "/list-indexes"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", path, rec.Code)
		}
	}
}

func TestDocumentHandlerRejectsOtherMethods(t *testing.T) {
	t.Setenv("ADMINKEY", "secret")
	mux := newAPIMux(handler.NewHTTP())
	req := httptest.NewRequest(http.MethodGet, "/document?indexName=x", nil)
	req.Header.Set("X-Admin-Key", "secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "POST, DELETE" {
		t.Fatalf("Allow = %q, want %q", got, "POST, DELETE")
	}
}

func TestAdminPage(t *testing.T) {
	mux := newAdminMux(handler.NewHTTP())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("GET /: status = %d, body has html = %v", rec.Code, strings.Contains(rec.Body.String(), "<html"))
	}
	for _, removed := range []string{"save-controller", "load-controller", "compact-index", "update-prefix"} {
		if strings.Contains(rec.Body.String(), removed) {
			t.Errorf("admin page still references removed route %q", removed)
		}
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nope: status = %d, want 404", rec.Code)
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("SEARCH52_TEST_VALUE", "")
	if got := getenv("SEARCH52_TEST_VALUE", "fallback"); got != "fallback" {
		t.Fatalf("empty env should fall back, got %q", got)
	}
	t.Setenv("SEARCH52_TEST_VALUE", "set")
	if got := getenv("SEARCH52_TEST_VALUE", "fallback"); got != "set" {
		t.Fatalf("got %q, want set", got)
	}
}
