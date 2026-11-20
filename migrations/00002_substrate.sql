-- The substrate: the entity spine, access control, and typed properties.
--
-- This lands before a second entity type exists, on purpose. Every subsystem
-- built after this inherits it, and retrofitting access control is not a
-- refactor, it is a rewrite of every query in the system.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Spaces: containers. Folders, projects, areas.
--
-- A space is NOT a permission scope. Access comes from grants, and conflating
-- "where a thing lives" with "who can see it" is what produces a permission
-- model nobody can reason about.
-- ---------------------------------------------------------------------------
CREATE TABLE spaces (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE RESTRICT,
    parent_id  uuid        REFERENCES spaces (id) ON DELETE RESTRICT,
    name       text        NOT NULL,
    kind       text        NOT NULL DEFAULT 'folder',
    created_by uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT spaces_kind_check CHECK (kind IN ('folder', 'project', 'area')),
    CONSTRAINT spaces_no_self_parent CHECK (parent_id IS DISTINCT FROM id)
);
CREATE INDEX spaces_org_parent_idx ON spaces (org_id, parent_id);

-- ---------------------------------------------------------------------------
-- Channels: conversations, and the primary way access is granted at scale.
--
-- "Channel membership IS the permission model" is inherited from the
-- predecessor's design and it is the good idea in it. Join a channel, gain
-- access to what is attached to it. Leave, lose it. No access-request theatre.
-- ---------------------------------------------------------------------------
CREATE TABLE channels (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE RESTRICT,
    slug       citext      NOT NULL,
    name       text        NOT NULL,
    topic      text,
    visibility text        NOT NULL DEFAULT 'private',
    created_by uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    archived_at timestamptz,

    CONSTRAINT channels_visibility_check CHECK (visibility IN ('public', 'private')),
    UNIQUE (org_id, slug)
);

CREATE TABLE channel_members (
    channel_id   uuid        NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    role         text        NOT NULL DEFAULT 'member',
    joined_at    timestamptz NOT NULL DEFAULT now(),
    last_read_at timestamptz,

    PRIMARY KEY (channel_id, principal_id),
    CONSTRAINT channel_members_role_check CHECK (role IN ('member', 'admin'))
);
CREATE INDEX channel_members_principal_idx ON channel_members (principal_id);

-- ---------------------------------------------------------------------------
-- Documents: the entity spine.
--
-- A task IS a document with kind='task'. That is the predecessor's best design
-- decision and it is kept deliberately: it means a work order and an asset are
-- also documents, and every one of them inherits versioning, access control,
-- linking, and search on day one instead of being a spreadsheet with opinions.
--
-- No CRDT. Editing is server-authoritative with optimistic concurrency on
-- `version`, and history lives in document_versions as full snapshots.
-- ---------------------------------------------------------------------------
CREATE TABLE documents (
    id          uuid        PRIMARY KEY,
    org_id      uuid        NOT NULL REFERENCES orgs (id) ON DELETE RESTRICT,
    space_id    uuid        REFERENCES spaces (id) ON DELETE SET NULL,
    channel_id  uuid        REFERENCES channels (id) ON DELETE SET NULL,
    kind        text        NOT NULL DEFAULT 'doc',
    title       text        NOT NULL DEFAULT '',
    body        text        NOT NULL DEFAULT '',
    body_format text        NOT NULL DEFAULT 'markdown',
    owner_id    uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    version     bigint      NOT NULL DEFAULT 1,
    created_by  uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    updated_by  uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    deleted_at  timestamptz,

    CONSTRAINT documents_kind_check
        CHECK (kind IN ('doc', 'task', 'wiki', 'work_order', 'asset', 'template', 'note')),
    CONSTRAINT documents_body_format_check
        CHECK (body_format IN ('markdown', 'plain')),
    CONSTRAINT documents_version_positive CHECK (version > 0)
);
CREATE INDEX documents_org_kind_idx    ON documents (org_id, kind) WHERE deleted_at IS NULL;
CREATE INDEX documents_space_idx       ON documents (space_id)     WHERE deleted_at IS NULL;
CREATE INDEX documents_channel_idx     ON documents (channel_id)   WHERE deleted_at IS NULL;
CREATE INDEX documents_owner_idx       ON documents (owner_id)     WHERE deleted_at IS NULL;
CREATE INDEX documents_updated_at_idx  ON documents (org_id, updated_at DESC) WHERE deleted_at IS NULL;

-- Task-specific fields live in a side table rather than as nullable columns on
-- documents. It keeps documents narrow, so task list queries get tight covering
-- indexes without dragging document bodies through the buffer pool.
CREATE TABLE task_fields (
    document_id      uuid        PRIMARY KEY REFERENCES documents (id) ON DELETE CASCADE,
    status           text        NOT NULL DEFAULT 'todo',
    priority         smallint    NOT NULL DEFAULT 0,
    assignee_id      uuid        REFERENCES principals (id) ON DELETE SET NULL,
    due_at           timestamptz,
    started_at       timestamptz,
    completed_at     timestamptz,
    parent_task_id   uuid        REFERENCES documents (id) ON DELETE SET NULL,
    estimate_minutes integer,

    CONSTRAINT task_fields_status_check
        CHECK (status IN ('todo', 'in_progress', 'blocked', 'done', 'cancelled')),
    CONSTRAINT task_fields_no_self_parent CHECK (parent_task_id IS DISTINCT FROM document_id)
);
CREATE INDEX task_fields_assignee_idx ON task_fields (assignee_id, status);
CREATE INDEX task_fields_due_idx      ON task_fields (due_at) WHERE due_at IS NOT NULL;

-- Full snapshots, not deltas. Cheap at this scale, trivially correct, and it
-- makes "show me what this looked like last Tuesday" a single row read.
CREATE TABLE document_versions (
    document_id uuid        NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    version     bigint      NOT NULL,
    title       text        NOT NULL,
    body        text        NOT NULL,
    author_id   uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at  timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (document_id, version)
);

-- One link table for every relationship between two things.
--
-- The predecessor had five separate tables for this: entity mentions, foreign
-- entities, document-to-email, github links, and document-to-task. They are the
-- same relation with different endpoints. Indexed both directions, which is what
-- makes "@-linkable in both directions" true rather than aspirational.
CREATE TABLE entity_links (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE RESTRICT,
    from_type  text        NOT NULL,
    from_id    uuid        NOT NULL,
    to_type    text        NOT NULL,
    to_id      uuid        NOT NULL,
    kind       text        NOT NULL DEFAULT 'mention',
    created_by uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),

    UNIQUE (from_type, from_id, to_type, to_id, kind)
);
CREATE INDEX entity_links_forward_idx ON entity_links (from_type, from_id);
CREATE INDEX entity_links_reverse_idx ON entity_links (to_type, to_id);

-- ---------------------------------------------------------------------------
-- Grants: the whole of access control.
--
-- Effective access is the MAXIMUM level over every grant whose subject the
-- caller holds. Five subject kinds, one table, one query shape.
--
-- Deliberately NOT a materialised access table. The predecessor materialised
-- entity_access and then needed rebuild jobs to keep it honest; a channel with
-- 500 members and 2000 documents is a million-row rebuild on every join. A
-- caller's subject set is bounded by their own memberships, which is tens of
-- rows, so filtering on it directly is both simpler and faster.
-- ---------------------------------------------------------------------------
CREATE TYPE access_level AS ENUM ('view', 'comment', 'edit', 'admin');

CREATE TABLE grants (
    id          uuid         PRIMARY KEY,
    org_id      uuid         NOT NULL REFERENCES orgs (id) ON DELETE RESTRICT,
    entity_type text         NOT NULL,
    entity_id   uuid         NOT NULL,
    -- 'principal:<uuid>' | 'channel:<uuid>' | 'team:<uuid>' | 'org:<uuid>'
    subject_key text         NOT NULL,
    level       access_level NOT NULL,
    granted_by  uuid         NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    granted_at  timestamptz  NOT NULL DEFAULT now(),
    expires_at  timestamptz,

    UNIQUE (entity_type, entity_id, subject_key)
);

-- The index that every permission-filtered list query uses. Subject first,
-- because the caller's subject set is the selective side.
CREATE INDEX grants_subject_idx ON grants (subject_key, entity_type, entity_id);
CREATE INDEX grants_entity_idx  ON grants (entity_type, entity_id);

-- ---------------------------------------------------------------------------
-- Typed properties: custom fields on anything.
--
-- This is the genuine differentiator over the per-seat tools. A property can
-- attach to a document, a task, a contact, or a company, so "asset tag" and
-- "line number" are first-class rather than text crammed into a description.
-- ---------------------------------------------------------------------------
CREATE TABLE property_defs (
    id           uuid        PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE RESTRICT,
    key          citext      NOT NULL,
    display_name text        NOT NULL,
    data_type    text        NOT NULL,
    is_multi     boolean     NOT NULL DEFAULT false,
    applies_to   text[]      NOT NULL DEFAULT '{}',
    -- Options live inline rather than in a child table. They are only ever read
    -- with their definition, so a join buys nothing.
    options      jsonb       NOT NULL DEFAULT '[]'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT property_defs_data_type_check
        CHECK (data_type IN ('text', 'number', 'boolean', 'date', 'select', 'principal', 'url')),
    UNIQUE (org_id, key)
);

CREATE TABLE property_values (
    entity_type text        NOT NULL,
    entity_id   uuid        NOT NULL,
    def_id      uuid        NOT NULL REFERENCES property_defs (id) ON DELETE CASCADE,
    value       jsonb       NOT NULL,
    updated_by  uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    updated_at  timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (entity_type, entity_id, def_id)
);
CREATE INDEX property_values_def_idx ON property_values (def_id);

-- +goose Down
DROP TABLE IF EXISTS property_values;
DROP TABLE IF EXISTS property_defs;
DROP TABLE IF EXISTS grants;
DROP TYPE  IF EXISTS access_level;
DROP TABLE IF EXISTS entity_links;
DROP TABLE IF EXISTS document_versions;
DROP TABLE IF EXISTS task_fields;
DROP TABLE IF EXISTS documents;
DROP TABLE IF EXISTS channel_members;
DROP TABLE IF EXISTS channels;
DROP TABLE IF EXISTS spaces;
