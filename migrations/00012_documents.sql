-- Checklists, and the index that makes reading history cheap.
--
-- document_versions has been written on every edit since the substrate landed
-- and read by nothing. This adds no columns to it: the read path was the only
-- thing missing.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Checklists.
--
-- Their own table rather than parent_task_id, and that is a change of mind
-- worth stating. A checklist item is not a task: it has no assignee, no due
-- date, no status beyond done or not, and no lifecycle. Making each one a
-- document would put a row in the spine, a grant, a version history, and a
-- search entry behind "verify dead", and then every list of work orders would
-- have to filter them out.
--
-- parent_task_id keeps its meaning for a REAL sub-task, which is a work order
-- that belongs to a bigger one. That is a different thing and phase nine uses
-- it.
-- ---------------------------------------------------------------------------
CREATE TABLE checklist_items (
    id          uuid        PRIMARY KEY,
    document_id uuid        NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    -- Sparse, so an item can be dragged between two others without rewriting
    -- the whole list. Gaps are normal and never closed up.
    position    integer     NOT NULL,
    text        text        NOT NULL,
    done_at     timestamptz,
    done_by     uuid        REFERENCES principals (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT checklist_items_text_not_blank CHECK (btrim(text) <> ''),
    -- done_at and done_by move together or the history is a lie about who did
    -- the work.
    CONSTRAINT checklist_items_done_together
        CHECK ((done_at IS NULL AND done_by IS NULL) OR (done_at IS NOT NULL AND done_by IS NOT NULL))
);
CREATE INDEX checklist_items_document_idx ON checklist_items (document_id, position);

-- History reads newest first and is paged. Without this it is a sequential
-- scan of every version of every document to show one document's.
CREATE INDEX document_versions_recent_idx
    ON document_versions (document_id, version DESC);

-- Templates are documents with kind='template', and the picker lists them by
-- name. Partial, because they are a handful of rows in a table of thousands.
CREATE INDEX documents_templates_idx ON documents (org_id, title)
    WHERE kind = 'template' AND deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS documents_templates_idx;
DROP INDEX IF EXISTS document_versions_recent_idx;
DROP TABLE IF EXISTS checklist_items;
