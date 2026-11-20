-- Somebody has to be able to record an hour without first naming a job.
--
-- time_entries.document_id has been NOT NULL since 00014, so every entry had to
-- hang off a document and the only ways in were a timer started from a document
-- page or a manual entry logged against one. That is a defensible design for a
-- maintenance system, where an hour with no work order attached is usually an
-- hour somebody forgot to file.
--
-- It is the wrong design for the thing this now has to do. People need to track
-- their time. Attaching it to a task or a project is useful when there is one
-- and an obstacle when there is not, and a system that refuses the entry until
-- a job is chosen gets the job chosen at random, which is worse than a null.

-- +goose Up

-- The link becomes optional. Nothing else about it changes: still ON DELETE
-- CASCADE, still indexed, still the thing every rollup joins through.
ALTER TABLE time_entries ALTER COLUMN document_id DROP NOT NULL;

-- The per-person index already existed and is what "my week" reads. The
-- document index now covers only the rows that have one, because an index entry
-- per unattached hour is dead weight on the query that never asks for them.
DROP INDEX IF EXISTS time_entries_document_idx;
CREATE INDEX time_entries_document_idx
    ON time_entries (document_id, started_at) WHERE document_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Approval.
--
-- Hours are approved by the managers of the person's department, or by whoever
-- their approver_id names, or by a super admin. That routing lives in Go; what
-- lives here is the state and the fact that it cannot be forged into existence
-- without saying who did it and when.
--
-- Three states, expressed as two nullable columns rather than an enum:
--
--   decided_at NULL                     pending
--   decided_at set, approved true       approved
--   decided_at set, approved false      sent back, with a reason
--
-- An enum column would be a fourth thing to keep consistent with these two, and
-- the check below already makes the illegal states unrepresentable.
--
-- Deliberately no default of "approved" for existing rows. Every entry that
-- predates this is left pending, because marking historic hours approved would
-- be this migration signing off work on behalf of managers who never saw it.
-- ---------------------------------------------------------------------------
ALTER TABLE time_entries
    ADD COLUMN approved    boolean,
    ADD COLUMN decided_at  timestamptz,
    ADD COLUMN decided_by  uuid REFERENCES principals (id) ON DELETE RESTRICT,
    ADD COLUMN decided_note text NOT NULL DEFAULT '',

    ADD CONSTRAINT time_entries_decision_together CHECK (
        (decided_at IS NULL AND approved IS NULL AND decided_by IS NULL) OR
        (decided_at IS NOT NULL AND approved IS NOT NULL AND decided_by IS NOT NULL)
    ),

    -- A running timer has not finished, so there is nothing to approve yet.
    -- Approving one would freeze a duration that is still moving.
    ADD CONSTRAINT time_entries_decide_only_finished CHECK (
        decided_at IS NULL OR ended_at IS NOT NULL
    );

-- The manager's queue: what is waiting, oldest first. Partial, because an
-- approved entry is never read by that page again and the pending set is small
-- next to the archive.
CREATE INDEX time_entries_pending_idx
    ON time_entries (org_id, started_at) WHERE decided_at IS NULL AND ended_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS time_entries_pending_idx;
ALTER TABLE time_entries
    DROP CONSTRAINT IF EXISTS time_entries_decide_only_finished,
    DROP CONSTRAINT IF EXISTS time_entries_decision_together,
    DROP COLUMN IF EXISTS decided_note,
    DROP COLUMN IF EXISTS decided_by,
    DROP COLUMN IF EXISTS decided_at,
    DROP COLUMN IF EXISTS approved;

DROP INDEX IF EXISTS time_entries_document_idx;
CREATE INDEX time_entries_document_idx ON time_entries (document_id, started_at);

-- Putting NOT NULL back would fail against any entry recorded without a task,
-- which is the whole point of the change. Deleting those rows to make the
-- constraint fit would be destroying somebody's recorded work to tidy a schema.
-- The column stays nullable on the way down and that is the honest outcome.
