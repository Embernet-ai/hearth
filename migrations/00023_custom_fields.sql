-- The property types an imported workspace actually uses.
--
-- This started as a second custom-field system -- its own definitions table and
-- a jsonb column on documents -- written without noticing that `property_defs`
-- and `property_values` have been in the substrate since 00002 and that
-- properties.go opens with "PropertyDef is a custom field". Two systems for one
-- job is how a schema rots: values land in whichever one the writer knew about,
-- and every reader has to check both. There is one, and this extends it.
--
-- What the ClickUp inventory needs that the original five types did not cover.
-- 62 definitions across 17 vendor types, and most collapse onto what already
-- exists: short_text and multi_key are text, drop_down is select, users is
-- principal, checkbox is boolean, and labels is select with is_multi, which the
-- table has carried from the start.
--
-- Three do not collapse without losing something a reader needs:
--
--   currency  A number renders as 1250.5. Money renders as 1,250.50 with a
--             symbol, and "Budget" and "Desired Salary" are both currency here.
--   email     Displays as a mailto and validates as an address. Storing it as
--             text means neither.
--   phone     Same argument, and this workspace has three of them.
--
-- Deliberately NOT added: location, progress, formula, attachment, signature
-- and relation. Each is one field in one space, none of them is edited here,
-- and a type nothing renders differently is a type that only makes the CHECK
-- longer. They import as text and keep their value.

-- +goose Up

ALTER TABLE property_defs DROP CONSTRAINT property_defs_data_type_check;
ALTER TABLE property_defs ADD CONSTRAINT property_defs_data_type_check
    CHECK (data_type IN ('text', 'number', 'boolean', 'date', 'select',
                         'principal', 'url', 'currency', 'email', 'phone'));

-- Where a definition came from, so a re-import updates its own rows and cannot
-- adopt one somebody created by hand.
--
-- Nullable, and null means "made here". Matching on `key` alone would let a
-- second import of a differently-named field with the same key overwrite it.
ALTER TABLE property_defs ADD COLUMN source      text;
ALTER TABLE property_defs ADD COLUMN external_id text;

CREATE UNIQUE INDEX property_defs_external_idx
    ON property_defs (org_id, source, external_id)
    WHERE source IS NOT NULL AND external_id IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS property_defs_external_idx;
ALTER TABLE property_defs DROP COLUMN IF EXISTS external_id;
ALTER TABLE property_defs DROP COLUMN IF EXISTS source;
ALTER TABLE property_defs DROP CONSTRAINT property_defs_data_type_check;
ALTER TABLE property_defs ADD CONSTRAINT property_defs_data_type_check
    CHECK (data_type IN ('text', 'number', 'boolean', 'date', 'select',
                         'principal', 'url'));
