-- What the HubSpot delta import remembers between runs.
--
-- hubspot_import_state holds, per object, the HubSpot modified time the last applied run read up to (its high-water
-- mark), so the next run asks HubSpot only for what changed after it. hubspot_import_records holds, per HubSpot record,
-- the Hearth record it is, and the value of each mapped field as HubSpot last said it and Hearth accepted it. That
-- snapshot is how a later run tells "HubSpot changed this" from "somebody here edited this": a field is brought up to
-- date only while Hearth still holds the value the import last left there.

-- +goose Up
CREATE TABLE hubspot_import_state (
    org_id      uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    object      text        NOT NULL,
    high_water  timestamptz NOT NULL,
    last_run_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, object)
);

CREATE TABLE hubspot_import_records (
    org_id           uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    object           text        NOT NULL,
    hubspot_id       text        NOT NULL,
    entity_id        uuid        NOT NULL,
    fields           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    hubspot_modified timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, object, hubspot_id)
);
CREATE INDEX hubspot_import_records_entity_idx ON hubspot_import_records (entity_id);

-- +goose Down
DROP TABLE IF EXISTS hubspot_import_records;
DROP TABLE IF EXISTS hubspot_import_state;
