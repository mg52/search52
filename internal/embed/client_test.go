package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func embedServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/embeddings", handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// -------------------- Embed (public API, end-to-end through retry+postJSON) --------------------

func TestEmbed_Success(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotContentType string
	var gotBody map[string]any
	srv := embedServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float64{1, 2, 3}}},
		})
	})

	c := NewEmbeddingClient(srv.URL, "secret", "my-model")
	got, err := c.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("Embed = %v, want [1 2 3]", got)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/embeddings" {
		t.Errorf("path = %q, want /embeddings", gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret")
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody["input"] != "hello world" {
		t.Errorf("body input = %v, want %q", gotBody["input"], "hello world")
	}
	if gotBody["model"] != "my-model" {
		t.Errorf("body model = %v, want %q", gotBody["model"], "my-model")
	}
}

func TestEmbed_NoAuthHeaderWhenAPIKeyEmpty(t *testing.T) {
	sawAuthHeader := false
	srv := embedServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuthHeader = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float64{1}}},
		})
	})

	c := NewEmbeddingClient(srv.URL, "", "m")
	if _, err := c.Embed(context.Background(), "x"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if sawAuthHeader {
		t.Fatal("Authorization header must be omitted when apiKey is empty (e.g. local Ollama)")
	}
}

func TestEmbed_EmptyDataReturnsError(t *testing.T) {
	srv := embedServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	})

	c := NewEmbeddingClient(srv.URL, "k", "m")
	_, err := c.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error for empty data")
	}
}

func TestEmbed_RetriesOnceThenSucceeds(t *testing.T) {
	var calls int32
	srv := embedServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float64{7}}},
		})
	})

	c := NewEmbeddingClient(srv.URL, "k", "m")
	got, err := c.Embed(context.Background(), "x")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 1 || got[0] != 7 {
		t.Errorf("Embed = %v, want [7]", got)
	}
	if n := atomic.LoadInt32(&calls); n < 2 {
		t.Errorf("expected at least 2 calls (one retry), got %d", n)
	}
}

func TestEmbed_ContextCanceledBeforeCall(t *testing.T) {
	srv := embedServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	c := NewEmbeddingClient(srv.URL, "k", "m")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled before the call; retry backoff must observe it and return promptly

	start := time.Now()
	_, err := c.Embed(ctx, "x")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error with a canceled context")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Embed took %v with a pre-canceled context; want a fast failure, not a wait through backoff", elapsed)
	}
}

// -------------------- NewEmbeddingClientFromEnv --------------------

func TestNewEmbeddingClientFromEnv_MissingVars(t *testing.T) {
	t.Setenv("SEARCH52_EMBEDDING_BASE_URL", "")
	t.Setenv("SEARCH52_EMBEDDING_MODEL", "")

	if _, err := NewEmbeddingClientFromEnv(); err == nil {
		t.Fatal("expected error when both env vars are missing")
	}

	t.Setenv("SEARCH52_EMBEDDING_BASE_URL", "http://example.invalid")
	if _, err := NewEmbeddingClientFromEnv(); err == nil {
		t.Fatal("expected error when SEARCH52_EMBEDDING_MODEL is missing")
	}
}

func TestNewEmbeddingClientFromEnv_Success(t *testing.T) {
	var gotAuth string
	srv := embedServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float64{9}}},
		})
	})

	t.Setenv("SEARCH52_EMBEDDING_BASE_URL", srv.URL)
	t.Setenv("SEARCH52_EMBEDDING_MODEL", "env-model")
	t.Setenv("SEARCH52_EMBEDDING_API_KEY", "env-key")

	c, err := NewEmbeddingClientFromEnv()
	if err != nil {
		t.Fatalf("NewEmbeddingClientFromEnv: %v", err)
	}
	got, err := c.Embed(context.Background(), "x")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 1 || got[0] != 9 {
		t.Errorf("Embed = %v, want [9]", got)
	}
	if gotAuth != "Bearer env-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer env-key")
	}
}

func TestNewEmbeddingClientFromEnv_APIKeyOptional(t *testing.T) {
	srv := embedServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float64{1}}},
		})
	})

	t.Setenv("SEARCH52_EMBEDDING_BASE_URL", srv.URL)
	t.Setenv("SEARCH52_EMBEDDING_MODEL", "env-model")
	t.Setenv("SEARCH52_EMBEDDING_API_KEY", "")

	if _, err := NewEmbeddingClientFromEnv(); err != nil {
		t.Fatalf("NewEmbeddingClientFromEnv: %v", err)
	}
}

// -------------------- postJSON (unexported, tested directly to avoid retry's backoff cost) --------------------

func TestPostJSON_Success(t *testing.T) {
	srv := embedServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"embedding": []float64{1, 2}}}})
	})

	var out embeddingResponse
	err := postJSON(context.Background(), srv.Client(), srv.URL+"/embeddings", "k", embeddingRequest{Input: "x", Model: "m"}, &out)
	if err != nil {
		t.Fatalf("postJSON: %v", err)
	}
	if len(out.Data) != 1 || len(out.Data[0].Embedding) != 2 {
		t.Fatalf("decoded = %+v", out)
	}
}

func TestPostJSON_APIErrorIncludesStatusAndBody(t *testing.T) {
	srv := embedServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request details", http.StatusBadRequest)
	})

	var out embeddingResponse
	err := postJSON(context.Background(), srv.Client(), srv.URL+"/embeddings", "k", embeddingRequest{}, &out)
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	msg := err.Error()
	if !strings.Contains(msg, "400") || !strings.Contains(msg, "bad request details") {
		t.Errorf("error = %q, want it to mention status 400 and the response body", msg)
	}
}

func TestPostJSON_MalformedResponseBody(t *testing.T) {
	srv := embedServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	})

	var out embeddingResponse
	if err := postJSON(context.Background(), srv.Client(), srv.URL+"/embeddings", "k", embeddingRequest{}, &out); err == nil {
		t.Fatal("expected a JSON decode error for a malformed response body")
	}
}

func TestPostJSON_NetworkErrorOnUnreachableHost(t *testing.T) {
	var out embeddingResponse
	err := postJSON(context.Background(), http.DefaultClient, "http://127.0.0.1:1/embeddings", "k", embeddingRequest{}, &out)
	if err == nil {
		t.Fatal("expected a network error connecting to a closed port")
	}
}

func TestPostJSON_ContextCanceled(t *testing.T) {
	srv := embedServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"embedding": []float64{1}}}})
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out embeddingResponse
	if err := postJSON(ctx, srv.Client(), srv.URL+"/embeddings", "k", embeddingRequest{}, &out); err == nil {
		t.Fatal("expected an error for a canceled context")
	}
}

// -------------------- retry (unexported, tested directly so cap/backoff/cancellation are precise and fast) --------------------

func TestRetry_SucceedsOnFirstAttempt(t *testing.T) {
	calls := 0
	start := time.Now()
	err := retry(context.Background(), 3, func() error {
		calls++
		return nil
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry needed on immediate success)", calls)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("elapsed = %v, want near-zero when the first attempt succeeds", elapsed)
	}
}

func TestRetry_RetriesThenSucceeds(t *testing.T) {
	calls := 0
	start := time.Now()
	err := retry(context.Background(), 2, func() error {
		calls++
		if calls == 1 {
			return errors.New("transient")
		}
		return nil
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want exactly 2", calls)
	}
	// One backoff of 2^0 = 1s must have elapsed between attempt 1 and 2.
	if elapsed < 900*time.Millisecond {
		t.Errorf("elapsed = %v, want >= ~1s (one real backoff wait)", elapsed)
	}
}

func TestRetry_ExhaustsAttemptsAndReturnsLastError(t *testing.T) {
	calls := 0
	wantErr := errors.New("persistent failure")
	err := retry(context.Background(), 2, func() error {
		calls++
		return wantErr
	})

	if calls != 2 {
		t.Errorf("calls = %d, want exactly 2 (must stop at the attempts cap, not retry forever)", calls)
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want the last attempt's error (%v)", err, wantErr)
	}
}

func TestRetry_ContextCancellationDuringBackoffShortCircuits(t *testing.T) {
	calls := 0
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := retry(ctx, 5, func() error {
		calls++
		return errors.New("always fails")
	})
	elapsed := time.Since(start)

	if calls != 1 {
		t.Errorf("calls = %d, want exactly 1: the context should expire during the first backoff wait, before a 2nd attempt", calls)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %v, want well under the 1s backoff — cancellation must cut the wait short", elapsed)
	}
}

func TestRetry_ZeroAttemptsNeverCallsFn(t *testing.T) {
	calls := 0
	err := retry(context.Background(), 0, func() error {
		calls++
		return errors.New("should not run")
	})
	if calls != 0 {
		t.Errorf("calls = %d, want 0 for attempts=0", calls)
	}
	if err != nil {
		t.Errorf("err = %v, want nil for attempts=0 (current contract: no attempts means no error)", err)
	}
}
