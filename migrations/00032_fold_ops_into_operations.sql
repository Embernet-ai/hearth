-- Ops and Operations are the same department, twice.
--
-- Both sit at the top level, both have the same five children by name, and the
-- documents are on the Operations side. Ops is the empty copy, left by a run
-- that built the tree before the naming was settled. Same shape as 00025.
--
-- The merge has to be RECURSIVE, and the first version of this was not.
--
-- It merged one level: for each child of the loser, find the twin under the
-- keeper, move that child's children into the twin, delete the child. That is
-- fine until the grandchildren collide too, and here they do. Both trees carry
-- New Business Development, and both of those carry "SUSE White Llabel" and
-- "Mexico Manufacturing Setup". Moving one under the other violates
-- spaces_unique_child, which is exactly what happened against production:
--
--   ERROR: duplicate key value violates unique constraint "spaces_unique_child"
--
-- The local reproduction had children and no grandchildren, so it passed. The
-- fix is to merge at every level rather than at the top one, which is what the
-- function below does by calling itself.
--
-- Nothing was lost by the failure: the whole block is one statement, so it
-- rolled back and the tree was untouched.

-- +goose Up
-- +goose StatementBegin

-- A function rather than nested loops, because "merge a node into its twin" is
-- the same operation at every depth and writing it once is the only way to be
-- sure the deepest level behaves like the top one.
--
-- Temporary: it exists for this transaction and nothing outside this migration
-- can call it.
CREATE OR REPLACE FUNCTION pg_temp.hearth_merge_space(loser uuid, keeper uuid)
RETURNS void AS $fn$
DECLARE
    child RECORD;
    twin  uuid;
    org   uuid;
BEGIN
    SELECT org_id INTO org FROM spaces WHERE id = keeper;

    FOR child IN SELECT id, name FROM spaces WHERE parent_id = loser LOOP
        SELECT id INTO twin FROM spaces
         WHERE parent_id = keeper
           AND lower(btrim(name)) = lower(btrim(child.name))
         LIMIT 1;

        IF twin IS NULL THEN
            -- No collision at this level, so the whole subtree moves intact.
            UPDATE spaces SET parent_id = keeper WHERE id = child.id;
        ELSE
            -- Two nodes of the same name under the same parent. Merge the
            -- subtree into the twin first, at whatever depth it goes, and only
            -- then remove the emptied node.
            PERFORM pg_temp.hearth_merge_space(child.id, twin);
        END IF;
    END LOOP;

    -- Whatever hangs off the loser itself, once its children are dealt with.
    UPDATE documents       SET space_id = keeper WHERE space_id = loser;
    UPDATE calendar_events SET space_id = keeper WHERE space_id = loser;

    INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
    SELECT gen_random_uuid(), g.org_id, 'space', keeper, g.subject_key, g.level, g.granted_by
      FROM grants g WHERE g.entity_type = 'space' AND g.entity_id = loser
    ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING;
    DELETE FROM grants WHERE entity_type = 'space' AND entity_id = loser;

    INSERT INTO space_managers (org_id, space_id, principal_id)
    SELECT org, keeper, m.principal_id FROM space_managers m WHERE m.space_id = loser
    ON CONFLICT (space_id, principal_id) DO NOTHING;
    DELETE FROM space_managers WHERE space_id = loser;

    INSERT INTO principal_departments (org_id, principal_id, space_id)
    SELECT org, pd.principal_id, keeper FROM principal_departments pd WHERE pd.space_id = loser
    ON CONFLICT (principal_id, space_id) DO NOTHING;
    DELETE FROM principal_departments WHERE space_id = loser;

    DELETE FROM spaces WHERE id = loser;
END;
$fn$ LANGUAGE plpgsql;

DO $$
DECLARE
    keeper uuid;
    loser  uuid;
BEGIN
    SELECT id INTO keeper FROM spaces
     WHERE parent_id IS NULL AND lower(btrim(name)) = 'operations' LIMIT 1;
    SELECT id INTO loser FROM spaces
     WHERE parent_id IS NULL AND lower(btrim(name)) = 'ops' LIMIT 1;

    -- Nothing to do on an installation that never had both. A migration that
    -- assumes this company's data would fail on anybody else's.
    IF keeper IS NULL OR loser IS NULL OR keeper = loser THEN
        RETURN;
    END IF;

    PERFORM pg_temp.hearth_merge_space(loser, keeper);
END $$;
-- +goose StatementEnd

-- +goose Down

-- Nothing. Recreating an empty duplicate department would be recreating the
-- mistake, and there is no record of which of the two rows anything came from
-- because they were the same department the whole time. 00025 says the same.
SELECT 1;
