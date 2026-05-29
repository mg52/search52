package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractVocabFromJSONExtractsAllUniqueTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docs.json")
	data := `[
		{"title":"alpha beta", "artist":"gamma", "album":"delta"},
		{"title":"alpha epsilon", "artist":"zeta eta", "album":"theta"}
	]`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := extractVocabFromJSON(path, []string{"title", "artist", "album"})
	if err != nil {
		t.Fatalf("extractVocabFromJSON: %v", err)
	}

	want := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta"}
	if len(got) != len(want) {
		t.Fatalf("expected all unique tokens, got %d words: %v", len(got), got)
	}
	seen := make(map[string]bool, len(got))
	for _, word := range got {
		seen[word] = true
	}
	for _, word := range want {
		if !seen[word] {
			t.Fatalf("missing word %q from %v", word, got)
		}
	}
}
