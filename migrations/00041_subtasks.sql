-- Subtasks: a task can sit inside another task.
--
-- ClickUp had them and the import flattened them. Training came over as
-- Training -> Codesys, Ignition, ... -> one task per employee, and arrived as a
-- single list of 119 where a task called "Luis" said nothing about which course
-- it was. The import did record each subtask's parent, as a 'concerns' link from
-- parent to child; this makes it a real parent, so a list can fold subtasks
-- under the task they belong to and a parent works the way a folder did there.
--
-- parent_id is DEFERRABLE for the same reason as 00030's and 00038's keys: a
-- key from documents to documents is a loop, backup orders tables by their keys
-- and skips deferrable ones, and restores with every deferrable check held to
-- commit. ON DELETE SET NULL: a parent purged for good leaves its subtasks as
-- ordinary tasks rather than taking them with it.

-- +goose Up
ALTER TABLE documents
    ADD COLUMN parent_id uuid REFERENCES documents (id) ON DELETE SET NULL
        DEFERRABLE INITIALLY IMMEDIATE;
CREATE INDEX documents_parent_idx ON documents (parent_id) WHERE parent_id IS NOT NULL;

-- The import's parent links become real parents.
--
-- The same link kind is made by hand from a document's page, so a link counts
-- only when it is unmistakably the import's: both ends came from ClickUp, and
-- the link was written during that import run -- within two hours of the
-- subtask itself arriving, since the importer links parents once every task in
-- the list is in. A child with more than one candidate is left alone rather
-- than guessed at, and so is any pair that points both ways.
WITH candidates AS (
    SELECT l.to_id AS child, l.from_id AS parent
      FROM entity_links l
      JOIN external_refs ec ON ec.entity_type = 'document' AND ec.entity_id = l.to_id   AND ec.source = 'clickup'
      JOIN external_refs ep ON ep.entity_type = 'document' AND ep.entity_id = l.from_id AND ep.source = 'clickup'
      JOIN documents c ON c.id = l.to_id
      JOIN documents p ON p.id = l.from_id
     WHERE l.kind = 'concerns'
       AND l.from_type = 'document' AND l.to_type = 'document'
       AND l.from_id <> l.to_id
       AND c.org_id = p.org_id
       AND l.created_at BETWEEN ec.imported_at AND ec.imported_at + interval '2 hours'
), single AS (
    SELECT child, (array_agg(parent))[1] AS parent
      FROM candidates
     GROUP BY child
    HAVING count(*) = 1
)
UPDATE documents d
   SET parent_id = s.parent
  FROM single s
 WHERE d.id = s.child
   AND d.parent_id IS NULL
   AND NOT EXISTS (SELECT 1 FROM single r WHERE r.child = s.parent AND r.parent = s.child);

-- +goose Down
DROP INDEX IF EXISTS documents_parent_idx;
ALTER TABLE documents DROP COLUMN IF EXISTS parent_id;
