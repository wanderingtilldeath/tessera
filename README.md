# CrawlerX

Feed it documents (page URLs and uploaded PDF, Markdown, HTML or text files), press **Start** in the dashboard, then search inside them by meaning. Under the hood it is a resumable, polite web crawler in Go. Work lives in Postgres (a lease-based queue using `FOR UPDATE SKIP LOCKED`), per-host politeness is shared through Redis, and any number of processes can crawl together. Kill one with `kill -9` and the rest (or a restart) finish its work without re-crawling pages that were already done.

**Only crawl your own site, or sites that allow it.** The API has no authentication and `POST /crawls` makes the server fetch URLs: keep it on a private network.

## Quick start

```sh
make up                                   # postgres+pgvector, redis, ollama, fakesite, 2 crawlers (needs Docker)
open http://localhost:8081                # the dashboard
```

The first `make up` downloads the embedding model (about 270 MB, into `./.ollama`). Crawling works while it downloads; passages become searchable as they are embedded.

In the dashboard:

1. **Add documents**: paste page URLs (one per line), drop or choose files, press **Start**. One line of progress shows what it is doing. *Options* holds the batch name, link depth, path prefixes and allowed hosts (depth 0 ingests exactly the pages you list).
2. **Documents** lists what the batch holds. Tick some to search only those.
3. **Search**: ask in your own words. It searches the ticked documents, else the whole batch.

The page is plain files under `internal/api/static/`, one job each: `index.html` (structure), `style.css` (look), and `js/` modules (`api` is the only one that talks to the server; `add`, `progress`, `library`, `search` and `results` each own one part of the page; `state` and `poller` are shared plumbing). Nothing polls while idle.

The same flow over the API:

```sh
curl -XPOST localhost:8081/batches -d '{"name":"docs","urls":["http://fakesite:9000/p/1"],"max_depth":0}'
curl -XPOST localhost:8081/batches/1/files -F files=@handbook.pdf -F files=@notes.md
curl localhost:8081/batches/1             # progress; "done": true when crawled, read and embedded
curl 'localhost:8081/search/semantic?q=how+many+vacation+days&batch=1'   # omit batch to search everything
curl 'localhost:8081/search/semantic?q=how+many+vacation+days&batch=1&page=2'   # next 10 results
```

Other endpoints: `POST /crawls` and `GET /crawls/1` (a single crawl; `path_prefixes` limits it to URL paths such as `/docs/`, and `crawlerx -seed` takes `-path-prefixes` / `PATH_PREFIXES`), `GET /search?q=nebula` (keyword search over page titles and descriptions), `GET /metrics`.

Without Docker, run Postgres and Redis yourself:

```sh
export DATABASE_URL=postgres://... REDIS_URL=redis://...
go run ./cmd/fakesite -pages 500 &
go run ./cmd/crawlerx -seed http://127.0.0.1:9000/ -max-depth 6 -exit-when-done
```

To **resume** after a crash, restart `crawlerx` *without* `-seed`: it works whatever is pending. (`-seed` starts a new crawl.)

## How it works

| piece | what it does |
|---|---|
| `internal/frontier` | queue + visited set in one table; claim with `SKIP LOCKED`; expired leases are re-claimed (crash recovery); shallowest-first so the depth limit means "within N hops" |
| `internal/politeness` | Redis Lua gate: one slot per host per delay, shared by all processes; a denied worker reschedules the URL instead of sleeping |
| `internal/robots` | RFC 9309 via `temoto/robotstxt`: Allow/Disallow, wildcards, Crawl-delay; 4xx = allow, 5xx = disallow; cached in Redis |
| `internal/fetch` | size cap (2 MiB), HTML only, honest User-Agent, no inline redirects (a redirect target is queued like any link), `Retry-After`, full-jitter backoff |
| `internal/parse`, `internal/urlnorm` | one parse per page; `<base href>`, meta robots; URL canonicalisation with a fuzz test |
| `internal/store` | batched page writes, `tsvector` + GIN full-text search, ETag/Last-Modified, chunk vectors (pgvector) |
| `internal/batch`, `internal/ingest` | batches of documents; text extraction from PDF, Markdown, HTML and text uploads into the same chunks the crawler produces |
| `internal/embed` | background embedding of chunks through Ollama; crawling never waits on it, and chunks without a vector just stay pending until the model is reachable |

Config is flags with env fallbacks: see `go run ./cmd/crawlerx -h`.

## Tests

```sh
go test -short ./...                      # no services needed
make test                                 # all tests, -race, needs TEST_DATABASE_URL and TEST_REDIS_URL
```

The suite includes the original deadlock regression (`internal/crawler/deadlock_test.go`) and end-to-end `kill -9` / SIGTERM / two-process tests (`internal/e2e`). Benchmark: `make bench`, results and caveats in [docs/BENCHMARK.md](docs/BENCHMARK.md). Concept notes: [docs/CONCEPTS.md](docs/CONCEPTS.md).

## Semantic search and embeddings

Chunks (heading-aware pieces of at most 1500 characters, taken from `<main>`, else `<article>`, else `<body>`, without nav, header, footer, aside or forms) are embedded with `nomic-embed-text` (768 dimensions) through a local [Ollama](https://ollama.com) and stored in Postgres with [pgvector](https://github.com/pgvector/pgvector). Search embeds your question the same way and returns the closest passages, with their heading and source. Re-crawling or re-uploading an unchanged document keeps its vectors; a changed one is re-embedded.

| setting | meaning |
|---|---|
| `EMBED_URL` / `-embed-url` | Ollama base URL (compose sets `http://ollama:11434`). Empty turns embedding off: chunks are still stored, and `/search/semantic` answers 503. |
| `EMBED_MODEL` / `-embed-model` | model name, default `nomic-embed-text`. It must produce 768-dimension vectors; another size needs a new migration. |

**Postgres must have pgvector.** Migration 00003 runs `CREATE EXTENSION vector`, which needs a role allowed to do that. `make up` uses the `pgvector/pgvector` image. If you bring your own database (including the one behind `TEST_DATABASE_URL`), use a pgvector 0.8+ build.

Uploads: PDF, Markdown, HTML or text, up to 25 MB per file, 50 files per request, 200 MB per request. Only the extracted text is stored, not the file. A scanned PDF has no text layer and is reported as unreadable (no OCR). Page URLs must be HTML pages; to ingest a PDF, upload it.

Search results come in pages: `page` (from 1) and `limit` (page size, default 10, at most 50). The response has `page`, `page_size` and `has_more`. A search returns at most 100 results in total, so `page` past that is a 400. In the dashboard, results show the matched text in a quote block with its source below it: a link for crawled pages, a plain file name for uploads.

Each batch is searched on its own: the same URL may belong to several batches. `GET /crawls/{id}/export` still streams one crawl's chunks as JSONL (`{id, url, title, heading, text, content_hash}`) if you would rather embed them yourself.
