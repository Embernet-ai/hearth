-- Removing a time entry stops being permanent.
--
-- "Remove" has meant DELETE FROM time_entries since 00014. That is fine for
-- a correction made the same minute and wrong the moment somebody notices it
-- a week later, when the row is already gone and there is nothing left to be
-- wrong about. A logged hour is somebody's paid work; losing the record of it
-- to a mis-click is a worse failure than showing one extra row to a manager
-- who will never look for it.

-- +goose Up

ALTER TABLE time_entries ADD COLUMN deleted_at timestamptz;

-- The queries that list entries all filter this out; the ones that do not
-- (an id lookup for Correct/Restore) need to see a removed row on purpose, so
-- this is a partial index for the common case rather than a blanket one.
CREATE INDEX time_entries_active_idx ON time_entries (org_id, principal_id)
    WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS time_entries_active_idx;
ALTER TABLE time_entries DROP COLUMN IF EXISTS deleted_at;
