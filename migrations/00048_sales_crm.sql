-- The sales side of the CRM: an owner on every record, a pipeline of deals,
-- and a timeline of notes and calls.
--
-- Contacts and companies were built for the maintenance question (who fixed
-- the compressor). Fireball also sells, and that work was living in a separate
-- CRM. This is the schema that lets one tool hold both, without disturbing a
-- single existing row: every column added here has a default, and the existing
-- queries never name them.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Companies: an owner, an address, a website, a revenue figure, and a kind for
-- somebody who has not bought anything yet.
-- ---------------------------------------------------------------------------
ALTER TABLE companies DROP CONSTRAINT companies_kind_check;
ALTER TABLE companies ADD CONSTRAINT companies_kind_check
    CHECK (kind IN ('vendor', 'contractor', 'customer', 'manufacturer', 'other', 'prospect'));

ALTER TABLE companies
    ADD COLUMN owner_id             uuid REFERENCES principals (id) ON DELETE SET NULL,
    -- The website as it was written, kept apart from domain. domain is the
    -- email matcher and is unique per org; two firms that share a website
    -- (a parent and its plant) must both be able to keep it.
    ADD COLUMN website              text   NOT NULL DEFAULT '',
    ADD COLUMN linkedin_url         text   NOT NULL DEFAULT '',
    ADD COLUMN address_street       text   NOT NULL DEFAULT '',
    ADD COLUMN address_city         text   NOT NULL DEFAULT '',
    ADD COLUMN address_state        text   NOT NULL DEFAULT '',
    ADD COLUMN address_postcode     text   NOT NULL DEFAULT '',
    ADD COLUMN address_country      text   NOT NULL DEFAULT '',
    -- Cents, like every other money column here. NULL is "nobody has said",
    -- which is different from a company that makes nothing.
    ADD COLUMN annual_revenue_cents bigint,
    ADD COLUMN revenue_currency     text   NOT NULL DEFAULT 'USD';
CREATE INDEX companies_owner_idx ON companies (org_id, owner_id) WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- Contacts: an owner, a LinkedIn page, and the extra addresses and numbers a
-- person has beyond the first.
-- ---------------------------------------------------------------------------
ALTER TABLE contacts
    ADD COLUMN owner_id       uuid REFERENCES principals (id) ON DELETE SET NULL,
    ADD COLUMN linkedin_url   text   NOT NULL DEFAULT '',
    ADD COLUMN extra_emails   text[] NOT NULL DEFAULT '{}',
    ADD COLUMN extra_phones   text[] NOT NULL DEFAULT '{}';
CREATE INDEX contacts_owner_idx ON contacts (org_id, owner_id) WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- Deals.
--
-- An ordinary entity, like a contact: grants, links, properties, and search
-- come from the substrate. Stage is a column and not a table, because the
-- eight stages are the vocabulary the sales team already uses and a stage
-- editor is the thing a pipeline nobody updates grows first.
--
-- position orders a card inside its column, so dragging one up the list is a
-- write and not a re-sort by amount.
-- ---------------------------------------------------------------------------
CREATE TABLE deals (
    id           uuid        PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    stage        text        NOT NULL DEFAULT 'new',
    amount_cents bigint      NOT NULL DEFAULT 0,
    currency     text        NOT NULL DEFAULT 'USD',
    close_date   date,
    company_id   uuid        REFERENCES companies (id) ON DELETE SET NULL,
    contact_id   uuid        REFERENCES contacts (id)  ON DELETE SET NULL,
    owner_id     uuid        REFERENCES principals (id) ON DELETE SET NULL,
    position     double precision NOT NULL DEFAULT 0,
    notes        text        NOT NULL DEFAULT '',

    created_by   uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz,

    CONSTRAINT deals_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT deals_stage_check CHECK (stage IN
        ('new', 'screening', 'meeting', 'proposal', 'customer', 'won', 'lost', 'no_response')),
    CONSTRAINT deals_amount_sane CHECK (amount_cents >= 0)
);
CREATE INDEX deals_org_stage_idx ON deals (org_id, stage, position) WHERE deleted_at IS NULL;
CREATE INDEX deals_company_idx   ON deals (company_id) WHERE deleted_at IS NULL;
CREATE INDEX deals_contact_idx   ON deals (contact_id) WHERE deleted_at IS NULL;
CREATE INDEX deals_owner_idx     ON deals (org_id, owner_id) WHERE deleted_at IS NULL;

ALTER TABLE deals ADD COLUMN search_tsv tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(name, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(notes, '')), 'C')
    ) STORED;
CREATE INDEX deals_search_idx ON deals USING GIN (search_tsv);

-- ---------------------------------------------------------------------------
-- Activities: the timeline. A note and a call are one table with a kind, the
-- same way a work order and a runbook are one table with a kind, because both
-- are "something that happened or was written, about these records".
--
-- occurred_at is when the thing happened (a call on Friday logged on Monday),
-- created_at is when it was written down. The timeline sorts on the first.
-- ---------------------------------------------------------------------------
CREATE TABLE crm_activities (
    id          uuid        PRIMARY KEY,
    org_id      uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    kind        text        NOT NULL,
    title       text        NOT NULL DEFAULT '',
    body        text        NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL DEFAULT now(),
    -- Calls only; NULL on a note.
    direction   text,
    disposition text        NOT NULL DEFAULT '',
    duration_ms bigint,

    created_by  uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    deleted_at  timestamptz,

    CONSTRAINT crm_activities_kind_check CHECK (kind IN ('note', 'call')),
    CONSTRAINT crm_activities_direction_check
        CHECK (direction IS NULL OR direction IN ('inbound', 'outbound'))
);
CREATE INDEX crm_activities_org_idx ON crm_activities (org_id, occurred_at DESC) WHERE deleted_at IS NULL;

-- One activity can be about several records: a call with a contact at a
-- company about a deal is one row here and three below.
CREATE TABLE crm_activity_targets (
    activity_id uuid NOT NULL REFERENCES crm_activities (id) ON DELETE CASCADE,
    target_type text NOT NULL,
    target_id   uuid NOT NULL,
    PRIMARY KEY (activity_id, target_type, target_id),
    CONSTRAINT crm_activity_targets_type_check CHECK (target_type IN ('company', 'contact', 'deal'))
);
CREATE INDEX crm_activity_targets_target_idx ON crm_activity_targets (target_type, target_id);

-- +goose Down
DROP TABLE IF EXISTS crm_activity_targets;
DROP TABLE IF EXISTS crm_activities;
DROP TABLE IF EXISTS deals;
DROP INDEX IF EXISTS contacts_owner_idx;
ALTER TABLE contacts
    DROP COLUMN IF EXISTS extra_phones, DROP COLUMN IF EXISTS extra_emails,
    DROP COLUMN IF EXISTS linkedin_url, DROP COLUMN IF EXISTS owner_id;
DROP INDEX IF EXISTS companies_owner_idx;
ALTER TABLE companies
    DROP COLUMN IF EXISTS revenue_currency, DROP COLUMN IF EXISTS annual_revenue_cents,
    DROP COLUMN IF EXISTS address_country, DROP COLUMN IF EXISTS address_postcode,
    DROP COLUMN IF EXISTS address_state, DROP COLUMN IF EXISTS address_city,
    DROP COLUMN IF EXISTS address_street, DROP COLUMN IF EXISTS linkedin_url,
    DROP COLUMN IF EXISTS website, DROP COLUMN IF EXISTS owner_id;
ALTER TABLE companies DROP CONSTRAINT companies_kind_check;
ALTER TABLE companies ADD CONSTRAINT companies_kind_check
    CHECK (kind IN ('vendor', 'contractor', 'customer', 'manufacturer', 'other'));
