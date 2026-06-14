// Package engine implements a lightweight, in-memory search engine
// built on an inverted index with prefix map and fuzzy (SymSpell) matching.
// It is concurrency-safe for reads/writes via an internal RWMutex and is
// designed to be persisted/restored via a single gob payload.
package engine

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
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

var ErrSearchCanceled = errors.New("search canceled")

type IndexStats struct {
	ActiveDocuments int
	StoredDocuments int
	IndexFields     []string
	FieldWeights    map[string]int
	Filters         map[string]bool
	ResultSize      int
}

type CompactStats struct {
	BeforeStored    int `json:"beforeStored"`
	BeforeActive    int `json:"beforeActive"`
	AfterStored     int `json:"afterStored"`
	AfterActive     int `json:"afterActive"`
	RemovedVersions int `json:"removedVersions"`
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

type termCandidate struct {
	term  string
	boost int // 1 normal, ExactmatchBoost for exact
}

type postingCandidate struct {
	m     map[uint32]int
	boost int
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

func (se *SearchEngine) Stats() IndexStats {
	se.mu.RLock()
	defer se.mu.RUnlock()

	active := 0
	for _, internalID := range se.ExternalToInternal {
		if !se.DocDeleted[internalID] {
			active++
		}
	}

	return IndexStats{
		ActiveDocuments: active,
		StoredDocuments: len(se.Documents),
		IndexFields:     append([]string(nil), se.IndexFields...),
		FieldWeights:    copyIntMap(se.FieldWeights),
		Filters:         copyBoolMap(se.Filters),
		ResultSize:      se.ResultSize,
	}
}

func (se *SearchEngine) FilterFields() map[string]bool {
	se.mu.RLock()
	defer se.mu.RUnlock()

	return copyBoolMap(se.Filters)
}

func (se *SearchEngine) StoredDocumentCount() int {
	se.mu.RLock()
	defer se.mu.RUnlock()

	return len(se.Documents)
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
	tmpFile := engineFile + ".tmp"

	f, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmpFile, err)
	}

	enc := gob.NewEncoder(f)
	if encErr := enc.Encode(payload); encErr != nil {
		f.Close()
		os.Remove(tmpFile)
		return fmt.Errorf("encode engine payload: %w", encErr)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("close %s: %w", tmpFile, err)
	}

	if err := os.Rename(tmpFile, engineFile); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("rename %s to %s: %w", tmpFile, engineFile, err)
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
	if err := validatePayload(payload); err != nil {
		return nil, fmt.Errorf("invalid engine payload: %w", err)
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
	if se.ResultSize <= 0 {
		se.ResultSize = 100
	}

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

	se.mu.Lock()
	for internalID, doc := range se.Documents {
		if doc == nil || se.DocDeleted[internalID] || !isCurrent(internalID) {
			continue
		}

		localScores := weightedTokenScores(doc, se.IndexFields, se.FieldWeights)
		if len(localScores) == 0 {
			continue
		}

		for token, score := range localScores {
			se.indexTokenLocked(token, internalID, score)
		}
		se.setFilterBitsLocked(doc, internalID, se.Filters)
	}
	se.mu.Unlock()

	return se, nil
}

func validatePayload(payload enginePayload) error {
	if payload.Documents == nil {
		return errors.New("missing documents")
	}
	if payload.DocDeleted == nil {
		return errors.New("missing deleted document map")
	}
	if payload.ExternalToInternal == nil {
		return errors.New("missing external ID map")
	}
	if payload.InternalToExternal == nil {
		return errors.New("missing internal ID map")
	}
	if len(payload.IndexFields) == 0 {
		return errors.New("missing index fields")
	}
	if payload.ResultSize < 0 {
		return errors.New("negative result size")
	}
	for ext, internal := range payload.ExternalToInternal {
		if ext == "" || internal == 0 {
			return fmt.Errorf("invalid external mapping %q -> %d", ext, internal)
		}
		if payload.InternalToExternal[internal] != ext {
			return fmt.Errorf("inconsistent ID mapping for %q", ext)
		}
	}
	return nil
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

	slog.Info("UpdatePrefix starting")
	start = time.Now()
	se.UpdatePrefix()
	slog.Info("UpdatePrefix done", "duration", time.Since(start))
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

// UpdatePrefix rebuilds the prefix lookup from currently-active postings and
// orders each prefix list by active document frequency descending.
func (se *SearchEngine) UpdatePrefix() {
	if SkipUpdatePrefix {
		slog.Info("SkipUpdatePrefix is true, skipping")
		return
	}
	se.mu.Lock()
	defer se.mu.Unlock()

	se.updatePrefixLocked()
}

// updatePrefixLocked rebuilds Prefix from DataMap. Caller must hold se.mu.Lock().
func (se *SearchEngine) updatePrefixLocked() {
	type termFreq struct {
		term string
		freq int
	}

	terms := make([]termFreq, 0, len(se.DataMap))
	for term, postings := range se.DataMap {
		freq := 0
		for internalID := range postings {
			if !se.DocDeleted[internalID] {
				freq++
			}
		}
		if freq > 0 {
			terms = append(terms, termFreq{term: term, freq: freq})
		}
	}

	sort.Slice(terms, func(i, j int) bool {
		if terms[i].freq != terms[j].freq {
			return terms[i].freq > terms[j].freq
		}
		return terms[i].term < terms[j].term
	})

	prefix := make(map[string][]string)
	for _, tf := range terms {
		for i := 1; i < len(tf.term); i++ {
			pfx := tf.term[:i]
			if len(prefix[pfx]) < MaxPrefixTerms {
				prefix[pfx] = append(prefix[pfx], tf.term)
			}
		}
	}
	se.Prefix = prefix
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
			se.setFilterBitsLocked(doc, internal, filters)
		}
		se.mu.Unlock()
	}
}

// CompactDeleted rebuilds every derived structure and drops tombstoned old
// document versions. Active external IDs are preserved, while internal IDs are
// reassigned densely from 1.
func (se *SearchEngine) CompactDeleted() CompactStats {
	se.mu.Lock()
	defer se.mu.Unlock()

	beforeStored := len(se.Documents)
	beforeActive := se.activeDocumentCountLocked()

	type activeDoc struct {
		externalID string
		doc        map[string]interface{}
	}
	active := make([]activeDoc, 0, beforeActive)
	for externalID, internalID := range se.ExternalToInternal {
		if se.DocDeleted[internalID] {
			continue
		}
		doc := se.Documents[internalID]
		if doc == nil {
			continue
		}
		active = append(active, activeDoc{
			externalID: externalID,
			doc:        copyDocument(doc),
		})
	}
	sort.Slice(active, func(i, j int) bool {
		return active[i].externalID < active[j].externalID
	})

	indexFields := append([]string(nil), se.IndexFields...)
	fieldWeights := copyIntMap(se.FieldWeights)
	filters := copyBoolMap(se.Filters)

	se.DataMap = make(map[string]map[uint32]int)
	se.DocDeleted = make(map[uint32]bool)
	se.ExternalToInternal = make(map[string]uint32)
	se.InternalToExternal = make(map[uint32]string)
	se.nextInternalID = 1
	se.Documents = make(map[uint32]map[string]interface{}, len(active))
	se.FilterBits = make(map[string][]uint64)
	se.Prefix = make(map[string][]string)
	se.Symspell = symspell.NewSymSpell()
	se.termSet = sync.Map{}

	for _, item := range active {
		internalID := se.nextInternalID
		se.nextInternalID++
		se.ExternalToInternal[item.externalID] = internalID
		se.InternalToExternal[internalID] = item.externalID
		se.Documents[internalID] = item.doc

		for token, score := range weightedTokenScores(item.doc, indexFields, fieldWeights) {
			se.indexTokenLocked(token, internalID, score)
		}
		se.setFilterBitsLocked(item.doc, internalID, filters)
	}
	se.updatePrefixLocked()

	afterStored := len(se.Documents)
	afterActive := se.activeDocumentCountLocked()
	return CompactStats{
		BeforeStored:    beforeStored,
		BeforeActive:    beforeActive,
		AfterStored:     afterStored,
		AfterActive:     afterActive,
		RemovedVersions: beforeStored - afterStored,
	}
}

func (se *SearchEngine) activeDocumentCountLocked() int {
	active := 0
	for _, internalID := range se.ExternalToInternal {
		if !se.DocDeleted[internalID] {
			active++
		}
	}
	return active
}

func (se *SearchEngine) setFilterBitsLocked(doc map[string]interface{}, internalID uint32, filters map[string]bool) {
	for field := range filters {
		value, exists := doc[field]
		if !exists {
			continue
		}
		for _, key := range filterKeys(field, value) {
			se.FilterBits[key] = filterBitSet(se.FilterBits[key], internalID)
		}
	}
}

func filterKeys(field string, value interface{}) []string {
	switch v := value.(type) {
	case int, int8, int16, int32, int64, float32, float64:
		return []string{fmt.Sprintf("%s:%v", field, v)}
	case string:
		if v == "" {
			return nil
		}
		return []string{fmt.Sprintf("%s:%s", field, v)}
	case []string:
		keys := make([]string, 0, len(v))
		for _, item := range v {
			if item != "" {
				keys = append(keys, fmt.Sprintf("%s:%s", field, item))
			}
		}
		return keys
	case []interface{}:
		keys := make([]string, 0, len(v))
		for _, item := range v {
			keys = append(keys, filterKeys(field, item)...)
		}
		return keys
	default:
		return nil
	}
}

func copyDocument(doc map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(doc))
	for k, v := range doc {
		out[k] = v
	}
	return out
}

// -------------------- Search --------------------
// SingleTermSearchLoop searches candidate terms with per-term boosts and
// returns the top-k matching documents ranked by score descending. If filters
// is non-empty, only documents passing the filter are considered. The whole
// function holds RLock to avoid concurrent map read/write panics with index
// updates.
func (se *SearchEngine) SingleTermSearchLoop(
	ctx context.Context,
	candidates []termCandidate,
	filters map[string][]interface{},
) ([]ReturnedDocument, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	se.mu.RLock()
	defer se.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return nil, ErrSearchCanceled
	}

	var allowed []uint64
	if len(filters) > 0 {
		allowed = se.applyFilterLocked(filters)
		if allowed == nil {
			return nil, nil
		}
	}

	k := se.ResultSize
	if k <= 0 {
		return nil, nil
	}

	dataMap := se.DataMap

	postings := make([]postingCandidate, 0, len(candidates))
	totalPostingSize := 0

	for _, cand := range candidates {
		m := dataMap[cand.term]
		if len(m) == 0 {
			continue
		}

		postings = append(postings, postingCandidate{
			m:     m,
			boost: cand.boost,
		})
		totalPostingSize += len(m)
	}

	if len(postings) == 0 {
		return nil, nil
	}

	deleted := se.DocDeleted
	h := make([]internalHit, 0, k)

	var visited map[uint32]struct{}
	if len(postings) > 1 {
		visited = make(map[uint32]struct{}, totalPostingSize)
	}

	scanned := 0

	for _, posting := range postings {
		for id, score := range posting.m {
			scanned++
			if scanned%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, ErrSearchCanceled
				}
			}

			if visited != nil {
				if _, seen := visited[id]; seen {
					continue
				}
				visited[id] = struct{}{}
			}

			if deleted[id] {
				continue
			}

			if allowed != nil && !filterBitTest(allowed, id) {
				continue
			}

			boostedScore := score * posting.boost

			if len(h) < k {
				h = heapPushHit(h, internalHit{
					id:    id,
					score: boostedScore,
				})

				if SkipWholeScan && len(h) >= k {
					goto BreakLoop
				}
			} else if h[0].score < boostedScore {
				heapReplaceTop(h, internalHit{
					id:    id,
					score: boostedScore,
				})
			}
		}
	}

BreakLoop:
	n := len(h)
	if n == 0 {
		return nil, nil
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

	return out, nil
}

// MultiTermSearchLoop returns the top-k matching documents for a multi-term
// query expressed as boosted groups of synonyms. Semantics: AND across groups,
// OR within a group. If filters is non-empty, only documents passing the filter
// are considered. RLock is held for the whole function.
func (se *SearchEngine) MultiTermSearchLoop(
	ctx context.Context,
	termArrList [][]termCandidate,
	filters map[string][]interface{},
) ([]ReturnedDocument, error) {
	if len(termArrList) == 0 {
		return nil, nil
	}

	se.mu.RLock()
	defer se.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return nil, ErrSearchCanceled
	}

	var allowed []uint64
	if len(filters) > 0 {
		allowed = se.applyFilterLocked(filters)
		if allowed == nil {
			return nil, nil
		}
	}

	k := se.ResultSize
	if k <= 0 {
		return nil, nil
	}

	dataMap := se.DataMap

	groups := make([][]postingCandidate, len(termArrList))
	groupSizes := make([]int, len(termArrList))

	for i, terms := range termArrList {
		if len(terms) == 0 {
			return nil, nil
		}

		group := make([]postingCandidate, 0, len(terms))
		sizeSum := 0

		for _, cand := range terms {
			if m := dataMap[cand.term]; m != nil {
				group = append(group, postingCandidate{
					m:     m,
					boost: cand.boost,
				})
				sizeSum += len(m)
			}
		}

		if len(group) == 0 {
			return nil, nil
		}

		groups[i] = group
		groupSizes[i] = sizeSum
	}

	anchorIdx := 0
	anchorSize := groupSizes[0]
	for i := 1; i < len(groupSizes); i++ {
		if groupSizes[i] < anchorSize {
			anchorSize = groupSizes[i]
			anchorIdx = i
		}
	}

	anchorGroup := groups[anchorIdx]

	var visited map[uint32]struct{}
	if len(anchorGroup) > 1 {
		visited = make(map[uint32]struct{}, anchorSize)
	}

	deleted := se.DocDeleted
	h := make([]internalHit, 0, k)

	scanned := 0

	for _, anchor := range anchorGroup {
		for internalID, score := range anchor.m {
			scanned++
			if scanned%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, ErrSearchCanceled
				}
			}

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

			total := score * anchor.boost
			valid := true

			for gi, group := range groups {
				if gi == anchorIdx {
					continue
				}

				found := false

				for _, cand := range group {
					if s, ok := cand.m[internalID]; ok {
						total += s * cand.boost
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
				if SkipWholeScan && len(h) >= k {
					goto BreakLoop
				}
			} else if h[0].score < total {
				heapReplaceTop(h, internalHit{id: internalID, score: total})
			}
		}
	}

BreakLoop:
	n := len(h)
	if n == 0 {
		return nil, nil
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

	return out, nil
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
	res, _ := se.SearchContext(context.Background(), query, filters)
	return res
}

func (se *SearchEngine) SearchContext(ctx context.Context, query string, filters map[string][]interface{}) (*SearchResult, error) {
	queryTokens := Tokenize(query)
	if len(queryTokens) == 0 {
		return nil, nil
	} else if len(queryTokens) == 1 {
		return se.SingleTermSearch(ctx, queryTokens, filters)
	} else {
		return se.MultiTermSearch(ctx, queryTokens, filters)
	}
}

func (se *SearchEngine) SingleTermSearch(
	ctx context.Context,
	queryTokens []string,
	filters map[string][]interface{},
) (*SearchResult, error) {
	if len(queryTokens) == 0 {
		return &SearchResult{}, nil
	}

	query := queryTokens[0]

	// TODO: make these configurable in SearchEngine.
	maxPrefixTokens := 3
	maxFuzzyTokens := 3

	candidates := make([]termCandidate, 0, 1+maxPrefixTokens+maxFuzzyTokens)

	_, exactExists := se.termSet.Load(query)

	se.mu.RLock()

	prefixTokens := append([]string(nil), se.Prefix[query]...)
	if len(prefixTokens) > maxPrefixTokens {
		prefixTokens = prefixTokens[:maxPrefixTokens]
	}

	var fuzzyWords []string
	if len(prefixTokens) == 0 && !exactExists {
		fuzzyWords = se.Symspell.FuzzySearch(query, maxFuzzyTokens)
	}

	se.mu.RUnlock()

	seen := make(map[string]struct{}, 1+len(prefixTokens)+len(fuzzyWords))

	if exactExists {
		candidates = append(candidates, termCandidate{
			term:  query,
			boost: ExactMatchBoost,
		})
		seen[query] = struct{}{}
	}

	if len(prefixTokens) > 0 {
		for _, term := range prefixTokens {
			if _, ok := seen[term]; ok {
				continue
			}

			candidates = append(candidates, termCandidate{
				term:  term,
				boost: 1,
			})
			seen[term] = struct{}{}
		}
	} else if len(fuzzyWords) > 0 {
		for _, term := range fuzzyWords {
			if _, ok := seen[term]; ok {
				continue
			}

			candidates = append(candidates, termCandidate{
				term:  term,
				boost: 1,
			})
			seen[term] = struct{}{}
		}
	}

	docs, err := se.SingleTermSearchLoop(ctx, candidates, filters)
	if err != nil {
		return nil, err
	}

	return &SearchResult{Docs: docs}, nil
}

func (se *SearchEngine) MultiTermSearch(
	ctx context.Context,
	queryTokens []string,
	filters map[string][]interface{},
) (*SearchResult, error) {
	if len(queryTokens) == 0 {
		return &SearchResult{}, nil
	}

	lastQueryIndex := len(queryTokens) - 1
	rawFirstTerms := queryTokens[:lastQueryIndex]
	rawLastTerm := queryTokens[lastQueryIndex]

	termArrList := make([][]termCandidate, len(queryTokens))

	_, lastExists := se.termSet.Load(rawLastTerm)

	se.mu.RLock()

	maxPrefix := multiTermPrefixLimit(rawLastTerm)
	if len(se.Prefix[rawLastTerm]) < maxPrefix {
		maxPrefix = len(se.Prefix[rawLastTerm])
	}

	lastTermGuessArr := append([]string(nil), se.Prefix[rawLastTerm][:maxPrefix]...)

	for k, firstTerm := range rawFirstTerms {
		if _, ok := se.termSet.Load(firstTerm); ok {
			termArrList[k] = append(termArrList[k], termCandidate{
				term:  firstTerm,
				boost: ExactMatchBoost,
			})
		}

		for _, fuzzy := range se.Symspell.FuzzySearch(firstTerm, fuzzyLimitForQuery(firstTerm)) {
			// Avoid adding duplicate exact term as fuzzy result.
			if fuzzy == firstTerm {
				continue
			}

			termArrList[k] = append(termArrList[k], termCandidate{
				term:  fuzzy,
				boost: 1,
			})
		}
	}

	se.mu.RUnlock()

	if lastExists {
		termArrList[lastQueryIndex] = append(termArrList[lastQueryIndex], termCandidate{
			term:  rawLastTerm,
			boost: ExactMatchBoost,
		})
	}

	for _, guess := range lastTermGuessArr {
		if guess == rawLastTerm {
			continue
		}

		termArrList[lastQueryIndex] = append(termArrList[lastQueryIndex], termCandidate{
			term:  guess,
			boost: 1,
		})
	}

	docs, err := se.MultiTermSearchLoop(ctx, termArrList, filters)
	if err != nil {
		return nil, err
	}

	return &SearchResult{Docs: docs}, nil
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

	se.setFilterBitsLocked(doc, internal, filters)

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
