// Package engine implements a lightweight, in-memory search engine
// built on an inverted index with prefix map and fuzzy (SymSpell) matching.
// It is concurrency-safe for reads/writes via an internal RWMutex and is
// designed to be persisted/restored via a single gob payload.
package engine

import (
	"encoding/gob"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/mg52/search52/internal/symspell"
)

type Document struct {
	ID    string
	Score int
}

type ReturnedDocument struct {
	ID    string
	Data  map[string]interface{}
	Score int
}

type SearchResult struct {
	Docs []ReturnedDocument
}

// enginePayload is the gob-serializable snapshot of SearchEngine state.
type enginePayload struct {
	Documents          map[uint32]map[string]interface{}
	DocDeleted         map[uint32]bool
	ExternalToInternal map[string]uint32
	InternalToExternal map[uint32]string
	NextInternalID     uint32
	IndexFields        []string
	FieldWeights       map[string]int
	Filters            map[string]bool
	ResultSize         int
}

// SearchEngine maintains an inverted index plus auxiliary structures for
// prefix and fuzzy lookup. It is safe for concurrent use.
type SearchEngine struct {
	// Core index
	DataMap    map[string]map[uint32]int // term -> internalDocID -> postings
	DocDeleted map[uint32]bool           // internalDocID -> deleted?

	// ID mapping: updates create a NEW internalDocID and delete the previous one
	ExternalToInternal map[string]uint32 // externalDocID -> current internalDocID
	InternalToExternal map[uint32]string // internalDocID -> externalDocID
	nextInternalID     uint32

	// Docs + filters
	Documents    map[uint32]map[string]interface{} // internalDocID -> doc fields
	FilterBits   map[string][]uint64               // "field:value" -> bitset of internalDocIDs
	Prefix       map[string][]string               // prefix -> matching terms (capped at MaxPrefixTerms)
	Symspell     *symspell.SymSpell
	IndexFields  []string
	FieldWeights map[string]int
	Filters      map[string]bool
	ResultSize   int

	// termSet is a lock-free set of all indexed terms, used for O(1) existence
	// checks in the search path without touching se.mu.
	termSet sync.Map

	mu sync.RWMutex
}

// NewSearchEngine constructs a new, empty engine ready to index documents.
func NewSearchEngine(indexFields []string, filters map[string]bool, resultSize int) *SearchEngine {
	return NewSearchEngineWithFieldWeights(indexFields, nil, filters, resultSize)
}

// NewSearchEngineWithFieldWeights constructs a new engine with per-index-field
// scoring weights. Missing or non-positive weights default to 1.
func NewSearchEngineWithFieldWeights(indexFields []string, fieldWeights map[string]int, filters map[string]bool, resultSize int) *SearchEngine {
	return &SearchEngine{
		DataMap:            make(map[string]map[uint32]int),
		DocDeleted:         make(map[uint32]bool),
		ExternalToInternal: make(map[string]uint32),
		InternalToExternal: make(map[uint32]string),
		nextInternalID:     1,
		Documents:          make(map[uint32]map[string]interface{}),
		IndexFields:        append([]string(nil), indexFields...),
		FieldWeights:       normalizeFieldWeights(indexFields, fieldWeights),
		Filters:            copyBoolMap(filters),
		Prefix:             make(map[string][]string),
		Symspell:           symspell.NewSymSpell(),
		FilterBits:         make(map[string][]uint64),
		ResultSize:         resultSize,
	}
}

func init() {
	gob.Register(enginePayload{})
	gob.Register(Document{})
	gob.Register(ReturnedDocument{})
	gob.Register(map[string]interface{}{})
	gob.Register([]interface{}{})
	gob.Register(&symspell.SymSpell{})
}

// -------------------- Persistence --------------------

// SaveAll writes the engine snapshot to a single gob file at path + "/engine.gob".
func (se *SearchEngine) SaveAll(path string) error {
	se.mu.RLock()
	defer se.mu.RUnlock()

	payload := enginePayload{
		Documents:          se.Documents,
		DocDeleted:         se.DocDeleted,
		ExternalToInternal: se.ExternalToInternal,
		InternalToExternal: se.InternalToExternal,
		NextInternalID:     se.nextInternalID,
		IndexFields:        se.IndexFields,
		FieldWeights:       se.FieldWeights,
		Filters:            se.Filters,
		ResultSize:         se.ResultSize,
	}

	engineFile := path + "/engine.gob"
	f, err := os.Create(engineFile)
	if err != nil {
		return fmt.Errorf("create %s: %w", engineFile, err)
	}
	defer f.Close()

	enc := gob.NewEncoder(f)
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("encode engine payload: %w", err)
	}
	return nil
}

// LoadAll restores documents + metadata, then rebuilds ALL derived structures
// (DataMap, FilterDocs, Prefix, Symspell) from Documents.
func LoadAll(path string) (*SearchEngine, error) {
	engineFile := path + "/engine.gob"
	f, err := os.Open(engineFile)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", engineFile, err)
	}
	defer f.Close()

	var payload enginePayload
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode engine payload: %w", err)
	}

	// Base engine (derived fields empty; we will rebuild)
	se := &SearchEngine{
		DataMap:            make(map[string]map[uint32]int),
		DocDeleted:         make(map[uint32]bool),
		ExternalToInternal: make(map[string]uint32),
		InternalToExternal: make(map[uint32]string),
		nextInternalID:     1,
		Documents:          make(map[uint32]map[string]interface{}),
		FilterBits:         make(map[string][]uint64),
		Prefix:             make(map[string][]string),
		Symspell:           symspell.NewSymSpell(),
		IndexFields:        nil,
		FieldWeights:       make(map[string]int),
		Filters:            make(map[string]bool),
		ResultSize:         100,
	}

	// Restore docs + metadata
	se.Documents = payload.Documents
	se.DocDeleted = payload.DocDeleted
	se.ExternalToInternal = payload.ExternalToInternal
	se.InternalToExternal = payload.InternalToExternal
	se.nextInternalID = payload.NextInternalID
	se.IndexFields = payload.IndexFields
	se.FieldWeights = normalizeFieldWeights(se.IndexFields, payload.FieldWeights)
	se.Filters = payload.Filters
	se.ResultSize = payload.ResultSize

	// Safety: if NextInternalID missing/zero in older payloads
	if se.nextInternalID == 0 {
		var max uint32
		for id := range se.InternalToExternal {
			if id > max {
				max = id
			}
		}
		se.nextInternalID = max + 1
		if se.nextInternalID == 0 {
			se.nextInternalID = 1
		}
	}

	// -------- Rebuild derived structures from Documents --------
	//
	// We rebuild ONLY current (non-deleted) versions:
	// - If ExternalToInternal points to internalID X, and X is not deleted => index it.
	// - Old internal IDs remain in Documents but are tombstoned => skipped.

	// Helper to check if an internal docID is the current one for its externalID
	isCurrent := func(internalID uint32) bool {
		ext := se.InternalToExternal[internalID]
		if ext == "" {
			return false
		}
		cur, ok := se.ExternalToInternal[ext]
		return ok && cur == internalID
	}

	for internalID, doc := range se.Documents {
		if doc == nil {
			continue
		}
		if se.DocDeleted[internalID] {
			continue
		}
		if !isCurrent(internalID) {
			continue
		}

		localScores := weightedTokenScores(doc, se.IndexFields, se.FieldWeights)
		if len(localScores) == 0 {
			continue
		}

		se.mu.Lock()
		for token, score := range localScores {
			se.indexTokenLocked(token, internalID, score)
		}

		for field := range se.Filters {
			val, ok := doc[field]
			if !ok {
				continue
			}
			var filterKey string
			switch val.(type) {
			case int, int8, int16, int32, int64, float32, float64:
				filterKey = fmt.Sprintf("%s:%v", field, val)
			case string:
				filterKey = fmt.Sprintf("%s:%s", field, val)
			default:
				continue
			}
			se.FilterBits[filterKey] = filterBitSet(se.FilterBits[filterKey], internalID)
		}
		se.mu.Unlock()
	}

	return se, nil
}

// -------------------- Indexing --------------------

// Index performs a full (re)index pass for the provided docs and logs timings.
//
// Steps:
//  1. InsertDocs        — assign internal IDs & store docs (updates create new internalID and delete old)
//  2. BuildDocumentIndex— tokenize/update inverted index & filters
func (se *SearchEngine) Index(docs []map[string]interface{}) {
	slog.Info("InsertDocs starting")
	start := time.Now()
	se.InsertDocs(docs)
	slog.Info("InsertDocs done", "duration", time.Since(start))

	slog.Info("BuildDocumentIndex starting")
	start = time.Now()
	se.BuildDocumentIndex(docs)
	slog.Info("BuildDocumentIndex done", "duration", time.Since(start))
}

// InsertDocs materializes raw documents into the Documents store without reindexing.
func (se *SearchEngine) InsertDocs(docs []map[string]interface{}) {
	for i, doc := range docs {
		if i%100_000 == 0 || i == len(docs)-1 {
			slog.Info("InsertDocs", "doc", i)
		}

		rawID, ok := doc["id"]
		if !ok || rawID == nil {
			continue
		}
		extID := fmt.Sprintf("%v", rawID)
		if extID == "" || extID == "<nil>" {
			continue
		}

		se.mu.Lock()

		// delete previous internal doc if exists
		if oldInternal, exists := se.ExternalToInternal[extID]; exists {
			se.DocDeleted[oldInternal] = true
		}

		// assign new internal id
		internal := se.nextInternalID
		se.nextInternalID++

		se.ExternalToInternal[extID] = internal
		se.InternalToExternal[internal] = extID

		se.Documents[internal] = make(map[string]interface{}, len(doc))
		for k, v := range doc {
			se.Documents[internal][k] = v
		}

		se.mu.Unlock()
	}
}

// indexTokenLocked adds term -> id to DataMap, Symspell, and Prefix map.
// Caller must hold se.mu.Lock().
func (se *SearchEngine) indexTokenLocked(term string, id uint32, score int) {
	docMap, termExists := se.DataMap[term]
	if !termExists {
		se.termSet.Store(term, struct{}{})
		// TODO: make it var in the engine definition
		if len(term) >= 4 {
			se.Symspell.AddWord(term)
		}
		for i := 1; i < len(term); i++ {
			pfx := term[:i]
			if len(se.Prefix[pfx]) < MaxPrefixTerms {
				se.Prefix[pfx] = append(se.Prefix[pfx], term)
			}
		}
		docMap = make(map[uint32]int)
		se.DataMap[term] = docMap
	}
	docMap[id] += score
}

// BuildDocumentIndex tokenizes index fields and updates the inverted index and filters.
// All tokens for a document are written under a single lock acquisition.
func (se *SearchEngine) BuildDocumentIndex(docs []map[string]interface{}) {
	for i, doc := range docs {
		if i%100_000 == 0 || i == len(docs)-1 {
			slog.Info("BuildDocumentIndex", "doc", i)
		}

		rawID, ok := doc["id"]
		if !ok || rawID == nil {
			continue
		}
		extID := fmt.Sprintf("%v", rawID)
		if extID == "" || extID == "<nil>" {
			continue
		}

		// resolve current internal id for this external id
		se.mu.RLock()
		internal, ok := se.ExternalToInternal[extID]
		deleted := ok && se.DocDeleted[internal]
		indexFields := se.IndexFields
		fieldWeights := se.FieldWeights
		filters := se.Filters
		se.mu.RUnlock()

		if !ok || deleted {
			continue
		}

		localScores := weightedTokenScores(doc, indexFields, fieldWeights)
		if len(localScores) == 0 {
			continue
		}

		// Write everything under a single lock.
		se.mu.Lock()
		if !se.DocDeleted[internal] {
			for token, score := range localScores {
				se.indexTokenLocked(token, internal, score)
			}
			for field := range filters {
				value, exists := doc[field]
				if !exists {
					continue
				}
				var filterKey string
				switch value.(type) {
				case int, int8, int16, int32, int64, float32, float64:
					filterKey = fmt.Sprintf("%s:%v", field, value)
				case string:
					filterKey = fmt.Sprintf("%s:%s", field, value)
				default:
					continue
				}
				se.FilterBits[filterKey] = filterBitSet(se.FilterBits[filterKey], internal)
			}
		}
		se.mu.Unlock()
	}
}

// -------------------- Search --------------------

// SearchOneTerm returns the top-k matching documents for a single term, ranked
// by score descending. If filters is non-empty, only documents passing the
// filter are considered. The whole function holds RLock to avoid concurrent
// map read/write panics with index updates.
func (se *SearchEngine) SearchOneTerm(query string, filters map[string][]interface{}) []ReturnedDocument {
	se.mu.RLock()
	defer se.mu.RUnlock()

	var allowed []uint64
	if len(filters) > 0 {
		allowed = se.applyFilterLocked(filters)
		if allowed == nil {
			return nil
		}
	}

	postings := se.DataMap[query]
	if len(postings) == 0 {
		return nil
	}

	k := se.ResultSize
	if k <= 0 {
		return nil
	}

	deleted := se.DocDeleted
	h := make([]internalHit, 0, k)

	for id, score := range postings {
		if deleted[id] {
			continue
		}
		if allowed != nil && !filterBitTest(allowed, id) {
			continue
		}

		if len(h) < k {
			h = heapPushHit(h, internalHit{id: id, score: score})
		} else if h[0].score < score {
			heapReplaceTop(h, internalHit{id: id, score: score})
		}
	}

	n := len(h)
	if n == 0 {
		return nil
	}

	// Extract in descending order via repeated heap-pop.
	extMap := se.InternalToExternal
	docs := se.Documents
	out := make([]ReturnedDocument, n)
	for i := n - 1; i >= 0; i-- {
		hit := h[0]
		if i > 0 {
			h[0] = h[i]
			siftDownHit(h, 0, i)
		}
		out[i] = ReturnedDocument{
			ID:    extMap[hit.id],
			Data:  docs[hit.id],
			Score: hit.score,
		}
	}
	return out
}

// SearchMultiTerms returns the top-k matching documents for a multi-term query
// expressed as groups of synonyms. Semantics: AND across groups, OR within a
// group. If filters is non-empty, only documents passing the filter are
// considered. RLock is held for the whole function.
func (se *SearchEngine) SearchMultiTerms(termArrList [][]string, filters map[string][]interface{}) []ReturnedDocument {
	if len(termArrList) == 0 {
		return nil
	}

	se.mu.RLock()
	defer se.mu.RUnlock()

	var allowed []uint64
	if len(filters) > 0 {
		allowed = se.applyFilterLocked(filters)
		if allowed == nil {
			return nil
		}
	}

	k := se.ResultSize
	if k <= 0 {
		return nil
	}

	dataMap := se.DataMap
	groups := make([][]map[uint32]int, len(termArrList))
	groupSizes := make([]int, len(termArrList))

	for i, terms := range termArrList {
		if len(terms) == 0 {
			return nil
		}

		group := make([]map[uint32]int, 0, len(terms))
		sizeSum := 0

		for _, term := range terms {
			if m := dataMap[term]; m != nil {
				group = append(group, m)
				sizeSum += len(m)
			}
		}

		if len(group) == 0 {
			return nil
		}

		groups[i] = group
		groupSizes[i] = sizeSum
	}

	// Anchor on the smallest group — fewest candidates to enumerate.
	anchorIdx := 0
	anchorSize := groupSizes[0]
	for i := 1; i < len(groupSizes); i++ {
		if groupSizes[i] < anchorSize {
			anchorSize = groupSizes[i]
			anchorIdx = i
		}
	}
	anchorGroup := groups[anchorIdx]

	// Dedup is only needed when the anchor group has multiple posting maps
	// (synonyms can overlap on the same docID). One map = no overlap possible.
	var visited map[uint32]struct{}
	if len(anchorGroup) > 1 {
		visited = make(map[uint32]struct{}, anchorSize)
	}

	deleted := se.DocDeleted
	h := make([]internalHit, 0, k)

	for _, anchorMap := range anchorGroup {
		for internalID, score := range anchorMap {
			if visited != nil {
				if _, seen := visited[internalID]; seen {
					continue
				}
				visited[internalID] = struct{}{}
			}

			if deleted[internalID] {
				continue
			}
			if allowed != nil && !filterBitTest(allowed, internalID) {
				continue
			}

			total := score
			valid := true

			for gi, group := range groups {
				if gi == anchorIdx {
					continue
				}

				found := false
				for _, m := range group {
					if s, ok := m[internalID]; ok {
						total += s
						found = true
						break
					}
				}

				if !found {
					valid = false
					break
				}
			}

			if !valid {
				continue
			}

			if len(h) < k {
				h = heapPushHit(h, internalHit{id: internalID, score: total})
			} else if h[0].score < total {
				heapReplaceTop(h, internalHit{id: internalID, score: total})
			}
		}
	}

	n := len(h)
	if n == 0 {
		return nil
	}

	extMap := se.InternalToExternal
	docs := se.Documents
	out := make([]ReturnedDocument, n)
	for i := n - 1; i >= 0; i-- {
		hit := h[0]
		if i > 0 {
			h[0] = h[i]
			siftDownHit(h, 0, i)
		}
		out[i] = ReturnedDocument{
			ID:    extMap[hit.id],
			Data:  docs[hit.id],
			Score: hit.score,
		}
	}
	return out
}

// applyFilterLocked resolves filters to a bitset without acquiring any lock.
// Caller must hold se.mu.RLock for the entire time the returned slice is used.
//
// Fast path (single field, single value): returns a direct reference into
// se.FilterBits — zero allocation. Multi-value OR and multi-field AND still
// allocate intermediate bitsets, but those cases are uncommon.
func (se *SearchEngine) applyFilterLocked(filters map[string][]interface{}) []uint64 {
	var result []uint64
	first := true

	for field, values := range filters {
		var fieldBits []uint64

		if len(values) == 1 {
			// Fast path: direct reference, no copy.
			key := fmt.Sprintf("%s:%v", field, values[0])
			fieldBits = se.FilterBits[key]
		} else {
			// Multi-value OR: must build a union (allocates once).
			for _, v := range values {
				key := fmt.Sprintf("%s:%v", field, v)
				bits := se.FilterBits[key]
				if len(bits) == 0 {
					continue
				}
				if fieldBits == nil {
					fieldBits = append([]uint64(nil), bits...)
				} else {
					fieldBits = filterBitOr(fieldBits, bits)
				}
			}
		}

		if len(fieldBits) == 0 {
			return nil
		}

		if first {
			result = fieldBits
			first = false
		} else {
			result = filterBitAnd(result, fieldBits)
			hasAny := false
			for _, w := range result {
				if w != 0 {
					hasAny = true
					break
				}
			}
			if !hasAny {
				return nil
			}
		}
	}

	return result
}

// ApplyFilter returns a bitset of internal docIDs that satisfy the given filters.
// Semantics: OR within a field's values, AND across different fields.
// Returns nil when filters produce no matches (or filters map is empty).
// For internal search paths use applyFilterLocked to avoid the copy.
func (se *SearchEngine) ApplyFilter(filters map[string][]interface{}) []uint64 {
	if len(filters) == 0 {
		return nil
	}

	se.mu.RLock()
	defer se.mu.RUnlock()

	var result []uint64
	first := true

	for field, values := range filters {
		var fieldUnion []uint64
		for _, v := range values {
			key := fmt.Sprintf("%s:%v", field, v)
			bits := se.FilterBits[key]
			if len(bits) == 0 {
				continue
			}
			if fieldUnion == nil {
				fieldUnion = append([]uint64(nil), bits...)
			} else {
				fieldUnion = filterBitOr(fieldUnion, bits)
			}
		}

		if fieldUnion == nil {
			return nil
		}

		if first {
			result = fieldUnion
			first = false
		} else {
			result = filterBitAnd(result, fieldUnion)
			hasAny := false
			for _, w := range result {
				if w != 0 {
					hasAny = true
					break
				}
			}
			if !hasAny {
				return nil
			}
		}
	}

	return result
}

// Search executes a query (single or multi-term), selecting between exact/prefix/fuzzy strategies.
func (se *SearchEngine) Search(query string, filters map[string][]interface{}) *SearchResult {
	queryTokens := Tokenize(query)
	if len(queryTokens) == 0 {
		return nil
	} else if len(queryTokens) == 1 {
		return se.SingleTermSearch(queryTokens, filters)
	} else {
		return se.MultiTermSearch(queryTokens, filters)
	}
}

// SingleTermSearch resolves a single-token query via prefix (preferred) or fuzzy expansions.
func (se *SearchEngine) SingleTermSearch(queryTokens []string, filters map[string][]interface{}) *SearchResult {
	parsedQuery := make(map[string][]string)
	// TODO: make it var in the engine definition
	maxPrefixTokens := 3
	maxFuzzyTokens := 3

	_, exactExists := se.termSet.Load(queryTokens[0])
	se.mu.RLock()
	prefixTokens := append([]string(nil), se.Prefix[queryTokens[0]]...)
	var fuzzyWords []string
	if len(prefixTokens) == 0 && !exactExists {
		fuzzyWords = se.Symspell.FuzzySearch(queryTokens[0], maxFuzzyTokens)
	}
	se.mu.RUnlock()
	if len(prefixTokens) > maxPrefixTokens {
		prefixTokens = prefixTokens[:maxPrefixTokens]
	}

	guessArr := prefixTokens

	if len(guessArr) > 0 {
		parsedQuery["prefix"] = append(parsedQuery["prefix"], guessArr...)
	} else if len(fuzzyWords) > 0 {
		parsedQuery["fuzzy"] = append(parsedQuery["fuzzy"], fuzzyWords...)
	}

	var finalDocs []ReturnedDocument
	if exactExists {
		finalDocs = append(finalDocs, se.SearchOneTerm(queryTokens[0], filters)...)
	}
	if parsedQuery["prefix"] != nil {
		for _, q := range parsedQuery["prefix"] {
			finalDocs = append(finalDocs, se.SearchOneTerm(q, filters)...)
		}
		limit := se.ResultSize
		if len(finalDocs) < limit {
			limit = len(finalDocs)
		}
		return &SearchResult{Docs: finalDocs[0:limit]}
	}
	if parsedQuery["fuzzy"] != nil {
		for _, q := range parsedQuery["fuzzy"] {
			finalDocs = append(finalDocs, se.SearchOneTerm(q, filters)...)
		}
		limit := se.ResultSize
		if len(finalDocs) < limit {
			limit = len(finalDocs)
		}
		return &SearchResult{Docs: finalDocs[0:limit]}
	}

	limit := se.ResultSize
	if len(finalDocs) < limit {
		limit = len(finalDocs)
	}
	return &SearchResult{Docs: finalDocs[0:limit]}
}

// MultiTermSearch executes a multi-token query using grouped boolean search strategies.
// First terms: exact match + fuzzy(10). Last term: exact match + adaptive prefix expansion.
func (se *SearchEngine) MultiTermSearch(queryTokens []string, filters map[string][]interface{}) *SearchResult {
	lastQueryIndex := len(queryTokens) - 1
	rawFirstTerms := queryTokens[:lastQueryIndex]
	rawLastTerm := queryTokens[lastQueryIndex]

	termArrList := make([][]string, len(queryTokens))

	_, lastExists := se.termSet.Load(rawLastTerm)
	se.mu.RLock()
	maxPrefix := prefixLimitForQuery(rawLastTerm)
	if len(se.Prefix[rawLastTerm]) < maxPrefix {
		maxPrefix = len(se.Prefix[rawLastTerm])
	}
	lastTermGuessArr := append([]string(nil), se.Prefix[rawLastTerm][:maxPrefix]...)
	for k, firstTerm := range rawFirstTerms {
		if _, ok := se.termSet.Load(firstTerm); ok {
			termArrList[k] = []string{firstTerm}
		}
		// TODO: make it var in the engine definition
		termArrList[k] = append(termArrList[k], se.Symspell.FuzzySearch(firstTerm, 10)...)
	}
	se.mu.RUnlock()

	if lastExists {
		termArrList[lastQueryIndex] = []string{rawLastTerm}
	}
	termArrList[lastQueryIndex] = append(termArrList[lastQueryIndex], lastTermGuessArr...)

	return &SearchResult{Docs: se.SearchMultiTerms(termArrList, filters)}
}

// AddOrUpdateDocument inserts or updates a single document.
// Semantics:
// - If extID is new: assign a new internal ID and index it.
// - If extID exists: mark old internal ID deleted, assign a new one, store+index new version.
func (se *SearchEngine) AddOrUpdateDocument(doc map[string]interface{}) error {
	if doc == nil {
		return fmt.Errorf("doc is nil")
	}

	rawID, ok := doc["id"]
	if !ok || rawID == nil {
		return fmt.Errorf("doc missing id field")
	}
	extID := fmt.Sprintf("%v", rawID)
	if extID == "" || extID == "<nil>" {
		return fmt.Errorf("invalid id value")
	}

	// 1) Resolve fields used for indexing/filtering (read-lock)
	se.mu.RLock()
	indexFields := append([]string(nil), se.IndexFields...)
	fieldWeights := copyIntMap(se.FieldWeights)
	filters := se.Filters
	se.mu.RUnlock()

	// 2) Tokenize and aggregate token scores without holding locks.
	localScores := weightedTokenScores(doc, indexFields, fieldWeights)

	// 3) Assign new internal ID, store doc, write tokens and filters under one lock
	se.mu.Lock()

	if oldInternal, exists := se.ExternalToInternal[extID]; exists {
		se.DocDeleted[oldInternal] = true
	}

	internal := se.nextInternalID
	se.nextInternalID++

	se.ExternalToInternal[extID] = internal
	se.InternalToExternal[internal] = extID

	se.Documents[internal] = make(map[string]interface{}, len(doc))
	for k, v := range doc {
		se.Documents[internal][k] = v
	}

	for token, score := range localScores {
		se.indexTokenLocked(token, internal, score)
	}

	for field := range filters {
		value, exists := doc[field]
		if !exists {
			continue
		}
		var filterKey string
		switch value.(type) {
		case int, int8, int16, int32, int64, float32, float64:
			filterKey = fmt.Sprintf("%s:%v", field, value)
		case string:
			filterKey = fmt.Sprintf("%s:%s", field, value)
		default:
			continue
		}
		se.FilterBits[filterKey] = filterBitSet(se.FilterBits[filterKey], internal)
	}

	se.mu.Unlock()

	return nil
}

// DeleteDocument tombstones the currently-active internal doc for the given external doc ID.
// It does NOT remove postings or filter entries; search excludes deleted docs via DocDeleted.
func (se *SearchEngine) DeleteDocument(externalID string) bool {
	if externalID == "" || externalID == "<nil>" {
		return false
	}

	se.mu.Lock()
	defer se.mu.Unlock()

	internal, ok := se.ExternalToInternal[externalID]
	if !ok {
		return false
	}

	se.DocDeleted[internal] = true

	return true
}
