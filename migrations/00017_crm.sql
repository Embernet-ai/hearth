-- Contacts, companies, and the parts that make a work order's cost real.
--
-- core.EntityContact and EntityCompany have been declared since the beginning
-- with nothing behind them. This is the migration that puts something there,
-- and the shape is deliberate: both are ordinary entities, so they get grants,
-- properties, links, and search from the substrate rather than from four new
-- pieces of code written specially for them.
--
-- The maintenance-shaped reason to have a CRM at all is the vendor who services
-- a machine. Not a sales pipeline: a phone number for the company that fixed
-- the compressor last time, on the compressor.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Companies.
--
-- kind rather than three tables. A firm that supplies parts and also sends an
-- engineer is both a vendor and a contractor, and modelling that as two rows
-- means two phone numbers that drift apart.
-- ---------------------------------------------------------------------------
CREATE TABLE companies (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    name       text        NOT NULL,
    -- The email domain, which is what an inbound message is matched against
    -- later. Stored lowercase and bare: example.com, not @example.com or a URL.
    domain     text        NOT NULL DEFAULT '',
    kind       text        NOT NULL DEFAULT 'vendor',
    notes      text        NOT NULL DEFAULT '',

    created_by uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,

    CONSTRAINT companies_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT companies_kind_check
        CHECK (kind IN ('vendor', 'contractor', 'customer', 'manufacturer', 'other'))
);
CREATE INDEX companies_org_idx ON companies (org_id, name) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX companies_domain_idx ON companies (org_id, lower(domain))
    WHERE domain <> '' AND deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- Contacts.
--
-- A person at a company, or a person at no company. company_id is nullable
-- because the electrician who answers his own phone is a contact and not a
-- firm, and forcing a one-person company for him produces a directory full of
-- rows nobody wanted.
-- ---------------------------------------------------------------------------
CREATE TABLE contacts (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    company_id uuid        REFERENCES companies (id) ON DELETE SET NULL,
    name       text        NOT NULL,
    email      text        NOT NULL DEFAULT '',
    phone      text        NOT NULL DEFAULT '',
    title      text        NOT NULL DEFAULT '',
    notes      text        NOT NULL DEFAULT '',

    created_by uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,

    CONSTRAINT contacts_name_not_blank CHECK (btrim(name) <> '')
);
CREATE INDEX contacts_org_idx     ON contacts (org_id, name) WHERE deleted_at IS NULL;
CREATE INDEX contacts_company_idx ON contacts (company_id) WHERE deleted_at IS NULL;
-- One row per address, so an inbound email resolves to exactly one person
-- rather than to whichever duplicate the sort happened to put first.
CREATE UNIQUE INDEX contacts_email_idx ON contacts (org_id, lower(email))
    WHERE email <> '' AND deleted_at IS NULL;

-- Both get the same generated search column documents have, so the Postgres
-- floor covers them without a second code path. Name weighted above the rest,
-- because somebody searching "Baldor" wants the company, not every note that
-- mentions it.
ALTER TABLE companies ADD COLUMN search_tsv tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(name, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(domain, '')), 'B') ||
        setweight(to_tsvector('english', coalesce(notes, '')), 'C')
    ) STORED;
CREATE INDEX companies_search_idx ON companies USING GIN (search_tsv);

ALTER TABLE contacts ADD COLUMN search_tsv tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(name, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(email, '')), 'B') ||
        setweight(to_tsvector('english', coalesce(title, '')), 'B') ||
        setweight(to_tsvector('english',
            coalesce(phone, '') || ' ' || coalesce(notes, '')), 'C')
    ) STORED;
CREATE INDEX contacts_search_idx ON contacts USING GIN (search_tsv);

-- ---------------------------------------------------------------------------
-- Parts.
--
-- on_hand is NOT a column. It is a sum over part_movements, for the same
-- reason downtime is records rather than a counter: a stored quantity can only
-- be adjusted, and "why does this say four when there are none on the shelf"
-- then has no answer. Every change to stock is a row that says who, when, and
-- against which job.
-- ---------------------------------------------------------------------------
CREATE TABLE parts (
    id              uuid        PRIMARY KEY,
    org_id          uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    -- The supplier's part number, which is what somebody reads off the box.
    sku             text        NOT NULL DEFAULT '',
    name            text        NOT NULL,
    -- The current price, used to fill the form in. What a movement actually
    -- cost is copied onto the movement, so a price rise next year does not
    -- rewrite what last year's repair cost.
    unit_cost_cents integer     NOT NULL DEFAULT 0,
    supplier_id     uuid        REFERENCES companies (id) ON DELETE SET NULL,
    -- Below this, the parts page says to order more. Zero means nobody has
    -- said, which reads as unknown rather than as "never reorder".
    reorder_at      integer     NOT NULL DEFAULT 0,

    created_by uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,

    CONSTRAINT parts_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT parts_cost_sane      CHECK (unit_cost_cents >= 0),
    CONSTRAINT parts_reorder_sane   CHECK (reorder_at >= 0)
);
CREATE INDEX parts_org_idx ON parts (org_id, name) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX parts_sku_idx ON parts (org_id, upper(sku))
    WHERE sku <> '' AND deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- Part movements.
--
-- One row per time stock moved, signed: negative consumed, positive received
-- or adjusted. document_id is the job or the purchase order it moved against,
-- which is what makes a work order's parts cost a query rather than a number
-- somebody typed.
--
-- unit_cost_cents is copied here at the moment of the movement. A part that
-- doubles in price next spring must not double what the repair last autumn is
-- recorded as having cost.
-- ---------------------------------------------------------------------------
CREATE TABLE part_movements (
    id              uuid        PRIMARY KEY,
    org_id          uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    part_id         uuid        NOT NULL REFERENCES parts (id) ON DELETE CASCADE,
    -- The work order it was fitted to, or the purchase order it arrived on.
    -- Nullable, because a stocktake correction belongs to neither.
    document_id     uuid        REFERENCES documents (id) ON DELETE SET NULL,
    quantity        numeric     NOT NULL,
    unit_cost_cents integer     NOT NULL DEFAULT 0,
    reason          text        NOT NULL DEFAULT 'consumed',
    note            text        NOT NULL DEFAULT '',

    moved_at   timestamptz NOT NULL DEFAULT now(),
    moved_by   uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,

    CONSTRAINT part_movements_reason_check
        CHECK (reason IN ('consumed', 'received', 'returned', 'adjusted')),
    CONSTRAINT part_movements_quantity_nonzero CHECK (quantity <> 0),
    CONSTRAINT part_movements_cost_sane CHECK (unit_cost_cents >= 0),
    -- The sign and the reason have to agree, or a receipt entered as a
    -- consumption quietly doubles the shelf instead of emptying it.
    CONSTRAINT part_movements_sign_matches_reason CHECK (
        (reason = 'consumed' AND quantity < 0) OR
        (reason = 'received' AND quantity > 0) OR
        (reason = 'returned' AND quantity > 0) OR
        (reason = 'adjusted'))
);
CREATE INDEX part_movements_part_idx ON part_movements (part_id, moved_at DESC);
CREATE INDEX part_movements_doc_idx  ON part_movements (document_id)
    WHERE document_id IS NOT NULL;
CREATE INDEX part_movements_org_idx  ON part_movements (org_id, moved_at DESC);

-- ---------------------------------------------------------------------------
-- A purchase order is a document.
--
-- Not a fifth table. It is the product's whole thesis applied to itself: a work
-- order, an asset, a runbook, and a purchase order are the same row with a
-- different kind, so a purchase order gets versioning, permissions, links,
-- attachments, comments, and search on the day it is created rather than in
-- the release after somebody asks for them.
-- ---------------------------------------------------------------------------
ALTER TABLE documents DROP CONSTRAINT documents_kind_check;
ALTER TABLE documents ADD CONSTRAINT documents_kind_check
    CHECK (kind IN ('doc', 'task', 'wiki', 'work_order', 'asset', 'template',
                    'note', 'purchase_order'));

-- +goose Down
ALTER TABLE documents DROP CONSTRAINT documents_kind_check;
ALTER TABLE documents ADD CONSTRAINT documents_kind_check
    CHECK (kind IN ('doc', 'task', 'wiki', 'work_order', 'asset', 'template', 'note'));
DROP TABLE IF EXISTS part_movements;
DROP TABLE IF EXISTS parts;
DROP TABLE IF EXISTS contacts;
DROP TABLE IF EXISTS companies;
