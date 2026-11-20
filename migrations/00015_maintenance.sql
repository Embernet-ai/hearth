-- Preventive maintenance: schedules, meters, and the occurrence table that
-- makes "exactly one successor" a constraint rather than an intention.
--
-- This is the phase that makes the product a maintenance system rather than a
-- task list, and the whole design turns on one decision: a schedule generates
-- FORWARD from completion and never from a sweep over everything overdue. A
-- sweep is how maintenance systems produce a hundred duplicate jobs after a
-- holiday and lose the crew permanently.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Schedules.
--
-- Calendar, meter, or both. Both means whichever arrives first, which is what
-- a real interval looks like: every ninety days OR every five hundred run
-- hours, because a machine that sat idle for a quarter does not need the same
-- service as one that ran three shifts.
-- ---------------------------------------------------------------------------
CREATE TABLE pm_schedules (
    id          uuid        PRIMARY KEY,
    org_id      uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    asset_id    uuid        NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    -- The template that becomes the work order. A schedule with no template
    -- would generate an empty job with no procedure on it, which is a
    -- reminder rather than a work order.
    template_id uuid        REFERENCES documents (id) ON DELETE SET NULL,
    name        text        NOT NULL,

    -- Calendar recurrence. Null means this schedule is meter-driven only.
    every_days  integer,

    -- Meter recurrence. Both together or neither: a threshold with no meter to
    -- read is a number nothing will ever reach.
    meter_key   text,
    every_units numeric,

    -- How far ahead to raise the job, so somebody has notice rather than
    -- finding out it was due this morning.
    lead_days   integer     NOT NULL DEFAULT 7,

    next_due_at       timestamptz NOT NULL DEFAULT now(),
    last_generated_at timestamptz,
    -- The meter reading the last occurrence was generated at, so the next
    -- threshold is measured from where the machine actually was rather than
    -- from where the schedule hoped it would be.
    last_generated_units numeric,

    enabled     boolean     NOT NULL DEFAULT true,
    created_by  uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT pm_schedules_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT pm_schedules_has_a_trigger
        CHECK (every_days IS NOT NULL OR (meter_key IS NOT NULL AND every_units IS NOT NULL)),
    CONSTRAINT pm_schedules_meter_pair
        CHECK ((meter_key IS NULL AND every_units IS NULL)
            OR (meter_key IS NOT NULL AND every_units IS NOT NULL AND every_units > 0)),
    CONSTRAINT pm_schedules_days_positive CHECK (every_days IS NULL OR every_days > 0),
    CONSTRAINT pm_schedules_lead_sane CHECK (lead_days BETWEEN 0 AND 365)
);
CREATE INDEX pm_schedules_asset_idx ON pm_schedules (asset_id);
CREATE INDEX pm_schedules_due_idx   ON pm_schedules (org_id, next_due_at) WHERE enabled;

-- ---------------------------------------------------------------------------
-- Occurrences.
--
-- One row per time the schedule came round. This is the table that makes the
-- guarantee enforceable rather than hoped for: the unique index on
-- (schedule, due date) means generating twice for the same occurrence writes
-- nothing, however many times a job is redelivered or a sweep runs.
--
-- It is also what makes compliance answerable. "What was due, what was done,
-- and what was missed" is a query over this table, not a reconstruction from
-- work orders that may have been renamed, reassigned, or deleted.
-- ---------------------------------------------------------------------------
CREATE TABLE pm_occurrences (
    id            uuid        PRIMARY KEY,
    schedule_id   uuid        NOT NULL REFERENCES pm_schedules (id) ON DELETE CASCADE,
    org_id        uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    due_at        timestamptz NOT NULL,
    work_order_id uuid        REFERENCES documents (id) ON DELETE SET NULL,
    generated_at  timestamptz NOT NULL DEFAULT now(),
    completed_at  timestamptz,
    -- Why it came round: 'calendar' or 'meter'. A compliance report that
    -- cannot say which is a report that cannot explain a short interval.
    reason        text        NOT NULL DEFAULT 'calendar',

    CONSTRAINT pm_occurrences_reason_check CHECK (reason IN ('calendar', 'meter', 'manual'))
);
CREATE UNIQUE INDEX pm_occurrences_once ON pm_occurrences (schedule_id, due_at);
CREATE INDEX pm_occurrences_open_idx ON pm_occurrences (schedule_id)
    WHERE completed_at IS NULL;
CREATE INDEX pm_occurrences_window_idx ON pm_occurrences (org_id, due_at);
CREATE INDEX pm_occurrences_wo_idx ON pm_occurrences (work_order_id)
    WHERE work_order_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Meter readings.
--
-- numeric rather than a float, because run hours and cycle counts are compared
-- against thresholds and a float that is 499.99999 does not trigger a service
-- at 500.
-- ---------------------------------------------------------------------------
CREATE TABLE meter_readings (
    id        uuid        PRIMARY KEY,
    org_id    uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    asset_id  uuid        NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    key       text        NOT NULL,
    value     numeric     NOT NULL,
    read_at   timestamptz NOT NULL DEFAULT now(),
    read_by   uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    note      text        NOT NULL DEFAULT '',

    CONSTRAINT meter_readings_key_not_blank CHECK (btrim(key) <> ''),
    CONSTRAINT meter_readings_value_sane CHECK (value >= 0)
);
CREATE INDEX meter_readings_asset_idx ON meter_readings (asset_id, key, read_at DESC);

-- +goose Down
DROP TABLE IF EXISTS meter_readings;
DROP TABLE IF EXISTS pm_occurrences;
DROP TABLE IF EXISTS pm_schedules;
