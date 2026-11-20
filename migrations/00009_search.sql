-- Search: the Postgres floor, and the bookkeeping the OpenSearch indexer needs.
--
-- Two engines, and the ordering between them is the point. OpenSearch answers
-- when it is there. Postgres answers when it is not, always, because it is the
-- same database the rows are already in and it cannot be down while the
-- application is up. Search that disappears with a dependency is a search box
-- people stop typing into.

-- +goose Up

-- ---------------------------------------------------------------------------
-- The Postgres index.
--
-- A GENERATED column rather than a trigger. A trigger is a second place the
-- rule lives and a second thing to remember on a bulk update, and the classic
-- symptom is a row that was edited by a migration and never reindexed. This
-- cannot drift: Postgres recomputes it as part of the write, or the write does
-- not happen.
--
-- Weighted, so a word in a title outranks the same word buried in a body.
-- 'english' rather than 'simple' so that "bearings" finds "bearing", which is
-- what somebody typing into a search box means.
-- ---------------------------------------------------------------------------
ALTER TABLE documents ADD COLUMN search_tsv tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(body, '')),  'B')
    ) STORED;

CREATE INDEX documents_search_idx ON documents USING gin (search_tsv);

-- ---------------------------------------------------------------------------
-- Attachment text.
--
-- Extraction is a job, not part of the upload: a 200 MB PDF must not hold the
-- request open, and a file whose text cannot be read is still a file that
-- uploaded successfully. The status column is what separates "not tried yet"
-- from "tried and this format has no text in it", which are different answers
-- and only one of them is worth retrying.
-- ---------------------------------------------------------------------------
ALTER TABLE files ADD COLUMN extracted_text    text;
ALTER TABLE files ADD COLUMN extraction_status text NOT NULL DEFAULT 'pending';
ALTER TABLE files ADD CONSTRAINT files_extraction_status_check
    CHECK (extraction_status IN ('pending', 'done', 'unsupported', 'failed'));

ALTER TABLE files ADD COLUMN search_tsv tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(filename, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(extracted_text, '')), 'B')
    ) STORED;

CREATE INDEX files_search_idx ON files USING gin (search_tsv) WHERE deleted_at IS NULL;
CREATE INDEX files_extraction_pending_idx ON files (id)
    WHERE extraction_status = 'pending' AND deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- What the external index knows.
--
-- Version rather than a timestamp. Clocks between two machines disagree, and
-- "indexed at 12:00:01, edited at 12:00:00" is not evidence of anything. The
-- document's own version counter is monotonic and comes from the same
-- transaction as the change, so indexed_version < version means stale, exactly.
--
-- This is also what makes a backfill checkable: it can report how many rows the
-- index is behind on rather than only that it ran.
-- ---------------------------------------------------------------------------
CREATE TABLE search_state (
    entity_type     text        NOT NULL,
    entity_id       uuid        NOT NULL,
    org_id          uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    indexed_version bigint      NOT NULL DEFAULT 0,
    indexed_at      timestamptz NOT NULL DEFAULT now(),
    -- The last failure, kept so a persistently unindexable row can be found
    -- rather than only being permanently absent from search results.
    error           text,

    PRIMARY KEY (entity_type, entity_id)
);
CREATE INDEX search_state_org_idx ON search_state (org_id, indexed_at);
CREATE INDEX search_state_failed_idx ON search_state (org_id) WHERE error IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS search_state;
DROP INDEX IF EXISTS files_extraction_pending_idx;
DROP INDEX IF EXISTS files_search_idx;
ALTER TABLE files DROP COLUMN IF EXISTS search_tsv;
ALTER TABLE files DROP COLUMN IF EXISTS extraction_status;
ALTER TABLE files DROP COLUMN IF EXISTS extracted_text;
DROP INDEX IF EXISTS documents_search_idx;
ALTER TABLE documents DROP COLUMN IF EXISTS search_tsv;
