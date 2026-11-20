-- What a company is to us: client, vendor, partner, prospect, and whatever else the
-- admins add, any number at once, with free tags beside them.
--
-- companies.kind was one word per company, and a firm that buys from us and sells to
-- us is two things. kind stays where it is, so nothing that reads it changes; types
-- are the richer answer and the one the CRM filters on.
--
-- erp_role is how a type lines up with EmberOps: client with a Customer, vendor
-- with a Supplier. Empty means the type has no accounting counterpart.

-- +goose Up

CREATE TABLE company_types (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    key        text        NOT NULL,
    label      text        NOT NULL,
    erp_role   text        NOT NULL DEFAULT '',
    position   integer     NOT NULL DEFAULT 0,
    archived   boolean     NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT company_types_key_shape CHECK (key ~ '^[a-z][a-z0-9_]{0,31}$'),
    CONSTRAINT company_types_label_not_blank CHECK (btrim(label) <> ''),
    CONSTRAINT company_types_erp_role_check CHECK (erp_role IN ('', 'customer', 'supplier')),
    UNIQUE (org_id, key)
);

CREATE TABLE company_type_links (
    company_id uuid NOT NULL REFERENCES companies (id) ON DELETE CASCADE,
    type_id    uuid NOT NULL REFERENCES company_types (id) ON DELETE CASCADE,
    PRIMARY KEY (company_id, type_id)
);
CREATE INDEX company_type_links_type_idx ON company_type_links (type_id);

CREATE TABLE company_tags (
    company_id uuid NOT NULL REFERENCES companies (id) ON DELETE CASCADE,
    tag        text NOT NULL,
    CONSTRAINT company_tags_not_blank CHECK (btrim(tag) <> ''),
    CONSTRAINT company_tags_short CHECK (length(tag) <= 48)
);
CREATE UNIQUE INDEX company_tags_unique_idx ON company_tags (company_id, lower(tag));
CREATE INDEX company_tags_tag_idx ON company_tags (lower(tag));

-- A company can now have no kind at all. The importer used to say "prospect" for
-- every Twenty company that had no deal, which was a guess, and a blank is the
-- honest value for one nobody has said anything about.
ALTER TABLE companies DROP CONSTRAINT companies_kind_check;
ALTER TABLE companies ADD CONSTRAINT companies_kind_check
    CHECK (kind IN ('', 'vendor', 'contractor', 'customer', 'manufacturer', 'other', 'prospect'));

-- The starting list, per organisation. Admins edit it afterwards.
INSERT INTO company_types (id, org_id, key, label, erp_role, position)
SELECT gen_random_uuid(), o.id, t.key, t.label, t.erp, t.pos
FROM orgs o
CROSS JOIN (VALUES
    ('client',       'Client',       'customer', 1),
    ('prospect',     'Prospect',     '',         2),
    ('vendor',       'Vendor',       'supplier', 3),
    ('partner',      'Partner',      '',         4),
    ('contractor',   'Contractor',   'supplier', 5),
    ('manufacturer', 'Manufacturer', 'supplier', 6)
) AS t(key, label, erp, pos)
ON CONFLICT DO NOTHING;

-- Companies the import tagged "prospect" with nothing behind it go back to blank.
-- Evidence of a prospect is a deal; a company Twenty held with no deal at all gave
-- no reason to call it anything. Only rows the Twenty import made are touched.
UPDATE companies c SET kind = ''
 WHERE c.kind = 'prospect'
   AND EXISTS (SELECT 1 FROM external_refs r WHERE r.entity_id = c.id
                AND r.source = 'twenty' AND r.entity_type = 'company')
   AND NOT EXISTS (SELECT 1 FROM deals d WHERE d.company_id = c.id AND d.deleted_at IS NULL);

-- First types, from what people or the data actually said: a kind somebody chose
-- (or a won or customer deal, which set customer) becomes the matching type.
-- 'other' and blank say nothing, so they get nothing.
INSERT INTO company_type_links (company_id, type_id)
SELECT c.id, t.id
FROM companies c
JOIN company_types t ON t.org_id = c.org_id AND t.key = CASE c.kind
        WHEN 'customer' THEN 'client' WHEN 'prospect' THEN 'prospect'
        WHEN 'vendor' THEN 'vendor' WHEN 'contractor' THEN 'contractor'
        WHEN 'manufacturer' THEN 'manufacturer' END
WHERE c.deleted_at IS NULL
ON CONFLICT DO NOTHING;

-- +goose Down
ALTER TABLE companies DROP CONSTRAINT companies_kind_check;
UPDATE companies SET kind = 'other' WHERE kind = '';
ALTER TABLE companies ADD CONSTRAINT companies_kind_check
    CHECK (kind IN ('vendor', 'contractor', 'customer', 'manufacturer', 'other', 'prospect'));
DROP TABLE IF EXISTS company_tags;
DROP TABLE IF EXISTS company_type_links;
DROP TABLE IF EXISTS company_types;
