-- Which version of an EmberOPS record Hearth last wrote or read into a
-- document spine row.
--
-- With the EmberOPS back end a document keeps its operational fields in
-- EmberOPS and a spine row in Hearth for files, comments, links, versions and
-- search. When someone edits the record inside EmberOPS its `modified` stamp
-- moves past the one recorded here, and Hearth copies the change into the spine
-- the next time it reads the document. Empty, and unused, in the default back
-- end.

-- +goose Up
CREATE TABLE emberops_doc_sync (
    document_id uuid        PRIMARY KEY REFERENCES documents (id) ON DELETE CASCADE,
    modified    text        NOT NULL,
    synced_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS emberops_doc_sync;
