package testutil

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"

	"github.com/wanderingtilldeath/docs-rag-ingest/internal/embed"
)

// FakeVector is a deterministic stand-in for an embedding model: a hashed
// bag of words. Texts that share words have a high cosine similarity, which
// is enough to test ranking and scoping without a real model. The task
// prefixes real models use are ignored.
func FakeVector(text string) []float32 {
	text = strings.TrimPrefix(strings.TrimPrefix(text, "search_document: "), "search_query: ")

	v := make([]float32, embed.Dim)
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		h := fnv.New32a()
		h.Write([]byte(w))
		v[h.Sum32()%embed.Dim]++
	}

	return v
}

// FakeEmbedder is an embed.Embedder over FakeVector.
type FakeEmbedder struct{}

func (FakeEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = FakeVector(t)
	}
	return out, nil
}

func (FakeEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	return FakeVector(text), nil
}

var _ embed.Embedder = FakeEmbedder{}

// FakeOllama serves POST /api/embed with FakeVector, like an Ollama server.
func FakeOllama(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if r.URL.Path != "/api/embed" || json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}

		out := make([][]float32, len(req.Input))
		for i, in := range req.Input {
			out[i] = FakeVector(in)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fake", "embeddings": out})
	}))
	t.Cleanup(srv.Close)

	return srv
}
