# Features

Living ledger of subsystems. Update with `/ledger-update` after behavioral or architectural changes.

### Durable Frontier (crawl queue + visited set)
- **Status**: Active
- **Last Updated**: 2026-10-20 @ 8410269
- **Entry Points / Key Files**:
  - `internal/frontier/frontier.go`: `Frontier` interface, status model, crawl scope (`AllowsURL`)
  - `internal/frontier/postgres.go`: `Claim`, `Enqueue`, `Finish`, `Reschedule`, `Release`
  - `internal/frontier/memory.go`: in-process implementation for tests and Redis-less runs
  - `internal/db/migrations/00001_init.sql`: `crawls`, `frontier` tables and indexes
- **Implementation Summary**: One row per canonical URL per crawl serves as both the work queue and the visited set (primary key `(crawl_id, url_hash)`). Workers claim rows with `FOR UPDATE SKIP LOCKED` and a lease (default 1 minute); an expired lease is re-claimed, which is the crash-recovery mechanism. Claims order by depth first, so a depth limit means "within N hops".
- **Data & Schema Dependencies**: Postgres `crawls`, `frontier`; partial index `frontier_claim_idx` covers only `pending`/`leased` rows.
- **Production Trade-Offs**: At-least-once processing: a worker that dies mid-fetch causes a refetch, bounded by `MAX_ATTEMPTS` (default 5) before the row is `dead`. Postgres is the queue, so claim throughput is limited by one table and one index; fine for thousands of URLs/s, not millions.

### Crawl Worker Loop
- **Status**: Modified
- **Last Updated**: 2026-10-21 @ b289496+uncommitted
- **Entry Points / Key Files**:
  - `cmd/tessera/main.go`: process bootstrap, flags, signal handling, `-seed`, `-exit-when-done`
  - `internal/crawler/crawler.go`: `Run`, `worker`, `process`, `handlePage`, `handleRedirect`
  - `internal/config/config.go`: flag > env > default configuration
- **Implementation Summary**: Each process runs N workers (default 8) that claim one URL at a time and run: robots check, politeness gate, fetch, then parse, enqueue links, save page and chunks, and finish the row. On shutdown claiming stops immediately while in-flight fetches get a grace period. Anything that cannot reach a terminal state is released so another worker takes it at once.
- **Data & Schema Dependencies**: `frontier`, `pages`, `chunks`; Redis (gate, robots cache); Prometheus metrics.
- **Production Trade-Offs**: Busy hosts are rescheduled instead of slept on, so workers never block on one slow host. Redirects are not followed inline: the target is enqueued as a new link so robots, scope and depth limits still apply. Lease must exceed fetch timeout (validated at startup).

### Politeness Gate (per-host rate limiting)
- **Status**: Modified
- **Last Updated**: 2026-10-21 @ b289496+uncommitted
- **Entry Points / Key Files**:
  - `internal/politeness/politeness.go`: `Gate` interface, Redis Lua scripts, in-memory twin
- **Implementation Summary**: A Lua script in Redis reserves the next request slot for a host atomically, using Redis server time so clock skew between crawlers does not matter. It never sleeps: a denied caller gets the wait time and reschedules the URL. `Penalize` pushes a host's next slot out after 429/503 with `Retry-After`.
- **Data & Schema Dependencies**: Redis keys `tessera:host:<host>` with PX expiry (prefix renamed from `crawlerx:`; old keys are orphaned and harmless).
- **Production Trade-Offs**: Redis is a hard dependency for multi-process politeness (a gate outage reschedules work by 2s rather than failing it). The gate is a single key per host, so one very hot host is serialized by design.

### Robots.txt Compliance
- **Status**: Active
- **Last Updated**: 2026-10-20 @ 8410269
- **Entry Points / Key Files**:
  - `internal/robots/robots.go`: `Check`, shared Redis cache, TTLs
- **Implementation Summary**: Evaluates RFC 9309 rules (Allow/Disallow, wildcards, Crawl-delay) through `temoto/robotstxt`. 4xx on robots.txt means allow, 5xx means disallow. The raw response is cached per host in a shared cache so many processes fetch it once per TTL.
- **Data & Schema Dependencies**: Redis cache (good results 24h, 5xx/unparsable 5m by default).
- **Production Trade-Offs**: Caching raw bytes keeps the cache format independent of the parser. A site that changes robots.txt is honored only after the TTL expires. Fail-closed on 5xx is conservative and can stall a crawl during an origin outage.

### HTTP Fetcher & Retry
- **Status**: Active
- **Last Updated**: 2026-10-20 @ 8410269
- **Entry Points / Key Files**:
  - `internal/fetch/fetch.go`: size cap, HTML-only, conditional GET (ETag/Last-Modified)
  - `internal/fetch/retry.go`: retryable statuses, full-jitter backoff, `Retry-After`
- **Implementation Summary**: Fetches with an honest User-Agent, a 2 MiB body cap and content-type filtering (non-HTML is `skipped`). Retryable statuses back off with full jitter and respect `Retry-After`. Stored validators allow 304 responses to short-circuit re-crawls.
- **Data & Schema Dependencies**: `pages.etag`, `pages.last_modified`.
- **Production Trade-Offs**: Truncated bodies are indexed with a note rather than failed. There is no SSRF protection in the fetcher itself: the API accepts arbitrary URLs (see API subsystem).

### Parsing, URL Canonicalisation & Chunking
- **Status**: Active
- **Last Updated**: 2026-10-20 @ 8410269
- **Entry Points / Key Files**:
  - `internal/parse/parse.go`: single-pass HTML parse (title, links, `<base href>`, meta robots)
  - `internal/parse/sections.go`: main-content extraction into heading-scoped sections
  - `internal/chunk/chunk.go`: `Split` into chunks of at most 1500 characters
  - `internal/urlnorm/urlnorm.go`: `Canonicalize`, fuzz-tested
- **Implementation Summary**: Content is taken from `<main>`, else `<article>`, else `<body>`, dropping nav, header, footer, aside and forms. Sections are packed into chunks paragraph by paragraph, then sentence by sentence, so chunks rarely start mid-sentence. Canonical URLs are the identity for deduplication (`url_hash` is sha256 of the canonical form).
- **Data & Schema Dependencies**: Produces `pages` rows and `chunks` rows; `internal/urlnorm/testdata/fuzz` holds fuzz seeds.
- **Production Trade-Offs**: Heuristic main-content extraction can miss pages with unusual layouts. Chunk size is a character cap, not a token count, so it is only loosely tied to the embedding model's limit.

### Page & Chunk Store
- **Status**: Active
- **Last Updated**: 2026-10-20 @ 8410269
- **Entry Points / Key Files**:
  - `internal/store/store.go`: `Store` interface and DTOs
  - `internal/store/postgres.go`: `Save` (batched writer), `SaveChunks`, `Search`, `ExportChunks`
  - `internal/store/memory.go`: in-memory implementation
  - `internal/db/migrations/00002_chunks.sql`: `chunks` table
- **Implementation Summary**: Page writes are batched through a background goroutine to cut round trips. `SaveChunks` compares `content_hash`: unchanged content keeps its rows (and vectors); changed content deletes and reinserts the page's chunks, which clears their embeddings so they are re-embedded. Keyword search uses a generated weighted `tsvector` with a GIN index.
- **Data & Schema Dependencies**: `pages`, `chunks`, `batch_chunks`; `GET /crawls/{id}/export` streams chunks as JSONL.
- **Production Trade-Offs**: `chunks` is keyed by `url_hash`, shared across crawls, so `crawl_id` is only the latest crawl to touch a page; batch membership therefore lives in `batch_chunks`. Chunk replacement is delete-then-insert in one transaction, which briefly locks that page's rows.

### Batches & Document Upload
- **Status**: Active
- **Last Updated**: 2026-10-20 @ 8410269
- **Entry Points / Key Files**:
  - `internal/api/batches.go`: `createBatch`, `uploadFiles`, `getBatch` (progress and `done` computation)
  - `internal/batch/batch.go`: `Registry` interface
  - `internal/batch/postgres.go`, `internal/batch/memory.go`: implementations
  - `internal/ingest/ingest.go`, `internal/ingest/extract.go`, `internal/ingest/pdftext.go`: PDF/Markdown/HTML/text extraction
  - `internal/db/migrations/00003_batches_embeddings.sql`: `batches`, `batch_documents`, `batch_chunks`
- **Implementation Summary**: A batch is a named set of documents: one crawl per submitted URL (depth 0 by default) plus uploaded files. Uploads are extracted to text, chunked and stored; only the text is kept, not the file. `GET /batches/{id}` reports done when every crawl is finished, every upload processed and (if embedding is on) every chunk embedded.
- **Data & Schema Dependencies**: `batches`, `batch_documents`, `batch_chunks`, `crawls.batch_id`, `chunks`.
- **Production Trade-Offs**: Limits: 100 URLs per batch, 50 files per request, 25 MB per file, 200 MB per request. Uploads are processed inline in the request, so large PDFs hold a request open. Scanned PDFs have no OCR and are reported failed. The PDF decoder (`internal/ingest/pdftext.go`, ToUnicode maps) is custom code and the likeliest source of extraction bugs.

### Embedding Pipeline
- **Status**: Active
- **Last Updated**: 2026-10-20 @ 8410269
- **Entry Points / Key Files**:
  - `internal/embed/embed.go`: `Run` background loop, `Embedder` interface
  - `internal/embed/ollama.go`: Ollama client (`nomic-embed-text`, 768 dims)
  - `internal/store/postgres.go`: `EmbedPending`
- **Implementation Summary**: Crawling never waits on embeddings. A loop claims 32 chunks with `embedding IS NULL` using `FOR UPDATE SKIP LOCKED`, embeds them, and writes the vectors in the same transaction. When Ollama is down it backs off from 1s to 30s and chunks simply stay pending.
- **Data & Schema Dependencies**: `chunks.embedding vector(768)`, partial index `chunks_unembedded_idx`; Ollama HTTP API (`EMBED_URL`, `EMBED_MODEL`).
- **Production Trade-Offs**: The embedding call happens while holding a row-lock transaction, so a slow model keeps those rows locked (other workers skip them). Changing the model dimension requires a new migration. An empty `EMBED_URL` disables embedding and semantic search answers 503.

### Semantic Search
- **Status**: Active
- **Last Updated**: 2026-10-20 @ 8410269
- **Entry Points / Key Files**:
  - `internal/api/batches.go`: `searchSemantic` (validation, paging, scope)
  - `internal/store/postgres.go`: `SearchChunks`, `EmbeddingStats`
  - `internal/db/migrations/00003_batches_embeddings.sql`: HNSW cosine index on `chunks.embedding`
- **Implementation Summary**: The query is embedded with the query-side prefix, then pgvector finds the nearest `offset+limit` chunks via the HNSW index, optionally filtered to a batch and/or specific URLs. For filtered searches it enables `hnsw.iterative_scan = relaxed_order` (pgvector 0.8+) so the filter does not starve results. The response says how many chunks are still pending embedding.
- **Data & Schema Dependencies**: `chunks`, `batch_chunks`; Ollama for the query vector.
- **Production Trade-Offs**: Results are capped at 100 total (paging past that is a 400) because vector search has no natural end. The query embedding is an external call on the request path (30s timeout, 502 when Ollama is down). Recall is approximate (HNSW). No reranking and no hybrid keyword+vector scoring.

### HTTP API & Dashboard
- **Status**: Active
- **Last Updated**: 2026-10-20 @ 8410269
- **Entry Points / Key Files**:
  - `internal/api/api.go`: routes (`/crawls`, `/batches`, `/search`, `/search/semantic`, `/healthz`, `/metrics`), keyword search
  - `internal/api/static/index.html`, `internal/api/static/style.css`: dashboard structure and look
  - `internal/api/static/js/`: `api.js` (only module that calls the server), `add`, `progress`, `library`, `search`, `results`, plus `state` and `poller`
- **Implementation Summary**: A Go 1.22+ `ServeMux` with method-and-path patterns serves JSON endpoints and the embedded static dashboard. Request bodies are size-limited and JSON decoding rejects unknown fields. The dashboard is plain ES modules with no build step; nothing polls while idle.
- **Data & Schema Dependencies**: Reads and writes through the `Frontier`, `Store` and `Registry` interfaces; static files embedded into the binary with `//go:embed static` in `internal/api/api.go`.
- **Production Trade-Offs**: **No authentication**, and `POST /crawls` and `POST /batches` make the server fetch arbitrary URLs (SSRF-prone), so it must sit on a private network. No per-client rate limiting.

### Observability
- **Status**: Modified
- **Last Updated**: 2026-10-21 @ b289496+uncommitted
- **Entry Points / Key Files**:
  - `internal/metrics/metrics.go`: Prometheus collectors (pages, fetch latency, queue depth, retries, robots blocks, per-host inflight, gate deferrals)
  - `deploy/prometheus.yml`: scrape config (compose `metrics` profile)
  - `cmd/tessera/main.go`: `/metrics`, optional pprof server, structured `slog` logging
- **Implementation Summary**: Metric names use the `tessera_` prefix (renamed from `crawlerx_`; old dashboards and alerts break). Counters and histograms are registered per process and exposed at `/metrics`. Queue depth is sampled periodically from the frontier. Logs are structured via `log/slog`.
- **Data & Schema Dependencies**: Prometheus (optional); no persistent state.
- **Production Trade-Offs**: Per-host inflight gauge has host-cardinality, which can grow large when crawling many hosts.

### Test Harness, Fake Site & Benchmark
- **Status**: Active
- **Last Updated**: 2026-10-20 @ 8410269
- **Entry Points / Key Files**:
  - `cmd/fakesite/main.go`, `internal/fakesite/fakesite.go`: synthetic site with a `-chaos` mode
  - `cmd/bench/main.go`: throughput benchmark (`make bench`)
  - `internal/e2e/e2e_test.go`, `internal/e2e/batch_test.go`: `kill -9`, SIGTERM and two-process tests
  - `internal/crawler/deadlock_test.go`: original deadlock regression
  - `internal/testutil/testutil.go`: per-test Postgres schemas
  - `Makefile`: `test-short`, `test`, `lint`, `fuzz`, `bench`
- **Implementation Summary**: Unit tests run without services (`go test -short`). Integration tests need Postgres with pgvector and Redis and run with `-p 1` because packages share one Redis. The fake site serves thousands of linked pages with injected failures so crawl behavior can be verified end to end.
- **Data & Schema Dependencies**: `TEST_DATABASE_URL`, `TEST_REDIS_URL`; each test gets its own schema.
- **Production Trade-Offs**: Integration tests serialize (`-p 1`), so the full suite is slow. There is no CI workflow (removed in commit `f6161b2`; deployments are manual), so tests only run when someone runs them.

### Deployment & Local Stack
- **Status**: Modified
- **Last Updated**: 2026-10-21 @ b289496+uncommitted
- **Entry Points / Key Files**:
  - `docker-compose.yml`: Postgres (pgvector 0.8 / PG17), Redis, Ollama, model pull, fakesite, two crawlers, optional Prometheus
  - `Dockerfile`: multi-stage build, one image per `TARGET` (`tessera` or `fakesite`), distroless nonroot runtime
  - `internal/db/db.go`: opens the pool and applies goose migrations embedded in the binary (`internal/db/migrations/`)
- **Implementation Summary**: `make up` starts the whole stack, with two crawler processes demonstrating multi-process coordination. Migrations run from the embedded SQL files at startup. The model (~270 MB) is pulled into `./.ollama` on first start.
- **Data & Schema Dependencies**: Postgres needs pgvector 0.8+ and a role allowed to `CREATE EXTENSION vector` (migration 00003).
- **Production Trade-Offs**: Project renamed from CrawlerX to Tessera: after upgrading run `make down` then `make up` so Postgres is recreated with the new `tessera` user and database. Compose credentials are development defaults (`tessera`/`tessera`) and Postgres and Redis ports are published to the host. Both crawlers run the API, so any instance can serve requests. There is no orchestration config beyond compose.
