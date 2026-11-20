-- Spaces carry an order, so the tree can be arranged rather than always sorted
-- by name.
--
-- Until now the rail and the Spaces page sorted departments and folders
-- alphabetically, which is no order at all once somebody wants Operations
-- above Sales because that is how the company reads. position is the order
-- among siblings -- the children of one parent -- and the tree orders by it,
-- then by name to break a tie. It is the groundwork the per-person sidebar
-- layout will build on later; for now one order is shared by everyone.
--
-- Backfilled from the current alphabetical order, so nothing jumps on the day
-- this lands; people move things deliberately afterwards.

-- +goose Up
ALTER TABLE spaces ADD COLUMN position integer NOT NULL DEFAULT 0;

WITH ordered AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY org_id, COALESCE(parent_id::text, 'root')
               ORDER BY lower(name), id
           ) - 1 AS pos
      FROM spaces
)
UPDATE spaces s SET position = o.pos FROM ordered o WHERE s.id = o.id;

-- +goose Down
ALTER TABLE spaces DROP COLUMN IF EXISTS position;
