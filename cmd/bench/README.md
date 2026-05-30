# Bench Tools

`cmd/bench` is a small CLI for generating benchmark data and measuring Search52.

```bash
go run ./cmd/bench <command> [flags]
```

Commands:

| Command | Purpose |
|---|---|
| `vocab` | Generate mock vocabulary or extract real tokens from JSON |
| `datagen` | Generate dummy JSON documents |
| `benchmark` | Run an in-process benchmark from a JSON file |
| `loadtest` | Send parallel HTTP requests to a running search service |

## 1. Dummy Vocab Benchmark

This flow uses generated vocabulary and generated documents. It does not require a real dataset.

### Generate Mock Vocabulary

```bash
go run ./cmd/bench vocab \
  -size 100000 \
  -out vocab.txt
```

| Flag | Default | Description |
|---|---|---|
| `-size` | `100000` | Number of unique mock words to generate when `-data` is not set |
| `-out` | `vocab.txt` | Output file |
| `-seed` | `42` | RNG seed |
| `-data` | `""` | Optional JSON document file to extract vocabulary from |
| `-fields` | `title,tags` | Comma-separated fields to extract when `-data` is set |

When `-data` is provided, `-size` is ignored and all unique tokens from the configured fields are written:

```bash
go run ./cmd/bench vocab \
  -data mb_5m.json \
  -fields title,artist,album \
  -out mb_vocab.txt
```

### Generate Dummy Documents

```bash
go run ./cmd/bench datagen \
  -count 1000000 \
  -vocab vocab.txt \
  -out data.json
```

For a larger run:

```bash
go run ./cmd/bench datagen \
  -count 5000000 \
  -vocab vocab.txt \
  -out data5m.json
```

Generated documents look like this:

```json
{"id":"d0","title":"rukpttue nhuks ghejk vgzadxl ptneuv","tags":"ghejk nhuks","year":2017}
```

Fields:

| Field | Shape |
|---|---|
| `title` | 3-20 words |
| `tags` | 1-10 words |
| `year` | 2000-2024 |

`datagen` flags:

| Flag | Default | Description |
|---|---|---|
| `-count` | `1000000` | Number of documents |
| `-vocab` | `vocab.txt` | Vocabulary file from the `vocab` command |
| `-out` | `data.json` | Output JSON file |
| `-seed` | `42` | RNG seed |

### Run In-Process Benchmark

This reads the JSON file, builds the engine in memory, warms up, then measures search latency. No HTTP server is needed.

```bash
go run ./cmd/bench benchmark \
  -data data.json \
  -vocab vocab.txt \
  -queries 10000 \
  -warmup 1000
```

| Flag | Default | Description |
|---|---|---|
| `-data` | `data.json` | JSON data file |
| `-vocab` | `vocab.txt` | Vocabulary file |
| `-queries` | `5000` | Queries measured per mode |
| `-warmup` | `500` | Warmup iterations before measuring |
| `-result-size` | `100` | Top-k result size |
| `-seed` | `99` | RNG seed for query generation |

The in-process benchmark uses the same query-shape ideas as the HTTP load test: single exact/prefix/misspelled queries and multi-term queries with optional prefix/misspell behavior.

## 2. Using A File HTTP Loadtest

This flow uses a JSON file, creates an index through the HTTP API, uploads the file, then sends parallel search traffic.

### Example With `mb_5m.json`

First extract vocabulary from the same text fields that will be indexed:

```bash
go run ./cmd/bench vocab \
  -data mb_5m.json \
  -fields title,artist,album \
  -out mb_vocab.txt
```

Start the HTTP service:

```bash
go run ./cmd/service
```

Create the index:

```bash
curl -X POST http://127.0.0.1:8080/create-index \
  -H 'Content-Type: application/json' \
  -d '{
    "indexName":"mb5m",
    "indexFields":["title","artist","album"],
    "filters":["year"],
    "resultCount":100
  }'
```

Upload the JSON file:

```bash
curl -X POST 'http://127.0.0.1:8080/add-to-index?indexName=mb5m' \
  -F 'file=@mb_5m.json'
```

You can verify the index is loaded before hammering it:

```bash
curl http://127.0.0.1:8080/list-indexes
```

Run the load test:

```bash
go run ./cmd/bench loadtest \
  -url http://127.0.0.1:8080/search \
  -vocab mb_vocab.txt \
  -index mb5m \
  -requests 100000 \
  -workers 16 \
  -seed 99 \
  -mode-mix balanced
```

### Loadtest Flags

| Flag | Default | Description |
|---|---|---|
| `-url` | `http://localhost:8080/search` | Search endpoint |
| `-vocab` | `vocab.txt` | Vocabulary file |
| `-index` | `bench` | Index name |
| `-workers` | `8` | Number of parallel worker goroutines |
| `-requests` | `10000` | Total requests to send |
| `-filter-pct` | `50` | Percentage of requests with a year filter in random mode |
| `-multi-pct` | `50` | Percentage of multi-term queries in random mode |
| `-mode-mix` | `random` | `random` uses `filter-pct`/`multi-pct`; `balanced` cycles evenly through the four benchmark modes |
| `-timeout` | `10s` | Per-request HTTP timeout |
| `-seed` | current time | RNG seed for reproducibility |
| `-keepalive` | `true` | Use HTTP keep-alive |

### Parallelism

`-workers` controls HTTP concurrency. For example:

```bash
-requests 1000000 -workers 16
```

means the script sends 1,000,000 total requests with 16 parallel worker goroutines. Each worker sends a request, reads and closes the response, then takes the next request from a shared atomic counter.

### Query Generation

The load-test command pre-generates query pools sized to the request count, with a minimum of 1,000. For `-requests 100000`, it builds:

| Pool | Size |
|---|---:|
| Single-term queries | 100000 |
| Multi-term queries | 100000 |
| Year filters | 100000 |

Single-term queries:

| Type | Target Share |
|---|---:|
| Exact | 65% |
| Prefix | 25% |
| Misspelled | 10% |

Multi-term queries:

| Behavior | Target Share |
|---|---:|
| 2-4 terms | evenly randomized |
| One misspelled token | 10% |
| Last token prefix-truncated | 25% when not misspelled |

In `balanced` mode, request index modulo 4 cycles evenly through:

| Mode |
|---|
| Single / NoFilter |
| Single / Filter |
| Multi / NoFilter |
| Multi / Filter |

In `random` mode, `-filter-pct` and `-multi-pct` decide the traffic mix.

### Output Tables

When the run finishes it prints Markdown tables:

| Table | Contents |
|---|---|
| `Load test totals` | Requested total requests, processed total requests, total duration, RPS |
| `Load test summary` | Overall and per-mode latency, status counts, errors, RPS, p50/p95/p99 |
| `Load test configuration` | URL, index, vocab size, query pool size, workers, seed, timeout, runtime settings |
| `Query generation details` | Exact/prefix/misspelled counts and multi-term distribution |

If all requests return `404`, the load test did run, but it probably hit a service where the requested index was not created or loaded. Check `/list-indexes` first.
