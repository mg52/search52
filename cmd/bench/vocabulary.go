package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strings"

	"github.com/mg52/search52/internal/engine"
)

func runVocab(args []string) {
	fs := flag.NewFlagSet("vocab", flag.ExitOnError)
	size := fs.Int("size", 100_000, "Number of unique words to generate")
	out := fs.String("out", "vocab.txt", "Output file path")
	seed := fs.Int64("seed", 42, "Random seed")
	data := fs.String("data", "", "Optional JSON document file to extract vocabulary from")
	fields := fs.String("fields", "title,tags", "Comma-separated fields to extract when -data is set")
	_ = fs.Parse(args)

	var (
		words []string
		err   error
	)
	if *data != "" {
		words, err = extractVocabFromJSON(*data, splitCSV(*fields), *size)
		if err != nil {
			fmt.Fprintf(os.Stderr, "extract vocab: %v\n", err)
			os.Exit(1)
		}
	} else {
		words = generateVocab(*size, *seed)
	}
	if err := writeVocab(*out, words); err != nil {
		fmt.Fprintf(os.Stderr, "write vocab: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Vocabulary: %d words → %s\n", len(words), *out)
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func extractVocabFromJSON(path string, fields []string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("size must be positive")
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("at least one field is required")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	dec := json.NewDecoder(bufio.NewReaderSize(f, 16*1024*1024))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("read opening token: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, fmt.Errorf("%s must be a JSON array", path)
	}

	seen := make(map[string]struct{}, limit)
	words := make([]string, 0, limit)
	for dec.More() && len(words) < limit {
		var doc map[string]interface{}
		if err := dec.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode document: %w", err)
		}
		for _, field := range fields {
			value, ok := doc[field]
			if !ok {
				continue
			}
			var tokens []string
			switch v := value.(type) {
			case string:
				tokens = engine.Tokenize(v)
			case []interface{}:
				for _, item := range v {
					if s, ok := item.(string); ok {
						tokens = append(tokens, engine.Tokenize(s)...)
					}
				}
			}
			for _, token := range tokens {
				if _, ok := seen[token]; ok {
					continue
				}
				seen[token] = struct{}{}
				words = append(words, token)
				if len(words) >= limit {
					break
				}
			}
			if len(words) >= limit {
				break
			}
		}
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("no tokens extracted from %s", path)
	}
	return words, nil
}

func generateVocab(n int, seed int64) []string {
	const charset = "abcdefghijklmnopqrstuvwxyz"
	r := rand.New(rand.NewSource(seed))
	seen := make(map[string]struct{}, n)
	words := make([]string, 0, n)
	buf := make([]byte, 12)
	for len(words) < n {
		length := 3 + r.Intn(10)
		for i := 0; i < length; i++ {
			buf[i] = charset[r.Intn(26)]
		}
		w := string(buf[:length])
		if _, exists := seen[w]; !exists {
			seen[w] = struct{}{}
			words = append(words, w)
		}
	}
	return words
}

func writeVocab(path string, words []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	for _, w := range words {
		bw.WriteString(w)
		bw.WriteByte('\n')
	}
	return bw.Flush()
}

func loadVocabFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	var words []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if w := strings.TrimSpace(sc.Text()); w != "" {
			words = append(words, w)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("%s is empty", path)
	}
	return words, nil
}
