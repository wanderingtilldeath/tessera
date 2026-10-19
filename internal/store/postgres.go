package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/wanderingtilldeath/docs-rag-ingest/internal/chunk"
)

const (
	defaultBatchSize = 64
	defaultLinger    = 10 * time.Millisecond
)

// upsertSQL keeps an existing row when the content has not changed, which
// skips rewriting the row, its tsvector and its GIN entries on a re-crawl.
const upsertSQL = `
INSERT INTO pages (url_hash, crawl_id, url, status_code, content_type, title, description, h1, snippet,
                   content_hash, etag, last_modified, size_bytes, latency_ms)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (url_hash) DO UPDATE SET
    crawl_id = EXCLUDED.crawl_id, url = EXCLUDED.url, status_code = EXCLUDED.status_code,
    content_type = EXCLUDED.content_type, title = EXCLUDED.title, description = EXCLUDED.description,
    h1 = EXCLUDED.h1, snippet = EXCLUDED.snippet, content_hash = EXCLUDED.content_hash,
    etag = EXCLUDED.etag, last_modified = EXCLUDED.last_modified, size_bytes = EXCLUDED.size_bytes,
    latency_ms = EXCLUDED.latency_ms, fetched_at = now()
WHERE pages.content_hash IS DISTINCT FROM EXCLUDED.content_hash`

type saveRequest struct {
	page Page
	done chan error
}

// Postgres is the production Store. Writes use group commit: concurrent
// Save calls are collected into one batch (up to batchSize rows, or whatever
// arrives within the linger window) and sent in a single round trip. Every
// caller still waits for its own batch to commit before Save returns.
type Postgres struct {
	pool      *pgxpool.Pool
	batchSize int
	linger    time.Duration

	reqs chan saveRequest
	quit chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

// NewPostgres starts the batch writer. Close it to flush and stop.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	s := &Postgres{
		pool:      pool,
		batchSize: defaultBatchSize,
		linger:    defaultLinger,
		reqs:      make(chan saveRequest),
		quit:      make(chan struct{}),
	}

	s.wg.Add(1)
	go s.run()

	return s
}

var errClosed = errors.New("store: closed")

func (s *Postgres) Save(ctx context.Context, p Page) error {
	req := saveRequest{page: p, done: make(chan error, 1)}

	select {
	case s.reqs <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.quit:
		return errClosed
	}

	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Postgres) run() {
	defer s.wg.Done()

	for {
		var batch []saveRequest

		select {
		case req := <-s.reqs:
			batch = append(batch, req)
		case <-s.quit:
			return
		}

		timer := time.NewTimer(s.linger)
	collect:
		for len(batch) < s.batchSize {
			select {
			case req := <-s.reqs:
				batch = append(batch, req)
			case <-timer.C:
				break collect
			case <-s.quit:
				break collect
			}
		}
		timer.Stop()

		s.flush(batch)
	}
}

func (s *Postgres) flush(batch []saveRequest) {
	// Detached from any caller's context: a caller that gave up must not
	// cancel the writes of the others in its batch.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pgBatch := &pgx.Batch{}
	for _, req := range batch {
		p := req.page
		pgBatch.Queue(upsertSQL, p.URLHash, p.CrawlID, p.URL, p.StatusCode, p.ContentType, p.Title,
			p.Description, p.H1, p.Snippet, p.ContentHash, p.ETag, p.LastModified, p.SizeBytes,
			int(p.Latency.Milliseconds()))
	}

	results := s.pool.SendBatch(ctx, pgBatch)
	defer results.Close()

	for _, req := range batch {
		_, err := results.Exec()
		if err != nil {
			err = fmt.Errorf("store: save %s: %w", req.page.URL, err)
		}
		req.done <- err
	}
}

func chunkRow(set ChunkSet, c chunk.Chunk) ChunkRow {
	return ChunkRow{
		ID: fmt.Sprintf("%s-%d", set.URLHash, c.Seq), URL: set.URL, Title: set.Title,
		Heading: c.Heading, Text: c.Text, ContentHash: set.ContentHash,
	}
}

func (s *Postgres) SaveChunks(ctx context.Context, set ChunkSet) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: save chunks %s: %w", set.URL, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	var stored string
	err = tx.QueryRow(ctx, `SELECT content_hash FROM chunks WHERE url_hash = $1 LIMIT 1`, set.URLHash).Scan(&stored)
	switch {
	case err == nil && stored == set.ContentHash:
		if set.CrawlID != 0 {
			if _, err := tx.Exec(ctx, `UPDATE chunks SET crawl_id = $2 WHERE url_hash = $1 AND crawl_id IS DISTINCT FROM $2`,
				set.URLHash, set.CrawlID); err != nil {
				return fmt.Errorf("store: save chunks %s: %w", set.URL, err)
			}
		}
		if err := addToBatch(ctx, tx, set); err != nil {
			return err
		}
		return tx.Commit(ctx)
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("store: save chunks %s: %w", set.URL, err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM chunks WHERE url_hash = $1`, set.URLHash); err != nil {
		return fmt.Errorf("store: save chunks %s: %w", set.URL, err)
	}
	batch := &pgx.Batch{}
	for _, c := range set.Chunks {
		batch.Queue(`INSERT INTO chunks (url_hash, seq, crawl_id, url, title, heading, text, content_hash)
			VALUES ($1, $2, NULLIF($3::bigint, 0), $4, $5, $6, $7, $8)`,
			set.URLHash, c.Seq, set.CrawlID, set.URL, set.Title, c.Heading, c.Text, set.ContentHash)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("store: save chunks %s: %w", set.URL, err)
	}
	if err := addToBatch(ctx, tx, set); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func addToBatch(ctx context.Context, tx pgx.Tx, set ChunkSet) error {
	if set.BatchID == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO batch_chunks (batch_id, url_hash) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		set.BatchID, set.URLHash); err != nil {
		return fmt.Errorf("store: add %s to batch %d: %w", set.URL, set.BatchID, err)
	}

	return nil
}

func (s *Postgres) ExportChunks(ctx context.Context, crawlID int64, fn func(ChunkRow) error) error {
	rows, err := s.pool.Query(ctx, `
		SELECT url_hash, seq, url, title, heading, text, content_hash
		FROM chunks WHERE crawl_id = $1 ORDER BY url, seq`, crawlID)
	if err != nil {
		return fmt.Errorf("store: export chunks: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			hash string
			seq  int
			r    ChunkRow
		)
		if err := rows.Scan(&hash, &seq, &r.URL, &r.Title, &r.Heading, &r.Text, &r.ContentHash); err != nil {
			return fmt.Errorf("store: export chunks scan: %w", err)
		}
		r.ID = fmt.Sprintf("%s-%d", hash, seq)
		if err := fn(r); err != nil {
			return err
		}
	}

	return rows.Err()
}

func (s *Postgres) Validators(ctx context.Context, urlHash string) (Validators, bool, error) {
	var v Validators

	err := s.pool.QueryRow(ctx,
		`SELECT etag, last_modified FROM pages WHERE url_hash = $1`, urlHash,
	).Scan(&v.ETag, &v.LastModified)
	if errors.Is(err, pgx.ErrNoRows) {
		return Validators{}, false, nil
	}
	if err != nil {
		return Validators{}, false, fmt.Errorf("store: validators: %w", err)
	}

	return v, v.ETag != "" || v.LastModified != "", nil
}

func (s *Postgres) Search(ctx context.Context, q string, limit int) ([]Hit, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	rows, err := s.pool.Query(ctx, `
		SELECT url, title, description, snippet, ts_rank(search, query) AS rank
		FROM pages, websearch_to_tsquery('english', $1) AS query
		WHERE search @@ query
		ORDER BY rank DESC, url
		LIMIT $2`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("store: search: %w", err)
	}
	defer rows.Close()

	hits := []Hit{}
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.URL, &h.Title, &h.Description, &h.Snippet, &h.Rank); err != nil {
			return nil, fmt.Errorf("store: search scan: %w", err)
		}
		hits = append(hits, h)
	}

	return hits, rows.Err()
}

func (s *Postgres) EmbedPending(ctx context.Context, limit int, embed func(ctx context.Context, texts []string) ([][]float32, error)) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: embed pending: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	rows, err := tx.Query(ctx, `
		SELECT url_hash, seq, title, heading, text FROM chunks
		WHERE embedding IS NULL ORDER BY url_hash, seq LIMIT $1
		FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return 0, fmt.Errorf("store: embed pending: %w", err)
	}

	type key struct {
		hash string
		seq  int
	}
	var (
		keys  []key
		texts []string
	)
	for rows.Next() {
		var (
			k                     key
			title, heading, chunk string
		)
		if err := rows.Scan(&k.hash, &k.seq, &title, &heading, &chunk); err != nil {
			rows.Close()
			return 0, fmt.Errorf("store: embed pending scan: %w", err)
		}
		keys = append(keys, k)
		texts = append(texts, EmbedText(title, heading, chunk))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("store: embed pending: %w", err)
	}
	if len(keys) == 0 {
		return 0, nil
	}

	vecs, err := embed(ctx, texts)
	if err != nil {
		return 0, err
	}
	if len(vecs) != len(keys) {
		return 0, fmt.Errorf("store: embedder returned %d vectors for %d chunks", len(vecs), len(keys))
	}

	batch := &pgx.Batch{}
	for i, k := range keys {
		batch.Queue(`UPDATE chunks SET embedding = $3::vector WHERE url_hash = $1 AND seq = $2`,
			k.hash, k.seq, vectorLiteral(vecs[i]))
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return 0, fmt.Errorf("store: store embeddings: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("store: embed pending: %w", err)
	}

	return len(keys), nil
}

func (s *Postgres) SearchChunks(ctx context.Context, query []float32, scope Scope, limit, offset int) ([]PassageHit, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: search chunks: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only

	// With a batch filter the HNSW index may return fewer than limit rows
	// that pass the filter; iterative scan (pgvector 0.8+) keeps scanning
	// until it has enough. Older pgvector lacks the setting: carry on without.
	if _, err := tx.Exec(ctx, `SAVEPOINT iter`); err == nil {
		if _, err := tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
			_, _ = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT iter`)
		}
	}

	// The index returns the nearest offset+limit rows (by distance alone, so it can be used); the
	// outer query then orders them fully and takes the page.
	rows, err := tx.Query(ctx, `
		WITH nearest AS MATERIALIZED (
			SELECT c.url_hash, c.seq, c.url, c.title, c.heading, c.text, c.embedding <=> $1::vector AS dist
			FROM chunks c
			WHERE c.embedding IS NOT NULL
			  AND ($2::bigint = 0 OR EXISTS (
			        SELECT 1 FROM batch_chunks b WHERE b.batch_id = $2 AND b.url_hash = c.url_hash))
			  AND (COALESCE(cardinality($4::text[]), 0) = 0 OR c.url = ANY($4::text[]))
			ORDER BY c.embedding <=> $1::vector
			LIMIT ($3::int + $5::int))
		SELECT url, title, heading, text, 1 - dist FROM nearest
		ORDER BY dist, url_hash, seq
		OFFSET $5::int LIMIT $3::int`,
		vectorLiteral(query), scope.BatchID, limit, scope.URLs, offset)
	if err != nil {
		return nil, fmt.Errorf("store: search chunks: %w", err)
	}
	defer rows.Close()

	hits := []PassageHit{}
	for rows.Next() {
		var h PassageHit
		if err := rows.Scan(&h.URL, &h.Title, &h.Heading, &h.Text, &h.Score); err != nil {
			return nil, fmt.Errorf("store: search chunks scan: %w", err)
		}
		hits = append(hits, h)
	}

	return hits, rows.Err()
}

func (s *Postgres) BatchDocuments(ctx context.Context, batchID int64, limit int) ([]DocInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.url, c.title, count(*), count(c.embedding)
		FROM chunks c JOIN batch_chunks b ON b.url_hash = c.url_hash
		WHERE b.batch_id = $1
		GROUP BY c.url_hash, c.url, c.title
		ORDER BY c.url
		LIMIT $2`, batchID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: batch documents: %w", err)
	}
	defer rows.Close()

	docs := []DocInfo{}
	for rows.Next() {
		var d DocInfo
		if err := rows.Scan(&d.URL, &d.Title, &d.Chunks, &d.Embedded); err != nil {
			return nil, fmt.Errorf("store: batch documents scan: %w", err)
		}
		docs = append(docs, d)
	}

	return docs, rows.Err()
}

func (s *Postgres) EmbeddingStats(ctx context.Context, batchID int64) (EmbedStats, error) {
	var st EmbedStats
	err := s.pool.QueryRow(ctx, `
		SELECT count(*), count(c.embedding) FROM chunks c
		WHERE $1::bigint = 0 OR EXISTS (SELECT 1 FROM batch_chunks b WHERE b.batch_id = $1 AND b.url_hash = c.url_hash)`,
		batchID).Scan(&st.Total, &st.Embedded)
	if err != nil {
		return EmbedStats{}, fmt.Errorf("store: embedding stats: %w", err)
	}

	return st, nil
}

// vectorLiteral formats v as a pgvector text literal: "[0.1,0.2,...]".
func vectorLiteral(v []float32) string {
	b := make([]byte, 0, len(v)*10+2)
	b = append(b, '[')
	for i, f := range v {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendFloat(b, float64(f), 'g', -1, 32)
	}

	return string(append(b, ']'))
}

// Close stops the writer after it finishes the batch in progress.
func (s *Postgres) Close() error {
	s.once.Do(func() { close(s.quit) })
	s.wg.Wait()
	return nil
}

var _ Store = (*Postgres)(nil)
