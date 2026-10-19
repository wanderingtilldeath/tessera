package ingest

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wanderingtilldeath/docs-rag-ingest/internal/batch"
	"github.com/wanderingtilldeath/docs-rag-ingest/internal/chunk"
	"github.com/wanderingtilldeath/docs-rag-ingest/internal/frontier"
	"github.com/wanderingtilldeath/docs-rag-ingest/internal/store"
)

const maxNameLen = 200

// Result is the outcome of ingesting one file.
type Result struct {
	Name   string `json:"name"`
	Status string `json:"status"` // batch.Done or batch.Failed
	Chunks int    `json:"chunks"`
	Error  string `json:"error,omitempty"`
}

// Process extracts a file's text, stores its chunks in the batch and records
// the outcome as a batch document. A file that cannot be read is reported in
// the Result (and recorded as failed); the returned error is only for
// failures to record anything at all (the database is unreachable).
func Process(ctx context.Context, st store.Store, reg batch.Registry, batchID int64, name string, data []byte) (Result, error) {
	name = cleanName(name)

	kind := KindOf(name)
	if kind == "" {
		kind = "unknown"
	}

	docID, err := reg.AddDocument(ctx, batchID, name, kind)
	if err != nil {
		return Result{}, err
	}

	chunks, perr := build(batchID, name, data)

	res := Result{Name: name, Status: batch.Done, Chunks: len(chunks.Chunks)}
	if perr == nil {
		if err := st.SaveChunks(ctx, chunks); err != nil {
			return Result{}, errors.Join(err, reg.FinishDocument(ctx, docID, batch.Failed, 0, "could not store document"))
		}
	} else {
		res.Status, res.Chunks, res.Error = batch.Failed, 0, perr.Error()
	}

	if err := reg.FinishDocument(ctx, docID, res.Status, res.Chunks, res.Error); err != nil {
		return Result{}, err
	}

	return res, nil
}

// build extracts and chunks one file.
func build(batchID int64, name string, data []byte) (store.ChunkSet, error) {
	title, sections, err := Extract(name, data)
	if err != nil {
		return store.ChunkSet{}, err
	}

	chunks := chunk.Split(sections, chunk.DefaultMaxChars)
	if len(chunks) == 0 {
		return store.ChunkSet{}, errors.New("no extractable text (a scanned PDF needs OCR first)")
	}

	url := fmt.Sprintf("upload://%d/%s", batchID, name)

	return store.ChunkSet{
		URLHash:     frontier.Hash(url),
		BatchID:     batchID,
		URL:         url,
		Title:       title,
		ContentHash: store.ContentHash(data),
		Chunks:      chunks,
	}, nil
}

// cleanName keeps only the file's base name: it becomes part of a URL and is
// shown in the UI, so path components and control characters are dropped.
func cleanName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)

	if runes := []rune(name); len(runes) > maxNameLen {
		name = string(runes[len(runes)-maxNameLen:])
	}
	if name == "" || name == "." || name == ".." || name == "/" {
		name = "untitled"
	}

	return name
}
