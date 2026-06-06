package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"strings"
)

// Default word source:
// https://raw.githubusercontent.com/wordnik/wordlist/main/wordlist-20210729.txt
// Wordnik wordlist repository: https://github.com/wordnik/wordlist
// License: MIT
//
// Example:
// go run ./scripts/generate_products.go \
//  -words /private/tmp/wordnik-wordlist-20210729.txt \
//  -exclude-file excluded_product_words.txt \
//  -out products_1m.json \
//  -count 1000000 \
//  -seed 52

var alphaWord = regexp.MustCompile(`^[a-z]+$`)

var categories = []string{
	"accessories",
	"appliances",
	"art",
	"automotive",
	"baby",
	"bags",
	"bakery",
	"bath",
	"bedding",
	"beverages",
	"bicycles",
	"camping",
	"cameras",
	"candles",
	"cleaning",
	"coffee",
	"collectibles",
	"computers",
	"crafts",
	"dairy",
	"decor",
	"dining",
	"drinks",
	"education",
	"events",
	"audio",
	"beauty",
	"books",
	"clothing",
	"electronics",
	"fitness",
	"garden",
	"grocery",
	"home",
	"kitchen",
	"office",
	"outdoor",
	"pets",
	"shoes",
	"sports",
	"tools",
	"toys",
	"travel",
	"eyewear",
	"furniture",
	"gaming",
	"gifts",
	"grilling",
	"hardware",
	"health",
	"hobbies",
	"industrial",
	"jewelry",
	"lighting",
	"luggage",
	"magazines",
	"medical",
	"mobile",
	"movies",
	"music",
	"office-supplies",
	"outdoor-living",
	"pantry",
	"party",
	"personal-care",
	"photo",
	"plants",
	"plumbing",
	"produce",
	"school",
	"security",
	"software",
	"storage",
	"tabletop",
	"tea",
	"video",
	"watches",
	"wellness",
	"wine",
	"workwear",
	"yard",
	"yoga",
	"bedroom",
	"breakfast",
	"car-care",
	"cat-supplies",
	"dog-supplies",
	"desk",
	"flooring",
	"fragrance",
	"freezer",
	"laundry",
	"meat",
	"outdoor-sports",
	"paper",
	"printer",
	"seafood",
	"smart-home",
	"snacks",
	"stationery",
}

type product struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Category    []string `json:"category"`
	Description string   `json:"description"`
}

func main() {
	wordsPath := flag.String("words", "/private/tmp/wordnik-wordlist-20210729.txt", "newline-delimited word list")
	excludeWords := flag.String("exclude", "", "comma-separated words to exclude from generated names and descriptions")
	excludePath := flag.String("exclude-file", "", "newline-delimited file of words to exclude from generated names and descriptions")
	outPath := flag.String("out", "products_1m.json", "output JSON array path")
	count := flag.Int("count", 1_000_000, "number of products to generate")
	seed := flag.Int64("seed", 52, "random seed")
	flag.Parse()

	excluded, err := loadExcludedWords(*excludeWords, *excludePath)
	if err != nil {
		fatal(err)
	}

	words, err := loadWords(*wordsPath, excluded)
	if err != nil {
		fatal(err)
	}
	if len(words) == 0 {
		fatal(fmt.Errorf("no usable words found in %s", *wordsPath))
	}

	out, err := os.Create(*outPath)
	if err != nil {
		fatal(err)
	}
	defer out.Close()

	w := bufio.NewWriterSize(out, 1<<20)
	defer w.Flush()

	r := rand.New(rand.NewSource(*seed))
	enc := json.NewEncoder(w)

	if _, err := w.WriteString("[\n"); err != nil {
		fatal(err)
	}
	for i := 0; i < *count; i++ {
		p := product{
			ID:          fmt.Sprintf("p%d", i+1),
			Name:        phrase(r, words, 1, 8),
			Category:    sampleCategories(r, 1+r.Intn(5)),
			Description: phrase(r, words, 5, 30),
		}
		if i > 0 {
			if _, err := w.WriteString(",\n"); err != nil {
				fatal(err)
			}
		}
		if err := enc.Encode(p); err != nil {
			fatal(err)
		}
	}
	if _, err := w.WriteString("]\n"); err != nil {
		fatal(err)
	}

	fmt.Printf("wrote %d products to %s using %d words\n", *count, *outPath, len(words))
}

func loadWords(path string, excluded map[string]struct{}) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	seen := make(map[string]struct{})
	words := make([]string, 0, 100_000)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		word := strings.ToLower(strings.TrimSpace(scanner.Text()))
		word = strings.Trim(word, `"`)
		if len(word) < 3 || len(word) > 14 || !alphaWord.MatchString(word) {
			continue
		}
		if _, ok := excluded[word]; ok {
			continue
		}
		if _, ok := seen[word]; ok {
			continue
		}
		seen[word] = struct{}{}
		words = append(words, word)
	}
	return words, scanner.Err()
}

func loadExcludedWords(inline, path string) (map[string]struct{}, error) {
	excluded := make(map[string]struct{})
	add := func(word string) {
		word = strings.ToLower(strings.TrimSpace(word))
		word = strings.Trim(word, `"`)
		if word != "" && !strings.HasPrefix(word, "#") {
			excluded[word] = struct{}{}
		}
	}

	for _, word := range strings.Split(inline, ",") {
		add(word)
	}
	if path == "" {
		return excluded, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		add(scanner.Text())
	}
	return excluded, scanner.Err()
}

func phrase(r *rand.Rand, words []string, minWords, maxWords int) string {
	n := minWords + r.Intn(maxWords-minWords+1)
	parts := make([]string, n)
	for i := range parts {
		parts[i] = words[r.Intn(len(words))]
	}
	return strings.Join(parts, " ")
}

func sampleCategories(r *rand.Rand, n int) []string {
	picked := make([]string, 0, n)
	used := make(map[string]struct{}, n)
	for len(picked) < n {
		category := categories[r.Intn(len(categories))]
		if _, ok := used[category]; ok {
			continue
		}
		used[category] = struct{}{}
		picked = append(picked, category)
	}
	return picked
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
