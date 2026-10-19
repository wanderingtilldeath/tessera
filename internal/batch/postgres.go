package batch

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres is the durable Registry.
type Postgres struct{ pool *pgxpool.Pool }

// NewPostgres wraps a pool whose schema has been migrated (see package db).
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (p *Postgres) CreateBatch(ctx context.Context, name string) (Batch, error) {
	b := Batch{Name: name}
	err := p.pool.QueryRow(ctx, `INSERT INTO batches (name) VALUES ($1) RETURNING id, created_at`, name).
		Scan(&b.ID, &b.CreatedAt)
	if err != nil {
		return Batch{}, fmt.Errorf("batch: create: %w", err)
	}

	return b, nil
}

func (p *Postgres) GetBatch(ctx context.Context, id int64) (Batch, error) {
	var b Batch
	err := p.pool.QueryRow(ctx, `SELECT id, name, created_at FROM batches WHERE id = $1`, id).
		Scan(&b.ID, &b.Name, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Batch{}, ErrNotFound
	}
	if err != nil {
		return Batch{}, fmt.Errorf("batch: get: %w", err)
	}

	return b, nil
}

func (p *Postgres) ListBatches(ctx context.Context, limit int) ([]Batch, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := p.pool.Query(ctx, `SELECT id, name, created_at FROM batches ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("batch: list: %w", err)
	}
	defer rows.Close()

	out := []Batch{}
	for rows.Next() {
		var b Batch
		if err := rows.Scan(&b.ID, &b.Name, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("batch: list scan: %w", err)
		}
		out = append(out, b)
	}

	return out, rows.Err()
}

func (p *Postgres) AddDocument(ctx context.Context, batchID int64, name, kind string) (int64, error) {
	var id int64
	err := p.pool.QueryRow(ctx,
		`INSERT INTO batch_documents (batch_id, name, kind) VALUES ($1, $2, $3) RETURNING id`, batchID, name, kind).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("batch: add document: %w", err)
	}

	return id, nil
}

func (p *Postgres) FinishDocument(ctx context.Context, docID int64, status string, chunks int, errMsg string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE batch_documents SET status = $2, chunk_count = $3, error = $4 WHERE id = $1`, docID, status, chunks, errMsg)
	if err != nil {
		return fmt.Errorf("batch: finish document: %w", err)
	}

	return nil
}

func (p *Postgres) Documents(ctx context.Context, batchID int64) ([]Document, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, name, kind, status, error, chunk_count, created_at FROM batch_documents WHERE batch_id = $1 ORDER BY id`, batchID)
	if err != nil {
		return nil, fmt.Errorf("batch: documents: %w", err)
	}
	defer rows.Close()

	out := []Document{}
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.Name, &d.Kind, &d.Status, &d.Error, &d.ChunkCount, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("batch: documents scan: %w", err)
		}
		out = append(out, d)
	}

	return out, rows.Err()
}

var _ Registry = (*Postgres)(nil)
