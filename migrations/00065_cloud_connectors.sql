-- +goose Up

-- Microsoft 365 and Google Workspace connectors, for people who self-host Hearth with a business tenant or a
-- Workspace domain.
--
-- Everything is off until an administrator turns it on, and every credential is one person's own: nobody connects
-- on behalf of the company, so Hearth never holds a token that can read a colleague's mailbox.

-- One person's link to their Microsoft account. Same shape as google_connections, because the rules are the same:
-- both tokens sealed with the key in the environment, the scopes the account actually granted, and a revoked
-- marker so the page can say "reconnect" instead of failing the same way each time.
CREATE TABLE microsoft_connections (
    principal_id  uuid        PRIMARY KEY REFERENCES principals (id) ON DELETE CASCADE,
    org_id        uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    -- The Entra tenant the account signed in from, and its object id inside it. The object id is what survives a
    -- rename; the address is mutable and gets recycled.
    tenant_id     text        NOT NULL,
    ms_oid        text        NOT NULL,
    email         citext      NOT NULL DEFAULT '',
    access_token  bytea       NOT NULL,
    refresh_token bytea       NOT NULL,
    expires_at    timestamptz NOT NULL,
    scopes        text[]      NOT NULL DEFAULT '{}',
    connected_at  timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    revoked_at    timestamptz,
    last_error    text        NOT NULL DEFAULT '',
    CONSTRAINT microsoft_connections_oid CHECK (btrim(ms_oid) <> '')
);
CREATE UNIQUE INDEX microsoft_connections_oid_idx ON microsoft_connections (org_id, tenant_id, ms_oid);
CREATE INDEX microsoft_connections_org_idx ON microsoft_connections (org_id);

-- The tenant or domain an administrator has connected for a provider. Microsoft: the Entra tenant id. Google: the
-- Workspace primary domain. A person's own connection is refused unless the account belongs to it, so a personal
-- Gmail or another company's tenant cannot be attached to this workspace.
CREATE TABLE connector_tenants (
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    provider     text        NOT NULL,
    tenant       text        NOT NULL,
    connected_by uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    connected_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, provider),
    CONSTRAINT connector_tenants_provider CHECK (provider IN ('google', 'microsoft')),
    CONSTRAINT connector_tenants_tenant CHECK (btrim(tenant) <> '')
);

-- Which connectors an administrator has switched on. No row means off.
CREATE TABLE connector_settings (
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    provider   text        NOT NULL,
    connector  text        NOT NULL,
    enabled    boolean     NOT NULL DEFAULT false,
    -- Calendar only: add a Meet or Teams link to events Hearth creates in the provider's calendar.
    meet_links boolean     NOT NULL DEFAULT false,
    changed_by uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    changed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, provider, connector),
    CONSTRAINT connector_settings_provider CHECK (provider IN ('google', 'microsoft')),
    CONSTRAINT connector_settings_connector CHECK (connector IN ('calendar', 'contacts', 'mail', 'files', 'chat'))
);

-- What the last run of one connector for one person did, for the status an administrator reads.
CREATE TABLE connector_status (
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    provider     text        NOT NULL,
    connector    text        NOT NULL,
    last_run_at  timestamptz,
    last_ok_at   timestamptz,
    last_error   text        NOT NULL DEFAULT '',
    items        integer     NOT NULL DEFAULT 0,
    PRIMARY KEY (principal_id, provider, connector),
    CONSTRAINT connector_status_provider CHECK (provider IN ('google', 'microsoft')),
    CONSTRAINT connector_status_connector CHECK (connector IN ('calendar', 'contacts', 'mail', 'files', 'chat'))
);
CREATE INDEX connector_status_org_idx ON connector_status (org_id, provider, connector);

-- Which Hearth record an outside item became, so a second sync updates it and never duplicates it.
-- remote_version is the provider's own change marker (an etag, or Graph's changeKey). local_hash is a hash of the
-- Hearth side as it stood at the last sync, so a change made in Hearth since is told apart from one made outside.
CREATE TABLE connector_links (
    org_id         uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    principal_id   uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    provider       text        NOT NULL,
    connector      text        NOT NULL,
    external_id    text        NOT NULL,
    entity_type    text        NOT NULL,
    entity_id      uuid        NOT NULL,
    remote_version text        NOT NULL DEFAULT '',
    local_hash     text        NOT NULL DEFAULT '',
    synced_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (principal_id, provider, connector, external_id),
    CONSTRAINT connector_links_provider CHECK (provider IN ('google', 'microsoft')),
    CONSTRAINT connector_links_connector CHECK (connector IN ('calendar', 'contacts'))
);
CREATE INDEX connector_links_entity_idx ON connector_links (entity_type, entity_id);

-- Messages that involve a person in the CRM, read from somebody's mailbox. Only the headers and a short preview
-- are kept, never the body or an attachment, and only for a message that names a contact Hearth already has.
CREATE TABLE connector_mail (
    id           uuid        PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    provider     text        NOT NULL,
    message_id   text        NOT NULL,
    contact_id   uuid        NOT NULL REFERENCES contacts (id) ON DELETE CASCADE,
    subject      text        NOT NULL DEFAULT '',
    from_addr    text        NOT NULL DEFAULT '',
    to_addrs     text[]      NOT NULL DEFAULT '{}',
    snippet      text        NOT NULL DEFAULT '',
    received_at  timestamptz NOT NULL,
    web_link     text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT connector_mail_provider CHECK (provider IN ('google', 'microsoft')),
    CONSTRAINT connector_mail_once UNIQUE (principal_id, provider, message_id, contact_id)
);
CREATE INDEX connector_mail_contact_idx ON connector_mail (contact_id, received_at DESC);

-- A file in OneDrive, SharePoint or Google Drive that somebody linked to a Hearth document, and the Hearth file it
-- became if they imported it.
CREATE TABLE connector_file_links (
    id           uuid        PRIMARY KEY,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    provider     text        NOT NULL,
    drive_id     text        NOT NULL DEFAULT '',
    item_id      text        NOT NULL,
    name         text        NOT NULL,
    web_url      text        NOT NULL DEFAULT '',
    document_id  uuid        NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    file_id      uuid        REFERENCES files (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT connector_file_links_provider CHECK (provider IN ('google', 'microsoft')),
    CONSTRAINT connector_file_links_once UNIQUE (document_id, provider, drive_id, item_id)
);
CREATE INDEX connector_file_links_doc_idx ON connector_file_links (document_id);

-- Capabilities that came from a sign-in provider's groups or roles, so the next sign-in can take back exactly what
-- the provider gave and never a grant somebody made by hand.
CREATE TABLE idp_capability_grants (
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    capability   text        NOT NULL,
    granted_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, principal_id, capability)
);

-- Which calendars a person syncs besides their primary one. A calendar with a space_id is a department's: its events
-- are the department's events in Hearth, so whoever Hearth gives that department can see them, and a calendar shared
-- with a person in Google or Microsoft reaches the people they work with in Hearth. A calendar belongs to one space
-- in one provider, so two people who both see it cannot import it twice.
CREATE TABLE connector_calendars (
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    provider     text        NOT NULL,
    calendar_id  text        NOT NULL,
    name         text        NOT NULL DEFAULT '',
    space_id     uuid        REFERENCES spaces (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (principal_id, provider, calendar_id),
    CONSTRAINT connector_calendars_provider CHECK (provider IN ('google', 'microsoft'))
);
CREATE UNIQUE INDEX connector_calendars_space_idx
    ON connector_calendars (org_id, provider, calendar_id) WHERE space_id IS NOT NULL;

-- A person's own Google Chat space or Teams chat, as an incoming webhook, for their notifications to be copied to.
-- The address is a credential (anyone holding it can post), so it is sealed like a token.
CREATE TABLE connector_chat_hooks (
    principal_id uuid        PRIMARY KEY REFERENCES principals (id) ON DELETE CASCADE,
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    provider     text        NOT NULL,
    webhook      bytea       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_ok_at   timestamptz,
    last_error   text        NOT NULL DEFAULT '',
    CONSTRAINT connector_chat_hooks_provider CHECK (provider IN ('google', 'microsoft'))
);

-- +goose Down
DROP TABLE IF EXISTS connector_chat_hooks;
DROP TABLE IF EXISTS connector_calendars;
DROP TABLE IF EXISTS idp_capability_grants;
DROP TABLE IF EXISTS connector_file_links;
DROP TABLE IF EXISTS connector_mail;
DROP TABLE IF EXISTS connector_links;
DROP TABLE IF EXISTS connector_status;
DROP TABLE IF EXISTS connector_settings;
DROP TABLE IF EXISTS connector_tenants;
DROP TABLE IF EXISTS microsoft_connections;
