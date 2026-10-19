package store

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

// Memory is an in-process Store for tests. Search is a naive substring
// match, not real full-text search.
type Memory struct {
	mu     sync.Mutex
	pages  map[string]Page
	chunks map[string]ChunkSet
	saves  int
	// embeddings is keyed by "<url hash>#<seq>"; members by url hash.
	embeddings map[string][]float32
	members    map[string]map[int64]bool
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		pages: make(map[string]Page), chunks: make(map[string]ChunkSet),
		embeddings: make(map[string][]float32), members: make(map[string]map[int64]bool),
	}
}

func (m *Memory) Save(_ context.Context, p Page) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.pages[p.URLHash] = p
	m.saves++

	return nil
}

func (m *Memory) SaveChunks(_ context.Context, set ChunkSet) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if set.BatchID != 0 {
		if m.members[set.URLHash] == nil {
			m.members[set.URLHash] = map[int64]bool{}
		}
		m.members[set.URLHash][set.BatchID] = true
	}

	if old, ok := m.chunks[set.URLHash]; ok && old.ContentHash == set.ContentHash {
		if set.CrawlID != 0 {
			old.CrawlID = set.CrawlID
		}
		m.chunks[set.URLHash] = old
		return nil
	}
	if old, ok := m.chunks[set.URLHash]; ok {
		for _, c := range old.Chunks {
			delete(m.embeddings, embKey(set.URLHash, c.Seq))
		}
	}
	m.chunks[set.URLHash] = set

	return nil
}

func embKey(hash string, seq int) string { return fmt.Sprintf("%s#%d", hash, seq) }

func (m *Memory) inScope(hash string, batchID int64) bool {
	return batchID == 0 || m.members[hash][batchID]
}

func (m *Memory) EmbedPending(ctx context.Context, limit int, embed func(ctx context.Context, texts []string) ([][]float32, error)) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	hashes := make([]string, 0, len(m.chunks))
	for h := range m.chunks {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)

	var (
		keys  []string
		texts []string
	)
	for _, h := range hashes {
		set := m.chunks[h]
		for _, c := range set.Chunks {
			k := embKey(h, c.Seq)
			if _, done := m.embeddings[k]; !done && len(keys) < limit {
				keys = append(keys, k)
				texts = append(texts, EmbedText(set.Title, c.Heading, c.Text))
			}
		}
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
	for i, k := range keys {
		m.embeddings[k] = vecs[i]
	}

	return len(keys), nil
}

func (m *Memory) SearchChunks(_ context.Context, query []float32, scope Scope, limit, offset int) ([]PassageHit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var hits []PassageHit
	for h, set := range m.chunks {
		if !m.inScope(h, scope.BatchID) || !urlInScope(set.URL, scope.URLs) {
			continue
		}
		for _, c := range set.Chunks {
			if vec, ok := m.embeddings[embKey(h, c.Seq)]; ok {
				hits = append(hits, PassageHit{URL: set.URL, Title: set.Title, Heading: c.Heading, Text: c.Text, Score: cosine(query, vec), seq: c.Seq})
			}
		}
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].URL != hits[j].URL {
			return hits[i].URL < hits[j].URL
		}
		return hits[i].seq < hits[j].seq
	})
	offset = max(0, min(offset, len(hits)))
	hits = hits[offset:]
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}

	return hits, nil
}

func urlInScope(url string, urls []string) bool {
	if len(urls) == 0 {
		return true
	}
	for _, u := range urls {
		if u == url {
			return true
		}
	}
	return false
}

func (m *Memory) BatchDocuments(_ context.Context, batchID int64, limit int) ([]DocInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	docs := []DocInfo{}
	for h, set := range m.chunks {
		if !m.inScope(h, batchID) || len(set.Chunks) == 0 {
			continue
		}
		d := DocInfo{URL: set.URL, Title: set.Title, Chunks: int64(len(set.Chunks))}
		for _, c := range set.Chunks {
			if _, ok := m.embeddings[embKey(h, c.Seq)]; ok {
				d.Embedded++
			}
		}
		docs = append(docs, d)
	}

	sort.Slice(docs, func(i, j int) bool { return docs[i].URL < docs[j].URL })
	if limit > 0 && len(docs) > limit {
		docs = docs[:limit]
	}

	return docs, nil
}

func (m *Memory) EmbeddingStats(_ context.Context, batchID int64) (EmbedStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var st EmbedStats
	for h, set := range m.chunks {
		if !m.inScope(h, batchID) {
			continue
		}
		for _, c := range set.Chunks {
			st.Total++
			if _, ok := m.embeddings[embKey(h, c.Seq)]; ok {
				st.Embedded++
			}
		}
	}

	return st, nil
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		if i >= len(b) {
			break
		}
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}

	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func (m *Memory) ExportChunks(_ context.Context, crawlID int64, fn func(ChunkRow) error) error {
	m.mu.Lock()
	var sets []ChunkSet
	for _, set := range m.chunks {
		if set.CrawlID == crawlID {
			sets = append(sets, set)
		}
	}
	m.mu.Unlock()

	sort.Slice(sets, func(i, j int) bool { return sets[i].URL < sets[j].URL })
	for _, set := range sets {
		for _, c := range set.Chunks {
			if err := fn(chunkRow(set, c)); err != nil {
				return err
			}
		}
	}

	return nil
}

func (m *Memory) Validators(_ context.Context, urlHash string) (Validators, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, ok := m.pages[urlHash]
	if !ok {
		return Validators{}, false, nil
	}

	return Validators{ETag: p.ETag, LastModified: p.LastModified}, p.ETag != "" || p.LastModified != "", nil
}

func (m *Memory) Search(_ context.Context, q string, limit int) ([]Hit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	q = strings.ToLower(q)

	var hits []Hit
	for _, p := range m.pages {
		hay := strings.ToLower(p.Title + " " + p.Description + " " + p.H1 + " " + p.Snippet)
		if strings.Contains(hay, q) {
			hits = append(hits, Hit{URL: p.URL, Title: p.Title, Description: p.Description, Snippet: p.Snippet})
		}
	}

	sort.Slice(hits, func(i, j int) bool { return hits[i].URL < hits[j].URL })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}

	return hits, nil
}

func (m *Memory) Close() error { return nil }

// Pages returns a copy of everything saved, keyed by URL.
func (m *Memory) Pages() map[string]Page {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make(map[string]Page, len(m.pages))
	for _, p := range m.pages {
		out[p.URL] = p
	}

	return out
}

var _ Store = (*Memory)(nil)
