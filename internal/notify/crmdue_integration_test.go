package notify_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/jobs"
	"github.com/Embernet-ai/hearth/internal/notify"
)

// A CRM follow-up due within a day tells the person who owes it, once a day, and links to the record.
func TestAFollowupDueSoonNotifiesItsAssigneeOnce(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	deal := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx, `INSERT INTO deals (id, org_id, name, created_by) VALUES ($1,$2,'Chase me',$3)`, deal, f.org, f.alice); err != nil {
		t.Fatal(err)
	}
	for title, due := range map[string]time.Time{"Due soon": time.Now().Add(3 * time.Hour), "Far off": time.Now().Add(72 * time.Hour)} {
		if _, err := f.pools.Web.Exec(ctx, `INSERT INTO crm_followups (id, org_id, title, due_at, assignee_id, target_type, target_id, created_by)
			VALUES ($1,$2,$3,$4,$5,'deal',$6,$5)`, uuid.NewString(), f.org, title, due, f.alice, deal); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := f.queue.Enqueue(ctx, jobs.Spec{OrgID: f.org, Kind: notify.KindFollowups, UniqueKey: "crm_followup_sweep"}); err != nil {
			t.Fatal(err)
		}
		job, err := f.queue.Claim(ctx, "test", []string{notify.KindFollowups})
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := f.worker.RunOne(ctx, job); err != nil {
			t.Fatalf("run: %v", err)
		}
		_ = f.queue.Complete(ctx, job.ID)
		// The sweep chains the next one an hour out, so the second pass has to be brought forward.
		_, _ = f.pools.Jobs.Exec(ctx, `UPDATE jobs SET run_after = now() WHERE kind = $1`, notify.KindFollowups)
	}
	got, err := f.svc.Inbox(ctx, f.alice, false, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("alice has %d notifications after two sweeps, want exactly 1", len(got))
	}
	if got[0].Title != "Follow up: Due soon" || got[0].Href() != "/deals/"+deal {
		t.Errorf("notification = %q to %q", got[0].Title, got[0].Href())
	}
}
