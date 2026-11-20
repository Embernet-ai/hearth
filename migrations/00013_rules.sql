-- Automations, and the two columns on the outbox that stop them eating
-- themselves.
--
-- Rules are the feature people cite as the reason they stay on the tool they
-- have. They are also the feature that goes wrong most memorably, and always
-- the same way: rule A sets a field that fires rule B, which sets the first
-- field back, and the pair of them run until somebody notices the queue.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Causation, on the event.
--
-- depth is how many rules deep a change is. A person's edit is zero; anything
-- a rule did is one more than what caused it. The engine refuses to act on
-- anything above zero, so two rules that trigger each other stop after exactly
-- one cycle, by construction rather than by a counter somebody has to reset.
--
-- On the outbox rather than in the payload, because the guard has to be
-- readable in SQL: "show me everything a rule caused this morning" is the
-- first question anybody asks when one has misbehaved.
-- ---------------------------------------------------------------------------
ALTER TABLE outbox ADD COLUMN cause_rule_id uuid;
ALTER TABLE outbox ADD COLUMN depth smallint NOT NULL DEFAULT 0;
CREATE INDEX outbox_caused_idx ON outbox (cause_rule_id, created_at DESC)
    WHERE cause_rule_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- The rules themselves.
--
-- trigger, conditions, and actions are jsonb because their shape changes with
-- every new condition type, and a rule is only ever read whole. Columns would
-- mean a migration per feature and a table of forty mostly-null fields.
-- ---------------------------------------------------------------------------
CREATE TABLE rules (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    space_id   uuid        REFERENCES spaces (id) ON DELETE SET NULL,
    name       text        NOT NULL,
    trigger    jsonb       NOT NULL DEFAULT '{}'::jsonb,
    conditions jsonb       NOT NULL DEFAULT '[]'::jsonb,
    actions    jsonb       NOT NULL DEFAULT '[]'::jsonb,
    -- Off until somebody turns it on. A rule that starts running the moment it
    -- is saved gives no chance to dry run it first, and the dry run is the
    -- whole reason anybody trusts one.
    enabled    boolean     NOT NULL DEFAULT false,
    created_by uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT rules_name_not_blank CHECK (btrim(name) <> '')
);
CREATE INDEX rules_org_enabled_idx ON rules (org_id) WHERE enabled;

-- ---------------------------------------------------------------------------
-- Every run, whether it matched or not.
--
-- An automation nobody can debug gets switched off, and the question is always
-- "why did it not fire", which a table of only the firings cannot answer. So a
-- run that considered an event and decided against it is recorded too, with
-- the condition that failed.
-- ---------------------------------------------------------------------------
CREATE TABLE rule_runs (
    id            bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    rule_id       uuid        NOT NULL REFERENCES rules (id) ON DELETE CASCADE,
    org_id        uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    event_id      bigint      NOT NULL,
    entity_type   text        NOT NULL,
    entity_id     uuid        NOT NULL,
    matched       boolean     NOT NULL,
    -- Why not, when it did not. One line, so a list of runs reads.
    reason        text,
    actions_taken jsonb       NOT NULL DEFAULT '[]'::jsonb,
    error         text,
    -- A dry run records everything a real one does and changes nothing, which
    -- is what makes "what would this have done" answerable from the same page.
    dry_run       boolean     NOT NULL DEFAULT false,
    ran_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX rule_runs_rule_idx ON rule_runs (rule_id, ran_at DESC);
-- One real run per rule per event. The unique index IS the "fires once"
-- guarantee: at-least-once delivery means the job can run twice, and without
-- this the second run does the actions again.
CREATE UNIQUE INDEX rule_runs_once ON rule_runs (rule_id, event_id) WHERE NOT dry_run;

-- +goose Down
DROP TABLE IF EXISTS rule_runs;
DROP TABLE IF EXISTS rules;
DROP INDEX IF EXISTS outbox_caused_idx;
ALTER TABLE outbox DROP COLUMN IF EXISTS depth;
ALTER TABLE outbox DROP COLUMN IF EXISTS cause_rule_id;
