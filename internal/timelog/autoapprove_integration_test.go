package timelog_test

// Salaried staff: time that approves itself, from the moment a super admin turns
// it on, and never before.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/authz"
)

type decision struct {
	approved bool
	decided  bool
	by       string
	note     string
	auto     bool
}

func (f *fx) decisionOf(t *testing.T, entryID string) decision {
	t.Helper()
	var d decision
	var approved *bool
	var by *string
	var decidedAt *time.Time
	if err := f.pools.Web.QueryRow(context.Background(), `
		SELECT approved, decided_at, decided_by::text, decided_note, auto_approved
		  FROM time_entries WHERE id = $1`, entryID).Scan(&approved, &decidedAt, &by, &d.note, &d.auto); err != nil {
		t.Fatalf("read decision: %v", err)
	}
	d.approved, d.decided = approved != nil && *approved, decidedAt != nil
	if by != nil {
		d.by = *by
	}
	return d
}

func TestTimeAutoApproveStartsWhenSwitchedOnAndNeverReachesBack(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()
	f.superAdmin(t, f.bob)
	alice := f.subj(t, f.alice)

	// Off by default: everything waits.
	e0, err := f.svc.LogOwn(ctx, f.org, alice, "", 30, time.Now(), "before the switch")
	if err != nil {
		t.Fatal(err)
	}
	if d := f.decisionOf(t, e0.ID); d.decided || d.auto {
		t.Fatalf("an entry was decided with the setting off: %+v", d)
	}
	// A timer started before the switch and stopped after it stays pending.
	if _, err := f.svc.Start(ctx, f.org, alice, "", "timer across the switch"); err != nil {
		t.Fatal(err)
	}

	// Only a super admin may set it, and a refusal changes nothing.
	if err := f.az.SetTimeAutoApprove(ctx, f.org, f.alice, f.alice, true); !errors.Is(err, authz.ErrNotSuperAdmin) {
		t.Fatalf("a person turning it on for themselves: %v, want ErrNotSuperAdmin", err)
	}
	if err := f.az.SetTimeAutoApprove(ctx, f.org, f.bob, f.alice, true); err != nil {
		t.Fatalf("super admin turning it on: %v", err)
	}
	// Setting it again changes nothing and keeps the original moment.
	var since1, since2 time.Time
	_ = f.pools.Web.QueryRow(ctx, `SELECT time_auto_approve_since FROM principals WHERE id=$1`, f.alice).Scan(&since1)
	if err := f.az.SetTimeAutoApprove(ctx, f.org, f.bob, f.alice, true); err != nil {
		t.Fatal(err)
	}
	_ = f.pools.Web.QueryRow(ctx, `SELECT time_auto_approve_since FROM principals WHERE id=$1`, f.alice).Scan(&since2)
	if !since1.Equal(since2) {
		t.Fatalf("turning it on twice moved the moment: %v then %v", since1, since2)
	}

	stopped, err := f.svc.Stop(ctx, f.org, f.alice)
	if err != nil {
		t.Fatal(err)
	}
	if d := f.decisionOf(t, stopped.ID); d.decided {
		t.Fatalf("a timer started before the switch was approved by it: %+v", d)
	}

	// Entries dated before the switch stay pending, even when logged after it.
	back, err := f.svc.LogOwn(ctx, f.org, alice, "", 60, time.Now().Add(-48*time.Hour), "last week's work")
	if err != nil {
		t.Fatal(err)
	}
	if d := f.decisionOf(t, back.ID); d.decided {
		t.Fatalf("a backdated entry was approved: %+v", d)
	}

	// New time approves itself, stamped as the system's decision.
	now, err := f.svc.LogOwn(ctx, f.org, alice, "", 45, time.Now().Add(time.Second), "after the switch")
	if err != nil {
		t.Fatal(err)
	}
	if d := f.decisionOf(t, now.ID); !d.approved || !d.auto || d.by != f.alice || !strings.Contains(d.note, "Auto-approved by the system") {
		t.Fatalf("an entry after the switch = %+v", d)
	}
	if _, err := f.svc.Start(ctx, f.org, alice, "", "timer after the switch"); err != nil {
		t.Fatal(err)
	}
	timed, err := f.svc.Stop(ctx, f.org, f.alice)
	if err != nil {
		t.Fatal(err)
	}
	if d := f.decisionOf(t, timed.ID); !d.approved || !d.auto {
		t.Fatalf("a timer after the switch = %+v", d)
	}
	// Nobody else is touched.
	other, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.bob), "", 20, time.Now().Add(time.Second), "bob")
	if err != nil {
		t.Fatal(err)
	}
	if d := f.decisionOf(t, other.ID); d.decided {
		t.Fatalf("somebody else's entry was approved: %+v", d)
	}
	// The queue still has the pending ones for an approver and not the auto ones.
	pend, err := f.svc.Pending(ctx, f.org, f.bob)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, p := range pend {
		ids[p.ID] = true
	}
	if !ids[e0.ID] || !ids[stopped.ID] || !ids[back.ID] || ids[now.ID] || ids[timed.ID] {
		t.Fatalf("pending queue = %v", ids)
	}

	// Off again: new entries wait again. Two audit rows record the two changes.
	if err := f.az.SetTimeAutoApprove(ctx, f.org, f.bob, f.alice, false); err != nil {
		t.Fatal(err)
	}
	later, err := f.svc.LogOwn(ctx, f.org, alice, "", 10, time.Now().Add(2*time.Second), "after off")
	if err != nil {
		t.Fatal(err)
	}
	if d := f.decisionOf(t, later.ID); d.decided {
		t.Fatalf("an entry after turning it off was approved: %+v", d)
	}
	var audits int
	if err := f.pools.Web.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE org_id=$1 AND action='person.time_auto_approve' AND entity_id=$2 AND actor_id=$3`,
		f.org, f.alice, f.bob).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audit rows = %d %v, want 2", audits, err)
	}
}

func TestSettingAnApproverIsAudited(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()
	f.superAdmin(t, f.bob)
	if err := f.az.SetApprover(ctx, f.org, f.bob, f.alice, f.bob); err != nil {
		t.Fatal(err)
	}
	if err := f.az.SetApprover(ctx, f.org, f.bob, f.alice, f.bob); err != nil { // no change, no row
		t.Fatal(err)
	}
	var n int
	if err := f.pools.Web.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE org_id=$1 AND action='person.approver' AND entity_id=$2`,
		f.org, f.alice).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows = %d %v, want 1", n, err)
	}
	// And the queue is theirs: alice's pending entry shows for bob as approver.
	e, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 15, time.Now(), "x")
	if err != nil {
		t.Fatal(err)
	}
	pend, _ := f.svc.Pending(ctx, f.org, f.bob)
	found := false
	for _, p := range pend {
		found = found || p.ID == e.ID
	}
	if !found {
		t.Fatal("the approver's queue does not list the entry")
	}
}
