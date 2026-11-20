package timelog_test

// A person's own time, and who signs it off, against a real Postgres.
//
// The checks worth having here are the ones about authority, because the
// routing has three rules and the expensive failure is the one where somebody
// can approve hours that are not theirs to approve. Every negative case is
// tested as well as its positive, since a permission check that always returns
// true passes every test that only looks at the happy path.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/timelog"
)

// org sets up the extra fixtures this file needs on top of setup(): a
// department, a manager, and the capability table.
//
// Registered as a cleanup AFTER setup's, so it runs BEFORE it. capability_grants
// holds granted_by as ON DELETE RESTRICT, so leaving rows behind would make
// setup's own `DELETE FROM principals` fail and every test in the package would
// start failing on teardown rather than on anything real.
func extra(t *testing.T, f *fx) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM capability_grants WHERE org_id=$1`,
			`DELETE FROM space_managers WHERE org_id=$1`,
			`UPDATE principals SET department_id=NULL, approver_id=NULL WHERE org_id=$1`,
			`DELETE FROM spaces WHERE org_id=$1`,
		} {
			_, _ = f.pools.Web.Exec(ctx, q, f.org)
		}
	})
}

func (f *fx) department(t *testing.T, name string) string {
	t.Helper()
	id := uuid.NewString()
	_, err := f.pools.Web.Exec(context.Background(),
		`INSERT INTO spaces (id, org_id, name, kind, created_by) VALUES ($1,$2,$3,'area',$4)`,
		id, f.org, name, f.alice)
	if err != nil {
		t.Fatalf("department: %v", err)
	}
	return id
}

func (f *fx) person(t *testing.T, name string) string {
	t.Helper()
	id := uuid.NewString()
	_, err := f.pools.Web.Exec(context.Background(),
		`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user',$3)`,
		id, f.org, name)
	if err != nil {
		t.Fatalf("person: %v", err)
	}
	return id
}

func (f *fx) putIn(t *testing.T, principal, department string) {
	t.Helper()
	// A row rather than a column: somebody is in several departments, so
	// membership is its own table. See 00031.
	if _, err := f.pools.Web.Exec(context.Background(),
		`INSERT INTO principal_departments (org_id, principal_id, space_id)
		 VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, f.org, principal, department); err != nil {
		t.Fatalf("put in department: %v", err)
	}
}

func (f *fx) manages(t *testing.T, principal, department string) {
	t.Helper()
	if _, err := f.pools.Web.Exec(context.Background(),
		`INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1,$2,$3)`,
		f.org, department, principal); err != nil {
		t.Fatalf("manages: %v", err)
	}
}

func (f *fx) superAdmin(t *testing.T, principal string) {
	t.Helper()
	if _, err := f.pools.Web.Exec(context.Background(),
		`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
		 VALUES ($1,$2,'super_admin',$2)`, f.org, principal); err != nil {
		t.Fatalf("super admin: %v", err)
	}
}

// This is the requirement in one test. Before 00027 it was impossible.
func TestTimeCanBeRecordedAgainstNoJobAtAll(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()

	e, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 45, time.Now(), "reading drawings")
	if err != nil {
		t.Fatalf("logging time with no document: %v", err)
	}
	if !e.Unattached() {
		t.Errorf("the entry claims a document %q, and none was given", e.DocumentID)
	}

	mine, err := f.svc.Mine(ctx, f.org, f.alice, time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour), false)
	if err != nil {
		t.Fatalf("mine: %v", err)
	}
	if len(mine) != 1 {
		t.Fatalf("my week has %d entries, want 1", len(mine))
	}
	if mine[0].Minutes == nil || *mine[0].Minutes != 45 {
		t.Errorf("the entry did not keep its 45 minutes")
	}
	if !mine[0].Pending() {
		t.Error("a new entry is not waiting on anybody, so nothing will ever approve it")
	}
}

// The top bar is the only control that stops a timer, and it renders from
// Running. An inner join to documents made an unattached timer invisible there,
// which is a clock running all night.
func TestARunningTimerWithNoJobIsStillVisible(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()

	if _, err := f.svc.Start(ctx, f.org, f.subj(t, f.alice), "", "thinking"); err != nil {
		t.Fatalf("start: %v", err)
	}
	running, err := f.svc.Running(ctx, f.org, f.alice)
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if running == nil {
		t.Fatal("the timer is running and nothing can see it, so nobody can stop it")
	}
	if running.DocumentID != "" {
		t.Errorf("the timer claims document %q", running.DocumentID)
	}

	stopped, err := f.svc.Stop(ctx, f.org, f.alice)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stopped.Minutes == nil {
		t.Error("stopping it recorded no duration")
	}
}

func TestAManagerOfTheDepartmentApproves(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()

	ops := f.department(t, "Operations "+uuid.NewString()[:8])
	f.putIn(t, f.alice, ops)
	f.manages(t, f.bob, ops)

	e, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 60, time.Now(), "a job")
	if err != nil {
		t.Fatalf("log: %v", err)
	}

	can, err := f.svc.CanApprove(ctx, f.org, f.bob, f.alice)
	if err != nil {
		t.Fatalf("can approve: %v", err)
	}
	if !can {
		t.Fatal("the manager of her department cannot approve her time")
	}

	pending, err := f.svc.Pending(ctx, f.org, f.bob)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != e.ID {
		t.Fatalf("the manager's queue holds %d entries, want the one she logged", len(pending))
	}

	if err := f.svc.Decide(ctx, f.org, f.bob, e.ID, true, "fine"); err != nil {
		t.Fatalf("decide: %v", err)
	}

	mine, _ := f.svc.Mine(ctx, f.org, f.alice, time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour), false)
	if len(mine) != 1 || !mine[0].WasApproved() {
		t.Fatal("the entry is not approved after being approved")
	}
	if mine[0].DeciderName == "" {
		t.Error("the entry does not say who approved it")
	}

	after, err := f.svc.PendingCount(ctx, f.org, f.bob)
	if err != nil {
		t.Fatalf("pending count: %v", err)
	}
	if after != 0 {
		t.Errorf("the queue still holds %d after everything in it was decided", after)
	}
}

func TestSomebodyElsesManagerCannotApprove(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()

	ops := f.department(t, "Operations "+uuid.NewString()[:8])
	sales := f.department(t, "Sales "+uuid.NewString()[:8])
	f.putIn(t, f.alice, ops)
	f.manages(t, f.bob, sales) // Bob runs a different department entirely.

	e, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 30, time.Now(), "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}

	can, err := f.svc.CanApprove(ctx, f.org, f.bob, f.alice)
	if err != nil {
		t.Fatalf("can approve: %v", err)
	}
	if can {
		t.Error("a manager of another department can approve her time")
	}
	if err := f.svc.Decide(ctx, f.org, f.bob, e.ID, true, ""); !errors.Is(err, timelog.ErrNotYours) {
		t.Errorf("deciding somebody else's time returned %v, want ErrNotYours", err)
	}

	queue, _ := f.svc.Pending(ctx, f.org, f.bob)
	if len(queue) != 0 {
		t.Errorf("her entry is sitting in another department's queue")
	}
}

func TestTheIndividualApproverOverridesTheDepartment(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()

	ops := f.department(t, "Operations "+uuid.NewString()[:8])
	f.putIn(t, f.alice, ops)
	// Nobody manages ops. Alice reports to Bob directly.
	if _, err := f.pools.Web.Exec(ctx,
		`UPDATE principals SET approver_id=$2 WHERE id=$1`, f.alice, f.bob); err != nil {
		t.Fatalf("set approver: %v", err)
	}

	e, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 15, time.Now(), "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if err := f.svc.Decide(ctx, f.org, f.bob, e.ID, true, ""); err != nil {
		t.Fatalf("her named approver could not approve: %v", err)
	}
}

func TestNobodyApprovesTheirOwnTimeIncludingASuperAdmin(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()

	f.superAdmin(t, f.alice)

	e, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 90, time.Now(), "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}

	can, err := f.svc.CanApprove(ctx, f.org, f.alice, f.alice)
	if err != nil {
		t.Fatalf("can approve: %v", err)
	}
	if can {
		t.Error("a super admin can sign off their own hours, which is not an approval")
	}
	if err := f.svc.Decide(ctx, f.org, f.alice, e.ID, true, ""); !errors.Is(err, timelog.ErrNotYours) {
		t.Errorf("self-approval returned %v, want ErrNotYours", err)
	}

	// And her own entry must not appear in her own queue.
	queue, _ := f.svc.Pending(ctx, f.org, f.alice)
	for _, q := range queue {
		if q.ID == e.ID {
			t.Error("her own entry is in her own approval queue")
		}
	}
}

func TestASuperAdminApprovesAnybodyElse(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()

	// Bob is a super admin. Alice is in no department and has no approver, so
	// the only rule that can reach her is the super admin one.
	f.superAdmin(t, f.bob)

	e, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 20, time.Now(), "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if err := f.svc.Decide(ctx, f.org, f.bob, e.ID, true, ""); err != nil {
		t.Fatalf("a super admin could not approve: %v", err)
	}
}

func TestSendingBackKeepsTheHoursAndTheReason(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()

	f.superAdmin(t, f.bob)
	e, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 480, time.Now(), "all day")
	if err != nil {
		t.Fatalf("log: %v", err)
	}

	if err := f.svc.Decide(ctx, f.org, f.bob, e.ID, false, "which job was this?"); err != nil {
		t.Fatalf("send back: %v", err)
	}

	mine, _ := f.svc.Mine(ctx, f.org, f.alice, time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour), false)
	if len(mine) != 1 {
		t.Fatalf("the entry disappeared when it was sent back, so nobody can see what was disputed")
	}
	if !mine[0].SentBack() {
		t.Error("the entry does not read as sent back")
	}
	if mine[0].Minutes == nil || *mine[0].Minutes != 480 {
		t.Error("the hours were changed by a rejection")
	}
	if mine[0].DecidedNote != "which job was this?" {
		t.Errorf("the reason is %q", mine[0].DecidedNote)
	}

	// And it can be put back to pending.
	if err := f.svc.Reopen(ctx, f.org, f.bob, e.ID); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	mine, _ = f.svc.Mine(ctx, f.org, f.alice, time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour), false)
	if !mine[0].Pending() {
		t.Error("a reopened entry is not pending again")
	}
}

// Workload used to take no actor and ask nothing, on a route any signed-in
// person could reach.
func TestWorkloadRefusesSomebodyWithoutTheGrant(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()

	from := time.Now().Add(-24 * time.Hour)
	_, err := f.svc.Workload(ctx, f.org, f.alice, from, from.AddDate(0, 0, 7))
	if !errors.Is(err, timelog.ErrNotAllowed) {
		t.Fatalf("somebody with no grant read everybody's workload: err was %v", err)
	}

	// With the grant it answers.
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
		 VALUES ($1,$2,'time_all',$2)`, f.org, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := f.svc.Workload(ctx, f.org, f.alice, from, from.AddDate(0, 0, 7)); err != nil {
		t.Fatalf("with time_all it still refused: %v", err)
	}
}

// Money is a separate grant, and it is dropped in the service rather than
// hidden by the template.
func TestRatesAreWithheldWithoutTheFinancialGrant(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()

	rate := 5500
	if err := f.svc.SetCapacity(ctx, f.org, f.bob, 2400, &rate); err != nil {
		t.Fatalf("set capacity: %v", err)
	}
	grant := func(c string) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx,
			`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
			 VALUES ($1,$2,$3,$2) ON CONFLICT DO NOTHING`, f.org, f.alice, c); err != nil {
			t.Fatalf("grant %s: %v", c, err)
		}
	}
	from := time.Now().Add(-24 * time.Hour)
	find := func() *timelog.Load {
		t.Helper()
		load, err := f.svc.Workload(ctx, f.org, f.alice, from, from.AddDate(0, 0, 7))
		if err != nil {
			t.Fatalf("workload: %v", err)
		}
		for i := range load {
			if load[i].PrincipalID == f.bob {
				return &load[i]
			}
		}
		t.Fatal("bob is not in the workload")
		return nil
	}

	grant("time_all")
	if got := find(); got.RateCents != nil {
		t.Errorf("a planner with no financial grant was handed a rate of %d", *got.RateCents)
	}

	grant("finance")
	if got := find(); got.RateCents == nil || *got.RateCents != rate {
		t.Error("somebody granted financial cannot see the rate")
	}
}

func TestMyTotalsSeparatePendingFromDecided(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()

	f.superAdmin(t, f.bob)
	from := time.Now().Add(-24 * time.Hour)
	to := time.Now().Add(24 * time.Hour)

	one, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 60, time.Now(), "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if _, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 30, time.Now(), ""); err != nil {
		t.Fatalf("log: %v", err)
	}
	if err := f.svc.Decide(ctx, f.org, f.bob, one.ID, true, ""); err != nil {
		t.Fatalf("decide: %v", err)
	}

	total, pending, err := f.svc.MineTotal(ctx, f.org, f.alice, from, to)
	if err != nil {
		t.Fatalf("total: %v", err)
	}
	if total != 90 {
		t.Errorf("the week totals %d minutes, want 90", total)
	}
	if pending != 30 {
		t.Errorf("%d minutes are waiting, want 30", pending)
	}
}
