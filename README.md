![build](https://github.com/mg52/search52/actions/workflows/go.yml/badge.svg)

# In-Memory Search Engine

A lightweight, fast, in-memory inverted-index search engine with HTTP handlers and on-disk persistence.  

## Features

- **Full-text indexing** on arbitrary JSON documents  
- **Prefix & fuzzy matching** — prefix map for instant completions + SymSpell for Levenshtein fuzzy suggestions  
- **Filters** on arbitrary document fields (OR within a field, AND across fields)  
- **HTTP API** for index creation, search, single-doc upsert/delete, bulk add  
- **Persistence** — saves documents + metadata and rebuilds all derived indexes on load  
- **Docker-ready** — runs as an unprivileged user, persists under a mounted volume  

---

## Benchmark

All numbers are from an Apple M1 Pro. The MusicBrainz load test uses the real `mb_5m.json` dataset. The in-process benchmark below uses generated 1 000 000 and 5 000 000 document datasets with **100 000 unique vocabulary words**, `title` (3-20 words) and `tags` (1-10 words) index fields, and `year` (2000-2024) as the filter field.

### MusicBrainz 5 M HTTP load test

Environment: Apple M1 Pro, darwin/arm64, Go 1.25.4, `GOMAXPROCS=10`.

Dataset: `mb_5m.json`, 5 000 000 MusicBrainz documents, 585 MB JSON. Index fields: `title`, `artist`, `album`. Filter field: `year`. Result size: 100. Hard-coded prefix map cap: 5 000. Multi-term last-token prefix expansion is adaptive: 100 completions for 1-2 chars, 60 for 3-5 chars, and 50 after that. Query vocabulary: all unique tokens extracted from the same indexed text fields. Query generation used the same rules as `benchmark.go`: single-term queries are 65% exact, 25% prefix, 10% misspelled; multi-term queries use 2-4 tokens, 10% have one misspelled token, and the last token is prefix-truncated 25% of the time. `-mode-mix balanced` sends equal traffic to Single/NoFilter, Single/Filter, Multi/NoFilter, and Multi/Filter.

Setup:

```bash
go run ./cmd/bench vocab \
  -data mb_5m.json \
  -fields title,artist,album \
  -out mb_vocab.txt

go run ./cmd/service

curl -X POST http://127.0.0.1:8080/create-index \
  -H 'Content-Type: application/json' \
  -d '{
    "indexName":"mb5m",
    "indexFields":["title","artist","album"],
    "filters":["year"],
    "resultCount":100
  }'

curl -X POST 'http://127.0.0.1:8080/add-to-index?indexName=mb5m' \
  -F 'file=@mb_5m.json'

go run ./cmd/bench loadtest \
  -url http://127.0.0.1:8080/search \
  -vocab mb_vocab.txt \
  -index mb5m \
  -requests 100000 \
  -workers 16 \
  -seed 99 \
  -mode-mix balanced
```

Index/load summary:

| Metric | Value |
|---|---:|
| Documents indexed | 5 000 000 |
| JSON file size | 585 MB |
| Vocabulary size | All unique tokens from `title`, `artist`, `album` |
| InsertDocs time | 5.73 s |
| BuildDocumentIndex time | 87.89 s |
| Engine-reported total index time | 93.63 s |
| HTTP upload wall time | 102.81 s |
| Service RSS after load test | ~3.85 GiB |

HTTP load-test results:

The load-test client drains each response body before recording latency.

| Scope | Workers | Queries | Errors | HTTP statuses | Wall time | RPS | avg | p50 | p95 | p99 |
|---|---:|---:|---:|---|---:|---:|---:|---:|---:|---:|
| Overall | 16 | 100 000 | 0 | 200:100000 | 3.028 s | 33 020.9 | 0.48 ms | 0.33 ms | 1.18 ms | 3.00 ms |
| Single / NoFilter | 16 | 25 000 | 0 | 200 | - | - | 0.61 ms | 0.45 ms | 1.35 ms | 3.66 ms |
| Single / Filter | 16 | 25 000 | 0 | 200 | - | - | 0.46 ms | 0.31 ms | 1.23 ms | 2.67 ms |
| Multi / NoFilter | 16 | 25 000 | 0 | 200 | - | - | 0.43 ms | 0.29 ms | 1.09 ms | 3.32 ms |
| Multi / Filter | 16 | 25 000 | 0 | 200 | - | - | 0.41 ms | 0.29 ms | 0.98 ms | 2.51 ms |

### In-process benchmark

```bash
go run ./cmd/bench benchmark -data data.json   -vocab vocab.txt -queries 10000 -warmup 1000
go run ./cmd/bench benchmark -data data5m.json -vocab vocab.txt -queries 10000 -warmup 1000
```

#### 1 M docs — heap delta ~800 MB

| Mode | avg | p50 | p99 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|
| SingleTerm / NoFilter | 29.4 µs | 21.2 µs |  88.9 µs | 14 136 | 10 |
| SingleTerm / Filter   |  8.8 µs |  6.6 µs |  26.1 µs |  3 457 | 13 |
| MultiTerm  / NoFilter | 16.6 µs | 12.9 µs |  73.2 µs |  2 831 | 25 |
| MultiTerm  / Filter   | 12.7 µs | 12.1 µs |  32.8 µs |  2 862 | 27 |

#### 5 M docs — heap delta ~2 773 MB

| Mode | avg | p50 | p99 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|
| SingleTerm / NoFilter | 67.7 µs | 40.4 µs | 318.6 µs | 14 148 | 10 |
| SingleTerm / Filter   | 53.3 µs | 36.5 µs | 230.7 µs |  6 715 | 13 |
| MultiTerm  / NoFilter | 82.0 µs | 41.8 µs | 594.6 µs |  3 472 | 25 |
| MultiTerm  / Filter   | 46.9 µs | 43.0 µs | 173.9 µs |  3 492 | 27 |

Filter queries are faster than no-filter equivalents because the bitset pre-prunes the candidate set before the posting-list scan.

---

## Quickstart

### Build & Run

```bash
go run ./cmd/service
```

The API service listens on `:8080`. A small admin UI listens on `:8081`.

### Docker

```bash
docker build -t searchengine:latest .

docker run -d \
  -p 8080:8080 \
  -p 8081:8081 \
  -v search_data:/data \
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

Every document is tokenized from the configured `IndexFields`. Tokenization lowercases the text, strips every non-alphanumeric character (via a compiled regexp), and drops a fixed stop-word list (`a`, `the`, `and`). Index fields can optionally define `fieldWeights`; missing weights default to `1`, while the HTTP API rejects non-positive weights. Each token receives a normalized score based on its field weight, so a token from a field with weight `3` is worth three times a token from a field with weight `1` while keeping the document's total score budget roughly stable. If the same token appears multiple times its scores are summed, so denser matches rank higher. `DataMap` is the primary posting-list and ranking structure used by the search hot path.

### 2. Internal IDs and Tombstones

Documents are identified internally by a monotonically increasing `uint32`:

| Map | Purpose |
|---|---|
| `ExternalToInternal` | caller's string ID → current internal ID |
| `InternalToExternal` | internal ID → caller's string ID |
| `Documents` | internal ID → raw field map |
| `DocDeleted` | internal ID → tombstoned? |

**Update semantics**: updating a document assigns a new internal ID and sets `DocDeleted[oldID] = true`. Searches skip tombstoned IDs at scan time, while the new version is tokenized and indexed normally. To reclaim old posting-list entries, call the compact endpoint; it rebuilds the inverted index, filter bitsets, prefix map, and fuzzy dictionary from active documents only.

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

`SearchOneTerm` and `SearchMultiTerms` acquire `RLock` **once at the top** and hold it across both filter resolution and the posting-map scan:

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

### 8. Persistence

`SaveAll` serialises only the raw document store and metadata (IDs, field config) to a single gob file. `LoadAll` restores the documents and then rebuilds all derived structures — `DataMap`, `FilterBits`, `Prefix`, `SymSpell` — by replaying the tokenisation pass. This keeps the snapshot compact and means the on-disk format never needs a schema migration when internal data structures change. If `engine.gob` is corrupt or fails validation during the HTTP load endpoint, the existing in-memory index is left unchanged and the endpoint returns a clear error.

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

### 1. Create Index

```bash
curl -X POST http://localhost:8080/create-index \
  -H 'Content-Type: application/json' \
  -d '{
    "indexName":   "products",
    "indexFields": ["name", "tags"],
    "fieldWeights": { "name": 3, "tags": 1 },
    "filters":     ["year"],
    "resultCount": 10
  }'
```

### 2. List Indexes

```bash
curl http://localhost:8080/list-indexes
```

The response includes every in-memory index and its current active document count. `storedDocs` includes old tombstoned document versions retained for update/delete performance, while `activeDocs` is the current live record count.

```json
{
  "status": "success",
  "statusCode": 200,
  "total": 1,
  "indexes": [
    {
      "name": "products",
      "activeDocs": 2,
      "storedDocs": 3,
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
  -F 'file=@docs.json'
```

The uploaded file can be either a JSON array of objects or a CSV file with a header row. Every document should include an `id` field; documents without a usable `id` are skipped by the indexer.

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

### 5. Add or Update Single Document

```bash
curl -X POST 'http://localhost:8080/document?indexName=products' \
  -H 'Content-Type: application/json' \
  -d '{
    "document": { "id": "14", "name": "New Name", "tags": ["x"], "year": "2021" }
  }'
```

`indexName` can also be sent in the JSON body instead of the query string.

### 6. Delete Single Document

```bash
curl -X DELETE 'http://localhost:8080/document?indexName=products&id=14'
```

### 7. Save Index

```bash
curl -X POST http://localhost:8080/save-controller \
  -H 'Content-Type: application/json' \
  -d '{ "indexName": "products" }'
```

Indexes are saved under `$SEARCH52_INDEX_DATA_DIR/<indexName>/engine.gob`; if `SEARCH52_INDEX_DATA_DIR` is not set, the service uses `./data`.

### 8. Load Index

```bash
curl -X POST http://localhost:8080/load-controller \
  -H 'Content-Type: application/json' \
  -d '{ "indexName": "products" }'
```

Load is rollback-safe: a corrupt or invalid `engine.gob` does not replace an already-loaded in-memory index.

### 9. Compact Index

```bash
curl -X POST http://localhost:8080/compact-index \
  -H 'Content-Type: application/json' \
  -d '{ "indexName": "products" }'
```

Compaction removes tombstoned old document versions from all postings, filters, prefix arrays, fuzzy data, and document maps. The response includes before/after active and stored document counts.

### 10. Health Check

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

## Benchmarking & Load Testing

All data-generation and testing tools live in a single binary under `cmd/bench`.

```
go run ./cmd/bench <command> [flags]

commands:
  vocab      generate vocabulary.txt
  datagen    generate a JSON document file
  benchmark  run an in-process latency benchmark from a JSON file
  loadtest   hammer a running HTTP search endpoint
```

### Step 1 — generate vocabulary

```bash
go run ./cmd/bench vocab -size 100000 -out vocab.txt

# Or extract vocabulary from a real JSON dataset
go run ./cmd/bench vocab \
  -data mb_5m.json \
  -fields title,artist,album \
  -out mb_vocab.txt
```

| Flag | Default | Description |
|---|---|---|
| `-size` | `100000` | Number of unique mock words to generate when `-data` is not set |
| `-out` | `vocab.txt` | Output file |
| `-seed` | `42` | RNG seed |
| `-data` | `""` | Optional JSON document file to extract vocabulary from. When set, all unique tokens from `-fields` are written. |
| `-fields` | `title,tags` | Comma-separated fields to extract when `-data` is set |

### Step 2 — generate documents

```bash
# 1 million documents
go run ./cmd/bench datagen -count 1000000 -vocab vocab.txt -out data.json


# 5 million documents
go run ./cmd/bench datagen -count 5000000 -vocab vocab.txt -out data5m.json
```

Generated documents look like:

```json
{"id":"d0","title":"rukpttue nhuks ghejk vgzadxl ptneuv","tags":"ghejk nhuks","year":2017}
```

Fields: `title` (3–20 words), `tags` (1–10 words), `year` (2000–2024).

| Flag | Default | Description |
|---|---|---|
| `-count` | `1000000` | Number of documents |
| `-vocab` | `vocab.txt` | Vocabulary file (from `vocab` command) |
| `-out` | `data.json` | Output JSON file |
| `-seed` | `42` | RNG seed |

### Step 3 — in-process benchmark

Reads the JSON file, builds the engine in memory, then runs search queries measuring latency. No HTTP server needed.

```bash
go run ./cmd/bench benchmark -data data.json -vocab vocab.txt -queries 10000 -warmup 1000
```

| Flag | Default | Description |
|---|---|---|
| `-data` | `data.json` | JSON data file |
| `-vocab` | `vocab.txt` | Vocabulary file |
| `-queries` | `5000` | Queries measured per mode |
| `-warmup` | `500` | Warmup iterations before measuring |
| `-result-size` | `100` | Top-k result size |
| `-seed` | `99` | RNG seed for query generation |

### Step 4 — HTTP load test

Start the service and load data, then run the load test.

```bash
go run ./cmd/service

# create index
curl -X POST http://localhost:8080/create-index \
  -H 'Content-Type: application/json' \
  -d '{"indexName":"bench","indexFields":["title","tags"],"filters":["year"],"resultCount":100}'

# upload 1 M docs
curl -X POST 'http://localhost:8080/add-to-index?indexName=bench' \
  -F 'file=@data.json'

# run load test
go run ./cmd/bench loadtest \
  -url http://localhost:8080/search \
  -vocab vocab.txt \
  -index bench \
  -requests 10000 \
  -workers 16 \
  -mode-mix balanced
```

| Flag | Default | Description |
|---|---|---|
| `-url` | `http://localhost:8080/search` | Search endpoint |
| `-vocab` | `vocab.txt` | Vocabulary file |
| `-index` | `bench` | Index name |
| `-workers` | `8` | Parallel goroutines |
| `-requests` | `10000` | Total requests |
| `-filter-pct` | `50` | % of requests with a year filter |
| `-multi-pct` | `50` | % of multi-term queries |
| `-mode-mix` | `random` | `random` uses `filter-pct`/`multi-pct`; `balanced` cycles evenly through the four benchmark modes |
| `-timeout` | `10s` | Per-request HTTP timeout |
| `-seed` | random | RNG seed for reproducibility |
| `-keepalive` | `true` | Use HTTP keep-alive |

The load-test command pre-generates query pools sized to the request count, with a minimum of 1 000. For `-requests 100000`, it builds 100 000 single-term queries, 100 000 multi-term queries, and 100 000 year filters, then selects by request index. In `balanced` mode this keeps the four traffic modes evenly split without cycling through only a tiny 1 000-query pool.

When the run finishes it prints Markdown tables:

- `Load test totals`: requested total requests, processed total requests, total duration, and requests per second.
- `Load test summary`: overall and per-mode latency, status, error, RPS, p50/p95/p99.
- `Load test configuration`: target URL, index, vocab size, query pool size, workers, seed, timeout, and runtime settings.
- `Query generation details`: actual generated counts for single exact/prefix/misspelled queries and multi-term misspelled/prefix/term-count distribution.

## License

MIT © mg52
