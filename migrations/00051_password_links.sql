-- Single-use links that let somebody set their own password.
--
-- An administrator used to have two bad choices for a colleague who had never
-- signed in or had forgotten the way in: invent a password and send it in the
-- clear, or write a hash into the database by hand. A link replaces both. Only
-- the SHA-256 of the token is stored, so a copy of the database cannot be used
-- to take over an account, the link works once, and it dies after 24 hours.

-- +goose Up
CREATE TABLE password_links (
    token_hash   bytea       PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
    created_by   uuid        REFERENCES principals(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    used_at      timestamptz,
    CONSTRAINT password_links_hash_len CHECK (octet_length(token_hash) = 32)
);
CREATE INDEX password_links_principal_idx ON password_links (principal_id) WHERE used_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS password_links;
