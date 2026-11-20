-- A calendar, and a way to subscribe to it from a phone.
--
-- Two things live here and only one of them is new. Hearth already knows when
-- most things happen: work is due, maintenance is scheduled, hours were logged
-- on a day. None of that is copied into a table here, because a calendar that
-- duplicates the due date is a calendar that disagrees with the work order by
-- Thursday. Those are read from where they already are.
--
-- What did not exist is an event that is only an event: a meeting, a shift,
-- somebody's leave. That is calendar_events, and it is the whole of the new
-- state.

-- +goose Up

CREATE TABLE calendar_events (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,

    -- Who or what it belongs to. Exactly one of these, enforced below.
    --
    --   principal_id   somebody's own: leave, an appointment, a shift
    --   space_id       a department's: a stand-up, a shutdown window
    --
    -- Modelled as two nullable columns with a check rather than an owner_type
    -- discriminator, because the foreign keys then do the work. A discriminator
    -- needs both columns nullable anyway and buys a third thing to keep
    -- consistent with them.
    principal_id uuid      REFERENCES principals (id) ON DELETE CASCADE,
    space_id     uuid      REFERENCES spaces (id) ON DELETE CASCADE,

    title      text        NOT NULL,
    notes      text        NOT NULL DEFAULT '',
    location   text        NOT NULL DEFAULT '',

    starts_at  timestamptz NOT NULL,
    ends_at    timestamptz NOT NULL,
    -- An all-day event is a date, not a midnight-to-midnight instant, and the
    -- difference shows up the moment somebody in another timezone reads it.
    -- Stored as a flag beside the timestamps so the renderer can say "Tuesday"
    -- rather than "Tuesday 00:00 to Wednesday 00:00".
    all_day    boolean     NOT NULL DEFAULT false,

    created_by uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT calendar_events_one_owner CHECK (
        (principal_id IS NOT NULL AND space_id IS NULL) OR
        (principal_id IS NULL AND space_id IS NOT NULL)
    ),
    CONSTRAINT calendar_events_order CHECK (ends_at >= starts_at),
    CONSTRAINT calendar_events_title CHECK (btrim(title) <> '')
);

-- The two reads this table gets: one person's month, and one department's.
CREATE INDEX calendar_events_person_idx
    ON calendar_events (principal_id, starts_at) WHERE principal_id IS NOT NULL;
CREATE INDEX calendar_events_space_idx
    ON calendar_events (space_id, starts_at) WHERE space_id IS NOT NULL;
CREATE INDEX calendar_events_org_idx ON calendar_events (org_id, starts_at);

-- ---------------------------------------------------------------------------
-- Feed tokens.
--
-- A calendar application subscribing to an .ics URL cannot sign in. It fetches
-- a URL on a timer, forever, with no cookie and no way to be prompted for
-- anything, so the URL itself has to carry the authority.
--
-- That makes the token a password in a query string, and it is treated like
-- one. It is random, it is per person, it is revocable by rotating it, and the
-- feed it opens is READ ONLY and covers only what that person can already see.
-- A leaked token exposes somebody's calendar to whoever holds it, which is why
-- rotating is one statement and the page says so plainly.
--
-- Only a hash is stored. The token is shown once, at the moment it is made, and
-- never again, so a database that leaks does not hand over everybody's feeds.
-- ---------------------------------------------------------------------------
CREATE TABLE calendar_feeds (
    principal_id uuid        PRIMARY KEY REFERENCES principals (id) ON DELETE CASCADE,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    token_hash   bytea       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_read_at timestamptz
);
-- The lookup the feed endpoint does on every fetch, by hash rather than by
-- person, because the URL carries the token and nothing else.
CREATE UNIQUE INDEX calendar_feeds_token_idx ON calendar_feeds (token_hash);

-- +goose Down
DROP TABLE IF EXISTS calendar_feeds;
DROP TABLE IF EXISTS calendar_events;
