-- Saved views on the CRM lists.
--
-- A saved view of companies, contacts or deals is the list's address: its filters, sort, grouping and layout, as the query
-- string the address bar already carries. That is the whole record, so a saved view and a link to the same list cannot
-- drift, and opening one runs the question again against the opener's own access (it holds no rows, so sharing one
-- discloses nothing). Documents have their own views (saved_views, 00007) over a query structure that is documents only.

-- +goose Up
CREATE TABLE crm_saved_views (
    id         uuid        PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE RESTRICT,
    owner_id   uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    list       text        NOT NULL,
    name       text        NOT NULL,
    query      text        NOT NULL DEFAULT '',
    shared     boolean     NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT crm_saved_views_list_check CHECK (list IN ('companies', 'contacts', 'deals')),
    CONSTRAINT crm_saved_views_name_not_blank CHECK (btrim(name) <> '')
);

-- A name is unique per person and list: two people are each allowed a "Mine", and one person can have a "Mine" on
-- companies and another on deals.
CREATE UNIQUE INDEX crm_saved_views_owner_name_key ON crm_saved_views (org_id, owner_id, list, name);
CREATE INDEX crm_saved_views_shared_idx ON crm_saved_views (org_id, list) WHERE shared;

-- +goose Down
DROP TABLE IF EXISTS crm_saved_views;
