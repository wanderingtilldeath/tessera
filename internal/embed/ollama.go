package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultModel is the Ollama model the 768-dimension column is sized for.
const DefaultModel = "nomic-embed-text"

// Task prefixes nomic-embed-text was trained with.
const (
	documentPrefix = "search_document: "
	queryPrefix    = "search_query: "
)

// Ollama embeds through a local Ollama server's /api/embed endpoint.
type Ollama struct {
	baseURL string
	model   string
	http    *http.Client
}

// NewOllama returns a client for the server at baseURL (for example
// http://ollama:11434). An empty model means DefaultModel.
func NewOllama(baseURL, model string) *Ollama {
	if model == "" {
		model = DefaultModel
	}

	return &Ollama{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		http:    &http.Client{Timeout: 2 * time.Minute},
	}
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error"`
}

func (o *Ollama) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	prefixed := make([]string, len(texts))
	for i, t := range texts {
		prefixed[i] = documentPrefix + t
	}

	return o.embed(ctx, prefixed)
}

func (o *Ollama) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vecs, err := o.embed(ctx, []string{queryPrefix + text})
	if err != nil {
		return nil, err
	}

	return vecs[0], nil
}

func (o *Ollama) embed(ctx context.Context, input []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Model: o.model, Input: input})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: ollama request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("embed: read ollama response: %w", err)
	}

	var out embedResponse
	if err := json.Unmarshal(raw, &out); err != nil && resp.StatusCode == http.StatusOK {
		return nil, fmt.Errorf("embed: decode ollama response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := out.Error
		if msg == "" {
			msg = strings.TrimSpace(string(raw[:min(len(raw), 200)]))
		}
		return nil, fmt.Errorf("embed: ollama returned %d: %s", resp.StatusCode, msg)
	}

	if len(out.Embeddings) != len(input) {
		return nil, fmt.Errorf("embed: ollama returned %d vectors for %d inputs", len(out.Embeddings), len(input))
	}
	for _, v := range out.Embeddings {
		if len(v) != Dim {
			return nil, fmt.Errorf("embed: model %q returned %d dimensions, the database expects %d", o.model, len(v), Dim)
		}
	}

	return out.Embeddings, nil
}

var _ Embedder = (*Ollama)(nil)
