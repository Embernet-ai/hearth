-- A person belongs to more than one department.
--
-- 00026 put department_id on principals, one column, one department. That was
-- wrong the first time somebody was asked to describe a real company: Dana
-- is in Marketing, Engineering, Red Team and White Team, and Luis is in Ops
-- and the same three. Neither of them fits in a column.
--
-- So membership becomes its own table, which is what a many-to-many always
-- should have been. Nothing is inherited: being in Engineering does not put
-- somebody in Red Team even though Red Team sits under it. Membership is exact,
-- because the alternative is a rule about the tree that has to be reasoned
-- about every time somebody asks who approves what.
--
-- There is a second, quieter benefit. principals.department_id referenced
-- spaces, and spaces.created_by references principals, and between them they
-- made the whole database unarchivable until 00030 deferred one of them. This
-- table references both and nothing references it, so the loop is gone rather
-- than deferred. 00030 stays: it is still correct, it is now belt and braces,
-- and Discover skipping deferrable keys is a good property to keep.

-- +goose Up

CREATE TABLE principal_departments (
    org_id       uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    principal_id uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    space_id     uuid        NOT NULL REFERENCES spaces (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (principal_id, space_id)
);
-- The two reads: everybody in a department, and every department somebody is
-- in. The second is the one the approval routing does on every decision.
CREATE INDEX principal_departments_space_idx ON principal_departments (space_id);
CREATE INDEX principal_departments_org_idx   ON principal_departments (org_id);

-- Carry across whatever the single column held, so nobody loses a membership
-- that was set through the People page between 1.2.1 and now.
INSERT INTO principal_departments (org_id, principal_id, space_id)
SELECT p.org_id, p.id, p.department_id
  FROM principals p
 WHERE p.department_id IS NOT NULL
ON CONFLICT (principal_id, space_id) DO NOTHING;

-- And the column goes, along with the foreign key that closed the loop.
ALTER TABLE principals DROP COLUMN department_id;

-- +goose Down

ALTER TABLE principals
    ADD COLUMN department_id uuid REFERENCES spaces (id)
        ON DELETE SET NULL DEFERRABLE INITIALLY IMMEDIATE;

-- Somebody in four departments cannot be put back into one column, so this
-- keeps the earliest membership and says so rather than choosing silently.
UPDATE principals p
   SET department_id = (
       SELECT pd.space_id FROM principal_departments pd
        WHERE pd.principal_id = p.id
        ORDER BY pd.created_at, pd.space_id
        LIMIT 1);

DROP TABLE IF EXISTS principal_departments;
