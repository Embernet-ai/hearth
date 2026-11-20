-- +goose Up

-- Local accounts, as a first-class feature rather than a development bypass.
--
-- Self-hosted software that cannot be used without a corporate identity provider
-- is a worse product. Local accounts are what make Hearth usable before SSO is
-- configured, by people outside the tenant, and on the day the identity provider
-- is down and somebody still needs to get in.
--
-- A principal may have BOTH a local credential and a federated identity. They
-- are two ways to prove you are the same principal, not two accounts.
CREATE TABLE local_credentials (
    principal_id  uuid        PRIMARY KEY REFERENCES principals (id) ON DELETE CASCADE,
    org_id        uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,

    -- The login identifier. Case-insensitive, because nobody remembers whether
    -- they signed up with a capital letter.
    email         citext      NOT NULL,

    -- argon2id, encoded with its parameters, so the cost can be raised later
    -- without invalidating existing hashes.
    password_hash text        NOT NULL,

    -- Throttling state. Kept on the row rather than in memory so it survives a
    -- restart and applies across replicas, which is the whole point: an attacker
    -- who can trigger a restart should not get a fresh budget.
    failed_attempts int       NOT NULL DEFAULT 0,
    locked_until    timestamptz,

    must_change_password boolean NOT NULL DEFAULT false,
    last_login_at        timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    UNIQUE (org_id, email)
);

CREATE INDEX local_credentials_email_idx ON local_credentials (email);

-- +goose Down
DROP TABLE IF EXISTS local_credentials;
