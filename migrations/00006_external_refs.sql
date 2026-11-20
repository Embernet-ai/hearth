-- External references: what a thing was called in the system it came from.
--
-- This exists so an import can run twice. Without it, re-running a ClickUp
-- import produces a second copy of every task, which is the failure mode that
-- makes people stop trusting an importer after exactly one bad afternoon.
--
-- Deliberately generic rather than a clickup_id column. GitHub sync, an email
-- ingest, and any future connector all need the same "I have seen this remote
-- id before" question answered, and they should not each grow their own table.

-- +goose Up
CREATE TABLE external_refs (
    org_id      uuid        NOT NULL REFERENCES orgs (id) ON DELETE RESTRICT,
    -- 'clickup', 'github', and so on. Free text, because a connector is a code
    -- change and should not require a migration.
    source      text        NOT NULL,
    external_id text        NOT NULL,
    entity_type text        NOT NULL,
    entity_id   uuid        NOT NULL,
    -- What the remote called it, kept for diagnosis when an import maps
    -- something to the wrong place. Not used for matching.
    external_url text,
    imported_at timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    -- The uniqueness that makes re-import an update instead of a duplicate.
    PRIMARY KEY (org_id, source, external_id)
);

-- The reverse question: "where did this document come from?", used to show
-- provenance on the page and to avoid re-exporting something we imported.
CREATE INDEX external_refs_entity_idx ON external_refs (entity_type, entity_id);

-- +goose Down
DROP TABLE IF EXISTS external_refs;
