-- What a sales team needs beside the pipeline: meetings and emails on the
-- timeline, why a deal was lost, and how long a deal has sat in its stage.
--
-- A meeting and an email are the same table as a note and a call, with a kind,
-- so one timeline and one search cover all four. The direction column already
-- allowed in or out; an email uses it the way a call does.
--
-- stage_changed_at is set by a trigger, for the same reason closed_at is: the
-- forms, the API, the importers and the sync all write the stage, and one rule
-- has to hold for every one of them.

-- +goose Up
ALTER TABLE crm_activities DROP CONSTRAINT crm_activities_kind_check;
ALTER TABLE crm_activities ADD CONSTRAINT crm_activities_kind_check
    CHECK (kind IN ('note', 'call', 'meeting', 'email'));

ALTER TABLE deals ADD COLUMN lost_reason text NOT NULL DEFAULT '';
ALTER TABLE deals ADD COLUMN stage_changed_at timestamptz;
-- The best date that was ever kept: when it closed, else when it was made.
UPDATE deals SET stage_changed_at = coalesce(closed_at, created_at);
ALTER TABLE deals ALTER COLUMN stage_changed_at SET DEFAULT now();
ALTER TABLE deals ALTER COLUMN stage_changed_at SET NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION deals_set_stage_changed_at() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.stage IS DISTINCT FROM NEW.stage THEN
        NEW.stage_changed_at := now();
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER deals_stage_changed_at BEFORE UPDATE OF stage ON deals
    FOR EACH ROW EXECUTE FUNCTION deals_set_stage_changed_at();

-- Follow-ups: what a salesperson owes a record and by when. They are the CRM's own rows, not documents, so they
-- work the same whichever back end holds Hearth's tasks. Who may see one is who may see the record it is about,
-- so sales sees every follow-up, the way it sees every deal.
CREATE TABLE crm_followups (
    id           uuid        PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    title        text        NOT NULL,
    body         text        NOT NULL DEFAULT '',
    due_at       timestamptz,
    assignee_id  uuid        REFERENCES principals (id) ON DELETE SET NULL,
    target_type  text        NOT NULL,
    target_id    uuid        NOT NULL,
    status       text        NOT NULL DEFAULT 'open',
    completed_at timestamptz,
    completed_by uuid        REFERENCES principals (id) ON DELETE SET NULL,
    created_by   uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz,
    CONSTRAINT crm_followups_target_check CHECK (target_type IN ('company', 'contact', 'deal')),
    CONSTRAINT crm_followups_status_check CHECK (status IN ('open', 'done'))
);
CREATE INDEX crm_followups_target_idx ON crm_followups (target_type, target_id) WHERE deleted_at IS NULL;
CREATE INDEX crm_followups_queue_idx ON crm_followups (org_id, status, due_at) WHERE deleted_at IS NULL;
CREATE INDEX crm_followups_assignee_idx ON crm_followups (assignee_id, status, due_at) WHERE deleted_at IS NULL;

-- Follow-ups and the sales reports read open deals by close date.
CREATE INDEX deals_open_close_idx ON deals (org_id, close_date) WHERE deleted_at IS NULL AND closed_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS crm_followups;
DROP INDEX IF EXISTS deals_open_close_idx;
DROP TRIGGER IF EXISTS deals_stage_changed_at ON deals;
DROP FUNCTION IF EXISTS deals_set_stage_changed_at();
ALTER TABLE deals DROP COLUMN IF EXISTS stage_changed_at;
ALTER TABLE deals DROP COLUMN IF EXISTS lost_reason;
DELETE FROM crm_activities WHERE kind IN ('meeting', 'email');
ALTER TABLE crm_activities DROP CONSTRAINT crm_activities_kind_check;
ALTER TABLE crm_activities ADD CONSTRAINT crm_activities_kind_check CHECK (kind IN ('note', 'call'));
