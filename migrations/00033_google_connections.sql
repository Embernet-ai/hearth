-- Each person connects their own Google account.
--
-- Per user, not one service account for the company, and that is the whole
-- design rather than a detail. A domain-wide service account is a single
-- credential that can read every mailbox in the organisation, it is invisible to
-- the people it reads, and revoking somebody's access means editing a config
-- rather than them leaving. Here each person grants Hearth access to their own
-- account, every call is made as them, and somebody leaving revokes their own
-- access by leaving.
--
-- Nobody can read a colleague's mail through Hearth, because Hearth never holds
-- a credential that could.

-- +goose Up

CREATE TABLE google_connections (
    principal_id uuid        PRIMARY KEY REFERENCES principals (id) ON DELETE CASCADE,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,

    -- Google's stable id for the account, and the address at the time of
    -- connecting. Matched on the subject rather than the address, for the same
    -- reason OIDC is: an address is mutable and reusing one that was recycled
    -- would hand somebody a colleague's connection.
    google_sub   text        NOT NULL,
    email        citext      NOT NULL,

    -- Both tokens encrypted, never in the clear.
    --
    -- The refresh token is the dangerous one: it is long-lived, it is the only
    -- thing that cannot be hashed because Google needs it back verbatim months
    -- later, and it opens somebody's mail and calendar. internal/platform/secret
    -- seals it with a key held in the environment, so a leaked backup is not a
    -- pile of working Google credentials unless the key went with it.
    --
    -- The access token is sealed too. It expires in an hour, which is a reason
    -- to care less and not a reason to store it in the clear beside one that
    -- does not.
    access_token  bytea      NOT NULL,
    refresh_token bytea      NOT NULL,
    expires_at    timestamptz NOT NULL,

    -- What they actually granted, as Google reported it back rather than as we
    -- asked. A person can untick a scope on the consent screen, and believing
    -- the request instead of the response is how a feature fails at the first
    -- API call with no explanation.
    scopes       text[]      NOT NULL DEFAULT '{}',

    connected_at timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    -- The last time a call using this connection failed in a way that means it
    -- is dead: the person revoked it in their Google account, or an admin did.
    -- Kept so the page can say "reconnect" rather than failing silently.
    revoked_at   timestamptz,
    last_error   text        NOT NULL DEFAULT '',

    CONSTRAINT google_connections_sub CHECK (btrim(google_sub) <> '')
);

-- One Google account cannot be connected to two people in the same
-- organisation. Without this, two colleagues could both connect the same shared
-- mailbox and each see it as their own.
CREATE UNIQUE INDEX google_connections_sub_idx ON google_connections (org_id, google_sub);
CREATE INDEX google_connections_org_idx ON google_connections (org_id);

-- ---------------------------------------------------------------------------
-- Events pulled from Google.
--
-- Stored rather than fetched on every render, because a calendar page that
-- makes a Google API call per person per load is a page that is slow when
-- Google is slow and blank when Google is down. This is a cache with an owner
-- and an expiry, not a second source of truth: nothing here is ever written
-- back to Google from this table.
-- ---------------------------------------------------------------------------
CREATE TABLE google_events (
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    google_id    text        NOT NULL,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,

    title        text        NOT NULL DEFAULT '',
    location     text        NOT NULL DEFAULT '',
    starts_at    timestamptz NOT NULL,
    ends_at      timestamptz NOT NULL,
    all_day      boolean     NOT NULL DEFAULT false,
    html_link    text        NOT NULL DEFAULT '',
    synced_at    timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (principal_id, google_id)
);
CREATE INDEX google_events_when_idx ON google_events (principal_id, starts_at);

-- +goose Down
DROP TABLE IF EXISTS google_events;
DROP TABLE IF EXISTS google_connections;
