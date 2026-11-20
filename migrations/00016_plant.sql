-- The plant tree: spaces that hold work, assets that hold each other, and the
-- downtime records that turn a maintenance history into a replacement
-- decision.
--
-- spaces has had parent_id and three kinds since the substrate landed and
-- nothing has ever set space_id on a document. This is the migration that
-- spends it, and it adds the other half: assets have parents too, because a
-- bearing lives in a gearbox, which drives a conveyor, which is part of line
-- three, which is in the north building. A failure at the bottom has to be
-- visible at the top or the tree is decoration.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Asset fields.
--
-- The typed side table for assets, exactly as task_fields is for tasks, and
-- for the same reason: these are the columns that need a foreign key, a unique
-- index, or a recursive join, and none of that is possible against a jsonb
-- property value.
--
-- Criticality is deliberately NOT here. It is already a seeded select property
-- and the property system exists precisely so that a field like it needs no
-- migration. Adding a column beside the property would give one question two
-- answers, and they would disagree the first afternoon somebody edited one.
-- ---------------------------------------------------------------------------
CREATE TABLE asset_fields (
    document_id     uuid        PRIMARY KEY REFERENCES documents (id) ON DELETE CASCADE,
    org_id          uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,

    -- The parent in the plant tree. SET NULL rather than CASCADE: deleting a
    -- line must not delete the machines on it, it must leave them at the top
    -- of the tree where somebody will notice them.
    parent_asset_id uuid        REFERENCES documents (id) ON DELETE SET NULL,

    -- The code on the label somebody scans. Short, uppercase, and unique
    -- inside the org, because a scanner that lands on the wrong machine is
    -- worse than one that lands nowhere.
    code            text        NOT NULL DEFAULT '',

    installed_at    timestamptz,
    retired_at      timestamptz,

    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- The cheap half of the cycle guard. It stops the one-step case for free
    -- and forever; the multi-step case needs a walk, which is done on write
    -- under a lock.
    CONSTRAINT asset_fields_no_self_parent
        CHECK (parent_asset_id IS DISTINCT FROM document_id),
    CONSTRAINT asset_fields_retired_after_installed
        CHECK (retired_at IS NULL OR installed_at IS NULL OR retired_at >= installed_at)
);

CREATE INDEX asset_fields_parent_idx ON asset_fields (parent_asset_id)
    WHERE parent_asset_id IS NOT NULL;
CREATE INDEX asset_fields_org_idx ON asset_fields (org_id);

-- Case-insensitive, because the code is read off a label by a person as often
-- as by a scanner, and "cv-003" and "CV-003" are the same machine.
CREATE UNIQUE INDEX asset_fields_code_idx ON asset_fields (org_id, upper(code))
    WHERE code <> '';

-- ---------------------------------------------------------------------------
-- Downtime.
--
-- Records rather than a counter. A single downtime_minutes column on the asset
-- can only be incremented, and "why is this four thousand minutes" then has no
-- answer at all. Every row here says when it started, when it came back, and
-- what was being done about it.
--
-- Same shape as time_entries: an end and a duration together or neither. A row
-- with an end and no duration is counted as zero by every rollup and nobody
-- ever finds out why the numbers are low.
-- ---------------------------------------------------------------------------
CREATE TABLE asset_downtime (
    id            uuid        PRIMARY KEY,
    org_id        uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    asset_id      uuid        NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    -- What was being done about it, when there is a job. Nullable, because a
    -- machine can be down before anybody has raised one.
    work_order_id uuid        REFERENCES documents (id) ON DELETE SET NULL,

    started_at    timestamptz NOT NULL DEFAULT now(),
    ended_at      timestamptz,
    minutes       integer,

    -- Planned downtime is not a failure and must not be counted as one. A
    -- report that adds the annual shutdown to the breakdown total makes the
    -- best-maintained machine on site look like the worst.
    planned       boolean     NOT NULL DEFAULT false,
    note          text        NOT NULL DEFAULT '',
    recorded_by   uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,

    CONSTRAINT asset_downtime_closed CHECK (
        (ended_at IS NULL AND minutes IS NULL) OR
        (ended_at IS NOT NULL AND minutes IS NOT NULL AND minutes >= 0)),
    CONSTRAINT asset_downtime_ends_after_start CHECK (ended_at IS NULL OR ended_at >= started_at)
);

CREATE INDEX asset_downtime_asset_idx ON asset_downtime (asset_id, started_at DESC);
CREATE INDEX asset_downtime_org_idx ON asset_downtime (org_id, started_at DESC);

-- A machine cannot be down twice at once. Without this, two people reporting
-- the same breakdown produce two overlapping records and the rollup double
-- counts the outage.
CREATE UNIQUE INDEX asset_downtime_one_open ON asset_downtime (asset_id)
    WHERE ended_at IS NULL;

-- ---------------------------------------------------------------------------
-- "Everything of this kind in this space" is the query the whole feature is
-- for, and the substrate's index on space_id alone cannot answer it without
-- reading every document in the space.
--
-- A second index rather than replacing the first: the existing one covers
-- "everything in this space" with no kind, and dropping it would make the
-- space page a sequential scan on the day somebody files a thousand documents.
-- ---------------------------------------------------------------------------
CREATE INDEX documents_space_kind_idx ON documents (space_id, kind)
    WHERE space_id IS NOT NULL AND deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS documents_space_kind_idx;
DROP TABLE IF EXISTS asset_downtime;
DROP TABLE IF EXISTS asset_fields;
