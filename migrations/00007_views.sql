-- Saved views, and the indexes that make filtering and sorting on custom
-- properties something the database does rather than something Go does after
-- the fact.
--
-- Post-filtering in Go breaks LIMIT: the database does not know which rows will
-- survive, so it hands back a full page, Go discards half of it, and the second
-- page silently loses records. Every filter here has to be expressible in SQL,
-- which is why the index below is not optional.

-- +goose Up

-- btree_gin lets a GIN index cover a plain uuid column alongside a jsonb one.
-- Without it there is no single index that answers "this property, this value",
-- and the def_id has to be a separate scan.
CREATE EXTENSION IF NOT EXISTS btree_gin;

-- ---------------------------------------------------------------------------
-- Saved views: a filtered, sorted, grouped list somebody named.
--
-- The query is jsonb rather than columns because the shape changes with every
-- new filter type, and a saved view is only ever read whole. It is also the
-- same structure the URL encodes, so a view and a link to a filtered list are
-- the same object and cannot drift apart.
--
-- A view is NOT an entity and gets no grants. It stores a question, not
-- answers: two people opening the same view run the same query against their
-- own subject set and see the rows they are each allowed to see. Granting a
-- view would imply it carries data, and then sharing one would be a disclosure.
-- ---------------------------------------------------------------------------
CREATE TABLE saved_views (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE RESTRICT,
    owner_id   uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    space_id   uuid        REFERENCES spaces (id) ON DELETE SET NULL,
    name       text        NOT NULL,
    kind       text        NOT NULL DEFAULT 'table',
    query      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    shared     boolean     NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT saved_views_kind_check
        CHECK (kind IN ('table', 'board', 'calendar', 'timeline')),
    CONSTRAINT saved_views_name_not_blank CHECK (btrim(name) <> '')
);

-- Names are unique per person, not per org. Two people are each allowed a view
-- called "Mine", and neither of them should have to find out the other got
-- there first.
CREATE UNIQUE INDEX saved_views_owner_name_key ON saved_views (org_id, owner_id, name);
CREATE INDEX saved_views_owner_idx  ON saved_views (org_id, owner_id);
CREATE INDEX saved_views_shared_idx ON saved_views (org_id) WHERE shared;
CREATE INDEX saved_views_space_idx  ON saved_views (space_id) WHERE space_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- The index that makes a custom property a real field.
--
-- Verified with EXPLAIN rather than assumed, because the two obvious ways to
-- write the lookup do not perform the same:
--
--   value @> to_jsonb($1)   Index Cond covers def_id AND value
--   value =  to_jsonb($1)   Index Cond covers def_id only, value is a filter
--
-- Containment is indexable and equality is not, so every property filter in
-- internal/docs is written with @>. On a scalar the two mean the same thing,
-- which is what makes the substitution safe.
-- ---------------------------------------------------------------------------
CREATE INDEX property_values_def_value_gin ON property_values USING gin (def_id, value);

-- Sorting a list of one kind by when it changed is the default on every screen,
-- and it was doing it with two separate indexes and a sort.
CREATE INDEX documents_org_kind_updated_idx
    ON documents (org_id, kind, updated_at DESC) WHERE deleted_at IS NULL;

-- Status belongs to task_fields, not documents, so the composite the plan asked
-- for splits in two: documents carries (org_id, kind) and this carries status.
-- One index over both tables is not a thing Postgres has.
CREATE INDEX task_fields_status_idx ON task_fields (status, document_id);

-- +goose Down
DROP INDEX IF EXISTS task_fields_status_idx;
DROP INDEX IF EXISTS documents_org_kind_updated_idx;
DROP INDEX IF EXISTS property_values_def_value_gin;
DROP TABLE IF EXISTS saved_views;
