-- +goose Up

-- Web Push: a phone or a browser that has been told it may ring when the app is closed.
--
-- One row per device, not per person: the same person has a phone and a laptop and turns each on
-- separately. The endpoint is the push service's own address for that browser and is unique across the
-- whole installation, so a shared device that a second person turns push on for moves to them rather
-- than ringing for both. The two keys are what the push service needs to encrypt to that browser.
CREATE TABLE push_subscriptions (
    id           uuid        PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    endpoint     text        NOT NULL,
    p256dh       text        NOT NULL,
    auth         text        NOT NULL,
    -- What the person sees in the list of their devices ("Android phone"), made from the browser's
    -- own description when they turned it on.
    label        text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_ok_at   timestamptz,
    -- The last answer the push service gave, for the person who wonders why nothing arrived.
    last_status  integer,
    CONSTRAINT push_subscriptions_endpoint_key UNIQUE (endpoint)
);
CREATE INDEX push_subscriptions_principal_idx ON push_subscriptions (principal_id);

-- Which kinds of notification ring. No row means every kind, so turning push on for the first time
-- rings for everything and the person narrows it, rather than turning it on to silence.
CREATE TABLE push_prefs (
    principal_id uuid        PRIMARY KEY REFERENCES principals (id) ON DELETE CASCADE,
    kinds        text[]      NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- The VAPID key, for an installation that was not given one. It is made once, by the first process to
-- start, and every later start reads it back, so an upgrade never strands a subscribed phone. The one
-- row is enforced by the check. A deployment that mounts its own key (the chart's Secret) never writes
-- here.
CREATE TABLE push_vapid (
    id          smallint    PRIMARY KEY CHECK (id = 1),
    key_pem     bytea       NOT NULL,
    sealed      boolean     NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS push_vapid;
DROP TABLE IF EXISTS push_prefs;
DROP TABLE IF EXISTS push_subscriptions;
