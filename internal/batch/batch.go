// Package batch records batches: named sets of documents (crawled URLs and
// uploaded files) that are searched together. The crawls themselves live in
// the frontier; this package holds the batch rows and the uploaded files.
package batch

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a batch does not exist.
var ErrNotFound = errors.New("batch: not found")

// Document statuses.
const (
	Processing = "processing"
	Done       = "done"
	Failed     = "failed"
)

// Batch is a named set of documents.
type Batch struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Document is one uploaded file.
type Document struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	ChunkCount int       `json:"chunks"`
	CreatedAt  time.Time `json:"created_at"`
}

// Registry stores batches and their uploaded documents.
type Registry interface {
	CreateBatch(ctx context.Context, name string) (Batch, error)
	GetBatch(ctx context.Context, id int64) (Batch, error)
	// ListBatches returns the most recent batches, newest first.
	ListBatches(ctx context.Context, limit int) ([]Batch, error)

	// AddDocument records an upload in the processing state and returns its id.
	AddDocument(ctx context.Context, batchID int64, name, kind string) (int64, error)
	// FinishDocument sets the outcome of an upload: Done with its chunk count,
	// or Failed with a reason.
	FinishDocument(ctx context.Context, docID int64, status string, chunks int, errMsg string) error
	// Documents returns a batch's uploads in upload order.
	Documents(ctx context.Context, batchID int64) ([]Document, error)
}
