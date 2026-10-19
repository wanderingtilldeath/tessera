package batch

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Memory is an in-process Registry for tests.
type Memory struct {
	mu      sync.Mutex
	nextB   int64
	nextD   int64
	batches map[int64]Batch
	docs    map[int64][]Document // by batch id
}

// NewMemory returns an empty in-memory registry.
func NewMemory() *Memory {
	return &Memory{batches: make(map[int64]Batch), docs: make(map[int64][]Document)}
}

func (m *Memory) CreateBatch(_ context.Context, name string) (Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextB++
	b := Batch{ID: m.nextB, Name: name, CreatedAt: time.Now()}
	m.batches[b.ID] = b

	return b, nil
}

func (m *Memory) GetBatch(_ context.Context, id int64) (Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	b, ok := m.batches[id]
	if !ok {
		return Batch{}, ErrNotFound
	}

	return b, nil
}

func (m *Memory) ListBatches(_ context.Context, limit int) ([]Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Batch, 0, len(m.batches))
	for _, b := range m.batches {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}

	return out, nil
}

func (m *Memory) AddDocument(_ context.Context, batchID int64, name, kind string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.batches[batchID]; !ok {
		return 0, ErrNotFound
	}
	m.nextD++
	m.docs[batchID] = append(m.docs[batchID], Document{ID: m.nextD, Name: name, Kind: kind, Status: Processing, CreatedAt: time.Now()})

	return m.nextD, nil
}

func (m *Memory) FinishDocument(_ context.Context, docID int64, status string, chunks int, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, list := range m.docs {
		for i := range list {
			if list[i].ID == docID {
				list[i].Status, list[i].ChunkCount, list[i].Error = status, chunks, errMsg
				return nil
			}
		}
	}

	return ErrNotFound
}

func (m *Memory) Documents(_ context.Context, batchID int64) ([]Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]Document{}, m.docs[batchID]...), nil
}

var _ Registry = (*Memory)(nil)
