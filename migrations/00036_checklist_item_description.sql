-- A checklist step can carry more than its own title.
--
-- "Test the login flow" is a fine step on its own; "Test the login flow --
-- use the staging account, not prod, and check both the email and Google
-- paths" is the same step with the detail somebody actually needs, and today
-- there is nowhere to put it except cramming it into the title itself. Kept
-- optional and separate from text rather than lengthening text: a task list
-- read at a glance should still read as a list of steps, not a list of
-- paragraphs.

-- +goose Up

ALTER TABLE checklist_items ADD COLUMN description text;

-- +goose Down
ALTER TABLE checklist_items DROP COLUMN IF EXISTS description;
