-- +goose Up

-- Salaried staff do not have their hours approved one at a time. A super admin
-- can mark a person so that time they log from then on is approved as it is
-- logged, by the system, and the record says so.
--
-- A moment rather than a flag, so the rule is only about work after it: an
-- entry whose start is before this moment is never approved by it, which keeps a
-- backlog filed for the person (an import, a late entry for last month) with
-- their approver. NULL is off, and nothing is back-filled: the default is the
-- way it has always worked, every entry waits for a manager.
ALTER TABLE principals
    ADD COLUMN time_auto_approve_since timestamptz;

-- An auto-approved entry is a decision like any other (approved, decided_at,
-- decided_by and a note, held together by time_entries_decision_together), with
-- one extra fact: nobody decided it. decided_by names the person themselves,
-- because that check wants somebody, and this flag is what says the decision was
-- the system's own rule and not a person signing off their own hours.
ALTER TABLE time_entries
    ADD COLUMN auto_approved boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE time_entries DROP COLUMN IF EXISTS auto_approved;
ALTER TABLE principals DROP COLUMN IF EXISTS time_auto_approve_since;
