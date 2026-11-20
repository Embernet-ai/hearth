-- Email, configured per organisation rather than per deployment.
--
-- Sending was an environment variable only, which is fine for one estate and
-- useless for a tenant that wants their own mail to go out under their own
-- name. Each org now keeps its own relay here, and the notification mailer
-- reads it at send time. With nothing set, or enabled off, email is simply not
-- sent -- the in-app inbox still works, exactly as before.
--
-- The password is the one credential that cannot be hashed, since it is
-- presented to the relay verbatim, so it is sealed with the same key that seals
-- a Google refresh token (internal/platform/secret). A stolen backup without
-- the key is not a working mail account. A deployment with no key configured
-- cannot store a password, which the settings page says plainly.

-- +goose Up
CREATE TABLE org_mail_settings (
    org_id          uuid        PRIMARY KEY REFERENCES orgs (id) ON DELETE CASCADE,
    enabled         boolean     NOT NULL DEFAULT false,
    host            text        NOT NULL DEFAULT '',
    port            integer     NOT NULL DEFAULT 587,
    from_address    text        NOT NULL DEFAULT '',
    from_name       text        NOT NULL DEFAULT '',
    username        text        NOT NULL DEFAULT '',
    password_sealed text        NOT NULL DEFAULT '',
    starttls        boolean     NOT NULL DEFAULT true,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    updated_by      uuid        REFERENCES principals (id) ON DELETE SET NULL,

    CONSTRAINT org_mail_port_check CHECK (port > 0 AND port <= 65535)
);

-- +goose Down
DROP TABLE IF EXISTS org_mail_settings;
