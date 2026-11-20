-- The public API and outgoing webhooks.
--
-- A product that cannot be integrated with is a product that gets a
-- spreadsheet beside it, and a product that cannot be left is not really
-- self-hosted whatever the licence says.

-- +goose Up

-- ---------------------------------------------------------------------------
-- API tokens.
--
-- Same shape as agent_tokens, deliberately, and NOT the same table. They look
-- identical today and they are answering different questions: an agent token
-- belongs to a non-human principal and carries protocol scopes, an API token
-- belongs to a person and acts as that person. Merging them means one column
-- deciding which rules apply, and that column gets read wrong exactly once.
-- ---------------------------------------------------------------------------
CREATE TABLE api_tokens (
    id           uuid        PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    hash         bytea       NOT NULL,
    -- read or write. Coarse on purpose: a permission system with thirty scopes
    -- is one nobody configures correctly, and the grants are the real
    -- enforcement underneath.
    scopes       text[]      NOT NULL DEFAULT '{read}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz,

    CONSTRAINT api_tokens_name_not_blank CHECK (btrim(name) <> '')
);
CREATE UNIQUE INDEX api_tokens_hash_idx ON api_tokens (hash);
CREATE INDEX api_tokens_org_idx ON api_tokens (org_id);

-- ---------------------------------------------------------------------------
-- Webhooks.
--
-- Fed by the outbox like everything else, so a delivery describes something
-- that definitely committed. A webhook fired inside the request that made the
-- change tells somebody about a rollback.
-- ---------------------------------------------------------------------------
CREATE TABLE webhooks (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    url        text        NOT NULL,
    -- The signing secret. Sent nowhere; used to compute the signature header
    -- so a receiver can tell a real delivery from anybody who guessed the URL.
    secret     text        NOT NULL,
    -- Which events, empty meaning all of them.
    events     text[]      NOT NULL DEFAULT '{}',
    enabled    boolean     NOT NULL DEFAULT true,
    created_by uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT webhooks_url_is_http CHECK (url LIKE 'http://%' OR url LIKE 'https://%')
);
CREATE INDEX webhooks_org_idx ON webhooks (org_id) WHERE enabled;

-- ---------------------------------------------------------------------------
-- Delivery log.
--
-- Every attempt, kept. A webhook that silently stopped working is the most
-- common integration failure there is, and the only thing that shortens the
-- argument is a page saying what was sent, when, and what came back.
-- ---------------------------------------------------------------------------
CREATE TABLE webhook_deliveries (
    id          uuid        PRIMARY KEY,
    org_id      uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    webhook_id  uuid        NOT NULL REFERENCES webhooks (id) ON DELETE CASCADE,
    event_id    bigint,
    event       text        NOT NULL DEFAULT '',
    entity_type text        NOT NULL DEFAULT '',
    entity_id   uuid,
    status      text        NOT NULL DEFAULT 'pending',
    code        integer     NOT NULL DEFAULT 0,
    attempts    integer     NOT NULL DEFAULT 0,
    last_error  text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,

    CONSTRAINT webhook_deliveries_status_check
        CHECK (status IN ('pending', 'delivered', 'failed'))
);
CREATE INDEX webhook_deliveries_hook_idx ON webhook_deliveries (webhook_id, created_at DESC);
CREATE INDEX webhook_deliveries_org_idx ON webhook_deliveries (org_id, created_at DESC);
-- One delivery per webhook per event, which is what makes a redelivered job
-- send once rather than twice.
CREATE UNIQUE INDEX webhook_deliveries_once ON webhook_deliveries (webhook_id, event_id)
    WHERE event_id IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhooks;
DROP TABLE IF EXISTS api_tokens;
