-- 00026 made the database impossible to back up, and this is the fix.
--
-- `principals.department_id` references `spaces`. `spaces.created_by` has
-- always referenced `principals`. Between them that is a loop, and the backup
-- orders tables so a restore can insert parents before children. It found the
-- loop, refused to guess an order, and failed:
--
--   hearthctl: backup: these tables reference each other in a loop and cannot
--   be ordered for a restore: agent_suggestions, ... principals, ... spaces ...
--
-- Every table, not two, because nothing downstream of the cycle can be placed
-- either. So the whole archive stopped being producible, which means the
-- release could not be undone. The comment in topological() said a cycle was
-- "not possible in this schema today" and was right until 00026.
--
-- Found by the post-release backup in RELEASING.md step 9, which is exactly the
-- job that step has.
--
-- The fix is not to drop the foreign key. A department that can point at a
-- deleted space is a worse problem than an ordering one, and it would be silent.
--
-- Instead the constraint becomes DEFERRABLE. restore.go already opens one
-- transaction and issues SET CONSTRAINTS ALL DEFERRED before it copies anything,
-- which until now deferred nothing at all because not one of the 151 foreign
-- keys in this schema was deferrable. That line was written in anticipation and
-- this is the release that needs it.
--
-- INITIALLY IMMEDIATE, so ordinary traffic is unchanged and a bad department id
-- is still refused by the statement that writes it. Only a transaction that
-- explicitly asks gets the deferral, and the restore is the only thing that
-- asks.
--
-- Discover() skips deferrable constraints when it builds the graph, because a
-- deferrable key genuinely does not constrain insert order inside the
-- transaction that defers it.

-- +goose Up

ALTER TABLE principals
    DROP CONSTRAINT principals_department_id_fkey,
    ADD CONSTRAINT principals_department_id_fkey
        FOREIGN KEY (department_id) REFERENCES spaces (id)
        ON DELETE SET NULL
        DEFERRABLE INITIALLY IMMEDIATE;

-- +goose Down

ALTER TABLE principals
    DROP CONSTRAINT principals_department_id_fkey,
    ADD CONSTRAINT principals_department_id_fkey
        FOREIGN KEY (department_id) REFERENCES spaces (id)
        ON DELETE SET NULL;
