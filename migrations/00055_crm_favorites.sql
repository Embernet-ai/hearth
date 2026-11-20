-- Favorites: the companies, contacts and deals a person keeps close.
--
-- A favorite is a person's own pointer to a record. It carries no access: the list of favorites is read through the same
-- grants as everything else, so a record somebody can no longer see simply stops appearing, and nobody learns what another
-- person has starred.

-- +goose Up
CREATE TABLE crm_favorites (
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE RESTRICT,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    entity_type  text        NOT NULL,
    entity_id    uuid        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (principal_id, entity_type, entity_id),
    CONSTRAINT crm_favorites_type_check CHECK (entity_type IN ('company', 'contact', 'deal'))
);
CREATE INDEX crm_favorites_org_idx ON crm_favorites (org_id, principal_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS crm_favorites;
