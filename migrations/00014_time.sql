-- Time, capacity, and the estimate column that has been there all along.
--
-- task_fields.estimate_minutes has existed since the substrate and nothing has
-- ever written to it. Nothing here changes that column: this migration adds the
-- other half, which is what actually happened, and the comparison becomes
-- possible for the first time.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Time entries.
--
-- A running timer is a row with no ended_at. That is the whole mechanism, and
-- it is why a timer survives a deploy: there is nothing in memory to lose. A
-- design that held the start in the process would reset every rollout, which
-- on a plant floor means somebody loses an afternoon.
--
-- minutes is stored rather than derived, because a manual correction is the
-- normal case. The timer is always forgotten at least once, and somebody
-- typing "that was about forty minutes" the next morning has to be able to say
-- so without inventing a start time to make the arithmetic work.
-- ---------------------------------------------------------------------------
CREATE TABLE time_entries (
    id           uuid        PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    document_id  uuid        NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,

    started_at   timestamptz NOT NULL DEFAULT now(),
    ended_at     timestamptz,
    minutes      integer,
    note         text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),

    -- Finished entries have both, running ones have neither. A row with an end
    -- and no duration would be counted as zero by every rollup.
    CONSTRAINT time_entries_finished_together
        CHECK ((ended_at IS NULL AND minutes IS NULL) OR (ended_at IS NOT NULL AND minutes IS NOT NULL)),
    CONSTRAINT time_entries_minutes_sane CHECK (minutes IS NULL OR (minutes >= 0 AND minutes <= 100000)),
    CONSTRAINT time_entries_order CHECK (ended_at IS NULL OR ended_at >= started_at)
);

CREATE INDEX time_entries_document_idx ON time_entries (document_id, started_at);
CREATE INDEX time_entries_person_idx   ON time_entries (principal_id, started_at DESC);

-- One running timer per person, enforced rather than hoped for. Two open
-- timers means the same hour billed to two jobs, and the person holding the
-- spanner is not the one who will notice.
CREATE UNIQUE INDEX time_entries_one_running
    ON time_entries (principal_id) WHERE ended_at IS NULL;

-- ---------------------------------------------------------------------------
-- Capacity and cost.
--
-- Both optional. A site that does not track cost leaves the rate null and
-- every cost figure reads as unknown rather than as zero, which is the
-- difference between "we do not measure this" and "this was free".
-- ---------------------------------------------------------------------------
CREATE TABLE capacity (
    principal_id     uuid        PRIMARY KEY REFERENCES principals (id) ON DELETE CASCADE,
    org_id           uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    minutes_per_week integer     NOT NULL DEFAULT 2400,
    -- Pence or cents, per hour. Integer, because money in a float is a
    -- rounding error waiting for an invoice.
    rate_cents       integer,
    updated_at       timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT capacity_minutes_sane CHECK (minutes_per_week BETWEEN 0 AND 10080),
    CONSTRAINT capacity_rate_sane CHECK (rate_cents IS NULL OR rate_cents >= 0)
);
CREATE INDEX capacity_org_idx ON capacity (org_id);

-- +goose Down
DROP TABLE IF EXISTS capacity;
DROP TABLE IF EXISTS time_entries;
