-- Two things the import proved were missing, both of them data-loss shaped.
--
-- The first is a duplicate tree. The importer built spaces for ClickUp's
-- hierarchy and nothing stopped it building them again on the next run: the
-- in-memory dedupe lived for one run only, CreateSpace had no uniqueness check,
-- and this table had no constraint. Two runs produced nineteen departments
-- where the source has nine, with documents split arbitrarily between the two
-- copies. A third run would have made three.
--
-- The second is the assignee. ClickUp allows several per task and task_fields
-- holds one, so every assignee after the first survived only as a sentence in
-- the body -- readable by a person, invisible to the workload page, the board
-- and every "assigned to me" filter.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Fold the duplicate spaces together BEFORE constraining, because the
-- constraint cannot be added while the rows it forbids are still there.
--
-- Repeated rather than done once: folding two "Engineering" nodes together
-- makes their children siblings, and those children may now be duplicates of
-- each other. The loop runs until a pass changes nothing, which on a three-level
-- tree is a handful of iterations and on a deeper one still terminates -- every
-- pass strictly reduces the row count.

-- +goose StatementBegin
DO $$
DECLARE
    folded integer;
BEGIN
    LOOP
        -- The survivor of each duplicate group is the oldest, so the ids that
        -- have been referenced longest are the ones that keep working.
        CREATE TEMP TABLE dup_map ON COMMIT DROP AS
        WITH ranked AS (
            SELECT id, org_id, parent_id, lower(btrim(name)) AS key, kind,
                   first_value(id) OVER (
                       PARTITION BY org_id, coalesce(parent_id::text,'root'), lower(btrim(name))
                       ORDER BY created_at, id
                   ) AS keeper
            FROM spaces
        )
        SELECT id AS loser, keeper FROM ranked WHERE id <> keeper;

        SELECT count(*) INTO folded FROM dup_map;
        EXIT WHEN folded = 0;

        -- Documents move to the survivor. This is the step that must not be
        -- lost: a document whose space is deleted without this becomes unfiled.
        UPDATE documents d SET space_id = m.keeper
        FROM dup_map m WHERE d.space_id = m.loser;

        -- Children reparent onto the survivor, which is what can create the
        -- next round of duplicates.
        UPDATE spaces s SET parent_id = m.keeper
        FROM dup_map m WHERE s.parent_id = m.loser;

        -- Grants on a space that is about to disappear. Moved rather than
        -- dropped, so nobody silently loses access to a tree they could see;
        -- ON CONFLICT because the survivor may already carry the same grant.
        INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, granted_at, expires_at)
        SELECT gen_random_uuid(), g.org_id, 'space', m.keeper, g.subject_key, g.level, g.granted_by, g.granted_at, g.expires_at
        FROM grants g JOIN dup_map m ON m.loser = g.entity_id
        WHERE g.entity_type = 'space'
        ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING;

        DELETE FROM grants g USING dup_map m
        WHERE g.entity_type = 'space' AND g.entity_id = m.loser;

        DELETE FROM spaces s USING dup_map m WHERE s.id = m.loser;

        DROP TABLE dup_map;
    END LOOP;
END $$;
-- +goose StatementEnd

-- Two partial indexes rather than one over COALESCE(parent_id, <sentinel>).
-- A sentinel uuid is a value that means "no parent" and looks like a parent,
-- and the day somebody uses it as a real id is the day this stops working.
CREATE UNIQUE INDEX spaces_unique_child
    ON spaces (org_id, parent_id, lower(btrim(name)))
    WHERE parent_id IS NOT NULL;

CREATE UNIQUE INDEX spaces_unique_root
    ON spaces (org_id, lower(btrim(name)))
    WHERE parent_id IS NULL;

-- ---------------------------------------------------------------------------
-- Every assignee, not just the first.
--
-- task_fields.assignee_id stays exactly as it is and stays the primary. Every
-- query, board, filter and rollup in this application reads it, and changing
-- that is a large refactor for a migration to force. This table carries the
-- full set alongside it, and is_primary marks which one the column mirrors.
CREATE TABLE document_assignees (
    document_id  uuid        NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    -- The one task_fields.assignee_id holds. Exactly one per document, which
    -- the partial unique index below enforces rather than trusting the writer.
    is_primary   boolean     NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (document_id, principal_id)
);

CREATE UNIQUE INDEX document_assignees_one_primary
    ON document_assignees (document_id) WHERE is_primary;

CREATE INDEX document_assignees_principal_idx
    ON document_assignees (principal_id);

-- Backfill from what is already there, so the new table is not empty for every
-- task imported before it existed.
INSERT INTO document_assignees (document_id, principal_id, is_primary)
SELECT document_id, assignee_id, true
FROM task_fields WHERE assignee_id IS NOT NULL
ON CONFLICT DO NOTHING;

-- +goose Down

DROP TABLE IF EXISTS document_assignees;
DROP INDEX IF EXISTS spaces_unique_root;
DROP INDEX IF EXISTS spaces_unique_child;
