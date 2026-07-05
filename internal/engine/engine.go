// Package engine implements a lightweight, in-memory search engine
// built on an inverted index with prefix map and fuzzy (SymSpell) matching.
// It is concurrency-safe for reads/writes via an internal RWMutex and is
// designed to be persisted/restored via a single gob payload.
package engine

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

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
	ActiveDocuments    int
	StoredDocuments    int
	TombstonedVersions int // postings/bits awaiting reclaim by CompactDeleted
	IndexFields        []string
	FieldWeights       map[string]int
	Filters            map[string]bool
	ResultSize         int
}

type CompactStats struct {
	BeforeStored    int `json:"beforeStored"`
	BeforeActive    int `json:"beforeActive"`
	AfterStored     int `json:"afterStored"`
	AfterActive     int `json:"afterActive"`
	RemovedVersions int `json:"removedVersions"`
}

// enginePayload is the gob-serializable snapshot of SearchEngine state. Only
// active documents are stored; the ID maps are rebuilt on load from each
// document's "id" field, so they are not persisted.
type enginePayload struct {
	Documents        map[uint32]map[string]interface{}
	IndexFields      []string
	FieldWeights     map[string]int
	Filters          map[string]bool
	ResultSize       int
	StaticPopularity map[string]int
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
	DataMap    map[string]map[uint32]int // term -> internalDocID -> postings
	DocDeleted map[uint32]bool           // internalDocID -> deleted?

	// ID mapping: updates create a NEW internalDocID and delete the previous one
	ExternalToInternal map[string]uint32 // externalDocID -> current internalDocID
	InternalToExternal map[uint32]string // internalDocID -> externalDocID
	nextInternalID     uint32

	Documents    map[uint32]map[string]interface{} // internalDocID -> doc fields
	FilterBits   map[string][]uint64               // "field:value" -> bitset of internalDocIDs
	Prefix       map[string][]string               // prefix -> matching terms (capped at MaxPrefixTerms)
	Symspell     *symspell.SymSpell
	IndexFields  []string
	FieldWeights map[string]int
	Filters      map[string]bool
	ResultSize   int

	// Popularity: StaticPopularity comes from the doc's "popularity" field at index time.
	StaticPopularity map[string]int // externalID → static popularity score

	// AI is the optional embedding/categorization index (nil when disabled).
	// It has its own mutex and is keyed by external doc ID, so it is not
	// affected by internal-ID churn or CompactDeleted.
	AI *AIIndex

	// termSet is a lock-free set of all indexed terms, used for O(1) existence
	// checks in the search path without touching se.mu. Stored as an atomic
	// pointer so CompactDeleted can swap the entire map without a data race.
	termSet atomic.Pointer[sync.Map]

	mu sync.RWMutex
}

// NewSearchEngine constructs a new engine with per-index-field scoring weights.
// Missing or non-positive weights default to 1. Pass nil for fieldWeights to
// use uniform weights.
func NewSearchEngine(indexFields []string, fieldWeights map[string]int, filters map[string]bool, resultSize int) *SearchEngine {
	se := &SearchEngine{
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
		StaticPopularity:   make(map[string]int),
	}
	se.termSet.Store(new(sync.Map))
	return se
}

func (se *SearchEngine) Stats() IndexStats {
	se.mu.RLock()
	defer se.mu.RUnlock()

	return IndexStats{
		ActiveDocuments:    len(se.ExternalToInternal),
		StoredDocuments:    len(se.Documents),
		TombstonedVersions: len(se.DocDeleted),
		IndexFields:        append([]string(nil), se.IndexFields...),
		FieldWeights:       copyIntMap(se.FieldWeights),
		Filters:            copyBoolMap(se.Filters),
		ResultSize:         se.ResultSize,
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

// SaveAll writes a gzip-compressed gob snapshot to path + "/engine.gob". Only
// active documents are saved. RLock is held only for the in-RAM snapshot, not
// for encoding or disk I/O, so writers block only briefly.
func (se *SearchEngine) SaveAll(path string) error {
	// Snapshot active state under a brief RLock. Inner document maps are
	// immutable, so a shallow copy is safe.
	se.mu.RLock()
	docs := make(map[uint32]map[string]interface{}, len(se.ExternalToInternal))
	staticPop := make(map[string]int, len(se.StaticPopularity))
	for extID, internalID := range se.ExternalToInternal {
		docs[internalID] = se.Documents[internalID]
		if v, ok := se.StaticPopularity[extID]; ok {
			staticPop[extID] = v
		}
	}
	payload := enginePayload{
		Documents:        docs,
		IndexFields:      append([]string(nil), se.IndexFields...),
		FieldWeights:     copyIntMap(se.FieldWeights),
		Filters:          copyBoolMap(se.Filters),
		ResultSize:       se.ResultSize,
		StaticPopularity: staticPop,
	}
	se.mu.RUnlock()

	// Encode and write to disk with no lock held: file → bufio → gzip → gob.
	engineFile := path + "/engine.gob"
	tmpFile := engineFile + ".tmp"

	f, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmpFile, err)
	}

	bw := bufio.NewWriterSize(f, 4<<20)
	gz := gzip.NewWriter(bw)

	encErr := gob.NewEncoder(gz).Encode(payload)
	gzErr := gz.Close()
	flushErr := bw.Flush()
	closeErr := f.Close()

	if encErr != nil || gzErr != nil || flushErr != nil {
		os.Remove(tmpFile)
		if encErr != nil {
			return fmt.Errorf("encode engine payload: %w", encErr)
		}
		if gzErr != nil {
			return fmt.Errorf("close gzip writer: %w", gzErr)
		}
		return fmt.Errorf("flush %s: %w", tmpFile, flushErr)
	}
	if closeErr != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("close %s: %w", tmpFile, closeErr)
	}

	if err := os.Rename(tmpFile, engineFile); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("rename %s to %s: %w", tmpFile, engineFile, err)
	}

	// Persist embeddings + categories alongside the document snapshot.
	if se.AIEnabled() {
		if err := se.PersistCategoryEmbed(path); err != nil {
			return fmt.Errorf("persist category embed: %w", err)
		}
	}

	return nil
}

// LoadAll restores documents + metadata from path + "/engine.gob" and rebuilds
// all derived structures (DataMap, FilterBits, Prefix, Symspell) in parallel.
// No lock is held: the engine is not yet visible to callers.
func LoadAll(path string) (*SearchEngine, error) {
	engineFile := path + "/engine.gob"
	f, err := os.Open(engineFile)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", engineFile, err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(bufio.NewReaderSize(f, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("gzip reader %s: %w", engineFile, err)
	}
	defer gr.Close()

	var payload enginePayload
	if err := gob.NewDecoder(gr).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode engine payload: %w", err)
	}
	if err := validatePayload(payload); err != nil {
		return nil, fmt.Errorf("invalid engine payload: %w", err)
	}

	fieldWeights := normalizeFieldWeights(payload.IndexFields, payload.FieldWeights)
	resultSize := payload.ResultSize
	if resultSize <= 0 {
		resultSize = 100
	}

	// Rebuild the ID maps from each document's "id" field.
	extToInt := make(map[string]uint32, len(payload.Documents))
	intToExt := make(map[uint32]string, len(payload.Documents))
	var nextInternalID uint32 = 1
	type activeDoc struct {
		internalID uint32
		doc        map[string]interface{}
	}
	active := make([]activeDoc, 0, len(payload.Documents))
	for internalID, doc := range payload.Documents {
		if doc == nil {
			continue
		}
		rawID := doc["id"]
		if rawID == nil {
			continue
		}
		extID := fmt.Sprintf("%v", rawID)
		if extID == "" || extID == "<nil>" {
			continue
		}
		extToInt[extID] = internalID
		intToExt[internalID] = extID
		if internalID >= nextInternalID {
			nextInternalID = internalID + 1
		}
		active = append(active, activeDoc{internalID: internalID, doc: doc})
	}

	// Parallel tokenization — weightedTokenScores is pure CPU, no shared state.
	tokenResults := make([]map[string]int, len(active))
	numWorkers := runtime.NumCPU()
	workCh := make(chan int, numWorkers*4)
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range workCh {
				tokenResults[i] = weightedTokenScores(active[i].doc, payload.IndexFields, fieldWeights)
			}
		}()
	}
	for i := range active {
		workCh <- i
	}
	close(workCh)
	wg.Wait()

	// Build all derived structures locally — no lock needed; engine not yet visible.
	newDataMap := make(map[string]map[uint32]int)
	newFilterBits := make(map[string][]uint64)
	newSymspell := symspell.NewSymSpell()
	newTermSet := new(sync.Map)

	for i, item := range active {
		for token, score := range tokenResults[i] {
			docMap, exists := newDataMap[token]
			if !exists {
				newTermSet.Store(token, struct{}{})
				if utf8.RuneCountInString(token) >= 4 {
					newSymspell.AddWord(token)
				}
				docMap = make(map[uint32]int)
				newDataMap[token] = docMap
			}
			docMap[item.internalID] += score
		}
		for field := range payload.Filters {
			value, exists := item.doc[field]
			if !exists {
				continue
			}
			for _, key := range filterKeys(field, value) {
				newFilterBits[key] = filterBitSet(newFilterBits[key], item.internalID)
			}
		}
	}

	// Build prefix map with terms sorted by document frequency (best prefix coverage).
	type termFreq struct {
		term string
		freq int
	}
	tfs := make([]termFreq, 0, len(newDataMap))
	for term, postings := range newDataMap {
		tfs = append(tfs, termFreq{term, len(postings)})
	}
	sort.Slice(tfs, func(i, j int) bool {
		if tfs[i].freq != tfs[j].freq {
			return tfs[i].freq > tfs[j].freq
		}
		return tfs[i].term < tfs[j].term
	})
	newPrefix := make(map[string][]string)
	for _, tf := range tfs {
		for i := range tf.term {
			if i == 0 {
				continue
			}
			pfx := tf.term[:i]
			if len(newPrefix[pfx]) < MaxPrefixTerms {
				newPrefix[pfx] = append(newPrefix[pfx], tf.term)
			}
		}
	}

	// Order fuzzy buckets by term frequency so the most common corrections come first.
	newSymspell.SortByFrequency(func(w string) int { return len(newDataMap[w]) })

	loadedStaticPop := payload.StaticPopularity
	if loadedStaticPop == nil {
		loadedStaticPop = make(map[string]int)
	}

	se := &SearchEngine{
		DataMap:            newDataMap,
		DocDeleted:         make(map[uint32]bool),
		ExternalToInternal: extToInt,
		InternalToExternal: intToExt,
		nextInternalID:     nextInternalID,
		Documents:          payload.Documents,
		FilterBits:         newFilterBits,
		Prefix:             newPrefix,
		Symspell:           newSymspell,
		IndexFields:        payload.IndexFields,
		FieldWeights:       fieldWeights,
		Filters:            payload.Filters,
		ResultSize:         resultSize,
		StaticPopularity:   loadedStaticPop,
	}
	se.termSet.Store(newTermSet)

	// Restore embeddings + categories if a category_embed.gob snapshot exists.
	// The embedder is not persisted; the caller re-attaches one via EnableAI.
	ai, err := loadCategoryEmbed(path)
	if err != nil {
		return nil, fmt.Errorf("load category embed: %w", err)
	}
	se.AI = ai

	return se, nil
}

func validatePayload(payload enginePayload) error {
	if payload.Documents == nil {
		return errors.New("missing documents")
	}
	if len(payload.IndexFields) == 0 {
		return errors.New("missing index fields")
	}
	if payload.ResultSize < 0 {
		return errors.New("negative result size")
	}
	return nil
}

// -------------------- Indexing --------------------

// Index performs a full (re)index pass for the provided docs and logs timings.
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

	slog.Info("SortSymspellByFrequency starting")
	start = time.Now()
	se.SortSymspellByFrequency()
	slog.Info("SortSymspellByFrequency done", "duration", time.Since(start))

	if se.AIEnabled() {
		slog.Info("CategorizeDocs starting")
		start = time.Now()
		ok, failed := se.CategorizeDocs(context.Background(), docs)
		slog.Info("CategorizeDocs done", "categorized", ok, "failed", failed, "duration", time.Since(start))
	}
}

// SortSymspellByFrequency orders the SymSpell delete buckets by descending term
// frequency so fuzzy corrections surface the most common candidates first. It
// runs as the finalize step of a bulk (re)index, under the write lock — fine for
// an admin operation. (LoadAll and CompactDeleted sort their fresh SymSpell
// directly, before it is visible.)
func (se *SearchEngine) SortSymspellByFrequency() {
	se.mu.Lock()
	defer se.mu.Unlock()
	se.Symspell.SortByFrequency(func(w string) int { return len(se.DataMap[w]) })
}

// UpdatePrefixAndFuzzy rebuilds the prefix lookup table and re-establishes the
// frequency ordering of the SymSpell fuzzy buckets from the current active
// postings. It is the maintenance counterpart to a bulk reindex for an engine
// that has only been mutated incrementally (AddOrUpdateDocument), where prefix
// coverage and fuzzy ranking drift over time.
func (se *SearchEngine) UpdatePrefixAndFuzzy() {
	se.UpdatePrefix()
	se.SortSymspellByFrequency()
}

// extractPopularity reads the "popularity" field from a document as an integer.
// Handles int, float64 (JSON default), int64, and int32.
func extractPopularity(doc map[string]interface{}) int {
	v, ok := doc["popularity"]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case int64:
		return int(n)
	case int32:
		return int(n)
	}
	return 0
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

		pop := extractPopularity(doc)

		se.mu.Lock()

		if oldInternal, exists := se.ExternalToInternal[extID]; exists {
			se.DocDeleted[oldInternal] = true
			delete(se.Documents, oldInternal)
			delete(se.InternalToExternal, oldInternal)
		}

		internal := se.nextInternalID
		se.nextInternalID++

		se.ExternalToInternal[extID] = internal
		se.InternalToExternal[internal] = extID

		se.Documents[internal] = make(map[string]interface{}, len(doc))
		for k, v := range doc {
			se.Documents[internal][k] = v
		}

		if pop > 0 {
			se.StaticPopularity[extID] = pop
		} else {
			delete(se.StaticPopularity, extID)
		}

		se.mu.Unlock()
	}
}

// indexTokenLocked adds term -> id to DataMap, Symspell, and Prefix map.
// Caller must hold se.mu.Lock().
func (se *SearchEngine) indexTokenLocked(term string, id uint32, score int) {
	docMap, termExists := se.DataMap[term]
	if !termExists {
		se.termSet.Load().Store(term, struct{}{})
		if utf8.RuneCountInString(term) >= 4 {
			se.Symspell.AddWord(term)
		}
		for i := range term {
			if i == 0 {
				continue
			}
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
// It holds the write lock only for the final pointer swap, so searches are
// blocked for nanoseconds rather than the full rebuild duration.
func (se *SearchEngine) UpdatePrefix() {
	if SkipUpdatePrefix {
		slog.Info("SkipUpdatePrefix is true, skipping")
		return
	}

	type termFreq struct {
		term string
		freq int
	}

	// Collect terms under RLock, then count each term's active document frequency
	// in parallel (concurrent map reads are safe under RLock). freq==0 terms (all
	// postings tombstoned) are skipped when the prefix map is built below.
	se.mu.RLock()
	terms := make([]termFreq, 0, len(se.DataMap))
	for term := range se.DataMap {
		terms = append(terms, termFreq{term: term})
	}

	numWorkers := runtime.NumCPU()
	jobCh := make(chan int, numWorkers*4)
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobCh {
				freq := 0
				for internalID := range se.DataMap[terms[i].term] {
					if !se.DocDeleted[internalID] {
						freq++
					}
				}
				terms[i].freq = freq
			}
		}()
	}
	for i := range terms {
		jobCh <- i
	}
	close(jobCh)
	wg.Wait()
	se.mu.RUnlock()

	// Sort and build the new prefix map with no lock held.
	sort.Slice(terms, func(i, j int) bool {
		if terms[i].freq != terms[j].freq {
			return terms[i].freq > terms[j].freq
		}
		return terms[i].term < terms[j].term
	})

	prefix := make(map[string][]string)
	for _, tf := range terms {
		if tf.freq == 0 {
			continue
		}
		for i := range tf.term {
			if i == 0 {
				continue
			}
			pfx := tf.term[:i]
			if len(prefix[pfx]) < MaxPrefixTerms {
				prefix[pfx] = append(prefix[pfx], tf.term)
			}
		}
	}

	// Swap under write lock — single pointer assignment.
	se.mu.Lock()
	se.Prefix = prefix
	se.mu.Unlock()
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

		se.mu.RLock()
		internal, ok := se.ExternalToInternal[extID]
		indexFields := se.IndexFields
		fieldWeights := se.FieldWeights
		filters := se.Filters
		se.mu.RUnlock()

		if !ok {
			continue
		}

		localScores := weightedTokenScores(doc, indexFields, fieldWeights)
		if len(localScores) == 0 {
			continue
		}

		// Re-check DocDeleted under the write lock: a concurrent AddOrUpdate may
		// have tombstoned this internalID since the RLock above.
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
// document versions. Active external IDs are preserved; internal IDs are
// reassigned densely from 1. RLock is held only for the snapshot and the final
// pointer swap; all heavy work runs with no lock held.
func (se *SearchEngine) CompactDeleted() CompactStats {
	// Snapshot active documents and config under read lock.
	se.mu.RLock()
	beforeStored := len(se.Documents)
	indexFields := append([]string(nil), se.IndexFields...)
	fieldWeights := copyIntMap(se.FieldWeights)
	filters := copyBoolMap(se.Filters)
	oldDataMapLen := len(se.DataMap)

	type activeDoc struct {
		externalID string
		doc        map[string]interface{}
	}
	// Stored docs are immutable, so referencing them directly is safe — no deep copy.
	active := make([]activeDoc, 0, len(se.Documents))
	for internalID, doc := range se.Documents {
		active = append(active, activeDoc{
			externalID: se.InternalToExternal[internalID],
			doc:        doc,
		})
	}
	se.mu.RUnlock()

	beforeActive := len(active)

	// Rebuild all structures locally with no lock held. Workers tokenize in
	// parallel and stream results to a single consumer that writes the fresh
	// structures (map mutations stay on one goroutine). Internal IDs are assigned
	// in arrival order; order is irrelevant for correctness.
	newDataMap := make(map[string]map[uint32]int, oldDataMapLen)
	newExtToInt := make(map[string]uint32, len(active))
	newIntToExt := make(map[uint32]string, len(active))
	newDocuments := make(map[uint32]map[string]interface{}, len(active))
	newFilterBits := make(map[string][]uint64)
	newSymspell := symspell.NewSymSpell()
	newTermSet := new(sync.Map)

	type tokResult struct {
		doc    map[string]interface{}
		extID  string
		tokens map[string]int
	}
	numWorkers := runtime.NumCPU()
	jobCh := make(chan int, numWorkers*4)
	resCh := make(chan tokResult, numWorkers*4)

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobCh {
				resCh <- tokResult{
					doc:    active[i].doc,
					extID:  active[i].externalID,
					tokens: weightedTokenScores(active[i].doc, indexFields, fieldWeights),
				}
			}
		}()
	}
	go func() {
		for i := range active {
			jobCh <- i
		}
		close(jobCh)
		wg.Wait()
		close(resCh)
	}()

	var nextID uint32 = 1
	for r := range resCh {
		id := nextID
		nextID++
		newExtToInt[r.extID] = id
		newIntToExt[id] = r.extID
		newDocuments[id] = r.doc

		for token, score := range r.tokens {
			docMap, exists := newDataMap[token]
			if !exists {
				newTermSet.Store(token, struct{}{})
				if utf8.RuneCountInString(token) >= 4 {
					newSymspell.AddWord(token)
				}
				docMap = make(map[uint32]int)
				newDataMap[token] = docMap
			}
			docMap[id] += score
		}

		for field := range filters {
			value, exists := r.doc[field]
			if !exists {
				continue
			}
			for _, key := range filterKeys(field, value) {
				newFilterBits[key] = filterBitSet(newFilterBits[key], id)
			}
		}
	}

	// Build prefix map in one sorted pass. All entries in newDataMap are active
	// (no tombstones), so len(postings) is the exact doc frequency.
	type termFreq struct {
		term string
		freq int
	}
	tfs := make([]termFreq, 0, len(newDataMap))
	for term, postings := range newDataMap {
		tfs = append(tfs, termFreq{term, len(postings)})
	}
	sort.Slice(tfs, func(i, j int) bool {
		if tfs[i].freq != tfs[j].freq {
			return tfs[i].freq > tfs[j].freq
		}
		return tfs[i].term < tfs[j].term
	})
	newPrefix := make(map[string][]string)
	for _, tf := range tfs {
		for i := range tf.term {
			if i == 0 {
				continue
			}
			pfx := tf.term[:i]
			if len(newPrefix[pfx]) < MaxPrefixTerms {
				newPrefix[pfx] = append(newPrefix[pfx], tf.term)
			}
		}
	}

	// Order fuzzy buckets by term frequency so the most common corrections come first.
	newSymspell.SortByFrequency(func(w string) int { return len(newDataMap[w]) })

	// Swap under write lock — only pointer assignments. StaticPopularity is left
	// untouched; AddOrUpdate/DeleteDocument keep it in sync with the active set.
	se.mu.Lock()
	se.DataMap = newDataMap
	se.DocDeleted = make(map[uint32]bool)
	se.ExternalToInternal = newExtToInt
	se.InternalToExternal = newIntToExt
	se.nextInternalID = nextID
	se.Documents = newDocuments
	se.FilterBits = newFilterBits
	se.Prefix = newPrefix
	se.Symspell = newSymspell
	se.termSet.Store(newTermSet)
	se.mu.Unlock()

	return CompactStats{
		BeforeStored:    beforeStored,
		BeforeActive:    beforeActive,
		AfterStored:     len(newDocuments),
		AfterActive:     len(active),
		RemovedVersions: beforeStored - len(newDocuments),
	}
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

// -------------------- Search --------------------

// SingleTermSearchLoop returns the top-k documents matching the candidate terms,
// ranked by boosted score. If filters is non-empty, only passing docs are
// considered. Holds RLock for the whole function.
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
		allowed = se.ApplyFilterLocked(filters)
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

	for _, cand := range candidates {
		m := dataMap[cand.term]
		if len(m) == 0 {
			continue
		}

		postings = append(postings, postingCandidate{
			m:     m,
			boost: cand.boost,
		})
	}

	if len(postings) == 0 {
		return nil, nil
	}

	deleted := se.DocDeleted
	h := make([]internalHit, 0, k)

	var visited *visitedBits
	if len(postings) > 1 {
		visited = getVisited(se.nextInternalID)
		defer putVisited(visited)
	}

	extMap := se.InternalToExternal
	staticPop := se.StaticPopularity
	hasPopularity := len(staticPop) > 0

	scanned := 0

	for _, posting := range postings {
		for id, score := range posting.m {
			scanned++
			if scanned%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, ErrSearchCanceled
				}
			}

			if visited != nil && visited.markSeen(id) {
				continue
			}

			if deleted[id] {
				continue
			}

			if allowed != nil && !filterBitTest(allowed, id) {
				continue
			}

			boostedScore := score * posting.boost
			if hasPopularity {
				boostedScore += staticPop[extMap[id]]
			}

			if len(h) < k {
				h = heapPushHit(h, internalHit{
					id:    id,
					score: boostedScore,
				})

				if SingleTermSkipWholeScan && len(h) >= k {
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
		allowed = se.ApplyFilterLocked(filters)
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

	var visited *visitedBits
	if len(anchorGroup) > 1 {
		visited = getVisited(se.nextInternalID)
		defer putVisited(visited)
	}

	deleted := se.DocDeleted
	h := make([]internalHit, 0, k)

	extMap := se.InternalToExternal
	staticPop := se.StaticPopularity
	hasPopularity := len(staticPop) > 0

	scanned := 0

	for _, anchor := range anchorGroup {
		for internalID, score := range anchor.m {
			scanned++
			if scanned%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, ErrSearchCanceled
				}
			}

			if visited != nil && visited.markSeen(internalID) {
				continue
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

			if hasPopularity {
				total += staticPop[extMap[internalID]]
			}

			if len(h) < k {
				h = heapPushHit(h, internalHit{id: internalID, score: total})
				if MultiTermSkipWholeScan && len(h) >= k {
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

// ApplyFilterLocked resolves filters to a bitset. Caller must hold se.mu.RLock
// while the returned slice is used. A single field+value returns a direct
// reference into se.FilterBits (no alloc); OR/AND cases allocate.
func (se *SearchEngine) ApplyFilterLocked(filters map[string][]interface{}) []uint64 {
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

// Search executes a query (single or multi-term), selecting between exact/prefix/fuzzy strategies.
func (se *SearchEngine) Search(ctx context.Context, query string, filters map[string][]interface{}) (*SearchResult, error) {
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

	maxPrefixTokens := SingleTermMaxPrefixTokens
	maxFuzzyTokens := SingleTermMaxFuzzyTokens

	candidates := make([]termCandidate, 0, 1+maxPrefixTokens+maxFuzzyTokens)

	_, exactExists := se.termSet.Load().Load(query)

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

	_, lastExists := se.termSet.Load().Load(rawLastTerm)

	se.mu.RLock()

	maxPrefix := multiTermPrefixLimit(rawLastTerm)
	if len(se.Prefix[rawLastTerm]) < maxPrefix {
		maxPrefix = len(se.Prefix[rawLastTerm])
	}

	lastTermGuessArr := append([]string(nil), se.Prefix[rawLastTerm][:maxPrefix]...)

	for k, firstTerm := range rawFirstTerms {
		if _, ok := se.termSet.Load().Load(firstTerm); ok {
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

	// Resolve fields used for indexing/filtering.
	se.mu.RLock()
	indexFields := append([]string(nil), se.IndexFields...)
	fieldWeights := copyIntMap(se.FieldWeights)
	filters := se.Filters
	se.mu.RUnlock()

	// Tokenize without holding locks.
	localScores := weightedTokenScores(doc, indexFields, fieldWeights)

	pop := extractPopularity(doc)

	// Assign new internal ID, store, and index under a single lock.
	se.mu.Lock()

	if oldInternal, exists := se.ExternalToInternal[extID]; exists {
		se.DocDeleted[oldInternal] = true
		delete(se.Documents, oldInternal)
		delete(se.InternalToExternal, oldInternal)
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

	if pop > 0 {
		se.StaticPopularity[extID] = pop
	} else {
		delete(se.StaticPopularity, extID)
	}

	se.mu.Unlock()

	// Embed + categorize outside se.mu (network call; AIIndex has its own lock).
	// A failure here leaves the document searchable but uncategorized.
	if se.AIEnabled() {
		if err := se.CategorizeDocument(context.Background(), extID, doc); err != nil {
			slog.Error("ai categorize failed", "doc", extID, "error", err)
		}
	}

	return nil
}

// DeleteDocument removes the given external doc ID from the engine immediately:
// Documents, ExternalToInternal, InternalToExternal, and StaticPopularity are
// cleaned up now. DocDeleted is set so that stale postings in DataMap and
// FilterBits (which are too expensive to remove individually) are excluded from
// search results until the next CompactDeleted.
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
	delete(se.Documents, internal)
	delete(se.ExternalToInternal, externalID)
	delete(se.InternalToExternal, internal)
	delete(se.StaticPopularity, externalID)

	if se.AI != nil {
		se.AI.RemoveDocument(externalID)
	}

	return true
}
