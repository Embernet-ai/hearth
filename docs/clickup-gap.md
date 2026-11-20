# Filling what the ClickUp import left out

`hearthctl clickup-gap` reads ClickUp again and adds only what Hearth is
missing. It exists because the first import (August 2026) never read task
attachments or any comment past the first 25, recorded each task's time as one
total under whoever ran it, and could not see anything made after it finished
or any space its token owner could not open.

## What it does

`snapshot` reads ClickUp (read only) and saves the whole read to the object
store as `imports/clickup-snapshot-<time>.json.gz`, then prints a comparison per
space: tasks, comments, attachments and time that ClickUp has and Hearth does
not. `apply -dry-run` prints the same comparison from a saved read. `apply`
writes.

Rules `apply` keeps:

- Matched on the ClickUp id the first import stored, so a second run adds
  nothing.
- A task changed in ClickUp is brought up to date only if Hearth's copy has not
  been edited since 2026-08-27 15:15 UTC. `owner_id` and `created_by` of an
  existing task are never touched, and nothing gets a notification or an
  `updated_at` of now.
- New tasks go in the department their list belongs to (`Ops` is `Operations`),
  assigned to the person ClickUp named if they can still sign in here. A name
  with no active account, and a task with none at all, goes to `-fallback`
  (default yara@example.com). Assignment is written through `docs.Reassign`.
- Attachments are downloaded and stored in Hearth's object storage and attached
  to their task. A comment by someone with no account here says who wrote it.
- Time goes on the right person. A ClickUp user with no active account here
  gets their time logged on the fallback, with a note saying so. A task's
  one-total entry from the first import is hidden (`deleted_at`, not deleted)
  once per-person entries exist for it.
- Every row written is printed as an `UNDO` line as it is written and saved to
  `imports/clickup-gap-undo-<time>.log`.

## Running it

1. Back up: `kubectl -n hearth create job --from=cronjob/hearth-backup pre-clickup-gap-<date>`.
2. Snapshot, from `deploy/jobs/clickup-gap.yaml` with args `["clickup-gap", "snapshot"]`.
3. Read the comparison in the Job's log.
4. `["clickup-gap", "apply", "-snapshot-key", "<key>", "-dry-run"]`, then without
   `-dry-run`.
5. Count again with another dry run: every "missing" column is zero.

## Undoing

Each `UNDO` line names what was written: a `document`, a `message`, a `file`, a
`time` entry, an `update` (with the version it came from, restored from the
document's history), or a `retired-time` entry (clear its `deleted_at`). The
whole database can also be put back from the backup taken in step 1.
