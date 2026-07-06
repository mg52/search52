![build](https://github.com/mg52/search52/actions/workflows/go.yml/badge.svg)

# In-Memory Search Engine

A lightweight, fast, in-memory inverted-index search engine with HTTP handlers and on-disk persistence.  

## Features

- **Full-text indexing** on arbitrary JSON documents  
- **Prefix & fuzzy matching** — prefix map for instant completions + SymSpell for Levenshtein fuzzy suggestions  
- **Filters** on arbitrary document fields (OR within a field, AND across fields)  
- **Popularity ranking** — a static `popularity` field boosts search scores  
- **Optional AI categorization & vector search** — embeds documents via an OpenAI-compatible endpoint, incrementally clusters them into categories, and runs a parallel vector search on multi-word queries whose hits are merged into the classic results  
- **HTTP API** for index creation, search, single-doc upsert/delete, bulk add  
- **Persistence** — saves documents + metadata + popularity and rebuilds all derived indexes on load; AI embeddings/categories persist to a separate snapshot  
- **Docker-ready** — runs as an unprivileged user, persists under a mounted volume  

---

## Benchmark

All numbers are from an Apple M1 Pro. The MusicBrainz load test uses the real `mb_5m.json` dataset. The in-process benchmark below uses generated 1 000 000 and 5 000 000 document datasets with **100 000 unique vocabulary words**, `title` (3-20 words) and `tags` (1-10 words) index fields, and `year` (2000-2024) as the filter field.

### MusicBrainz 5 M HTTP load test

Environment: Apple M1 Pro, darwin/arm64, Go 1.25.4, `GOMAXPROCS=10`.

Dataset: `mb_5m.json`, 5 000 000 MusicBrainz documents, 585 MB JSON. Index fields: `title`, `artist`, `album`. Filter field: `year`. Result size: 100. Hard-coded prefix map cap: 5 000. Multi-term last-token prefix expansion is adaptive: 100 completions for 1-2 chars, 60 for 3-5 chars, and 50 after that. Query vocabulary: all unique tokens extracted from the same indexed text fields. Single-term queries are 65% exact, 25% prefix, 10% misspelled; multi-term queries use 2-4 tokens, 10% have one misspelled token, and the last token is prefix-truncated 25% of the time. `-mode-mix balanced` sends equal traffic to Single/NoFilter, Single/Filter, Multi/NoFilter, and Multi/Filter.

Index/load summary:

| Metric | Value |
|---|---:|
| Documents indexed | 5 000 000 |
| JSON file size | 585 MB |
| Vocabulary size | All unique tokens from `title`, `artist`, `album` |
| Total index time | 94.85 s |

HTTP load-test results:

16 workers, 100000 queries in 1 minute 3 seconds.

| Scope | avg | p50 | p95 | p99 |
|---|---:|---:|---:|---:|
| Overall | 10.12ms | 4.62ms | 32.03ms | 90.71ms |
| Single / NoFilter | 7.96ms | 4.30ms | 24.96ms | 54.66ms |
| Single / Filter | 7.70ms | 3.51ms | 24.38ms | 60.46ms |
| Multi / NoFilter | 16.89ms | 6.60ms | 58.06ms | 177.62ms |
| Multi / Filter | 7.94ms | 4.24ms | 26.09ms | 53.91ms |

Bench tooling documentation lives in [cmd/bench/README.md](cmd/bench/README.md)

---

## Quickstart

### Run Locally

```bash
export ADMINKEY='local-dev-key'
export SEARCH52_INDEX_DATA_DIR='./data'
go run ./cmd/service
```

The API service listens on `:8080`. A small admin UI listens on `:8081`.

Open the admin UI:

```text
http://localhost:8081
```

Enter the same `ADMINKEY` value in the UI header before creating indexes, writing documents, saving, loading, or compacting. Read-only actions such as listing indexes, search, and health checks do not require the key.

### Docker

```bash
docker build -t searchengine:latest .

docker run -d \
  -p 8080:8080 \
  -p 8081:8081 \
  -v search_data:/data \
  -e ADMINKEY='change-me' \
  -e SEARCH52_INDEX_DATA_DIR=/data \
  --name searchengine \
  searchengine:latest
```

---

## How It Works

### 1. Inverted Index

```go
DataMap map[string]map[uint32]int
// term → internalDocID → score
```

Every document is tokenized from the configured `IndexFields`. Tokenization lowercases the text, strips every non-ASCII-alphanumeric character, and drops a fixed stop-word list (`a`, `the`, `and`). Index fields can optionally define `fieldWeights`; missing weights default to `1`, while the HTTP API rejects non-positive weights. Each token receives a normalized score based on its field weight, so a token from a field with weight `3` is worth three times a token from a field with weight `1` while keeping the document's total score budget roughly stable. If the same token appears multiple times its scores are summed, so denser matches rank higher. `DataMap` is the primary posting-list and ranking structure used by the search hot path.

### 2. Internal IDs and Tombstones

Documents are identified internally by a monotonically increasing `uint32`:

| Map | Purpose |
|---|---|
| `ExternalToInternal` | caller's string ID → current internal ID (active docs only) |
| `InternalToExternal` | internal ID → caller's string ID (active docs only) |
| `Documents` | internal ID → raw field map (active docs only) |
| `DocDeleted` | tombstoned internal ID → true |

**Update / delete semantics**: updating a document assigns a new internal ID for the new version; deleting removes it outright. In both cases the old internal ID is **eagerly removed** from `Documents`, `ExternalToInternal`, `InternalToExternal`, and the popularity maps, and is recorded in `DocDeleted`. The doc-level maps therefore only ever hold live records — `storedDocs` always equals `activeDocs`.

What is *not* cleaned up eagerly are the old version's entries in the inverted index (`DataMap`) and filter bitsets (`FilterBits`); removing a single ID from every posting list is too expensive, so those stale entries linger and are skipped at scan time via the `DocDeleted` tombstone. `len(DocDeleted)` is the count of versions awaiting reclaim and is surfaced as `deletedVersions` in the list-indexes response — a good signal for when to compact. The compact endpoint rebuilds the inverted index, filter bitsets, prefix map, and fuzzy dictionary from active documents only, then clears all tombstones. Compaction reassigns internal IDs densely from 1, and rebuilds in parallel (workers tokenize while a single consumer writes, so peak memory stays bounded). Popularity scores are keyed by external ID and survive compaction untouched.

### 3. Prefix and Fuzzy Matching

At index time, every new term seeds two auxiliary structures:

- **`Prefix map[string][]string`** — maps every prefix of a term to a hard-coded list of up to 5 000 completions. Lookup is a single map read — O(1). At query time the first 3 completions are used for single-term prefix search. Multi-term search uses an adaptive prefix count for the last token: 100 completions for 1-2 chars, 60 for 3-5 chars, and 50 after that.
- **`SymSpell`** — a Levenshtein-distance index used for fuzzy suggestions when the query token has no prefix candidates (i.e. it is not a known prefix of any indexed term). Only terms with length ≥ 4 are added to SymSpell to avoid noise from very short tokens.

Single-term search includes the exact term when it exists, then adds prefix candidates (up to 3). If there are no prefix candidates and no exact term, it falls back to SymSpell suggestions. Multi-term search applies exact + fuzzy expansion on all-but-last tokens, and exact + adaptive prefix expansion on the last token (the partially-typed word).

### 4. Bitset Filters

Filter fields (e.g. `year`) are stored as permanent per-value bitsets instead of per-query maps:

```go
FilterBits map[string][]uint64
// "year:2020" → []uint64  (bit i set ↔ internalDocID i matches)
```

At index time each matching internal ID flips one bit: `bits[id>>6] |= 1 << (id&63)`. At query time a single bit test replaces a map lookup:

```
filterBitTest(allowed, id)  →  bits[id>>6] & (1<<(id&63)) != 0
```

**Memory**: ~1.25 MB per filter value per 1 M documents, paid once at index time.  
**Per-query allocation**: zero for the common case (single field, single value) — `applyFilterLocked` returns a direct slice reference into `se.FilterBits` with no copy.

Multi-value filters within one field are ORed (bitwise union); multiple fields are ANDed (bitwise intersection). Both operations produce a new `[]uint64` and are O(N/64) where N is the highest internal ID.

### 5. Top-k Extraction with a Specialized Min-Heap

Search maintains a bounded min-heap of the top-k candidates:

```go
type internalHit struct { id uint32; score int }
```

The heap operations (`heapPushHit`, `heapReplaceTop`, `siftDownHit`) are inlined directly over `[]internalHit` with no interface dispatch or boxing — unlike `container/heap` which boxes every element into `any`. At k = 100 this saves ~200 allocations per query.

**Fill phase**: while `len(h) < k`, push every passing candidate. Once full, replace the root only when `candidate.score > h[0].score` (the current minimum).

**Extraction phase**: repeated inline heap-pop fills the result slice from the last index down, yielding results in descending score order without an extra sort pass.

### 6. Concurrency and Zero-Copy Filter Resolution

The engine uses a single `sync.RWMutex` plus a lock-free `termSet`:

- All search paths hold **`RLock`** — unlimited concurrent readers, no blocking between searches.
- Index writes (`InsertDocs`, `BuildDocumentIndex`, `AddOrUpdateDocument`) hold **`Lock`** — exclusive, blocks new readers until the write completes.
- **`termSet sync.Map`** — a lock-free set of every indexed term. `SingleTermSearch` and `MultiTermSearch` check exact-term existence here before acquiring `RLock`, avoiding a mutex round-trip for the common case.

`SingleTermSearchLoop` and `MultiTermSearchLoop` acquire `RLock` **once at the top** and hold it across both filter resolution and the posting-map scan:

```
RLock acquired
  └─ applyFilterLocked()   →  returns direct []uint64 reference (no copy)
  └─ posting-map scan      →  filterBitTest reads from same reference
RLock released
```

For the common case (single field, single value), `applyFilterLocked` returns a slice header pointing directly into `se.FilterBits` with zero allocation.

### 7. Multi-Term Search: Anchor-Group Strategy

Multi-term queries use a boolean AND-across-groups, OR-within-group model. A "group" is a set of synonyms or expansions for one query token.

Rather than intersecting all groups eagerly, the engine picks the **smallest group** (fewest total posting entries) as the anchor and iterates only its candidates. For each candidate it checks membership in every other group with a map lookup — O(1) per group. This avoids materialising a full intersection set and keeps multi-term search fast even when individual terms are common.

Score for a matching document is the sum of its scores across all matched groups.

### 8. Popularity Ranking

A static signal adds a flat boost to a document's match score, keyed by external ID:

```go
StaticPopularity map[string]int // from the doc's "popularity" field at index time
```

- **Static popularity** is read from each document's `popularity` field (int or numeric) when it is indexed or updated. Writing a document with no/zero `popularity` clears any previous value. Deleting a document clears its popularity entry.

During search, `staticPop[extID]` is added to the boosted term score before the candidate enters the top-k heap, in both single- and multi-term paths. The lookup is guarded by a `hasPopularity` flag so indexes that use no popularity pay zero overhead. Because the map is keyed by external ID and maintained in sync with the active set, it needs no special handling in compaction.

### 9. Persistence

`SaveAll` serialises only the active document store, metadata (field config), and the popularity map to a single gob file — tombstoned versions are never written. `LoadAll` restores the documents and popularity, then rebuilds all derived structures — `DataMap`, `FilterBits`, `Prefix`, `SymSpell`, and the ID maps — by replaying the tokenisation pass. This keeps the snapshot compact and means the on-disk format never needs a schema migration when internal data structures change. If `engine.gob` is corrupt or fails validation during the HTTP load endpoint, the existing in-memory index is left unchanged and the endpoint returns a clear error.

### 10. AI Categorization & Vector Search (optional)

An index created with `"aiEnabled": true` additionally embeds every document via an OpenAI-compatible `/embeddings` endpoint (OpenAI itself, or anything that mirrors its API, e.g. Ollama) and incrementally clusters it into categories — a separate subsystem (`AIIndex`) that never blocks or is blocked by the classic inverted index:

```go
type AIIndex struct {
    docs           map[string]AIDocument          // external doc ID -> embedding + categories
    categories     map[string]*Category           // name -> centroid (running vector sum)
    catDocs        map[string]map[string]struct{} // category name -> member doc IDs
    nextCategoryID int
}
```

**Categorization**: a document's index-field values are joined into one string and embedded. It joins every existing category whose cosine similarity to the category's centroid is ≥ `SEARCH52_AI_CATEGORY_THRESHOLD` (default `0.60`), highest similarity first, up to `SEARCH52_AI_MAX_CATEGORIES_PER_DOC` (default `3`) categories. If none qualify, it seeds a new category — unless `SEARCH52_AI_MAX_CATEGORIES` (default `100`) is already reached, in which case it joins the single nearest category regardless of threshold. A category's centroid stores the running **sum** of its member vectors (cosine similarity is scale-invariant, so the sum ranks identically to the mean), which also keeps member removal exact on document update/delete.

Bulk indexing embeds documents in batches of 256, with `SEARCH52_AI_EMBED_CONCURRENCY` (default `8`) embedding requests in flight per batch; each batch is then clustered sequentially in input order, so category discovery is deterministic for a given input. Per-document embedding failures are logged and skipped — the document stays fully searchable via the classic index, just uncategorized.

**Search integration**: `Search` runs the AI vector search *in parallel* with the classic search once the query has at least `SEARCH52_AI_PARALLEL_SEARCH_MIN_TOKENS` (default `2`) tokens. The vector search embeds the query, selects the `SEARCH52_AI_SEARCH_TOP_N_CATEGORIES` (default `3`) nearest categories by centroid similarity, and ranks their member documents by cosine similarity to the query — both steps use the same bounded min-heap technique as §5. It applies the same filter bitset as the classic path, so a filtered-out document never occupies a result slot. Its hits are **appended** to the classic results — by design there is no merge, rerank, or dedup — and are distinguishable via `ReturnedDocument.AI == true`. Below the token threshold, or when AI is disabled, `Search` runs classic-only with zero AI overhead.

**Concurrency**: `AIIndex` holds its own `RWMutex`, independent of the engine's, so clustering (CPU-bound vector math) and embedding (network I/O) never contend with classic search or indexing. When both locks are needed, lock order is always engine-mutex-then-AI-mutex, never the reverse, so cross-lock deadlock is structurally impossible. This ordering opens a brief eventual-consistency window (a just-written document may not be AI-clusterable yet); the read side closes it by re-validating every AI hit against the live document map before returning it, so a stale or deleted candidate is silently dropped rather than surfaced.

**Persistence**: embeddings and categories are written to a separate `category_embed.gob` file, independent of `engine.gob`. `PersistCategoryEmbed` (`POST /persist-category-embed`) writes only this file, without touching the document snapshot; `SaveAll` writes both files when AI is enabled. The embedder itself is never serialized — `LoadAll` restores vectors and categories as inert data, and the caller must reattach a live embedder via `EnableAI`. The HTTP layer does this automatically on `/load-controller`, constructing the embedder from `SEARCH52_EMBEDDING_*` env vars; if that config is missing it logs a warning and leaves the index searchable (classic-only) rather than failing the load.

---

## HTTP API

All endpoints return JSON and use HTTP status codes (`201 Created`, `200 OK`, `400 Bad Request`, `404 Not Found`, `405 Method Not Allowed`, `409 Conflict`, `504 Gateway Timeout`, `500 Internal Server Error`). Error responses have a fixed schema:

```json
{
  "status": "error",
  "statusCode": 400,
  "error": "message"
}
```

Input validation is intentionally strict: index/filter/field names may contain only letters, numbers, `_`, `-`, and `.`; unknown JSON fields are rejected; `resultCount` must be between 1 and 10 000; search queries are capped at 512 characters; filter strings are capped at 2 048 characters; individual filter values are capped at 256 characters; JSON request bodies are capped at 1 MiB except single-document writes, which allow 5 MiB; bulk uploads allow up to 1 GiB.

Mutating endpoints require an admin key. Set `ADMINKEY` in the service environment and send the same value with the `X-Admin-Key` header. Missing or wrong keys return `401 Unauthorized`.

Protected endpoints:

- `POST /create-index`
- `POST /add-to-index`
- `POST /document`
- `DELETE /document`
- `POST /save-controller`
- `POST /persist-category-embed`
- `POST /load-controller`
- `POST /compact-index`

Read-only endpoints (`/list-indexes`, `/search`, `/health`) do not require the key.

The admin UI port exposes the same API under `/api/...`, for example `/api/create-index` and `/api/search`.

### 1. Create Index

```bash
curl -X POST http://localhost:8080/create-index \
  -H 'Content-Type: application/json' \
  -H 'X-Admin-Key: local-dev-key' \
  -d '{
    "indexName":   "products",
    "indexFields": ["name", "tags"],
    "fieldWeights": { "name": 3, "tags": 1 },
    "filters":     ["year"],
    "resultCount": 10
  }'
```

Pass `"aiEnabled": true` to additionally embed and categorize every document (see [§10](#10-ai-categorization--vector-search-optional)):

```bash
curl -X POST http://localhost:8080/create-index \
  -H 'Content-Type: application/json' \
  -H 'X-Admin-Key: local-dev-key' \
  -d '{
    "indexName":   "products",
    "indexFields": ["name", "tags"],
    "resultCount": 10,
    "aiEnabled":   true
  }'
```

This requires an embedding endpoint configured in the service environment: `SEARCH52_EMBEDDING_BASE_URL` and `SEARCH52_EMBEDDING_MODEL` are required, `SEARCH52_EMBEDDING_API_KEY` is optional (e.g. for a local Ollama server). If they are missing, `create-index` returns `400 Bad Request` and no index is created.

### 2. List Indexes

```bash
curl http://localhost:8080/list-indexes
```

The response includes every in-memory index and its document counts. `activeDocs` is the current live record count; `storedDocs` is the number of documents materialised in memory (always equal to `activeDocs`, since tombstoned versions are removed eagerly); `deletedVersions` is the number of stale posting/bitset versions awaiting reclaim by compaction (`len(DocDeleted)`) — when this grows large, run compact.

```json
{
  "status": "success",
  "statusCode": 200,
  "total": 1,
  "indexes": [
    {
      "name": "products",
      "activeDocs": 2,
      "storedDocs": 2,
      "deletedVersions": 1,
      "indexFields": ["name", "tags"],
      "filters": ["year"],
      "resultCount": 10
    }
  ]
}
```

### 3. Bulk Add to Index

```bash
curl -X POST 'http://localhost:8080/add-to-index?indexName=products' \
  -H 'X-Admin-Key: local-dev-key' \
  -F 'file=@docs.json'
```

The uploaded file can be either a JSON array of objects or a CSV file with a header row. Every document should include an `id` field; documents without a usable `id` are skipped by the indexer. An optional numeric `popularity` field is read as a static ranking boost.

JSON example:

```json
[
  { "id": "1", "name": "foo", "tags": ["a", "b"], "year": "2020" },
  { "id": "2", "name": "bar", "tags": ["c"],       "year": "2021" }
]
```

CSV example:

```csv
id,name,tags,year
1,foo,a b,2020
2,bar,c,2021
```

### 4. Search

```bash
# Simple query
curl 'http://localhost:8080/search?index=products&q=laptop'

# With a single filter
curl 'http://localhost:8080/search?index=products&q=laptop&filter=year:2020'

# With multiple filters (AND across fields, OR within a field)
curl 'http://localhost:8080/search?index=products&q=laptop&filter=year:2020,year:2021,category:electronics'
```

| Param | Description |
|---|---|
| `index` | Index name |
| `q` | Search query (single or multi-term) |
| `filter` | Comma-separated `field:value` pairs |

Search requests use a 10 second context timeout. If a query exceeds that budget, the endpoint returns `504 Gateway Timeout`.

On an AI-enabled index, a query with at least `SEARCH52_AI_PARALLEL_SEARCH_MIN_TOKENS` tokens (2 by default) also runs the vector/category search described in [§10](#10-ai-categorization--vector-search-optional) and appends its hits to `response.docs`. Each document in the response carries an `"AI"` field: `true` for a vector-search hit, `false` for a classic inverted-index hit. Filters apply identically to both; there is no dedup between them, so the same document can appear twice if both paths find it.

### 5. Add or Update Single Document

```bash
curl -X POST 'http://localhost:8080/document?indexName=products' \
  -H 'Content-Type: application/json' \
  -H 'X-Admin-Key: local-dev-key' \
  -d '{
    "document": { "id": "14", "name": "New Name", "tags": ["x"], "year": "2021" }
  }'
```

`indexName` can also be sent in the JSON body instead of the query string.

### 6. Delete Single Document

```bash
curl -X DELETE 'http://localhost:8080/document?indexName=products&id=14' \
  -H 'X-Admin-Key: local-dev-key'
```

### 7. Save Index

```bash
curl -X POST http://localhost:8080/save-controller \
  -H 'Content-Type: application/json' \
  -H 'X-Admin-Key: local-dev-key' \
  -d '{ "indexName": "products" }'
```

Indexes are saved under `$SEARCH52_INDEX_DATA_DIR/<indexName>/engine.gob`; if `SEARCH52_INDEX_DATA_DIR` is not set, the service uses `./data`. If AI is enabled on the index, this also writes `category_embed.gob` alongside it (see below).

### 8. Persist Category Embed (AI)

```bash
curl -X POST http://localhost:8080/persist-category-embed \
  -H 'Content-Type: application/json' \
  -H 'X-Admin-Key: local-dev-key' \
  -d '{ "indexName": "products" }'
```

Writes **only** the AI state — per-document embeddings and categories — to `$SEARCH52_INDEX_DATA_DIR/<indexName>/category_embed.gob`, without touching the document snapshot (`engine.gob`). Useful for checkpointing embeddings on their own cadence, independent of how often documents are saved. Returns `400 Bad Request` if the index does not have AI enabled.

### 9. Load Index

```bash
curl -X POST http://localhost:8080/load-controller \
  -H 'Content-Type: application/json' \
  -H 'X-Admin-Key: local-dev-key' \
  -d '{ "indexName": "products" }'
```

Load is rollback-safe: a corrupt or invalid `engine.gob` does not replace an already-loaded in-memory index. If a `category_embed.gob` snapshot exists, its embeddings and categories are restored too, and the service reattaches a live embedder from the `SEARCH52_EMBEDDING_*` environment variables so newly indexed documents keep getting categorized; if that config is missing, the index loads successfully but stays classic-only (a warning is logged).

### 10. Compact Index

```bash
curl -X POST http://localhost:8080/compact-index \
  -H 'Content-Type: application/json' \
  -H 'X-Admin-Key: local-dev-key' \
  -d '{ "indexName": "products" }'
```

Compaction removes tombstoned old document versions from all postings, filters, prefix arrays, and fuzzy data, and reassigns internal IDs densely from 1. Popularity scores are preserved. The response includes before/after active and stored document counts plus the number of removed versions.

### 11. Health Check

```bash
curl http://localhost:8080/health
```

```json
{ "status": "ok", "statusCode": 200, "duration": "5µs", "durationMs": 0 }
```

---

## Admin UI

Start the service and open:

```text
http://localhost:8081
```

The admin UI runs on a separate port and shares the same in-memory engine instance as the API server. It can:

- list indexes and active document counts
- create an index
- add or update a single JSON document
- delete a single document by ID
- run search queries with optional filters
- save and load indexes from disk
- compact an index to remove tombstoned versions

The UI port can be changed with `SEARCH52_ADMIN_ADDR`; the API port can be changed with `SEARCH52_API_ADDR`.

Mutating UI actions require the same `ADMINKEY` used by the server. Enter it in the password field in the UI header; the browser stores it in local storage and sends it as `X-Admin-Key` for protected requests.

---

## Testing

```bash
# Run all tests
go test -count=1 ./...

# With race detection
go test -race -count=1 ./...

# With filtered coverage
./scripts/coverage.sh coverage.out
go tool cover -func=coverage.out
```

---

## License

MIT © mg52
