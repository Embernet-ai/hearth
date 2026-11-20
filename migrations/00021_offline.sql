-- Work done with no signal.
--
-- The person doing the job is standing at the machine with one hand free and
-- no bars. What they do there has to survive the walk back to the office, and
-- it has to arrive exactly once however many times the phone retries.

-- +goose Up

-- ---------------------------------------------------------------------------
-- offline_ops is the record of everything a phone did while it was on its own.
--
-- It exists for two reasons and neither of them is history.
--
-- The first is exactly-once. A phone at the edge of a signal sends a batch,
-- the reply is lost, and it sends the same batch again. Without a key the
-- second send logs the same forty minutes twice, and nobody finds that until
-- the invoice. The unique index on (principal_id, key) is what makes the
-- second attempt read back the first attempt's answer instead of applying it.
--
-- The second is conflicts. A queued edit that loses the version check is not a
-- failure to retry, it is a question for a person, and a question that lives
-- only in the browser's storage dies with the browser. The row outlives the
-- phone, so a conflict raised on a device that then fell in a drum of coolant
-- is still there to be answered from a desk.
-- ---------------------------------------------------------------------------
CREATE TABLE offline_ops (
    id           uuid        PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    -- Whose queue this was. Access to a conflict is the person's own, not the
    -- org's: somebody else's unresolved edit is not a decision you can make.
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    -- The client's idempotency key, generated on the phone when the action was
    -- taken. Text rather than uuid because it comes from a browser and a
    -- malformed one should be refused by this service with a sentence, not by
    -- the driver with a parse error.
    key          text        NOT NULL,

    -- The request as it would have been made online. Replaying it goes through
    -- the same handler a browser reaches, which is the only way the offline
    -- path cannot drift away from the online one.
    path         text        NOT NULL,
    body         text        NOT NULL DEFAULT '',
    -- What it was about, so the page listing conflicts can say "a status change
    -- on the number four conveyor" rather than showing a URL.
    entity_id    uuid,
    kind         text        NOT NULL DEFAULT '',

    -- When the phone did it, which is not when it arrived. Both are kept: the
    -- gap between them is the only measure of how long the signal was out.
    queued_at    timestamptz NOT NULL,
    received_at  timestamptz NOT NULL DEFAULT now(),

    status       text        NOT NULL,
    code         integer     NOT NULL DEFAULT 0,
    message      text        NOT NULL DEFAULT '',
    resolved_at  timestamptz,

    -- pending  the replay is in flight right now
    -- applied  it landed
    -- conflict it lost the version check and needs a person
    -- rejected the server refused it and would refuse it again
    -- unknown  a replay started and never recorded its outcome, so whether it
    --          landed cannot be answered from here and must not be guessed
    -- resolved a conflict or an unknown that a person decided
    -- discarded a conflict or an unknown that a person threw away
    CONSTRAINT offline_ops_status_check CHECK (status IN
        ('pending', 'applied', 'conflict', 'rejected',
         'unknown', 'resolved', 'discarded')),
    CONSTRAINT offline_ops_key_not_blank CHECK (btrim(key) <> ''),
    CONSTRAINT offline_ops_path_is_local CHECK (path LIKE '/%' AND path NOT LIKE '//%')
);

-- The whole point. One row per key per person, so a repeated batch reads the
-- first answer back instead of doing the work again.
CREATE UNIQUE INDEX offline_ops_key_idx ON offline_ops (principal_id, key);

-- The outbox page: what this person still has to decide, newest first.
CREATE INDEX offline_ops_undecided_idx ON offline_ops (principal_id, queued_at DESC)
    WHERE status IN ('conflict', 'unknown');

-- Housekeeping deletes the settled rows of one org by age.
CREATE INDEX offline_ops_received_idx ON offline_ops (org_id, received_at);

-- +goose Down
DROP TABLE IF EXISTS offline_ops;
