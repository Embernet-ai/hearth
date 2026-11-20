package timelog_test

// Time, against a real Postgres.
//
// The interesting checks are about arithmetic that has to be right when
// somebody is deciding whether to replace a machine, and about a timer that has
// to survive the thing timers usually do not survive.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

type fx struct {
	svc   *timelog.Service
	docs  *docs.Service
	az    *authz.Service
	pools *pg.Pools
	dsn   string
	org   string
	alice string
	bob   string
}

func setup(t *testing.T) *fx {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pools.Close)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := pg.Migrate(ctx, pools, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	az := authz.New(pools.Web)
	f := &fx{
		svc: timelog.New(pools.Web, az), docs: docs.New(pools.Web, az),
		az: az, pools: pools, dsn: dsn,
		org: uuid.NewString(), alice: uuid.NewString(), bob: uuid.NewString(),
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Time Test')`, f.org, "tt-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Alice')`, f.alice, f.org)
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Bob')`, f.bob, f.org)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			// Before principals: time_entry_tags.created_by is ON DELETE
			// RESTRICT, so a tag left behind blocks the principal delete below.
			`DELETE FROM time_entry_tags WHERE org_id=$1`,
			`DELETE FROM time_entries WHERE org_id=$1`,
			`DELETE FROM capacity WHERE org_id=$1`,
			`DELETE FROM entity_links WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

func (f *fx) subj(t *testing.T, p string) core.Subjects {
	t.Helper()
	s, err := f.az.SubjectsFor(context.Background(), f.org, p)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	return s
}

func (f *fx) workOrder(t *testing.T, title string) *docs.Document {
	t.Helper()
	d, err := f.docs.Create(context.Background(), f.org, f.subj(t, f.alice), docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: title,
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	if err != nil {
		t.Fatalf("create %s: %v", title, err)
	}
	return d
}

// A running timer survives a deploy and resumes correctly.
//
// The process is genuinely replaced here: new pools, a new service, everything
// in memory thrown away. That is what a rollout does, and a timer held in a
// variable would not survive it.
func TestARunningTimerSurvivesADeploy(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	d := f.workOrder(t, "Bearing replacement")

	started, err := f.svc.Start(ctx, f.org, alice, d.ID, "stripping the guard")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Back-date it, so there is an elapsed time worth checking rather than
	// zero minutes and a hopeful assertion.
	if _, err := f.pools.Web.Exec(ctx,
		`UPDATE time_entries SET started_at = now() - interval '25 minutes' WHERE id=$1`,
		started.ID); err != nil {
		t.Fatalf("back-date: %v", err)
	}

	// The deploy. Everything in memory goes away.
	f.pools.Close()
	fresh, err := pg.Open(ctx, pg.DefaultOptions(f.dsn))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(fresh.Close)
	f.pools = fresh
	svc := timelog.New(fresh.Web, authz.New(fresh.Web))

	running, err := svc.Running(ctx, f.org, f.alice)
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if running == nil {
		t.Fatal("the timer did not survive the restart")
	}
	if running.DocumentID != d.ID {
		t.Errorf("the timer came back on the wrong job")
	}
	if e := running.Elapsed(); e < 24 || e > 27 {
		t.Errorf("elapsed came back as %d minutes, want about 25", e)
	}
	if running.DocTitle != "Bearing replacement" {
		t.Errorf("the timer does not say what it is on: %q", running.DocTitle)
	}

	stopped, err := svc.Stop(ctx, f.org, f.alice)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stopped.Minutes == nil || *stopped.Minutes < 24 || *stopped.Minutes > 27 {
		t.Errorf("stopped at %v minutes, want about 25", stopped.Minutes)
	}
	if running, _ := svc.Running(ctx, f.org, f.alice); running != nil {
		t.Error("a timer is still running after being stopped")
	}
}

// Two open timers is the same hour billed to two jobs, and the person holding
// the spanner is not the one who will notice.
func TestOnlyOneTimerRunsAtOnce(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	first := f.workOrder(t, "First")
	second := f.workOrder(t, "Second")

	if _, err := f.svc.Start(ctx, f.org, alice, first.ID, ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := f.svc.Start(ctx, f.org, alice, second.ID, ""); !errors.Is(err, timelog.ErrAlreadyRunning) {
		t.Fatalf("a second timer started: %v", err)
	}

	// Somebody else is unaffected: the constraint is per person, not global.
	if err := f.az.Grant(ctx, f.org, second.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessComment, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := f.svc.Start(ctx, f.org, f.subj(t, f.bob), second.ID, ""); err != nil {
		t.Fatalf("bob could not start his own timer: %v", err)
	}
}

// Actual minutes roll up to the linked asset. This is the number that turns a
// maintenance history into a replacement decision.
func TestActualMinutesRollUpToTheLinkedAsset(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	asset, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindAsset, Title: "Conveyor, Line 3",
	})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}

	// Three jobs on the conveyor, and one on something else that must not be
	// counted.
	for _, spec := range []struct {
		title   string
		minutes int
		linked  bool
	}{
		{"Bearing", 90, true},
		{"Belt tension", 45, true},
		{"Guard", 30, true},
		{"Unrelated pump job", 500, false},
	} {
		wo := f.workOrder(t, spec.title)
		if spec.linked {
			if err := f.docs.Link(ctx, f.org, alice, wo.Ref(), asset.Ref(), docs.LinkConcerns); err != nil {
				t.Fatalf("link: %v", err)
			}
		}
		if _, err := f.svc.Log(ctx, f.org, alice, wo.ID, spec.minutes, time.Now(), ""); err != nil {
			t.Fatalf("log %s: %v", spec.title, err)
		}
	}

	got, err := f.svc.ForAsset(ctx, f.org, alice, asset.ID)
	if err != nil {
		t.Fatalf("for asset: %v", err)
	}
	if got.Minutes != 165 {
		t.Errorf("the asset rolled up %d minutes, want 165", got.Minutes)
	}
	if got.WorkOrders != 3 {
		t.Errorf("%d work orders counted, want 3", got.WorkOrders)
	}
	// No rate anywhere, so cost is unknown rather than zero.
	if got.CostCents != nil {
		t.Errorf("cost came back as %v with nobody on a rate, want unknown", *got.CostCents)
	}

	// With a rate, the cost is real. Ninety pounds an hour over 165 minutes.
	rate := 9000
	if err := f.svc.SetCapacity(ctx, f.org, f.alice, 2400, &rate); err != nil {
		t.Fatalf("set capacity: %v", err)
	}
	got, err = f.svc.ForAsset(ctx, f.org, alice, asset.ID)
	if err != nil {
		t.Fatalf("for asset: %v", err)
	}
	if got.CostCents == nil {
		t.Fatal("cost is still unknown with a rate set")
	}
	if want := 165 * 9000 / 60; *got.CostCents != want {
		t.Errorf("cost came out %d, want %d", *got.CostCents, want)
	}
}

// Estimate against actual, on the column that has been there all along.
func TestEstimateAgainstActual(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	d := f.workOrder(t, "Quarterly service")

	// Nothing estimated yet, so there is no percentage to report rather than a
	// misleading zero.
	e, err := f.svc.EffortFor(ctx, f.org, alice, d.ID)
	if err != nil {
		t.Fatalf("effort: %v", err)
	}
	if e.EstimateMinutes != nil {
		t.Errorf("a fresh work order arrived with an estimate")
	}
	if e.HasEstimate() {
		t.Error("a work order with no estimate reported having one")
	}

	sixty := 60
	if err := f.svc.SetEstimate(ctx, f.org, alice, d.ID, &sixty); err != nil {
		t.Fatalf("set estimate: %v", err)
	}
	if _, err := f.svc.Log(ctx, f.org, alice, d.ID, 90, time.Now(), "took longer"); err != nil {
		t.Fatalf("log: %v", err)
	}

	e, err = f.svc.EffortFor(ctx, f.org, alice, d.ID)
	if err != nil {
		t.Fatalf("effort: %v", err)
	}
	if e.EstimateMinutes == nil || *e.EstimateMinutes != 60 {
		t.Fatalf("estimate came back %v", e.EstimateMinutes)
	}
	if e.ActualMinutes != 90 {
		t.Errorf("actual came back %d", e.ActualMinutes)
	}
	if !e.HasEstimate() || e.OverPercent() != 50 {
		t.Errorf("over = %d%% (estimated=%v), want 50%%", e.OverPercent(), e.HasEstimate())
	}

	// An asset has no lifecycle, so it has no estimate, and saying so beats
	// silently doing nothing while the form looks like it saved.
	asset, _ := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindAsset, Title: "Conveyor",
	})
	if err := f.svc.SetEstimate(ctx, f.org, alice, asset.ID, &sixty); err == nil {
		t.Error("an asset accepted an estimate")
	}
}

// Workload never counts a cancelled work order. Counting one is how a planner
// concludes somebody is full when they are free.
func TestWorkloadNeverCountsCancelledOrCompletedWork(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	due := time.Now().Add(48 * time.Hour)
	mk := func(title, status string, estimate int) {
		t.Helper()
		d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
			Kind: docs.KindWorkOrder, Title: title,
			Task: &docs.TaskFields{Status: docs.StatusTodo, AssigneeID: &f.bob, DueAt: &due},
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		if err := f.svc.SetEstimate(ctx, f.org, alice, d.ID, &estimate); err != nil {
			t.Fatalf("estimate %s: %v", title, err)
		}
		if status != docs.StatusTodo {
			if _, err := f.docs.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
				Task: &docs.TaskPatch{Status: &status},
			}); err != nil {
				t.Fatalf("status %s: %v", title, err)
			}
		}
	}
	mk("Open one", docs.StatusTodo, 120)
	mk("Open two", docs.StatusInProgress, 60)
	mk("Cancelled", docs.StatusCancelled, 999)
	mk("Finished", docs.StatusDone, 888)

	// Deleted work, and work due next quarter. Both are still open and still
	// assigned to Bob, and neither is his problem this week. They are here
	// because task_fields joins straight to the person: the document join is
	// what applies "not deleted" and "due in the window", and a sum that
	// forgets to require it counts them anyway.
	deleted, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Deleted but assigned",
		Task: &docs.TaskFields{Status: docs.StatusTodo, AssigneeID: &f.bob, DueAt: &due},
	})
	if err != nil {
		t.Fatalf("create deleted: %v", err)
	}
	est := 777
	if err := f.svc.SetEstimate(ctx, f.org, alice, deleted.ID, &est); err != nil {
		t.Fatalf("estimate deleted: %v", err)
	}
	if err := f.docs.Delete(ctx, f.org, alice, deleted.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	next := time.Now().AddDate(0, 3, 0)
	later, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Due next quarter",
		Task: &docs.TaskFields{Status: docs.StatusTodo, AssigneeID: &f.bob, DueAt: &next},
	})
	if err != nil {
		t.Fatalf("create later: %v", err)
	}
	est2 := 666
	if err := f.svc.SetEstimate(ctx, f.org, alice, later.ID, &est2); err != nil {
		t.Fatalf("estimate later: %v", err)
	}

	// Workload is everybody else's hours, so it now takes an actor and refuses
	// one who has not been granted them. Alice gets the grant here; the refusal
	// is checked in TestWorkloadRefusesSomebodyWithoutTheGrant.
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
		 VALUES ($1,$2,'time_all',$2)`, f.org, f.alice); err != nil {
		t.Fatalf("grant time_all: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pools.Web.Exec(context.Background(),
			`DELETE FROM capability_grants WHERE org_id=$1`, f.org)
	})

	from := time.Now().Add(-24 * time.Hour)
	load, err := f.svc.Workload(ctx, f.org, f.alice, from, from.AddDate(0, 0, 7))
	if err != nil {
		t.Fatalf("workload: %v", err)
	}

	var bob *timelog.Load
	for i := range load {
		if load[i].PrincipalID == f.bob {
			bob = &load[i]
		}
	}
	if bob == nil {
		t.Fatal("bob is not in the workload at all")
	}
	if bob.Assigned != 180 {
		t.Errorf("bob is carrying %d minutes, want 180.\n"+
			"180 is the two open jobs due this week. Anything more means the\n"+
			"cancelled one, the finished one, the deleted one, or the job due\n"+
			"next quarter was counted, and a planner reading that concludes he\n"+
			"is full when he is free.", bob.Assigned)
	}
	if bob.Open != 2 {
		t.Errorf("bob has %d open jobs, want 2", bob.Open)
	}

	// Capacity drives the percentage, and the default is a working week rather
	// than zero, so a site that has never set one still gets a usable number.
	if bob.Capacity != 2400 {
		t.Errorf("default capacity is %d minutes", bob.Capacity)
	}
	if bob.Percent() != 7 {
		t.Errorf("load is %d%%, want 7", bob.Percent())
	}

	// A short week makes the same work an overload, which is the number a
	// planner is looking for.
	short := 120
	if err := f.svc.SetCapacity(ctx, f.org, f.bob, short, nil); err != nil {
		t.Fatalf("set capacity: %v", err)
	}
	load, _ = f.svc.Workload(ctx, f.org, f.alice, from, from.AddDate(0, 0, 7))
	for i := range load {
		if load[i].PrincipalID == f.bob && load[i].Percent() <= 100 {
			t.Errorf("bob at %d minutes against a %d minute week reads as %d%%",
				load[i].Assigned, short, load[i].Percent())
		}
	}
}

// A correction is the normal path, not an escape hatch. The timer is always
// forgotten at least once.
func TestTimeCanBeCorrectedByItsAuthorOrAnAdmin(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	d := f.workOrder(t, "Corrected job")

	if err := f.az.Grant(ctx, f.org, d.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessComment, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	bob := f.subj(t, f.bob)

	entry, err := f.svc.Log(ctx, f.org, bob, d.ID, 30, time.Now(), "guessed")
	if err != nil {
		t.Fatalf("log: %v", err)
	}

	// Its author can fix it.
	if err := f.svc.Correct(ctx, f.org, bob, entry.ID, 75, nil, "actually an hour and a quarter"); err != nil {
		t.Fatalf("correct: %v", err)
	}
	entries, err := f.svc.Entries(ctx, f.org, alice, d.ID)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 1 || entries[0].Minutes == nil || *entries[0].Minutes != 75 {
		t.Fatalf("the correction did not take: %+v", entries)
	}
	if entries[0].Note != "actually an hour and a quarter" {
		t.Errorf("the note did not change: %q", entries[0].Note)
	}

	// So can the document's admin, because a timesheet nobody can fix is a
	// timesheet nobody trusts.
	if err := f.svc.Correct(ctx, f.org, alice, entry.ID, 60, nil, ""); err != nil {
		t.Fatalf("admin correct: %v", err)
	}
	// And an unrelated person cannot.
	carol := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Carol')`,
		carol, f.org); err != nil {
		t.Fatalf("carol: %v", err)
	}
	if err := f.svc.Correct(ctx, f.org, f.subj(t, carol), entry.ID, 5, nil, ""); err == nil {
		t.Error("a stranger rewrote somebody else's timesheet")
	}
}

// Correcting the day is opt-in: a bare minutes-and-note correction (nil for
// on) must leave started_at exactly where it was, and ended_at has to move
// WITH minutes on every correction, not only the first one -- a second
// correction after the entry already had an ended_at used to leave that
// timestamp quietly disagreeing with the minutes it was supposed to describe.
func TestCorrectingTheDayMovesBothTimestampsConsistently(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	d := f.workOrder(t, "Day correction")

	logged := time.Date(2026, 9, 10, 0, 0, 0, 0, time.Local)
	entry, err := f.svc.Log(ctx, f.org, alice, d.ID, 60, logged, "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}

	// A plain correction (on = nil) must not move the day.
	if err := f.svc.Correct(ctx, f.org, alice, entry.ID, 90, nil, ""); err != nil {
		t.Fatalf("correct without a date: %v", err)
	}
	entries, err := f.svc.Entries(ctx, f.org, alice, d.ID)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if !entries[0].StartedAt.Equal(logged) {
		t.Errorf("started_at moved on a correction that named no date: got %v, want %v",
			entries[0].StartedAt, logged)
	}
	if got := entries[0].EndedAt.Sub(entries[0].StartedAt); got != 90*time.Minute {
		t.Errorf("ended_at disagrees with the corrected minutes: got %v, want 90m", got)
	}

	// Naming a date moves both, and keeps them consistent with the minutes.
	moved := time.Date(2026, 9, 12, 0, 0, 0, 0, time.Local)
	if err := f.svc.Correct(ctx, f.org, alice, entry.ID, 45, &moved, ""); err != nil {
		t.Fatalf("correct with a date: %v", err)
	}
	entries, err = f.svc.Entries(ctx, f.org, alice, d.ID)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if !entries[0].StartedAt.Equal(moved) {
		t.Errorf("started_at did not move to the corrected date: got %v, want %v",
			entries[0].StartedAt, moved)
	}
	if got := entries[0].EndedAt.Sub(entries[0].StartedAt); got != 45*time.Minute {
		t.Errorf("ended_at disagrees with the corrected minutes after a date change: got %v, want 45m", got)
	}
}

// An entry logged as "No job, just my time" has a NULL document_id, and
// scanning that into a plain string inside Correct/Remove failed outright
// before either function could even reach its own author-or-admin check.
// There was no document page to reach the fix from either, so this path was
// never exercised until the weekly time list got its own Correct and Remove
// controls.
func TestUnattachedEntryCanBeCorrectedAndRemovedByItsAuthor(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	entry, err := f.svc.LogOwn(ctx, f.org, alice, "", 480, time.Now(), "eight hours, no job")
	if err != nil {
		t.Fatalf("log own: %v", err)
	}

	if err := f.svc.Correct(ctx, f.org, alice, entry.ID, 90, nil, "actually an hour and a half"); err != nil {
		t.Fatalf("correct: %v", err)
	}

	// An unrelated person still cannot -- there is no document to check
	// admin on, so the only door left is "are you the author".
	carol := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Carol')`,
		carol, f.org); err != nil {
		t.Fatalf("carol: %v", err)
	}
	if err := f.svc.Correct(ctx, f.org, f.subj(t, carol), entry.ID, 5, nil, ""); err == nil {
		t.Error("a stranger corrected an unattached entry that was not theirs")
	}

	if err := f.svc.Remove(ctx, f.org, alice, entry.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

// Recording work needs comment access, not edit: doing the job is not the same
// authority as changing what the job is.
func TestRecordingTimeNeedsCommentNotEdit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := f.workOrder(t, "Somebody else's job")

	if _, err := f.svc.Log(ctx, f.org, f.subj(t, f.bob), d.ID, 30, time.Now(), ""); err == nil {
		t.Fatal("a stranger logged time against a job they cannot see")
	}

	if err := f.az.Grant(ctx, f.org, d.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessComment, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := f.svc.Log(ctx, f.org, f.subj(t, f.bob), d.ID, 30, time.Now(), ""); err != nil {
		t.Fatalf("comment access was not enough to record work: %v", err)
	}
	// And it still is not enough to change the estimate, which is a statement
	// about the work rather than a record of it.
	sixty := 60
	if err := f.svc.SetEstimate(ctx, f.org, f.subj(t, f.bob), d.ID, &sixty); err == nil {
		t.Error("comment access was enough to set the estimate")
	}
}
