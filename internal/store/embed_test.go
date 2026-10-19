package store_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/wanderingtilldeath/docs-rag-ingest/internal/chunk"
	"github.com/wanderingtilldeath/docs-rag-ingest/internal/frontier"
	"github.com/wanderingtilldeath/docs-rag-ingest/internal/store"
	"github.com/wanderingtilldeath/docs-rag-ingest/internal/testutil"
)

// eachStore runs fn against the in-memory store and, when a test database is
// configured, the Postgres store, so the fake cannot drift from the real thing.
func eachStore(t *testing.T, fn func(t *testing.T, s store.Store)) {
	t.Run("memory", func(t *testing.T) { fn(t, store.NewMemory()) })
	t.Run("postgres", func(t *testing.T) {
		pool := testutil.Postgres(t)
		// batch_chunks references batches; the tests use batch ids 1 to 3.
		if _, err := pool.Exec(context.Background(), `INSERT INTO batches (name) SELECT 'b' FROM generate_series(1, 3)`); err != nil {
			t.Fatal(err)
		}
		s := store.NewPostgres(pool)
		defer s.Close()
		fn(t, s)
	})
}

func chunkSet(url, hash string, batch, crawl int64, texts ...string) store.ChunkSet {
	set := store.ChunkSet{URLHash: frontier.Hash(url), BatchID: batch, CrawlID: crawl, URL: url, Title: url, ContentHash: hash}
	for i, text := range texts {
		set.Chunks = append(set.Chunks, chunk.Chunk{Seq: i, Heading: "H", Text: text})
	}
	return set
}

func embedAll(t *testing.T, s store.Store) int {
	t.Helper()

	total := 0
	for {
		n, err := s.EmbedPending(context.Background(), 5, testutil.FakeEmbedder{}.EmbedDocuments)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return total
		}
		total += n
	}
}

func search(t *testing.T, s store.Store, q string, batch int64) []store.PassageHit {
	t.Helper()

	hits, err := s.SearchChunks(context.Background(), testutil.FakeVector(q), store.Scope{BatchID: batch}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func TestEmbedAndSearchChunks(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		for _, set := range []store.ChunkSet{
			chunkSet("http://a.test/leave", "h1", 1, 1, "annual leave is twenty five days", "carry over unused leave in march"),
			chunkSet("http://a.test/food", "h2", 1, 1, "the cafeteria serves vegetarian lunch"),
		} {
			if err := s.SaveChunks(ctx, set); err != nil {
				t.Fatal(err)
			}
		}

		if st, _ := s.EmbeddingStats(ctx, 0); st.Total != 3 || st.Embedded != 0 {
			t.Fatalf("before embedding: %+v", st)
		}
		if hits := search(t, s, "leave", 0); len(hits) != 0 {
			t.Fatalf("chunks without vectors must not be returned: %+v", hits)
		}

		if n := embedAll(t, s); n != 3 {
			t.Fatalf("embedded %d chunks, want 3", n)
		}
		if st, _ := s.EmbeddingStats(ctx, 0); st.Total != 3 || st.Embedded != 3 {
			t.Fatalf("after embedding: %+v", st)
		}

		hits := search(t, s, "vegetarian lunch", 0)
		if len(hits) != 3 || hits[0].URL != "http://a.test/food" || hits[0].Text != "the cafeteria serves vegetarian lunch" {
			t.Fatalf("hits = %+v", hits)
		}
		if hits[0].Score <= hits[1].Score || hits[0].Score > 1.0001 {
			t.Errorf("scores should fall with distance and stay <= 1: %+v", hits)
		}

		if got, _ := s.SearchChunks(ctx, testutil.FakeVector("leave"), store.Scope{}, 2, 0); len(got) != 2 {
			t.Errorf("limit not applied: %d hits", len(got))
		}
	})
}

func TestBatchScoping(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()

		shared := "http://a.test/shared"
		must := func(set store.ChunkSet) {
			t.Helper()
			if err := s.SaveChunks(ctx, set); err != nil {
				t.Fatal(err)
			}
		}
		must(chunkSet(shared, "same", 1, 1, "shared page about quasars"))
		must(chunkSet("http://a.test/only-b1", "x", 1, 1, "private page about quasars"))
		must(chunkSet("upload://2/notes.md", "y", 2, 0, "uploaded note about quasars"))
		embedAll(t, s)

		urls := func(batch int64) string {
			var u []string
			for _, h := range search(t, s, "quasars", batch) {
				u = append(u, h.URL)
			}
			return strings.Join(u, ",")
		}
		if got := urls(1); !strings.Contains(got, shared) || !strings.Contains(got, "only-b1") || strings.Contains(got, "upload://") {
			t.Errorf("batch 1 = %s", got)
		}
		if got := urls(2); got != "upload://2/notes.md" {
			t.Errorf("batch 2 = %s", got)
		}
		if got := urls(0); strings.Count(got, ",") != 2 {
			t.Errorf("all = %s, want 3 pages", got)
		}
		if got := urls(99); got != "" {
			t.Errorf("unknown batch should match nothing, got %s", got)
		}

		// The same, unchanged page joins a second batch: it is found there
		// at once, without being embedded again.
		must(chunkSet(shared, "same", 2, 3, "shared page about quasars"))
		if n := embedAll(t, s); n != 0 {
			t.Errorf("unchanged page was re-embedded (%d chunks)", n)
		}
		if got := urls(2); !strings.Contains(got, shared) {
			t.Errorf("batch 2 after join = %s", got)
		}
		if got := urls(1); !strings.Contains(got, shared) {
			t.Errorf("joining batch 2 must not remove it from batch 1: %s", got)
		}

		if st, _ := s.EmbeddingStats(ctx, 2); st.Total != 2 || st.Embedded != 2 {
			t.Errorf("batch 2 stats = %+v", st)
		}
	})
}

func TestChangedPageIsReEmbedded(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		if err := s.SaveChunks(ctx, chunkSet("http://a.test/p", "v1", 1, 1, "old text about apples")); err != nil {
			t.Fatal(err)
		}
		embedAll(t, s)

		if err := s.SaveChunks(ctx, chunkSet("http://a.test/p", "v2", 1, 1, "new text about oranges")); err != nil {
			t.Fatal(err)
		}
		if st, _ := s.EmbeddingStats(ctx, 1); st.Total != 1 || st.Embedded != 0 {
			t.Fatalf("a changed page must lose its stale vector: %+v", st)
		}
		if hits := search(t, s, "apples", 1); len(hits) != 0 {
			t.Fatalf("stale content still searchable: %+v", hits)
		}

		embedAll(t, s)
		if hits := search(t, s, "oranges", 1); len(hits) != 1 || hits[0].Text != "new text about oranges" {
			t.Fatalf("hits = %+v", hits)
		}
	})
}

func TestEmbedPendingIsAtomic(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		if err := s.SaveChunks(ctx, chunkSet("http://a.test/p", "v1", 1, 1, "one", "two", "three")); err != nil {
			t.Fatal(err)
		}

		boom := errors.New("model unavailable")
		if _, err := s.EmbedPending(ctx, 10, func(context.Context, []string) ([][]float32, error) { return nil, boom }); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if _, err := s.EmbedPending(ctx, 10, func(context.Context, []string) ([][]float32, error) {
			return [][]float32{testutil.FakeVector("only one")}, nil // wrong count
		}); err == nil {
			t.Fatal("a short response must be an error")
		}
		if st, _ := s.EmbeddingStats(ctx, 1); st.Embedded != 0 {
			t.Fatalf("a failed batch must leave its chunks pending: %+v", st)
		}

		// The text sent to the model carries title and heading for context.
		var sent []string
		if _, err := s.EmbedPending(ctx, 10, func(_ context.Context, texts []string) ([][]float32, error) {
			sent = texts
			return testutil.FakeEmbedder{}.EmbedDocuments(ctx, texts)
		}); err != nil {
			t.Fatal(err)
		}
		if len(sent) != 3 || sent[0] != "http://a.test/p | H\none" {
			t.Fatalf("embedded text = %q", sent)
		}
	})
}

func TestUploadedChunksHaveNoCrawl(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		if err := s.SaveChunks(ctx, chunkSet("upload://1/a.md", "v1", 1, 0, "hello")); err != nil {
			t.Fatal(err)
		}
		// Re-saving unchanged with crawl 0 must not disturb anything either.
		if err := s.SaveChunks(ctx, chunkSet("upload://1/a.md", "v1", 1, 0, "hello")); err != nil {
			t.Fatal(err)
		}

		n := 0
		if err := s.ExportChunks(ctx, 1, func(store.ChunkRow) error { n++; return nil }); err != nil || n != 0 {
			t.Fatalf("export of crawl 1 = %d chunks, %v", n, err)
		}
		if st, _ := s.EmbeddingStats(ctx, 1); st.Total != 1 {
			t.Fatalf("stats = %+v", st)
		}
	})
}

func TestPostgresRejectsWrongDimension(t *testing.T) {
	pool := testutil.Postgres(t)
	if _, err := pool.Exec(context.Background(), `INSERT INTO batches (name) VALUES ('b')`); err != nil {
		t.Fatal(err)
	}
	s := store.NewPostgres(pool)
	defer s.Close()
	ctx := context.Background()

	if err := s.SaveChunks(ctx, chunkSet("http://a.test/p", "v1", 1, 1, "text")); err != nil {
		t.Fatal(err)
	}
	_, err := s.EmbedPending(ctx, 10, func(context.Context, []string) ([][]float32, error) {
		return [][]float32{{1, 2, 3}}, nil
	})
	if err == nil {
		t.Fatal("a vector of the wrong size must be an error, not silently stored")
	}
	if st, _ := s.EmbeddingStats(ctx, 1); st.Embedded != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestSearchWithinChosenDocuments(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		for _, set := range []store.ChunkSet{
			chunkSet("http://a.test/one", "h1", 1, 1, "comets and asteroids"),
			chunkSet("http://a.test/two", "h2", 1, 1, "comets in the kuiper belt", "more comets"),
			chunkSet("upload://1/three.md", "h3", 1, 0, "a comet note"),
		} {
			if err := s.SaveChunks(ctx, set); err != nil {
				t.Fatal(err)
			}
		}
		embedAll(t, s)

		urls := func(scope store.Scope) []string {
			hits, err := s.SearchChunks(ctx, testutil.FakeVector("comets"), scope, 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			var out []string
			for _, h := range hits {
				if !seen[h.URL] {
					seen[h.URL] = true
					out = append(out, h.URL)
				}
			}
			sort.Strings(out)
			return out
		}

		if got := urls(store.Scope{BatchID: 1, URLs: []string{"http://a.test/one", "upload://1/three.md"}}); strings.Join(got, ",") != "http://a.test/one,upload://1/three.md" {
			t.Errorf("two chosen documents: %v", got)
		}
		if got := urls(store.Scope{URLs: []string{"http://a.test/two"}}); strings.Join(got, ",") != "http://a.test/two" {
			t.Errorf("one chosen document: %v", got)
		}
		if got := urls(store.Scope{URLs: []string{"http://a.test/nope"}}); len(got) != 0 {
			t.Errorf("an unknown document matches nothing, got %v", got)
		}
		if got := urls(store.Scope{BatchID: 2, URLs: []string{"http://a.test/one"}}); len(got) != 0 {
			t.Errorf("documents outside the batch are excluded: %v", got)
		}
		if got := urls(store.Scope{BatchID: 1}); len(got) != 3 {
			t.Errorf("no URL filter = whole batch: %v", got)
		}
	})
}

func TestBatchDocuments(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		for _, set := range []store.ChunkSet{
			chunkSet("http://a.test/b", "h1", 1, 1, "x", "y"),
			chunkSet("upload://1/a.md", "h2", 1, 0, "z"),
			chunkSet("http://a.test/other-batch", "h3", 2, 1, "w"),
		} {
			if err := s.SaveChunks(ctx, set); err != nil {
				t.Fatal(err)
			}
		}

		docs, err := s.BatchDocuments(ctx, 1, 10)
		if err != nil || len(docs) != 2 || docs[0].Embedded != 0 || docs[1].Embedded != 0 {
			t.Fatalf("before embedding: %+v, %v", docs, err)
		}
		embedAll(t, s)

		docs, err = s.BatchDocuments(ctx, 1, 10)
		if err != nil || len(docs) != 2 || docs[0].URL != "http://a.test/b" || docs[1].URL != "upload://1/a.md" {
			t.Fatalf("docs = %+v, %v", docs, err)
		}
		if docs[0].Chunks != 2 || docs[1].Chunks != 1 {
			t.Errorf("chunk counts: %+v", docs)
		}
		if docs[0].Embedded != 2 || docs[1].Embedded != 1 {
			t.Errorf("after embedding every chunk should be embedded: %+v", docs)
		}
		if docs, _ := s.BatchDocuments(ctx, 1, 1); len(docs) != 1 {
			t.Errorf("limit not applied: %+v", docs)
		}
		if docs, _ := s.BatchDocuments(ctx, 3, 10); len(docs) != 0 {
			t.Errorf("an empty batch has documents: %+v", docs)
		}
	})
}

func TestSearchChunksPages(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		// 7 chunks that all match, plus two pairs of exactly equal text (equal distance), to check
		// that ties do not shuffle between pages.
		for i, text := range []string{
			"comets one", "comets two two", "comets three three three", "comets four four four four",
			"comets same", "comets same", "comets other",
		} {
			url := "http://a.test/" + string(rune('a'+i))
			if err := s.SaveChunks(ctx, chunkSet(url, "h"+url, 1, 1, text)); err != nil {
				t.Fatal(err)
			}
		}
		embedAll(t, s)
		q := testutil.FakeVector("comets")
		page := func(limit, offset int) []store.PassageHit {
			hits, err := s.SearchChunks(ctx, q, store.Scope{BatchID: 1}, limit, offset)
			if err != nil {
				t.Fatal(err)
			}
			return hits
		}
		key := func(h store.PassageHit) string { return h.URL }

		all := page(7, 0)
		if len(all) != 7 {
			t.Fatalf("all = %d hits", len(all))
		}

		// Pages of 3 are exactly the consecutive slices of the full ordering: no gaps, no overlap.
		var paged []string
		for offset := 0; offset < 9; offset += 3 {
			for _, h := range page(3, offset) {
				paged = append(paged, key(h))
			}
		}
		var want []string
		for _, h := range all {
			want = append(want, key(h))
		}
		if strings.Join(paged, ",") != strings.Join(want, ",") {
			t.Fatalf("pages = %v\nwant   %v", paged, want)
		}

		if got := page(3, 6); len(got) != 1 {
			t.Errorf("last page = %d hits, want 1", len(got))
		}
		if got := page(3, 7); len(got) != 0 {
			t.Errorf("past the end = %d hits, want 0", len(got))
		}
		if got := page(3, -5); len(got) != 3 || key(got[0]) != want[0] {
			t.Errorf("a negative offset must act as 0: %v", got)
		}
	})
}
