-- Putting the work back under the org chart.
--
-- The ClickUp import ran after the departments were arranged, and quietly
-- rebuilt what that arranging had just fixed. The importer matched a space by
-- (parent, name); Red Team had been moved under Engineering; so it looked for
-- Red Team at the root, did not find it, created a SECOND one beside
-- Engineering, and filed all 205 of its tasks there. Same for White Team,
-- White Team Service, Embernet and Ops.
--
-- The shape that leaves is the worst available: the org chart still looks
-- correct and every department under it is empty, because the work sits in a
-- parallel set of root spaces nobody navigates to. Nothing errored, no count
-- changed, and the totals still reconcile.
--
-- The code no longer does this -- the source's top level is matched by name
-- anywhere in the tree now, so an import respects where somebody has filed a
-- space. This repairs what the one run that predates that fix left behind.
--
-- A one-off repair, and safe to be one: on a database where it does not apply
-- the merge selects nothing and the fold exits on its first pass.

-- +goose Up

-- +goose StatementBegin
DO $$
DECLARE
    merged  integer;
    folded  integer;
BEGIN
    -- Nothing to do unless a root space has a same-named twin that somebody
    -- has filed under a department. That pairing is the signature of this
    -- specific mistake and of nothing else: an ordinary tree does not contain
    -- "Red Team" at the root AND "Red Team" under Engineering.
    SELECT count(*) INTO merged
    FROM spaces r
    JOIN spaces k ON lower(btrim(k.name)) = lower(btrim(r.name))
                 AND k.id <> r.id AND k.parent_id IS NOT NULL
    WHERE r.parent_id IS NULL;

    IF merged = 0 THEN
        RETURN;
    END IF;

    -- The uniqueness has to stand down for the move, because reparenting a
    -- child onto the keeper is precisely what creates the duplicate the fold
    -- then removes. Inside one transaction, so it is never absent to anything
    -- else.
    DROP INDEX spaces_unique_child;
    DROP INDEX spaces_unique_root;

    CREATE TEMP TABLE root_dup ON COMMIT DROP AS
    SELECT r.id AS loser, k.id AS keeper
    FROM spaces r
    JOIN spaces k ON lower(btrim(k.name)) = lower(btrim(r.name))
                 AND k.id <> r.id AND k.parent_id IS NOT NULL
    WHERE r.parent_id IS NULL;

    -- Children first: the documents live in the projects beneath these nodes
    -- rather than on the nodes themselves, so this is the step that actually
    -- carries the work back.
    UPDATE spaces s SET parent_id = m.keeper FROM root_dup m WHERE s.parent_id = m.loser;
    UPDATE documents d SET space_id = m.keeper FROM root_dup m WHERE d.space_id = m.loser;

    INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, granted_at, expires_at)
    SELECT gen_random_uuid(), g.org_id, 'space', m.keeper, g.subject_key, g.level,
           g.granted_by, g.granted_at, g.expires_at
    FROM grants g JOIN root_dup m ON m.loser = g.entity_id
    WHERE g.entity_type = 'space'
    ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING;

    DELETE FROM grants g USING root_dup m
     WHERE g.entity_type = 'space' AND g.entity_id = m.loser;
    DELETE FROM spaces s USING root_dup m WHERE s.id = m.loser;
    DROP TABLE root_dup;

    -- Fold whatever that made duplicate, the same way 00024 does. Merging two
    -- parents makes their children siblings, and those children may now
    -- duplicate each other. Every pass strictly reduces the row count, so this
    -- terminates.
    LOOP
        CREATE TEMP TABLE dup ON COMMIT DROP AS
        WITH ranked AS (
            SELECT id, first_value(id) OVER (
                       PARTITION BY org_id, coalesce(parent_id::text,'root'), lower(btrim(name))
                       ORDER BY created_at, id) AS keeper
            FROM spaces)
        SELECT id AS loser, keeper FROM ranked WHERE id <> keeper;

        SELECT count(*) INTO folded FROM dup;
        EXIT WHEN folded = 0;

        UPDATE documents d SET space_id = x.keeper FROM dup x WHERE d.space_id = x.loser;
        UPDATE spaces    s SET parent_id = x.keeper FROM dup x WHERE s.parent_id = x.loser;

        INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, granted_at, expires_at)
        SELECT gen_random_uuid(), g.org_id, 'space', x.keeper, g.subject_key, g.level,
               g.granted_by, g.granted_at, g.expires_at
        FROM grants g JOIN dup x ON x.loser = g.entity_id
        WHERE g.entity_type = 'space'
        ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING;

        DELETE FROM grants g USING dup x WHERE g.entity_type='space' AND g.entity_id = x.loser;
        DELETE FROM spaces s USING dup x WHERE s.id = x.loser;
        DROP TABLE dup;
    END LOOP;

    CREATE UNIQUE INDEX spaces_unique_child
        ON spaces (org_id, parent_id, lower(btrim(name))) WHERE parent_id IS NOT NULL;
    CREATE UNIQUE INDEX spaces_unique_root
        ON spaces (org_id, lower(btrim(name))) WHERE parent_id IS NULL;
END $$;
-- +goose StatementEnd

-- +goose Down

-- Nothing. The duplicate roots were a mistake, and recreating them would be
-- recreating the mistake. The indexes 00024 added are still in place.
SELECT 1;
