-- +goose Up
-- pgvector goes into public so every schema (including the per-test schemas)
-- resolves the vector type through its search_path.
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;

-- A batch is a named set of documents submitted together: one crawl per URL
-- plus any uploaded files.
CREATE TABLE batches (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE crawls ADD COLUMN batch_id BIGINT REFERENCES batches (id) ON DELETE SET NULL;
CREATE INDEX crawls_batch_idx ON crawls (batch_id) WHERE batch_id IS NOT NULL;

-- Uploaded files. Only the extracted text is kept (as chunks), not the file.
CREATE TABLE batch_documents (
    id          BIGSERIAL PRIMARY KEY,
    batch_id    BIGINT      NOT NULL REFERENCES batches (id) ON DELETE CASCADE,
    name        TEXT        NOT NULL,
    kind        TEXT        NOT NULL,
    status      TEXT        NOT NULL DEFAULT 'processing',
    error       TEXT        NOT NULL DEFAULT '',
    chunk_count INTEGER     NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT batch_documents_status_chk CHECK (status IN ('processing', 'done', 'failed'))
);
CREATE INDEX batch_documents_batch_idx ON batch_documents (batch_id, id);

-- Which pages' chunks belong to which batch. chunks.crawl_id cannot scope
-- search: it is re-pointed to the latest crawl of a shared URL, so a URL could
-- belong to only one batch.
CREATE TABLE batch_chunks (
    batch_id BIGINT NOT NULL REFERENCES batches (id) ON DELETE CASCADE,
    url_hash TEXT   NOT NULL,
    PRIMARY KEY (batch_id, url_hash)
);
CREATE INDEX batch_chunks_hash_idx ON batch_chunks (url_hash);

-- Uploaded documents have no crawl. 768 dimensions matches nomic-embed-text;
-- another model needs another migration.
ALTER TABLE chunks ALTER COLUMN crawl_id DROP NOT NULL;
ALTER TABLE chunks ADD COLUMN embedding vector(768);
CREATE INDEX chunks_embedding_idx ON chunks USING hnsw (embedding vector_cosine_ops);
-- Keeps "what still needs an embedding" cheap as the table grows.
CREATE INDEX chunks_unembedded_idx ON chunks (url_hash, seq) WHERE embedding IS NULL;

-- +goose Down
DROP INDEX chunks_unembedded_idx;
DROP INDEX chunks_embedding_idx;
ALTER TABLE chunks DROP COLUMN embedding;
DELETE FROM chunks WHERE crawl_id IS NULL;
ALTER TABLE chunks ALTER COLUMN crawl_id SET NOT NULL;
DROP TABLE batch_chunks;
DROP TABLE batch_documents;
ALTER TABLE crawls DROP COLUMN batch_id;
DROP TABLE batches;
