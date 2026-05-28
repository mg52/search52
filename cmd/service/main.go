package main

import (
	"log"
	"net/http"

	"github.com/mg52/search52/internal/handler"
)

func main() {
	ht := handler.NewHTTP()

	mux := http.NewServeMux()
	mux.HandleFunc("/create-index", ht.CreateIndex)
	mux.HandleFunc("/search", ht.Search)
	mux.HandleFunc("/add-to-index", ht.AddToIndex)
	mux.HandleFunc("/save-controller", ht.SaveEngine)
	mux.HandleFunc("/load-controller", ht.LoadEngine)
	mux.HandleFunc("/health", ht.Health)
	mux.HandleFunc("/document", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			ht.AddOrUpdateDocument(w, r)
		case http.MethodDelete:
			ht.DeleteDocument(w, r)
		default:
			w.Header().Set("Allow", "POST, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	addr := ":8080"
	log.Printf("Starting server on %s…", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
