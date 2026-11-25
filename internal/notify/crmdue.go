package notify

import (
	"context"
	"fmt"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// handleFollowupsDue tells the person who owes a CRM follow-up that it is due within a day, or late. Like the task
// sweep it runs hourly and names the day in its dedupe key, so a person is told once a day, not once an hour, and a
// restart tells nobody twice.
func (f *Fanout) handleFollowupsDue(ctx context.Context, j *jobs.Job) error {
	rows, err := f.db.Query(ctx, `
		SELECT f.id::text, f.title, f.assignee_id::text, f.due_at, f.target_type, f.target_id::text, f.kind
		FROM crm_followups f
		WHERE f.org_id = $1 AND f.deleted_at IS NULL AND f.status = 'open'
		  AND f.assignee_id IS NOT NULL AND f.due_at IS NOT NULL
		  AND f.due_at <= now() + $2::interval
		LIMIT 2000`, j.OrgID, dueWindow.String())
	if err != nil {
		return fmt.Errorf("notify: follow-up sweep: %w", err)
	}
	type due struct {
		id, title, assignee, tt, tid, kind string
		at                                 time.Time
	}
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.title, &d.assignee, &d.at, &d.tt, &d.tid, &d.kind); err != nil {
			rows.Close()
			return fmt.Errorf("notify: scan follow-up: %w", err)
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	today := time.Now().UTC().Format("2006-01-02")
	for _, d := range list {
		created, err := f.svc.Deliver(ctx, Delivery{
			OrgID: j.OrgID, PrincipalID: d.assignee, Reason: ReasonDueSoon,
			Ref: core.Ref{Type: core.EntityType(d.tt), ID: d.tid}, Title: followupLabel(d.kind) + d.title,
			Excerpt:   dueExcerpt(d.at),
			DedupeKey: "crmfu:" + d.id + ":" + today,
		})
		if err != nil {
			return err
		}
		if created {
			if err := f.maybeEmail(ctx, j.OrgID, d.assignee); err != nil {
				return err
			}
		}
	}
	_, err = f.queue.Enqueue(ctx, jobs.Spec{
		OrgID: j.OrgID, Kind: KindFollowups, UniqueKey: "crm_followup_sweep",
		RunAfter: time.Now().Add(time.Hour),
	})
	return err
}

// followupLabel is how a reminder starts: "Call: ", "Email: ", "Meeting: ", or "Follow up: " for a plain to do.
func followupLabel(kind string) string {
	switch kind {
	case "call":
		return "Call: "
	case "email":
		return "Email: "
	case "meeting":
		return "Meeting: "
	}
	return "Follow up: "
}
