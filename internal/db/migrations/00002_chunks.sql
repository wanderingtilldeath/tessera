-- +goose Up
-- Text chunks of a page for RAG export. Replaced as a set per url_hash
-- whenever the page's content_hash changes.
CREATE TABLE chunks (
    url_hash     TEXT    NOT NULL,
    seq          INTEGER NOT NULL,
    crawl_id     BIGINT  NOT NULL,
    url          TEXT    NOT NULL,
    title        TEXT    NOT NULL,
    heading      TEXT    NOT NULL,
    text         TEXT    NOT NULL,
    content_hash TEXT    NOT NULL,
    PRIMARY KEY (url_hash, seq)
);
CREATE INDEX chunks_crawl_idx ON chunks (crawl_id, url, seq);

ALTER TABLE crawls ADD COLUMN path_prefixes TEXT[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE crawls DROP COLUMN path_prefixes;
DROP TABLE chunks;
