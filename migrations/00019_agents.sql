-- Canvas, and agents that reach the product through the same door a person
-- does.
--
-- principals.kind has permitted 'agent' since the first migration with nothing
-- creating one. This is what spends it, and the reason it matters is
-- attribution: an agent that acts as a shared service account writes history
-- that says "the integration did it", which is not an answer anybody accepts
-- when a work order was reassigned overnight.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Canvas.
--
-- A spatial surface for the things a list cannot hold: a line diagram, a
-- floor plan, a decision somebody sketched in a meeting. What makes it worth
-- having rather than a picture is that a card ON it is a live entity, so the
-- box labelled Conveyor 3 opens the asset and shows whether it is down right
-- now.
-- ---------------------------------------------------------------------------
CREATE TABLE canvases (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    space_id   uuid        REFERENCES spaces (id) ON DELETE SET NULL,
    name       text        NOT NULL,
    created_by uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,

    CONSTRAINT canvases_name_not_blank CHECK (btrim(name) <> '')
);
CREATE INDEX canvases_org_idx ON canvases (org_id, name) WHERE deleted_at IS NULL;

CREATE TABLE canvas_nodes (
    id          uuid        PRIMARY KEY,
    canvas_id   uuid        NOT NULL REFERENCES canvases (id) ON DELETE CASCADE,
    org_id      uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    -- What the card is. A note has no entity, which is how somebody labels a
    -- region of the diagram without inventing a document for it.
    entity_type text,
    entity_id   uuid,
    text        text        NOT NULL DEFAULT '',

    x integer NOT NULL DEFAULT 0,
    y integer NOT NULL DEFAULT 0,
    w integer NOT NULL DEFAULT 220,
    h integer NOT NULL DEFAULT 120,
    style jsonb NOT NULL DEFAULT '{}'::jsonb,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT canvas_nodes_size_sane CHECK (w BETWEEN 40 AND 4000 AND h BETWEEN 30 AND 4000),
    -- Bounded, so a drag that goes wrong cannot put a card somewhere nobody
    -- can scroll to and nobody can find again.
    CONSTRAINT canvas_nodes_position_sane
        CHECK (x BETWEEN -100000 AND 100000 AND y BETWEEN -100000 AND 100000),
    CONSTRAINT canvas_nodes_entity_pair
        CHECK ((entity_type IS NULL AND entity_id IS NULL)
            OR (entity_type IS NOT NULL AND entity_id IS NOT NULL)),
    CONSTRAINT canvas_nodes_has_something
        CHECK (entity_id IS NOT NULL OR btrim(text) <> '')
);
CREATE INDEX canvas_nodes_canvas_idx ON canvas_nodes (canvas_id);
CREATE INDEX canvas_nodes_entity_idx ON canvas_nodes (entity_type, entity_id)
    WHERE entity_id IS NOT NULL;

-- One card per entity per canvas. Dropping the same asset on twice produces
-- two boxes that drift apart, and then the diagram has two Conveyor 3s.
CREATE UNIQUE INDEX canvas_nodes_once ON canvas_nodes (canvas_id, entity_type, entity_id)
    WHERE entity_id IS NOT NULL;

-- Edges, so a line diagram is a diagram rather than a scatter of boxes.
CREATE TABLE canvas_edges (
    id        uuid PRIMARY KEY,
    canvas_id uuid NOT NULL REFERENCES canvases (id) ON DELETE CASCADE,
    org_id    uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    from_node uuid NOT NULL REFERENCES canvas_nodes (id) ON DELETE CASCADE,
    to_node   uuid NOT NULL REFERENCES canvas_nodes (id) ON DELETE CASCADE,
    label     text NOT NULL DEFAULT '',

    CONSTRAINT canvas_edges_not_self CHECK (from_node <> to_node)
);
CREATE UNIQUE INDEX canvas_edges_once ON canvas_edges (from_node, to_node);
CREATE INDEX canvas_edges_canvas_idx ON canvas_edges (canvas_id);

-- ---------------------------------------------------------------------------
-- Agent tokens.
--
-- The hash, never the token. A stolen database backup must not be a pile of
-- working credentials, which is the same reason sessions store a hash.
--
-- scopes bounds what the token may do BELOW what its principal may do. The
-- grants are still the ceiling: a read-only token for an agent that can edit
-- is narrower than the agent, never wider, and there is a test that says so.
-- ---------------------------------------------------------------------------
CREATE TABLE agent_tokens (
    id           uuid        PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    -- SHA-256 of the token, raw bytes.
    hash         bytea       NOT NULL,
    scopes       text[]      NOT NULL DEFAULT '{read}',
    created_by   uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz,

    CONSTRAINT agent_tokens_name_not_blank CHECK (btrim(name) <> '')
);
CREATE UNIQUE INDEX agent_tokens_hash_idx ON agent_tokens (hash);
CREATE INDEX agent_tokens_principal_idx ON agent_tokens (principal_id);
CREATE INDEX agent_tokens_org_idx ON agent_tokens (org_id);

-- ---------------------------------------------------------------------------
-- Suggestions.
--
-- An agent proposes; a person accepts. Anything an agent wants to write that
-- is not obviously safe lands here first, and nothing in this table has
-- changed anything until somebody presses accept.
--
-- This is the difference between an assistant and a liability. An agent that
-- writes directly is one bad prompt away from reassigning every work order on
-- site, and the fact that it is attributable afterwards does not help the
-- shift that lost its morning.
-- ---------------------------------------------------------------------------
CREATE TABLE agent_suggestions (
    id           uuid        PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    -- What it wants to do, and to what.
    action       text        NOT NULL,
    entity_type  text        NOT NULL DEFAULT 'document',
    entity_id    uuid,
    payload      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    -- Why, in the agent's own words, because a suggestion nobody can evaluate
    -- gets accepted blindly or ignored entirely.
    reason       text        NOT NULL DEFAULT '',

    status       text        NOT NULL DEFAULT 'pending',
    created_at   timestamptz NOT NULL DEFAULT now(),
    decided_at   timestamptz,
    decided_by   uuid        REFERENCES principals (id) ON DELETE SET NULL,
    result       text        NOT NULL DEFAULT '',

    CONSTRAINT agent_suggestions_status_check
        CHECK (status IN ('pending', 'accepted', 'rejected', 'failed')),
    CONSTRAINT agent_suggestions_decided_together CHECK (
        (status = 'pending' AND decided_at IS NULL) OR
        (status <> 'pending' AND decided_at IS NOT NULL))
);
CREATE INDEX agent_suggestions_pending_idx ON agent_suggestions (org_id, created_at DESC)
    WHERE status = 'pending';
CREATE INDEX agent_suggestions_entity_idx ON agent_suggestions (entity_type, entity_id);

-- +goose Down
DROP TABLE IF EXISTS agent_suggestions;
DROP TABLE IF EXISTS agent_tokens;
DROP TABLE IF EXISTS canvas_edges;
DROP TABLE IF EXISTS canvas_nodes;
DROP TABLE IF EXISTS canvases;
