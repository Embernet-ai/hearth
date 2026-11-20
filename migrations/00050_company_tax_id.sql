-- A company's tax ID (an EIN for a US firm). Optional, and Hearth's to own.
--
-- EmberOps refuses to make a Supplier without one under the US compliance pack,
-- and the sync sends it as us_ein when it is here. A company without one is
-- reported as needing an EIN rather than failing the run.

-- +goose Up
ALTER TABLE companies ADD COLUMN tax_id text NOT NULL DEFAULT '';
ALTER TABLE companies ADD CONSTRAINT companies_tax_id_short CHECK (length(tax_id) <= 32);

-- +goose Down
ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_tax_id_short;
ALTER TABLE companies DROP COLUMN IF EXISTS tax_id;
