-- When a deal was won or lost, so "won this month" can be a query and not a guess.
--
-- A deal had a stage and an updated_at, and updated_at moves on every edit, a
-- renamed deal or a moved card among them. Counting wins by it would call last
-- year's win this month's the first time somebody fixed a typo in it. closed_at
-- is set when a deal enters won, lost, or no response, cleared when it goes
-- back to an open stage, and left alone by every other edit. A trigger does it
-- so the one rule holds for every writer: the forms, the API, an importer.

-- +goose Up
ALTER TABLE deals ADD COLUMN closed_at timestamptz;

-- Deals that were already closed: the time of the last stage change recorded
-- for them when there is one, otherwise the last time the row changed, which is
-- the best that was ever kept. Only these older rows can be off; every close
-- from here on is exact.
UPDATE deals d SET closed_at = COALESCE(
    (SELECT max(o.created_at) FROM outbox o
      WHERE o.entity_type = 'deal' AND o.entity_id = d.id
        AND o.payload->>'stage' IN ('won', 'lost', 'no_response')),
    d.updated_at)
 WHERE d.stage IN ('won', 'lost', 'no_response');

CREATE INDEX deals_closed_idx ON deals (org_id, owner_id, closed_at) WHERE closed_at IS NOT NULL AND deleted_at IS NULL;

-- +goose StatementBegin
CREATE FUNCTION deals_set_closed_at() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.stage IN ('won', 'lost', 'no_response') THEN
        IF TG_OP = 'INSERT' THEN
            NEW.closed_at := COALESCE(NEW.closed_at, now());
        ELSIF OLD.stage IS DISTINCT FROM NEW.stage THEN
            NEW.closed_at := now();
        END IF;
    ELSE
        NEW.closed_at := NULL;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER deals_closed_at BEFORE INSERT OR UPDATE OF stage ON deals
    FOR EACH ROW EXECUTE FUNCTION deals_set_closed_at();

-- +goose Down
DROP TRIGGER IF EXISTS deals_closed_at ON deals;
DROP FUNCTION IF EXISTS deals_set_closed_at();
DROP INDEX IF EXISTS deals_closed_idx;
ALTER TABLE deals DROP COLUMN IF EXISTS closed_at;
