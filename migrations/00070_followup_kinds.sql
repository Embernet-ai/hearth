-- What kind of follow-up it is: a call, an email, a meeting, or a plain to do. It changes how the queue reads ("Call Dana")
-- and lets a salesperson work all the calls in one sitting; it changes nothing about who sees it or when it is due.

-- +goose Up
ALTER TABLE crm_followups ADD COLUMN kind text NOT NULL DEFAULT 'todo';
ALTER TABLE crm_followups ADD CONSTRAINT crm_followups_kind_check CHECK (kind IN ('todo', 'call', 'email', 'meeting'));

-- +goose Down
ALTER TABLE crm_followups DROP CONSTRAINT IF EXISTS crm_followups_kind_check;
ALTER TABLE crm_followups DROP COLUMN IF EXISTS kind;
