-- Who may reach which part of this, and who signs off somebody's hours.
--
-- Two things arrive together because the second cannot be expressed without the
-- first: approving somebody's time is a capability, and the person who holds it
-- is found through a department.
--
-- What was here before: exactly one owner. auth.IsOwner compares a principal
-- against orgs.owner_id, a single person who claimed the installation, and that
-- one comparison gates the whole Administration group in the rail. There was no
-- way to say "these four people", and no way at all to say "this engineer may
-- see everything except the money".

-- +goose Up

-- ---------------------------------------------------------------------------
-- Capabilities.
--
-- This is a different axis from authz and does not belong in it. authz answers
-- "may this person open this document", per entity, by grant and by level.
-- "May this person see anybody's pay rate" is not a question about a row. It is
-- a question about a section of the application, and modelling it as a grant on
-- some pretend entity would be a lie that every later reader has to decode.
--
-- super_admin is a capability rather than a column beside them, so there is one
-- table and one question to ask. Holding it implies all the others, which is
-- resolved in Go rather than by writing six rows per admin: six rows would go
-- stale the day a seventh capability is added, and the person who added it
-- would have no reason to suspect.
-- ---------------------------------------------------------------------------
CREATE TABLE capability_grants (
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    capability   text        NOT NULL,

    -- Who handed it over, and when. A permission with no provenance is one
    -- nobody can ask about later, and this is exactly the sort of row somebody
    -- will want explained during an audit.
    granted_by   uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    granted_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (org_id, principal_id, capability),

    -- Constrained rather than free text. A typo in a capability name is a
    -- permission that silently never matches, which reads as "the page is
    -- broken" rather than as "the grant is wrong". Adding one is a migration,
    -- and that is the correct amount of friction for widening what a role can
    -- reach.
    CONSTRAINT capability_grants_known CHECK (capability IN (
        'super_admin',  -- everything, and the right to grant the rest
        'finance',      -- rates, costs, anything with money in it
        'time_all',     -- everybody's hours, load and capacity
        'audit',        -- the audit log
        'people',       -- colleagues, teams, provisioning
        'agents'        -- agents, API tokens, integrations
    ))
);
CREATE INDEX capability_grants_principal_idx
    ON capability_grants (principal_id, capability);

-- ---------------------------------------------------------------------------
-- Departments, and who manages them.
--
-- A department is a top-level space. That is already true in practice: the rail
-- renders depth 0 as Departments and depth 1 as sub-departments, and the eight
-- that exist are Engineering, Finance, HR, Knowledge base, Marketing,
-- Operations, Sales. Inventing a second table of departments beside the space
-- tree would be the "one custom-field system, not two" mistake again.
--
-- ON DELETE SET NULL rather than RESTRICT: losing a department should leave its
-- people unfiled and visible, not make the space undeletable. An unfiled person
-- is a thing somebody can see and fix.
-- ---------------------------------------------------------------------------
ALTER TABLE principals
    ADD COLUMN department_id uuid REFERENCES spaces (id) ON DELETE SET NULL;
CREATE INDEX principals_department_idx
    ON principals (department_id) WHERE department_id IS NOT NULL;

-- A department can have more than one manager, because one person going on
-- holiday must not stop every timesheet under them.
CREATE TABLE space_managers (
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    space_id     uuid        NOT NULL REFERENCES spaces (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (space_id, principal_id)
);
CREATE INDEX space_managers_principal_idx ON space_managers (principal_id);
CREATE INDEX space_managers_org_idx       ON space_managers (org_id);

-- The individual override.
--
-- Department is the default and covers almost everybody. This is for the person
-- who sits across two teams, or who reports past their department for a
-- reason that is nobody's business but theirs. Set, it wins; null, the
-- department decides.
--
-- Self-reference is refused. Approving your own hours is not an approval, and a
-- constraint is a better place to say so than a code path somebody can forget.
ALTER TABLE principals
    ADD COLUMN approver_id uuid REFERENCES principals (id) ON DELETE SET NULL,
    ADD CONSTRAINT principals_no_self_approval
        CHECK (approver_id IS DISTINCT FROM id);

-- +goose Down
ALTER TABLE principals
    DROP CONSTRAINT IF EXISTS principals_no_self_approval,
    DROP COLUMN IF EXISTS approver_id,
    DROP COLUMN IF EXISTS department_id;
DROP TABLE IF EXISTS space_managers;
DROP TABLE IF EXISTS capability_grants;
