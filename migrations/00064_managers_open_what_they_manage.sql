-- Managers open what they manage.
--
-- Whoever may delegate a folder (a manager of it, or of a folder above it) can
-- also open it and everything under it, at view level at least. It only adds:
-- a row is never written over a grant somebody already holds, so nobody's level
-- goes down.
--
-- It is done the way a delegation is, with grant rows marked by the folder they
-- came from (via_space_id, migration 00063), so every list, search and page that
-- asks the grants table sees it without learning a new rule. via_manager marks
-- the rows that exist because somebody manages the folder, so that taking a
-- manager off, or taking back a delegation to the same person, never removes the
-- other kind.

-- +goose Up
ALTER TABLE grants ADD COLUMN via_manager boolean NOT NULL DEFAULT false;

-- Give a manager view on a folder, everything under it and every document in it.
-- +goose StatementBegin
CREATE FUNCTION hearth_apply_manager(space uuid, who uuid) RETURNS void AS $$
DECLARE
    org uuid;
BEGIN
    SELECT s.org_id INTO org FROM spaces s WHERE s.id = space;
    IF org IS NULL THEN RETURN; END IF;
    INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, via_space_id, via_manager)
    SELECT gen_random_uuid(), org, 'space', t.id, 'principal:' || who::text, 'view', who, space, true
      FROM hearth_space_subtree(space) t
    ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING;
    INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, via_space_id, via_manager)
    SELECT gen_random_uuid(), org, 'document', d.id, 'principal:' || who::text, 'view', who, space, true
      FROM documents d JOIN hearth_space_subtree(space) t ON t.id = d.space_id
     WHERE d.deleted_at IS NULL
    ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING;
END
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- A new manager, and a manager taken off.
-- +goose StatementBegin
CREATE FUNCTION hearth_managers_changed() RETURNS trigger AS $$
DECLARE
    r record;
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM hearth_apply_manager(NEW.space_id, NEW.principal_id);
        RETURN NULL;
    END IF;
    DELETE FROM grants
     WHERE via_space_id = OLD.space_id AND via_manager
       AND subject_key = 'principal:' || OLD.principal_id::text;
    -- Still a manager somewhere else: what that gives is theirs again, where
    -- this role had been the one holding a row.
    FOR r IN SELECT m.space_id FROM space_managers m WHERE m.principal_id = OLD.principal_id
    LOOP
        PERFORM hearth_apply_manager(r.space_id, OLD.principal_id);
    END LOOP;
    RETURN NULL;
END
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER space_managers_open
    AFTER INSERT OR DELETE ON space_managers
    FOR EACH ROW EXECUTE FUNCTION hearth_managers_changed();

-- New work, and work that moves, takes the manager rows as well as the
-- delegations: the same triggers, now carrying via_manager along.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION hearth_delegate_document() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        DELETE FROM grants g
         WHERE g.entity_type = 'document' AND g.entity_id = NEW.id
           AND g.via_space_id IS NOT NULL
           AND (NEW.space_id IS NULL
                OR g.via_space_id NOT IN (SELECT a.id FROM hearth_space_ancestors(NEW.space_id) a));
    END IF;
    IF NEW.space_id IS NOT NULL THEN
        INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, expires_at, via_space_id, via_manager)
        SELECT gen_random_uuid(), NEW.org_id, 'document', NEW.id, g.subject_key, g.level,
               g.granted_by, g.expires_at, g.via_space_id, g.via_manager
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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION hearth_delegate_space() RETURNS trigger AS $$
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
        INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, expires_at, via_space_id, via_manager)
        SELECT gen_random_uuid(), NEW.org_id, 'space', t.id, p.subject_key, p.level,
               p.granted_by, p.expires_at, p.via_space_id, p.via_manager
          FROM grants p, hearth_space_subtree(NEW.id) t
         WHERE p.entity_type = 'space' AND p.entity_id = NEW.parent_id
           AND p.via_space_id IS NOT NULL
           AND (p.expires_at IS NULL OR p.expires_at > now())
        ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING;
        INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, expires_at, via_space_id, via_manager)
        SELECT gen_random_uuid(), NEW.org_id, 'document', d.id, p.subject_key, p.level,
               p.granted_by, p.expires_at, p.via_space_id, p.via_manager
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

-- Everybody who manages a folder today opens it from now on. One pass; a row
-- somebody already holds is left exactly as it is.
-- +goose StatementBegin
DO $$
DECLARE
    r record;
BEGIN
    FOR r IN SELECT space_id, principal_id FROM space_managers LOOP
        PERFORM hearth_apply_manager(r.space_id, r.principal_id);
    END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS space_managers_open ON space_managers;
DROP FUNCTION IF EXISTS hearth_managers_changed();
DROP FUNCTION IF EXISTS hearth_apply_manager(uuid, uuid);
DELETE FROM grants WHERE via_manager;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION hearth_delegate_document() RETURNS trigger AS $$
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
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION hearth_delegate_space() RETURNS trigger AS $$
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
ALTER TABLE grants DROP COLUMN IF EXISTS via_manager;
