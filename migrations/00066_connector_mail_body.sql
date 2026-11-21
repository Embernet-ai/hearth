-- +goose Up

-- Mail linked to a contact keeps the whole message, not just a preview: the connectors read and write a person's
-- mailbox as that person, so what Hearth shows on a contact is the message itself.
ALTER TABLE connector_mail ADD COLUMN body text NOT NULL DEFAULT '';
ALTER TABLE connector_mail ADD COLUMN html boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE connector_mail DROP COLUMN IF EXISTS html;
ALTER TABLE connector_mail DROP COLUMN IF EXISTS body;
