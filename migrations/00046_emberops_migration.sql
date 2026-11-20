-- Progress of `hearthctl emberops-migrate`, one row per step.
--
-- The copy into EmberOPS is idempotent (every record is upserted by its Hearth
-- id), so this table is not what makes it safe to run twice. It is what lets a
-- run that stopped resume from the last page it finished instead of reading
-- every table again. Empty, and unused, in the default back end.

-- +goose Up
CREATE TABLE emberops_migration (
    step       text        PRIMARY KEY,
    cursor     text        NOT NULL DEFAULT '',
    done       boolean     NOT NULL DEFAULT false,
    rows_done  integer     NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS emberops_migration;
