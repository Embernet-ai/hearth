-- Tags for time entries -- "onsite", "remote", whatever an org needs to sort
-- an hour by for billing, without hardcoding a fixed list into the product.
--
-- Two tables, not one array column on time_entries. A tag needs a name that
-- can be corrected without touching every entry that carries it, and it wants
-- a glossary line (when to use it), which a bare text[] has nowhere to put.
-- The join table is what makes "does this entry already have this tag" a
-- primary-key lookup instead of an array-contains scan, and what makes a
-- batch-tag operation naturally idempotent: applying a tag an entry already
-- carries is a no-op, not a duplicate row, because the primary key forbids one.

-- +goose Up

CREATE TABLE time_entry_tags (
    id          uuid        PRIMARY KEY,
    org_id      uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    -- citext, not text: "Onsite" and "onsite" typed a week apart by two
    -- different people must land on the same tag, not fork into two. Same
    -- type principals.handle already uses for the same reason (00001).
    name        citext      NOT NULL,
    description text        NOT NULL DEFAULT '',
    created_by  uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT time_entry_tags_name_unique UNIQUE (org_id, name)
);

CREATE TABLE time_entry_tag_links (
    entry_id   uuid        NOT NULL REFERENCES time_entries (id) ON DELETE CASCADE,
    tag_id     uuid        NOT NULL REFERENCES time_entry_tags (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),

    -- The primary key is the whole mechanism: it is what makes applying a
    -- tag to an entry that already carries it a no-op rather than a second
    -- row, which is what makes a batch-tag operation safe to run over a
    -- week that is only partly tagged already.
    PRIMARY KEY (entry_id, tag_id)
);
CREATE INDEX time_entry_tag_links_tag_idx ON time_entry_tag_links (tag_id);

-- +goose Down
DROP TABLE IF EXISTS time_entry_tag_links;
DROP TABLE IF EXISTS time_entry_tags;
