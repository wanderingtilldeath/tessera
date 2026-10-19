// Package store persists crawled pages and serves full-text search over them.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/wanderingtilldeath/docs-rag-ingest/internal/chunk"
)

// Page is one stored document.
type Page struct {
	URLHash      string
	CrawlID      int64
	URL          string
	StatusCode   int
	ContentType  string
	Title        string
	Description  string
	H1           string
	Snippet      string
	ContentHash  string
	ETag         string
	LastModified string
	SizeBytes    int
	Latency      time.Duration
}

// ChunkSet is all chunks of one page.
type ChunkSet struct {
	URLHash string
	// CrawlID is the crawl that produced the chunks; 0 for an uploaded file.
	CrawlID int64
	// BatchID, when non-zero, adds the page to that batch so batch-scoped
	// search finds it. A page can belong to several batches.
	BatchID     int64
	URL         string
	Title       string
	ContentHash string
	Chunks      []chunk.Chunk
}

// ChunkRow is one exported chunk. ID is stable for a page's content: the URL
// hash plus the chunk's position.
type ChunkRow struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Heading     string `json:"heading"`
	Text        string `json:"text"`
	ContentHash string `json:"content_hash"`
}

// Hit is a search result.
type Hit struct {
	URL         string  `json:"url"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Snippet     string  `json:"snippet"`
	Rank        float64 `json:"rank"`
}

// PassageHit is a semantic search result: one chunk and how close it is to the
// query (cosine similarity, higher is closer).
type PassageHit struct {
	URL     string  `json:"url"`
	Title   string  `json:"title"`
	Heading string  `json:"heading"`
	Text    string  `json:"text"`
	Score   float64 `json:"score"`

	seq int // chunk position; only used to order ties in the in-memory store
}

// Scope limits a semantic search. The zero value searches every chunk.
type Scope struct {
	// BatchID, when non-zero, restricts the search to that batch.
	BatchID int64
	// URLs, when non-empty, restricts it to chunks of these documents.
	URLs []string
}

// DocInfo is one stored document of a batch (a crawled page or an uploaded
// file) and how far along its chunks are.
type DocInfo struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	Chunks   int64  `json:"chunks"`
	Embedded int64  `json:"embedded"`
}

// EmbedStats counts chunks and how many already have an embedding.
type EmbedStats struct {
	Total    int64 `json:"total"`
	Embedded int64 `json:"embedded"`
}

// EmbedText is the text that represents a chunk to the embedding model: the
// page title and heading give a short chunk the context it lacks on its own.
func EmbedText(title, heading, text string) string {
	head := title
	if heading != "" {
		head += " | " + heading
	}
	return head + "\n" + text
}

// Validators are the cache validators saved from the last fetch of a URL.
type Validators struct {
	ETag         string
	LastModified string
}

// Store saves pages and searches them.
type Store interface {
	// Save durably stores p; it returns after the write is committed, so a
	// caller can safely mark the URL done afterwards.
	Save(ctx context.Context, p Page) error
	// SaveChunks replaces a page's chunks atomically. If the stored chunks
	// already have set.ContentHash they are kept and only re-attached to
	// set.CrawlID, so unchanged pages still appear in the new crawl's export.
	SaveChunks(ctx context.Context, set ChunkSet) error
	// ExportChunks calls fn for each chunk of a crawl, ordered by URL then
	// position, and stops at the first error from fn.
	ExportChunks(ctx context.Context, crawlID int64, fn func(ChunkRow) error) error
	// Validators returns the validators from the last stored fetch of a URL.
	Validators(ctx context.Context, urlHash string) (Validators, bool, error)
	// Search returns up to limit pages matching q, best first.
	Search(ctx context.Context, q string, limit int) ([]Hit, error)
	// EmbedPending takes up to limit chunks that have no embedding, passes
	// their EmbedText to embed and stores the returned vectors, all in one
	// transaction: if embed fails nothing is stored and the chunks stay
	// pending. Concurrent callers get different chunks. It returns how many
	// chunks were embedded.
	EmbedPending(ctx context.Context, limit int, embed func(ctx context.Context, texts []string) ([][]float32, error)) (int, error)
	// SearchChunks returns up to limit embedded chunks closest to query, best
	// first, within scope, skipping the first offset. Equal distances are
	// ordered by URL then position, so consecutive pages do not overlap.
	SearchChunks(ctx context.Context, query []float32, scope Scope, limit, offset int) ([]PassageHit, error)
	// BatchDocuments lists the documents of a batch that have chunks, by URL,
	// up to limit.
	BatchDocuments(ctx context.Context, batchID int64, limit int) ([]DocInfo, error)
	// EmbeddingStats counts chunks, in one batch (non-zero batchID) or in all.
	EmbeddingStats(ctx context.Context, batchID int64) (EmbedStats, error)
	// Close flushes and releases resources.
	Close() error
}

// ContentHash is the dedupe fingerprint of a page body.
func ContentHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
