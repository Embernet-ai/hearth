-- Notes and calls findable beside the records they are about.
--
-- Deals already had a search column (00048); the timeline of notes and calls did not, so a word typed into a call
-- note was findable only by opening every record. Same shape as documents, companies and contacts: a generated
-- tsvector, so no writer has to remember to keep it current.

-- +goose Up
ALTER TABLE crm_activities ADD COLUMN search_tsv tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(body, '')), 'B')
    ) STORED;
CREATE INDEX crm_activities_search_idx ON crm_activities USING GIN (search_tsv) WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS crm_activities_search_idx;
ALTER TABLE crm_activities DROP COLUMN IF EXISTS search_tsv;
