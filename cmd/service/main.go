package main

import (
	"context"
	"embed"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mg52/search52/internal/handler"
)

//go:embed admin.html
var adminFiles embed.FS

func main() {
	ht := handler.NewHTTP()

	apiAddr := getenv("SEARCH52_API_ADDR", ":8080")
	adminAddr := getenv("SEARCH52_ADMIN_ADDR", ":8081")

	apiSrv := &http.Server{
		Addr:              apiAddr,
		Handler:           newAPIMux(ht),
		ReadHeaderTimeout: 10 * time.Second,
	}
	adminSrv := &http.Server{
		Addr:              adminAddr,
		Handler:           newAdminMux(ht),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		log.Printf("Starting API server on %s", apiAddr)
		errCh <- apiSrv.ListenAndServe()
	}()
	go func() {
		log.Printf("Starting admin UI on %s", adminAddr)
		errCh <- adminSrv.ListenAndServe()
	}()

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-stopCh:
		log.Printf("Received %s, shutting down", sig)
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Server failed: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = apiSrv.Shutdown(ctx)
	_ = adminSrv.Shutdown(ctx)
}

func newAPIMux(ht *handler.HTTP) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/create-index", ht.CreateIndex)
	mux.HandleFunc("/search", ht.Search)
	mux.HandleFunc("/add-to-index", ht.AddToIndex)
	mux.HandleFunc("/list-indexes", ht.ListIndexes)
	mux.HandleFunc("/save-controller", ht.SaveEngine)
	mux.HandleFunc("/load-controller", ht.LoadEngine)
	mux.HandleFunc("/compact-index", ht.CompactIndex)
	mux.HandleFunc("/health", ht.Health)
	mux.HandleFunc("/document", documentHandler(ht))
	return mux
}

func newAdminMux(ht *handler.HTTP) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", adminPage)
	mux.HandleFunc("/api/create-index", ht.CreateIndex)
	mux.HandleFunc("/api/search", ht.Search)
	mux.HandleFunc("/api/add-to-index", ht.AddToIndex)
	mux.HandleFunc("/api/list-indexes", ht.ListIndexes)
	mux.HandleFunc("/api/save-controller", ht.SaveEngine)
	mux.HandleFunc("/api/load-controller", ht.LoadEngine)
	mux.HandleFunc("/api/compact-index", ht.CompactIndex)
	mux.HandleFunc("/api/document", documentHandler(ht))
	return mux
}

func documentHandler(ht *handler.HTTP) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			ht.AddOrUpdateDocument(w, r)
		case http.MethodDelete:
			ht.DeleteDocument(w, r)
		default:
			w.Header().Set("Allow", "POST, DELETE")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"status":"error","statusCode":405,"error":"method not allowed"}`))
		}
	}
}

func getenv(name, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}

func adminPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html, err := adminFiles.ReadFile("admin.html")
	if err != nil {
		http.Error(w, "admin UI not found", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(html)
}
