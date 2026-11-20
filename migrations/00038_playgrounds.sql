-- A personal playground: a whole organisation of its own, full of made-up
-- work, where somebody learns Hearth by doing it and nothing they do can
-- reach real work. Settings -> Tutorial. See internal/playground.
--
-- An organisation rather than a flagged corner of the real one, because the
-- wall between organisations already exists on every row and every query; a
-- corner would need a new exclusion in search, reports, billing, time and
-- notifications, and the day one of them is forgotten, made-up hours land on
-- a real invoice.
--
-- playground_of names the real person it belongs to -- one each, at most --
-- and is what every guard asks: an organisation with it set sends nothing
-- out, creates no real logins, mints no tokens. Its people never get a
-- password or a sign-in identity, so no login path can arrive in one.
--
-- entered_at is for tidying: a playground nobody has opened in months is
-- removed, and simply built fresh if its person ever comes back.
--
-- The key is DEFERRABLE for the same reason as 00030's: principals already
-- point at orgs, so orgs pointing back at principals closes a loop, and the
-- backup orders tables by their keys and refuses a loop (it skips deferrable
-- ones, which do not constrain insert order inside its restore transaction).
-- Found by the backup tests in CI, before it ever reached a real backup.

-- +goose Up
ALTER TABLE orgs ADD COLUMN playground_of uuid
    REFERENCES principals (id) DEFERRABLE INITIALLY IMMEDIATE;
ALTER TABLE orgs ADD COLUMN entered_at timestamptz;
CREATE UNIQUE INDEX orgs_playground_of_idx ON orgs (playground_of) WHERE playground_of IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS orgs_playground_of_idx;
ALTER TABLE orgs DROP COLUMN IF EXISTS entered_at;
ALTER TABLE orgs DROP COLUMN IF EXISTS playground_of;
