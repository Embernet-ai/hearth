-- +goose Up

-- One person's read-only link to GitHub: the token they made for it, sealed with the key in the environment, the login it
-- belongs to, and the repositories they chose to see numbers for. Nothing is shared between people and nothing here ever
-- writes to GitHub.
CREATE TABLE github_connections (
    principal_id uuid        PRIMARY KEY REFERENCES principals (id) ON DELETE CASCADE,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    login        text        NOT NULL,
    token        bytea       NOT NULL,
    repos        text[]      NOT NULL DEFAULT '{}',
    connected_at timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    last_error   text        NOT NULL DEFAULT '',
    CONSTRAINT github_connections_login CHECK (btrim(login) <> '')
);
CREATE INDEX github_connections_org_idx ON github_connections (org_id);

-- +goose Down
DROP TABLE github_connections;
