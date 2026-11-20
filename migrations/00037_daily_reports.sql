-- A daily status update gets a real home.
--
-- Gabriel was writing these in the Comments tab because there was nowhere
-- else -- hard to find, and the preview didn't render well (the markdown fix
-- earlier today covers the second half). The first half needed a place with
-- its own name, not a busier Comments tab: the same row shape as a purchase
-- order (migration 00017), same reasoning -- a different kind rather than new
-- machinery, so it gets versioning, permissions, links, attachments,
-- comments, and search on the day it lands.

-- +goose Up
ALTER TABLE documents DROP CONSTRAINT documents_kind_check;
ALTER TABLE documents ADD CONSTRAINT documents_kind_check
    CHECK (kind IN ('doc', 'task', 'wiki', 'work_order', 'asset', 'template',
                    'note', 'purchase_order', 'daily_report'));

-- +goose Down
ALTER TABLE documents DROP CONSTRAINT documents_kind_check;
ALTER TABLE documents ADD CONSTRAINT documents_kind_check
    CHECK (kind IN ('doc', 'task', 'wiki', 'work_order', 'asset', 'template',
                    'note', 'purchase_order'));
