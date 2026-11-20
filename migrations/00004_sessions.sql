-- +goose Up

-- Server-side sessions. The cookie holds an opaque id and nothing else.
--
-- Deliberately NOT a JWT in a cookie. A JWT is valid until it expires, so
-- revoking one means maintaining a denylist, which is a session table with extra
-- steps and worse ergonomics. A row here means logout, offboarding, and "sign
-- out everywhere" are all a DELETE.
CREATE TABLE sessions (
    -- The random token is hashed, never stored raw. A leaked database backup
    -- should not be a pile of working credentials.
    id           bytea       PRIMARY KEY,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,

    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,

    user_agent   text,
    ip           inet,

    -- Set when an admin is acting as someone else. Auditors ask, and the answer
    -- has to be in the data rather than in someone's memory.
    impersonated_by uuid REFERENCES principals (id) ON DELETE SET NULL,

    CONSTRAINT sessions_id_length CHECK (octet_length(id) = 32)
);

CREATE INDEX sessions_principal_idx ON sessions (principal_id);
CREATE INDEX sessions_expiry_idx    ON sessions (expires_at);

-- +goose Down
DROP TABLE IF EXISTS sessions;
