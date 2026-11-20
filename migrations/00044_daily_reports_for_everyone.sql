-- Daily reports for everybody, one a day.
--
-- Three things arrive together because each is useless without the others.
--
-- 1. report_date. A daily report is "mine, for this day", and until now the
--    day was only a word in the title. Giving it a column lets the database
--    say what the product promises: one report per person per day. The unique
--    index ignores soft-deleted rows, so deleting a report frees the day.
--
-- 2. spaces.open_to_all. Filing a report needs a department to file it under,
--    and a person with no grant and no department saw an empty picker and
--    could not file at all (four live accounts were in exactly that spot).
--    A space marked open_to_all counts as a department for every principal in
--    the org, today and in future, with no per-person rows to forget. It is
--    the same "member" meaning as principal_departments (core.MemberSpaces):
--    open it, file into it, and see only what is granted to you, which is
--    your own work. Nobody reads a colleague's report from it unless a grant
--    on another space says they may.
--
-- 3. The "Daily reports" space itself, one per real org. Playground orgs and
--    orgs created later get theirs from docs.EnsureReportSpace the first time
--    somebody opens the report form, so there is no manual step anywhere.

-- +goose Up
ALTER TABLE spaces
    ADD COLUMN open_to_all boolean NOT NULL DEFAULT false;

ALTER TABLE documents
    ADD COLUMN report_date date;

-- The day each existing report was written, in the office's time zone. The
-- three reports that exist are on different days, so the index below holds.
UPDATE documents
   SET report_date = (created_at AT TIME ZONE 'America/New_York')::date
 WHERE kind = 'daily_report';

CREATE UNIQUE INDEX documents_one_report_per_person_per_day
    ON documents (org_id, created_by, report_date)
 WHERE kind = 'daily_report' AND deleted_at IS NULL AND report_date IS NOT NULL;

CREATE INDEX documents_report_date_idx
    ON documents (org_id, report_date)
 WHERE kind = 'daily_report' AND deleted_at IS NULL;

INSERT INTO spaces (id, org_id, parent_id, name, kind, created_by, open_to_all)
SELECT gen_random_uuid(), o.id, NULL, 'Daily reports', 'area',
       (SELECT p.id
          FROM principals p
          JOIN local_credentials lc ON lc.principal_id = p.id
         WHERE p.org_id = o.id
         ORDER BY p.created_at
         LIMIT 1),
       true
  FROM orgs o
 WHERE o.playground_of IS NULL
   AND EXISTS (SELECT 1 FROM local_credentials lc WHERE lc.org_id = o.id)
   AND NOT EXISTS (SELECT 1 FROM spaces s
                    WHERE s.org_id = o.id AND s.open_to_all AND s.name = 'Daily reports');

-- +goose Down
DELETE FROM spaces WHERE open_to_all AND name = 'Daily reports'
   AND NOT EXISTS (SELECT 1 FROM documents d WHERE d.space_id = spaces.id);
DROP INDEX documents_report_date_idx;
DROP INDEX documents_one_report_per_person_per_day;
ALTER TABLE documents DROP COLUMN report_date;
ALTER TABLE spaces DROP COLUMN open_to_all;
