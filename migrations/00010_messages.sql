-- Messages: channel conversations and entity comments, in one table.
--
-- They are the same thing with a different home. A comment is a message
-- attached to a work order instead of to a channel, and a reply is a message
-- with a parent. Modelling them as three tables produces three sets of
-- mentions, three notification paths, and three places to get permissions
-- wrong, which is exactly what the predecessor had.

-- +goose Up

CREATE TABLE messages (
    id          uuid        PRIMARY KEY,
    org_id      uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,

    -- Exactly one home, enforced below. A message lives in a channel, or on an
    -- entity, and a row with neither is unreachable while a row with both
    -- would appear twice and be edited from two places.
    channel_id  uuid        REFERENCES channels (id) ON DELETE CASCADE,
    entity_type text,
    entity_id   uuid,

    -- A reply. Self-referencing rather than a thread table, because a thread
    -- is a message that happens to have children and giving it its own row
    -- means deciding when one starts.
    parent_id   uuid        REFERENCES messages (id) ON DELETE CASCADE,

    author_id   uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    body        text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    edited_at   timestamptz,
    -- Soft, because a reply whose parent vanished is an orphan, and because a
    -- deleted message that was quoted in a notification should still resolve.
    deleted_at  timestamptz,

    CONSTRAINT messages_needs_a_home CHECK (
        (channel_id IS NOT NULL AND entity_type IS NULL AND entity_id IS NULL)
        OR (channel_id IS NULL AND entity_type IS NOT NULL AND entity_id IS NOT NULL)),
    CONSTRAINT messages_body_present CHECK (btrim(body) <> '' OR deleted_at IS NOT NULL)
);

-- A channel timeline reads newest first and pages backwards.
CREATE INDEX messages_channel_idx ON messages (channel_id, created_at DESC)
    WHERE deleted_at IS NULL AND channel_id IS NOT NULL;

-- A comment thread reads oldest first, because a conversation about one work
-- order is short and reads in order.
CREATE INDEX messages_entity_idx ON messages (entity_type, entity_id, created_at)
    WHERE deleted_at IS NULL AND entity_id IS NOT NULL;

CREATE INDEX messages_parent_idx ON messages (parent_id, created_at)
    WHERE parent_id IS NOT NULL;
CREATE INDEX messages_author_idx ON messages (author_id, created_at DESC);

-- Channels gain a purpose line. The topic column was already there and is the
-- one-line "what is this for" that stops a channel list becoming forty names
-- nobody can tell apart.
CREATE INDEX channels_org_visibility_idx ON channels (org_id, visibility)
    WHERE archived_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS channels_org_visibility_idx;
DROP TABLE IF EXISTS messages;
