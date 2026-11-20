-- Hearth's contacts and companies, mirrored into Mautic for email marketing, and what comes back.
--
-- Hearth is the CRM and Mautic is the mailer. A contact is sent across keyed on its email address, and the Mautic id it got is
-- kept here so the next run updates it rather than looking it up. Whether somebody may be emailed lives on the contact itself,
-- because it is a fact about the person and not about the mailer: once they opt out (they click unsubscribe, an address bounces,
-- or a person marks it by hand) Hearth remembers it and puts the do-not-contact back in Mautic if Mautic ever loses it. Nothing in
-- the sync clears it.

-- +goose Up
ALTER TABLE contacts
    ADD COLUMN marketing_opt_out_at     timestamptz,
    ADD COLUMN marketing_opt_out_reason text NOT NULL DEFAULT '',
    ADD CONSTRAINT contacts_marketing_opt_out_reason_check
        CHECK (marketing_opt_out_reason IN ('', 'unsubscribed', 'bounced', 'manual'));

-- One row per contact that has been sent to Mautic.
CREATE TABLE mautic_links (
    contact_id         uuid        PRIMARY KEY REFERENCES contacts (id) ON DELETE CASCADE,
    org_id             uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    mautic_id          bigint      NOT NULL,
    -- What was last sent, as a hash, so a run that finds the same record sends nothing.
    payload_hash       text        NOT NULL DEFAULT '',
    -- The segments and tags the contact was in at the last send, so taking one away is a removal and not a guess.
    segments           text[]      NOT NULL DEFAULT '{}',
    tags               text[]      NOT NULL DEFAULT '{}',
    company_mautic_id  bigint,
    pushed_at          timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX mautic_links_mautic_idx ON mautic_links (org_id, mautic_id);

CREATE TABLE mautic_company_links (
    company_id    uuid        PRIMARY KEY REFERENCES companies (id) ON DELETE CASCADE,
    org_id        uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    mautic_id     bigint      NOT NULL,
    payload_hash  text        NOT NULL DEFAULT '',
    pushed_at     timestamptz NOT NULL DEFAULT now()
);

-- Where each pull from Mautic stopped (the last id it read from each stats table).
CREATE TABLE mautic_sync_state (
    org_id uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    key    text NOT NULL,
    value  text NOT NULL,
    PRIMARY KEY (org_id, key)
);

-- +goose Down
DROP TABLE IF EXISTS mautic_sync_state;
DROP TABLE IF EXISTS mautic_company_links;
DROP TABLE IF EXISTS mautic_links;
ALTER TABLE contacts
    DROP CONSTRAINT IF EXISTS contacts_marketing_opt_out_reason_check,
    DROP COLUMN IF EXISTS marketing_opt_out_reason,
    DROP COLUMN IF EXISTS marketing_opt_out_at;
