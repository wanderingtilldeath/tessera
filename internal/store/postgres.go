package store

import (
	"context"
	"errors"
	"fmt"
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
		if _, err := tx.Exec(ctx, `UPDATE chunks SET crawl_id = $2 WHERE url_hash = $1 AND crawl_id <> $2`,
			set.URLHash, set.CrawlID); err != nil {
			return fmt.Errorf("store: save chunks %s: %w", set.URL, err)
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
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			set.URLHash, c.Seq, set.CrawlID, set.URL, set.Title, c.Heading, c.Text, set.ContentHash)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("store: save chunks %s: %w", set.URL, err)
	}

	return tx.Commit(ctx)
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

// Close stops the writer after it finishes the batch in progress.
func (s *Postgres) Close() error {
	s.once.Do(func() { close(s.quit) })
	s.wg.Wait()
	return nil
}

var _ Store = (*Postgres)(nil)
