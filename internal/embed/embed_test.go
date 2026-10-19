package embed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func vec(n int, fill float32) []float32 {
	v := make([]float32, n)
	for i := range v {
		v[i] = fill
	}
	return v
}

func TestOllamaPrefixesAndOrder(t *testing.T) {
	var got embedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		out := make([][]float32, len(got.Input))
		for i := range out {
			out[i] = vec(Dim, float32(i+1))
		}
		_ = json.NewEncoder(w).Encode(embedResponse{Embeddings: out})
	}))
	defer srv.Close()

	o := NewOllama(srv.URL+"/", "")
	docs, err := o.EmbedDocuments(context.Background(), []string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != DefaultModel || got.Input[0] != "search_document: alpha" || got.Input[1] != "search_document: beta" {
		t.Errorf("request = %+v", got)
	}
	if len(docs) != 2 || docs[0][0] != 1 || docs[1][0] != 2 {
		t.Errorf("vectors out of order: %v", docs)
	}

	q, err := o.EmbedQuery(context.Background(), "what?")
	if err != nil || len(q) != Dim || got.Input[0] != "search_query: what?" {
		t.Errorf("query: %v %v, request %+v", len(q), err, got)
	}
}

func TestOllamaErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		handler http.HandlerFunc
		want    string
	}{
		"model missing": {func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"model \"nomic-embed-text\" not found, try pulling it first"}`)
		}, "not found, try pulling"},
		"wrong dimension": {func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float32{vec(384, 1)}})
		}, "384 dimensions"},
		"wrong count": {func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(embedResponse{})
		}, "0 vectors for 1 inputs"},
		"not json": {func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>") }, "decode"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			_, err := NewOllama(srv.URL, "").EmbedDocuments(context.Background(), []string{"x"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}

	// Server down: a clear error, not a panic.
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	if _, err := NewOllama(srv.URL, "").EmbedQuery(context.Background(), "x"); err == nil {
		t.Fatal("expected a connection error")
	}
}

// stubEmbedder is never really called: fakePending ignores the embed func.
type stubEmbedder struct{}

func (stubEmbedder) EmbedDocuments(context.Context, []string) ([][]float32, error) { return nil, nil }
func (stubEmbedder) EmbedQuery(context.Context, string) ([]float32, error)         { return nil, nil }

type fakePending struct {
	mu    sync.Mutex
	calls int
	// fail is how many initial calls fail; work is how many chunks remain.
	fail, work int
}

func (f *fakePending) EmbedPending(ctx context.Context, limit int, _ func(context.Context, []string) ([][]float32, error)) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	if f.fail > 0 {
		f.fail--
		return 0, errors.New("ollama is down")
	}
	n := min(limit, f.work)
	f.work -= n
	return n, nil
}

func (f *fakePending) snapshot() (calls, work int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.work
}

func TestRunDrainsWorkAndSurvivesFailures(t *testing.T) {
	f := &fakePending{fail: 1, work: 3 * BatchSize}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, f, stubEmbedder{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, work := f.snapshot(); work == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("work was not drained after a transient failure")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop when its context ended")
	}

	if calls, _ := f.snapshot(); calls < 4 { // 1 failure + 3 batches
		t.Errorf("calls = %d, want at least 4", calls)
	}
}
