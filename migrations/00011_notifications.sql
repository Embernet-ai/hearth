-- Notifications, watching, and the preferences that stop people turning the
-- whole thing off.
--
-- The measure of success for this table is not how many rows it gets. It is
-- that nobody disables it, which means the only things in it are things
-- somebody would have wanted to be told.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Watching.
--
-- Explicit, and also automatic on the two actions that mean "this is mine
-- now": being assigned something, and saying something about it. A system that
-- only watches what you tick tells you nothing about the job you were handed
-- this morning, and one that watches everything you touched tells you about a
-- document you opened by mistake in March.
--
-- reason records WHY, so unwatching can be honest about what it is undoing and
-- so an automatic watch can be distinguished from one somebody chose.
-- ---------------------------------------------------------------------------
CREATE TABLE watches (
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    entity_type  text        NOT NULL,
    entity_id    uuid        NOT NULL,
    reason       text        NOT NULL DEFAULT 'manual',
    created_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (principal_id, entity_type, entity_id),
    CONSTRAINT watches_reason_check
        CHECK (reason IN ('manual', 'assigned', 'commented', 'created'))
);
CREATE INDEX watches_entity_idx ON watches (entity_type, entity_id);

-- ---------------------------------------------------------------------------
-- The inbox.
--
-- One row per person per thing worth telling them. dedupe_key is what makes
-- at-least-once delivery survivable: the fan-out job can run twice and the
-- second run writes nothing, because the key names the event and the recipient
-- rather than the attempt.
-- ---------------------------------------------------------------------------
CREATE TABLE notifications (
    id           uuid        PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,

    reason       text        NOT NULL,
    entity_type  text        NOT NULL,
    entity_id    uuid        NOT NULL,
    -- What to show. Denormalised on purpose: an inbox that joins to whatever
    -- the row is now shows a title that has since changed, and a notification
    -- about something that was deleted becomes a blank line.
    title        text        NOT NULL DEFAULT '',
    excerpt      text        NOT NULL DEFAULT '',
    actor_id     uuid        REFERENCES principals (id) ON DELETE SET NULL,

    dedupe_key   text        NOT NULL,
    read_at      timestamptz,
    -- emailed_at separates "in the inbox" from "sent out", because they happen
    -- at different times and a digest has to know what it has already covered.
    emailed_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT notifications_reason_check
        CHECK (reason IN ('assigned', 'mentioned', 'commented', 'changed', 'due_soon'))
);

CREATE UNIQUE INDEX notifications_dedupe_key ON notifications (principal_id, dedupe_key);
-- The inbox query: mine, newest first, unread first.
CREATE INDEX notifications_inbox_idx
    ON notifications (principal_id, created_at DESC);
CREATE INDEX notifications_unread_idx
    ON notifications (principal_id) WHERE read_at IS NULL;
-- What the digest has not covered yet.
CREATE INDEX notifications_unsent_idx
    ON notifications (principal_id, created_at) WHERE emailed_at IS NULL;

-- ---------------------------------------------------------------------------
-- Preferences.
--
-- Quiet hours are not a nicety. A maintenance team is on shift at three in the
-- morning and a work order raised then must not wake the day crew, and the
-- alternative to honouring that is everybody turning email off entirely and
-- then missing the one that mattered.
-- ---------------------------------------------------------------------------
CREATE TABLE notification_prefs (
    principal_id uuid        PRIMARY KEY REFERENCES principals (id) ON DELETE CASCADE,
    -- 'off' | 'immediate' | 'digest'
    email        text        NOT NULL DEFAULT 'digest',
    -- Local time of day, as minutes past midnight, because a person thinks in
    -- their own clock and the server thinks in UTC.
    quiet_from   smallint,
    quiet_to     smallint,
    -- IANA name. Stored rather than guessed: a browser can say what it thinks
    -- and be wrong, and a site with two shifts has people in one place anyway.
    timezone     text        NOT NULL DEFAULT 'UTC',
    digest_at    smallint    NOT NULL DEFAULT 480,
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT notification_prefs_email_check
        CHECK (email IN ('off', 'immediate', 'digest')),
    CONSTRAINT notification_prefs_quiet_range
        CHECK ((quiet_from IS NULL AND quiet_to IS NULL)
               OR (quiet_from BETWEEN 0 AND 1439 AND quiet_to BETWEEN 0 AND 1439)),
    CONSTRAINT notification_prefs_digest_range CHECK (digest_at BETWEEN 0 AND 1439)
);

-- +goose Down
DROP TABLE IF EXISTS notification_prefs;
DROP TABLE IF EXISTS notifications;
DROP TABLE IF EXISTS watches;
