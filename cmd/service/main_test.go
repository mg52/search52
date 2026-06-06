package main

import (
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
		{
			name:   "save index",
			method: http.MethodPost,
			path:   "/api/save-controller",
			body:   `{}`,
		},
		{
			name:   "load index",
			method: http.MethodPost,
			path:   "/api/load-controller",
			body:   `{}`,
		},
		{
			name:   "compact index",
			method: http.MethodPost,
			path:   "/api/compact-index",
			body:   `{}`,
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
