-- The work that decides whether this is a product or a demo that happens to be
-- running: who did what, who belongs to which team, and where the people come
-- from.

-- +goose Up

-- ---------------------------------------------------------------------------
-- The audit log.
--
-- Append only, and enforced by the database rather than by everybody
-- remembering. A log the application can quietly rewrite is a log an auditor is
-- right to disbelieve, and "we only ever insert" is a property of today's code
-- rather than of the data.
--
-- before and after are jsonb rather than a diff, because the question asked
-- three months later is "what did it say before" and a diff cannot answer that
-- without replaying every row since the beginning.
-- ---------------------------------------------------------------------------
CREATE TABLE audit_log (
    id          bigserial   PRIMARY KEY,
    org_id      uuid        NOT NULL REFERENCES orgs (id) ON DELETE RESTRICT,
    -- Who. Nullable for exactly one case: something the system did with nobody
    -- behind it, such as a scheduled export. A null here means "nobody", never
    -- "we did not record it", and the page says so.
    actor_id    uuid        REFERENCES principals (id) ON DELETE RESTRICT,
    -- What, as a dotted verb: document.delete, grant.add, team.member.remove,
    -- export.download. Text rather than an enum, because an enum needs a
    -- migration to add a verb and the pressure that creates is to log the
    -- nearest existing one instead.
    action      text        NOT NULL,
    entity_type text        NOT NULL DEFAULT '',
    entity_id   uuid,
    before      jsonb,
    after       jsonb,
    -- Where from, for the questions that are really about a person's laptop.
    ip          inet,
    at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT audit_log_action_not_blank CHECK (btrim(action) <> '')
);

-- ON DELETE RESTRICT above, deliberately.
--
-- Every other reference to principals cascades, because deleting a person
-- should take their sessions with them. The audit log is the one place where
-- the reverse is true: a foreign key that cascades would let deleting a
-- principal erase the record of what they did, which is precisely the record
-- somebody deleting a principal might want gone.

CREATE INDEX audit_log_org_at_idx ON audit_log (org_id, at DESC);
CREATE INDEX audit_log_actor_idx ON audit_log (org_id, actor_id, at DESC);
CREATE INDEX audit_log_entity_idx ON audit_log (entity_type, entity_id, at DESC)
    WHERE entity_id IS NOT NULL;
CREATE INDEX audit_log_action_idx ON audit_log (org_id, action, at DESC);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_log_is_append_only() RETURNS trigger AS $$
BEGIN
    -- One deliberate way out, and it is not a way the application can take by
    -- accident. See audit_purge_org below for what it is for.
    IF TG_OP <> 'UPDATE'
       AND COALESCE(current_setting('hearth.audit_purge', true), '') = 'on' THEN
        RETURN CASE TG_OP WHEN 'DELETE' THEN OLD ELSE NULL END;
    END IF;
    RAISE EXCEPTION 'audit_log is append only: % was refused', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- A trigger rather than a rule, because a rule that turns an UPDATE into
-- nothing succeeds silently and the caller believes it worked. This one
-- refuses, loudly, and the refusal is what a test can prove.
--
-- UPDATE has no way out at all. There is no legitimate reason to change what a
-- row says happened, and an escape hatch nobody needs is one somebody
-- eventually uses.
CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_is_append_only();
CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_is_append_only();
-- TRUNCATE bypasses a row level trigger entirely, which would leave the whole
-- protection above as theatre.
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_is_append_only();

-- +goose StatementBegin
-- The one legal way to remove audit rows: taking a whole tenant away.
--
-- It exists because a log that literally cannot be deleted from makes it
-- impossible to remove an organisation, and "we can never delete your data" is
-- not a defensible answer to somebody leaving. It takes an org and nothing
-- finer: there is no way to reach in and remove the one row about the one
-- thing somebody would rather nobody saw.
--
-- The setting is transaction local, so it is gone the moment this returns, and
-- a test asserts that no source file outside a test fixture ever calls this.
CREATE OR REPLACE FUNCTION audit_purge_org(target uuid) RETURNS bigint AS $$
DECLARE
    removed bigint;
BEGIN
    PERFORM set_config('hearth.audit_purge', 'on', true);
    DELETE FROM audit_log WHERE org_id = target;
    GET DIAGNOSTICS removed = ROW_COUNT;
    PERFORM set_config('hearth.audit_purge', 'off', true);
    RETURN removed;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- ---------------------------------------------------------------------------
-- Teams.
--
-- Spending the team subject the grants table has accepted since the substrate
-- migration. Nothing in the permission model changes: a team is a subject key
-- like a channel is, so granting a team access to a space is the same statement
-- that grants a person one, and the MAX over the subject set does the rest.
--
-- external_id is how a team maps to a group in the directory. Null means the
-- team was made here and the directory does not know about it, which is a
-- normal thing for a small site and must not be quietly overwritten by a sync.
-- ---------------------------------------------------------------------------
CREATE TABLE teams (
    id          uuid        PRIMARY KEY,
    org_id      uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    name        text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    -- The directory's own id for the group. Stable across a rename there,
    -- which is why the join is on this rather than on the name.
    external_id text,
    source      text        NOT NULL DEFAULT 'local',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT teams_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT teams_source_check CHECK (source IN ('local', 'scim'))
);
CREATE UNIQUE INDEX teams_org_name_idx ON teams (org_id, lower(name));
CREATE UNIQUE INDEX teams_external_idx ON teams (org_id, external_id)
    WHERE external_id IS NOT NULL;

CREATE TABLE team_members (
    team_id      uuid        NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    added_at     timestamptz NOT NULL DEFAULT now(),
    added_by     uuid        REFERENCES principals (id) ON DELETE SET NULL,

    PRIMARY KEY (team_id, principal_id)
);
CREATE INDEX team_members_principal_idx ON team_members (principal_id);

-- ---------------------------------------------------------------------------
-- Provisioning tokens.
--
-- What the directory authenticates with when it pushes a change. Its own table
-- rather than another row in api_tokens, because this one acts as nobody: it
-- creates and suspends people and it must not be able to read a work order.
-- Sharing a table with tokens that act as a person is how that boundary gets
-- lost during a refactor.
-- ---------------------------------------------------------------------------
CREATE TABLE scim_tokens (
    id           uuid        PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    hash         bytea       NOT NULL,
    created_by   uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz,

    CONSTRAINT scim_tokens_name_not_blank CHECK (btrim(name) <> '')
);
CREATE UNIQUE INDEX scim_tokens_hash_idx ON scim_tokens (hash);
CREATE INDEX scim_tokens_org_idx ON scim_tokens (org_id) WHERE revoked_at IS NULL;

-- Where a principal came from, so a sync can tell the people it manages from
-- the people somebody typed in. A directory that deactivates an account it did
-- not create is a directory that locks the owner out of their own installation.
ALTER TABLE principals ADD COLUMN source text NOT NULL DEFAULT 'local';
ALTER TABLE principals ADD COLUMN external_id text;
ALTER TABLE principals ADD CONSTRAINT principals_source_check
    CHECK (source IN ('local', 'scim', 'oidc'));
CREATE UNIQUE INDEX principals_external_idx ON principals (org_id, external_id)
    WHERE external_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS principals_external_idx;
ALTER TABLE principals DROP CONSTRAINT IF EXISTS principals_source_check;
ALTER TABLE principals DROP COLUMN IF EXISTS external_id;
ALTER TABLE principals DROP COLUMN IF EXISTS source;
DROP TABLE IF EXISTS scim_tokens;
DROP TABLE IF EXISTS team_members;
DROP TABLE IF EXISTS teams;
DROP TRIGGER IF EXISTS audit_log_no_truncate ON audit_log;
DROP TRIGGER IF EXISTS audit_log_no_delete ON audit_log;
DROP TRIGGER IF EXISTS audit_log_no_update ON audit_log;
DROP TABLE IF EXISTS audit_log;
DROP FUNCTION IF EXISTS audit_purge_org(uuid);
DROP FUNCTION IF EXISTS audit_log_is_append_only();
