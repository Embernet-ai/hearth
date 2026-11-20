-- +goose Up

-- Case-insensitive text, for handles and email addresses. Comparing these with
-- lower() everywhere works right up until one query forgets.
CREATE EXTENSION IF NOT EXISTS citext;

-- An org is the tenant. A self-host has exactly one, but the column exists from
-- the first migration because retrofitting tenancy is not a refactor, it is a
-- rewrite of every query in the system.
CREATE TABLE orgs (
    id          uuid        PRIMARY KEY,
    slug        citext      NOT NULL UNIQUE,
    name        text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- A principal is anything that can act: a person, a bot, an agent, or the
-- system itself. It is the ONLY foreign key target for "who did this".
--
-- The id is an opaque UUID and never an email address. The predecessor used
-- `macro|<email>` as its principal id, which bakes a mutable string into every
-- foreign key in the database, and renaming that prefix was only survivable
-- because the table happened to be empty at the time. It will not be empty twice.
CREATE TABLE principals (
    id            uuid        PRIMARY KEY,
    org_id        uuid        NOT NULL REFERENCES orgs (id) ON DELETE RESTRICT,
    kind          text        NOT NULL,
    display_name  text        NOT NULL,
    handle        citext,
    avatar_key    text,
    status        text        NOT NULL DEFAULT 'active',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT principals_kind_check
        CHECK (kind IN ('user', 'bot', 'agent', 'system')),
    CONSTRAINT principals_status_check
        CHECK (status IN ('active', 'suspended', 'deleted'))
);

-- Handles are unique per org, and only when set. Bots and agents get one so they
-- can be @-mentioned; the system principal does not need one.
CREATE UNIQUE INDEX principals_org_handle_key
    ON principals (org_id, handle) WHERE handle IS NOT NULL;

CREATE INDEX principals_org_kind_idx ON principals (org_id, kind);

-- How an external identity provider identifies a principal.
--
-- Keyed on (issuer, subject) because that pair is what an OIDC provider
-- guarantees to be stable and unique. Email is stored for display and is
-- explicitly NOT a join key: people marry, domains get acquired, and an address
-- that was a primary key becomes a data migration.
CREATE TABLE user_identities (
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    issuer       text        NOT NULL,
    subject      text        NOT NULL,
    email        citext,
    raw_claims   jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (issuer, subject)
);

CREATE INDEX user_identities_principal_idx ON user_identities (principal_id);
CREATE INDEX user_identities_email_idx     ON user_identities (email) WHERE email IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS user_identities;
DROP TABLE IF EXISTS principals;
DROP TABLE IF EXISTS orgs;
