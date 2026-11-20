-- Email, calls, and the outside world.
--
-- Work arrives by email and by phone whether or not the software is ready for
-- it. A maintenance system that only accepts work typed into its own form gets
-- a shadow inbox beside it within a fortnight, and then the two disagree.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Inbound addresses.
--
-- One address per space, so where a message lands is a property of the address
-- it was sent to rather than of a rule somebody has to maintain. maintenance@
-- goes to the maintenance area, and the crew tell each other the address
-- instead of being trained on a tool.
-- ---------------------------------------------------------------------------
CREATE TABLE inbound_addresses (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    space_id   uuid        REFERENCES spaces (id) ON DELETE SET NULL,
    -- The full address the MTA delivers to. Stored lowercase, because a sender
    -- will capitalise it and a match that respects case never fires.
    address    text        NOT NULL,
    -- What arriving mail becomes: a work order for a maintenance address, a
    -- task for a general one.
    kind       text        NOT NULL DEFAULT 'work_order',
    -- Who the created work is attributed to. Every write needs a principal
    -- behind it, and "the mail robot" is not an answer anybody accepts when a
    -- work order appeared overnight.
    created_by uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    -- Who to grant the created work to besides the sender's contact. Usually a
    -- channel, so the crew see it without anybody sharing it.
    channel_id uuid        REFERENCES channels (id) ON DELETE SET NULL,
    enabled    boolean     NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT inbound_addresses_kind_check
        CHECK (kind IN ('work_order', 'task', 'note')),
    CONSTRAINT inbound_addresses_has_at CHECK (position('@' in address) > 1)
);
CREATE UNIQUE INDEX inbound_addresses_addr_idx ON inbound_addresses (lower(address));

-- ---------------------------------------------------------------------------
-- Email messages.
--
-- message_id is the idempotency key and the threading key at once. A relay
-- that retries a delivery must produce one work order, and a reply has to
-- point at the message it answers or the sender's client shows two unrelated
-- emails.
-- ---------------------------------------------------------------------------
CREATE TABLE email_messages (
    id          uuid        PRIMARY KEY,
    org_id      uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    -- RFC 5322 Message-ID, angle brackets stripped.
    message_id  text        NOT NULL,
    in_reply_to text        NOT NULL DEFAULT '',
    -- The whole References chain, oldest first. A client threads on this, and
    -- keeping only in_reply_to breaks the thread the moment somebody replies
    -- from a phone that rewrites headers.
    references_ids text[]   NOT NULL DEFAULT '{}',

    entity_type text        NOT NULL DEFAULT 'document',
    entity_id   uuid,
    direction   text        NOT NULL,
    from_addr   text        NOT NULL DEFAULT '',
    to_addr     text        NOT NULL DEFAULT '',
    subject     text        NOT NULL DEFAULT '',
    -- The raw message in object storage, so a dispute about what arrived is
    -- settled by reading it rather than by trusting a parser.
    raw_key     text        NOT NULL DEFAULT '',
    received_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT email_messages_direction_check CHECK (direction IN ('inbound', 'outbound'))
);
-- The guarantee: one message id, one row, one work order, however many times
-- the relay retries.
CREATE UNIQUE INDEX email_messages_msgid_idx ON email_messages (org_id, message_id);
CREATE INDEX email_messages_entity_idx ON email_messages (entity_type, entity_id);
CREATE INDEX email_messages_thread_idx ON email_messages (org_id, in_reply_to)
    WHERE in_reply_to <> '';

-- ---------------------------------------------------------------------------
-- Calls.
--
-- A recording is an ordinary attachment, verified by the same digest as every
-- other file. A second storage path for audio would be a second thing to back
-- up and a second place for a checksum to go unchecked.
-- ---------------------------------------------------------------------------
CREATE TABLE calls (
    id                uuid        PRIMARY KEY,
    org_id            uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    contact_id        uuid        REFERENCES contacts (id) ON DELETE SET NULL,
    -- What the call was about, when it was about something.
    entity_type       text        NOT NULL DEFAULT 'document',
    entity_id         uuid        REFERENCES documents (id) ON DELETE SET NULL,
    direction         text        NOT NULL,
    number            text        NOT NULL DEFAULT '',
    started_at        timestamptz NOT NULL DEFAULT now(),
    duration_s        integer     NOT NULL DEFAULT 0,
    outcome           text        NOT NULL DEFAULT 'spoke',
    note              text        NOT NULL DEFAULT '',
    recording_file_id uuid        REFERENCES files (id) ON DELETE SET NULL,
    logged_by         uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT calls_direction_check CHECK (direction IN ('in', 'out')),
    CONSTRAINT calls_outcome_check
        CHECK (outcome IN ('spoke', 'voicemail', 'no_answer', 'engaged')),
    CONSTRAINT calls_duration_sane CHECK (duration_s >= 0)
);
CREATE INDEX calls_contact_idx ON calls (contact_id, started_at DESC);
CREATE INDEX calls_entity_idx  ON calls (entity_type, entity_id);
CREATE INDEX calls_org_idx     ON calls (org_id, started_at DESC);

-- +goose Down
DROP TABLE IF EXISTS calls;
DROP TABLE IF EXISTS email_messages;
DROP TABLE IF EXISTS inbound_addresses;
