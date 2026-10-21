// Package embed turns text into vectors for semantic search. Embedding runs
// in the background, decoupled from crawling: a chunk is saved first and gets
// its vector later, so a slow or absent embedding service never fails a crawl.
package embed

import (
	"context"
	"log/slog"
	"time"
)

// Dim is the vector size the database column holds (vector(768)).
const Dim = 768

// Embedder embeds documents and search queries. Models like nomic-embed-text
// are trained with different prefixes for the two, so they are separate calls.
type Embedder interface {
	// EmbedDocuments returns one vector per text, in order.
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
	// EmbedQuery returns the vector for a search query.
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// Pending is the part of the store the background loop needs.
type Pending interface {
	EmbedPending(ctx context.Context, limit int, embed func(ctx context.Context, texts []string) ([][]float32, error)) (int, error)
}

const (
	// BatchSize is how many chunks one embedding request carries.
	BatchSize = 32

	idleWait   = 3 * time.Second
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

// Run embeds pending chunks until ctx ends. Several processes can run it
// against one database: they claim different chunks. When the embedding
// service is down it backs off and retries; nothing is lost, because chunks
// without a vector simply stay pending.
func Run(ctx context.Context, store Pending, e Embedder, log *slog.Logger) {
	backoff := minBackoff

	for ctx.Err() == nil {
		n, err := store.EmbedPending(ctx, BatchSize, e.EmbedDocuments)

		switch {
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			log.Warn("embedding failed, will retry", "error", err, "retry_in", backoff)
			sleep(ctx, backoff)
			backoff = min(backoff*2, maxBackoff)
		case n == 0:
			backoff = minBackoff
			sleep(ctx, idleWait)
		default:
			backoff = minBackoff
			log.Debug("embedded chunks", "count", n)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
