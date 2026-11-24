-- A record of every merge of duplicate contacts or companies: who merged what into what, and what the removed record
-- held, so a merge can always be explained and a field it carried over can be found again.

-- +goose Up
CREATE TABLE crm_merges (
    id          uuid        PRIMARY KEY,
    org_id      uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    entity_type text        NOT NULL,
    survivor_id uuid        NOT NULL,
    loser_id    uuid        NOT NULL,
    merged_by   uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    merged_at   timestamptz NOT NULL DEFAULT now(),
    loser       jsonb       NOT NULL,
    CONSTRAINT crm_merges_type_check CHECK (entity_type IN ('contact', 'company'))
);
CREATE INDEX crm_merges_survivor_idx ON crm_merges (org_id, survivor_id);

-- +goose Down
DROP TABLE IF EXISTS crm_merges;
