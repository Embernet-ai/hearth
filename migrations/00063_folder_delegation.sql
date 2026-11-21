-- Delegating one folder.
--
-- A space manager can give a person view, comment or edit on one folder or
-- subfolder, at any depth, with an optional end date. The person reaches that
-- folder and everything under it and nothing above or beside it.
--
-- Access in Hearth is a grant row per space and per document, so a delegation is
-- the same rows with one extra fact: which folder it was delegated from.
-- via_space_id is that folder. A row with no via_space_id is any other grant
-- (a department grant from the People page, an owner's admin, a share) and a
-- delegation never changes or removes one of those.
--
-- Two triggers keep the rows true on every path that makes or moves work: the
-- web pages, the API, templates, imports, the EmberOps back end's spine. A new
-- space or document takes the delegated rows of the space it sits in; a move
-- drops the delegated rows of a folder it has left and takes those of the one it
-- has entered. Doing it in the database is what stops a path that forgot from
-- leaving a contractor on something that was moved out from under them.
--
-- principals.contractor marks an account that exists to reach what it was given
-- and nothing else. It is not in any department, and the open daily reports
-- department (spaces.open_to_all) does not reach it.

-- +goose Up
ALTER TABLE grants ADD COLUMN via_space_id uuid;
CREATE INDEX grants_via_idx ON grants (via_space_id, subject_key) WHERE via_space_id IS NOT NULL;

ALTER TABLE principals ADD COLUMN contractor boolean NOT NULL DEFAULT false;

-- +goose StatementBegin
CREATE FUNCTION hearth_space_ancestors(from_id uuid) RETURNS TABLE (id uuid) AS $$
    WITH RECURSIVE up AS (
        SELECT s.id, s.parent_id, 0 AS depth FROM spaces s WHERE s.id = from_id
      UNION ALL
        SELECT p.id, p.parent_id, u.depth + 1 FROM spaces p JOIN up u ON p.id = u.parent_id
         WHERE u.depth < 32
    )
    SELECT up.id FROM up
$$ LANGUAGE sql STABLE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION hearth_space_subtree(from_id uuid) RETURNS TABLE (id uuid) AS $$
    WITH RECURSIVE down AS (
        SELECT s.id, 0 AS depth FROM spaces s WHERE s.id = from_id
      UNION ALL
        SELECT c.id, d.depth + 1 FROM spaces c JOIN down d ON c.parent_id = d.id
         WHERE d.depth < 32
    )
    SELECT down.id FROM down
$$ LANGUAGE sql STABLE;
-- +goose StatementEnd

-- A document takes the delegated rows of its space, and loses those of a folder
-- it has left.
-- +goose StatementBegin
CREATE FUNCTION hearth_delegate_document() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        DELETE FROM grants g
         WHERE g.entity_type = 'document' AND g.entity_id = NEW.id
           AND g.via_space_id IS NOT NULL
           AND (NEW.space_id IS NULL
                OR g.via_space_id NOT IN (SELECT a.id FROM hearth_space_ancestors(NEW.space_id) a));
    END IF;
    IF NEW.space_id IS NOT NULL THEN
        INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, expires_at, via_space_id)
        SELECT gen_random_uuid(), NEW.org_id, 'document', NEW.id, g.subject_key, g.level,
               g.granted_by, g.expires_at, g.via_space_id
          FROM grants g
         WHERE g.entity_type = 'space' AND g.entity_id = NEW.space_id
           AND g.via_space_id IS NOT NULL
           AND (g.expires_at IS NULL OR g.expires_at > now())
        ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING;
    END IF;
    RETURN NULL;
END
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER documents_delegate
    AFTER INSERT OR UPDATE OF space_id ON documents
    FOR EACH ROW EXECUTE FUNCTION hearth_delegate_document();

-- A space takes the delegated rows of its parent, along with everything under
-- it, and loses those of a folder it has left. A delegation rooted inside the
-- moved folder travels with it.
-- +goose StatementBegin
CREATE FUNCTION hearth_delegate_space() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        DELETE FROM grants g
         WHERE g.via_space_id IS NOT NULL
           AND g.via_space_id NOT IN (SELECT t.id FROM hearth_space_subtree(NEW.id) t)
           AND (NEW.parent_id IS NULL
                OR g.via_space_id NOT IN (SELECT a.id FROM hearth_space_ancestors(NEW.parent_id) a))
           AND ((g.entity_type = 'space' AND g.entity_id IN (SELECT t.id FROM hearth_space_subtree(NEW.id) t))
             OR (g.entity_type = 'document' AND g.entity_id IN
                   (SELECT d.id FROM documents d
                     WHERE d.space_id IN (SELECT t.id FROM hearth_space_subtree(NEW.id) t))));
    END IF;
    IF NEW.parent_id IS NOT NULL THEN
        INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, expires_at, via_space_id)
        SELECT gen_random_uuid(), NEW.org_id, 'space', t.id, p.subject_key, p.level,
               p.granted_by, p.expires_at, p.via_space_id
          FROM grants p, hearth_space_subtree(NEW.id) t
         WHERE p.entity_type = 'space' AND p.entity_id = NEW.parent_id
           AND p.via_space_id IS NOT NULL
           AND (p.expires_at IS NULL OR p.expires_at > now())
        ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING;
        INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, expires_at, via_space_id)
        SELECT gen_random_uuid(), NEW.org_id, 'document', d.id, p.subject_key, p.level,
               p.granted_by, p.expires_at, p.via_space_id
          FROM grants p, hearth_space_subtree(NEW.id) t, documents d
         WHERE p.entity_type = 'space' AND p.entity_id = NEW.parent_id
           AND p.via_space_id IS NOT NULL
           AND (p.expires_at IS NULL OR p.expires_at > now())
           AND d.space_id = t.id AND d.deleted_at IS NULL
        ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING;
    END IF;
    RETURN NULL;
END
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER spaces_delegate
    AFTER INSERT OR UPDATE OF parent_id ON spaces
    FOR EACH ROW EXECUTE FUNCTION hearth_delegate_space();

-- +goose Down
DROP TRIGGER IF EXISTS spaces_delegate ON spaces;
DROP TRIGGER IF EXISTS documents_delegate ON documents;
DROP FUNCTION IF EXISTS hearth_delegate_space();
DROP FUNCTION IF EXISTS hearth_delegate_document();
DROP FUNCTION IF EXISTS hearth_space_subtree(uuid);
DROP FUNCTION IF EXISTS hearth_space_ancestors(uuid);
ALTER TABLE principals DROP COLUMN IF EXISTS contractor;
DROP INDEX IF EXISTS grants_via_idx;
ALTER TABLE grants DROP COLUMN IF EXISTS via_space_id;
