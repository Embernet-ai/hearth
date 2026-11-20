-- Durable background work, and the transactional outbox that feeds it.
--
-- Everything after this depends on both. Indexing, notifications, email,
-- recurrence, and webhooks are all work that must happen and must not happen
-- inside the request, and every one of them needs an event that cannot describe
-- a write that was rolled back. Building those first and retrofitting durability
-- is not a refactor, it is doing them twice.
--
-- No Redis. A queue in Postgres with FOR UPDATE SKIP LOCKED is a few hundred
-- lines and shares the transaction with the data it is about, which is the
-- entire trick: a second datastore cannot participate in the commit, so a
-- second datastore can always disagree with the first.

-- +goose Up

-- ---------------------------------------------------------------------------
-- The queue.
--
-- The id is a bigint identity rather than a uuid, unlike every other table
-- here, because a queue is the one place insertion order is the point. A uuid
-- has no order, so FIFO would need a timestamp with a tiebreaker, and two jobs
-- enqueued in the same microsecond would run in whichever order the index felt
-- like.
-- ---------------------------------------------------------------------------
CREATE TABLE jobs (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    kind         text        NOT NULL,
    payload      jsonb       NOT NULL DEFAULT '{}'::jsonb,

    -- unique_key collapses duplicates while one is still pending. Re-indexing
    -- a document edited four times in a minute is one job, not four, and the
    -- alternative is a queue that grows faster than it drains during a busy
    -- afternoon.
    unique_key   text,

    run_after    timestamptz NOT NULL DEFAULT now(),
    attempts     integer     NOT NULL DEFAULT 0,
    max_attempts integer     NOT NULL DEFAULT 8,

    -- A claim is a LEASE, not a lock. The worker holding it heartbeats while it
    -- runs; if it dies, the lease lapses and somebody else picks the job up.
    -- That makes delivery at-least-once, which every handler has to be written
    -- for, and it is the only model that survives a pod being killed mid-job.
    claimed_at   timestamptz,
    claimed_by   text,

    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT jobs_attempts_sane CHECK (attempts >= 0 AND max_attempts > 0)
);

-- The claim query's index. Partial, on exactly the rows a claim considers, so
-- a backlog of in-flight work does not make finding the next ready job slower.
CREATE INDEX jobs_ready_idx ON jobs (run_after, id) WHERE claimed_at IS NULL;
-- Lapsed leases are found by the same claim, so they need their own path in.
CREATE INDEX jobs_leased_idx ON jobs (claimed_at) WHERE claimed_at IS NOT NULL;

-- Deduplication is only while pending. Once a job has run, the same key may be
-- enqueued again, which is what makes "reindex this document" repeatable.
CREATE UNIQUE INDEX jobs_unique_key
    ON jobs (org_id, kind, unique_key) WHERE unique_key IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Dead letters.
--
-- The whole job is copied here rather than a reference to it, because the job
-- row is deleted when it dies. A dead letter holding only an id to something
-- that no longer exists is a tombstone with no information in it, and the one
-- question anybody asks of this table is "what was it trying to do".
-- ---------------------------------------------------------------------------
CREATE TABLE jobs_dead (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id     bigint      NOT NULL,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    kind       text        NOT NULL,
    payload    jsonb       NOT NULL DEFAULT '{}'::jsonb,
    attempts   integer     NOT NULL,
    error      text        NOT NULL,
    failed_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX jobs_dead_org_idx ON jobs_dead (org_id, failed_at DESC);

-- ---------------------------------------------------------------------------
-- The transactional outbox.
--
-- A row here is written in the SAME transaction as the change it describes, so
-- an event cannot exist for a write that rolled back and a write cannot commit
-- without its event. Publishing directly to a queue from application code
-- breaks that in both directions: the commit succeeds and the publish fails, or
-- the publish succeeds and the commit does not.
--
-- published_at is set by the relay when the event has been turned into jobs.
-- Both happen in one transaction, so an event is turned into work exactly once
-- even though the work itself runs at least once.
-- ---------------------------------------------------------------------------
CREATE TABLE outbox (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    entity_type  text        NOT NULL,
    entity_id    uuid        NOT NULL,
    event        text        NOT NULL,
    payload      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    actor_id     uuid        REFERENCES principals (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);

-- Partial, so the relay's scan is over the handful that are pending rather than
-- over the whole history of everything that has ever happened.
CREATE INDEX outbox_unpublished_idx ON outbox (id) WHERE published_at IS NULL;
CREATE INDEX outbox_entity_idx      ON outbox (entity_type, entity_id, id DESC);

-- +goose Down
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS jobs_dead;
DROP TABLE IF EXISTS jobs;
