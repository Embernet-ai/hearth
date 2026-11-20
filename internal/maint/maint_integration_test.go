package maint_test

// Preventive maintenance, against a real Postgres.
//
// Every check here is about the same failure: a maintenance system that
// produces duplicate work. It is the failure that ends the trial, because a
// crew who arrive to find four identical jobs on one pump stop reading the
// list, and nothing you ship afterwards gets them back.

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
	"github.com/Embernet-ai/hearth/internal/jobs"
	"github.com/Embernet-ai/hearth/internal/maint"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
)

type fx struct {
	svc    *maint.Service
	gen    *maint.Generator
	docs   *docs.Service
	az     *authz.Service
	queue  *jobs.Queue
	relay  *jobs.Relay
	worker *jobs.Worker
	pools  *pg.Pools
	org    string
	alice  string
	bob    string
}

func setup(t *testing.T) *fx {
	t.Helper()
	// A database of this package's own. Sharing one with every other
	// integration package meant this fixture's drain claimed jobs belonging
	// to another one, and the failure surfaced over there as work that
	// never happened. See internal/platform/pg/pgtest.
	dsn := pgtest.Database(t, "hearth_test_maint")
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
	d := docs.New(pools.Web, az)
	svc := maint.New(pools.Web, az, d)

	f := &fx{
		svc: svc, docs: d, az: az, pools: pools,
		queue: jobs.NewQueue(pools.Jobs, time.Minute),
		relay: jobs.NewRelay(pools.Jobs, log),
		org:   uuid.NewString(), alice: uuid.NewString(), bob: uuid.NewString(),
	}
	f.gen = maint.NewGenerator(svc, f.queue, log)
	f.worker = jobs.NewWorker(f.queue, f.relay, log, jobs.WorkerOptions{ID: "maint-test"})
	f.gen.Register(f.worker)
	f.relay.Route(maint.Routes()...)

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Maint Test')`, f.org, "mt-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Alice')`, f.alice, f.org)
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Bob')`, f.bob, f.org)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM meter_readings WHERE org_id=$1`,
			`DELETE FROM pm_occurrences WHERE org_id=$1`,
			`DELETE FROM pm_schedules WHERE org_id=$1`,
			`DELETE FROM jobs WHERE org_id=$1`,
			`DELETE FROM jobs_dead WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM entity_links WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM checklist_items WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM property_values WHERE entity_id IN (SELECT id FROM documents WHERE org_id=$1)`,
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

// asset makes something to hang a schedule off.
func (f *fx) asset(t *testing.T, title string) *docs.Document {
	t.Helper()
	d, err := f.docs.Create(context.Background(), f.org, f.subj(t, f.alice), docs.CreateParams{
		Kind: docs.KindAsset, Title: title,
	})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	return d
}

// drain runs the outbox and every maintenance job it produces.
//
// The sweep chains its successor an hour out, so a settled queue here means
// "nothing left to do now" rather than "nothing left ever", which is exactly
// the shape the running system has.
func (f *fx) drain(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for round := 0; round < 200; round++ {
		if _, err := f.relay.Drain(ctx); err != nil {
			t.Fatalf("drain: %v", err)
		}
		job, err := f.queue.Claim(ctx, "test", f.worker.Kinds())
		if errors.Is(err, jobs.ErrNoJob) {
			n, err := f.relay.Drain(ctx)
			if err != nil {
				t.Fatalf("drain: %v", err)
			}
			if n == 0 {
				return
			}
			continue
		}
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := f.worker.RunOne(ctx, job); err != nil {
			t.Fatalf("run: %v", err)
		}
		if err := f.queue.Complete(ctx, job.ID); err != nil {
			t.Fatalf("complete: %v", err)
		}
	}
	t.Fatal("maintenance never settled, which is what a generation loop looks like")
}

// sweep runs one pass of the raiser, the way the hourly job does.
func (f *fx) sweep(t *testing.T) {
	t.Helper()
	if err := f.gen.Start(context.Background(), f.org); err != nil {
		t.Fatalf("start sweep: %v", err)
	}
	f.drain(t)
	// The chained successor sits an hour out and would otherwise stop the next
	// sweep in this test from being enqueued at all, because the key is unique.
	if _, err := f.pools.Web.Exec(context.Background(),
		`DELETE FROM jobs WHERE org_id=$1 AND kind=$2`, f.org, maint.KindSweep); err != nil {
		t.Fatalf("clear chained sweep: %v", err)
	}
}

func (f *fx) countOccurrences(t *testing.T, scheduleID string) int {
	t.Helper()
	var n int
	if err := f.pools.Web.QueryRow(context.Background(),
		`SELECT count(*) FROM pm_occurrences WHERE schedule_id=$1`, scheduleID).Scan(&n); err != nil {
		t.Fatalf("count occurrences: %v", err)
	}
	return n
}

func (f *fx) countWorkOrders(t *testing.T, assetID string) int {
	t.Helper()
	var n int
	err := f.pools.Web.QueryRow(context.Background(), `
		SELECT count(*) FROM documents d
		JOIN entity_links l ON l.from_id = d.id AND l.from_type='document'
		WHERE d.org_id=$1 AND d.kind='work_order' AND l.to_id=$2 AND l.kind='concerns'`,
		f.org, assetID).Scan(&n)
	if err != nil {
		t.Fatalf("count work orders: %v", err)
	}
	return n
}

func days(n int) time.Time { return time.Now().AddDate(0, 0, n) }

func intp(n int) *int { return &n }

// -----------------------------------------------------------------------------

// A schedule ignored for a year is the case that destroys trust. Enumerating
// missed periods would produce 365 work orders on the morning somebody turns
// the system back on.
func TestAYearOfNeglectProducesOneJobAndNot365(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a := f.asset(t, "Pump 4")

	sch, err := f.svc.Save(ctx, f.org, f.subj(t, f.alice), maint.Schedule{
		AssetID: a.ID, Name: "Daily check", EveryDays: intp(1),
		LeadDays: 0, NextDueAt: days(-365),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// Five passes, which in the real system is five hours of a machine nobody
	// touched.
	for i := 0; i < 5; i++ {
		f.sweep(t)
	}

	if n := f.countOccurrences(t, sch.ID); n != 1 {
		t.Errorf("a year of neglect produced %d occurrences, want exactly 1.\n"+
			"A sweep that enumerates missed periods is how a maintenance system\n"+
			"loses a crew permanently.", n)
	}
	if n := f.countWorkOrders(t, a.ID); n != 1 {
		t.Errorf("a year of neglect produced %d work orders, want exactly 1", n)
	}
	_ = ctx
}

// Completing one is what schedules the next. Exactly one, and not before the
// interval has passed.
func TestCompletingGeneratesExactlyOneSuccessor(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	a := f.asset(t, "Conveyor 3")

	sch, err := f.svc.Save(ctx, f.org, alice, maint.Schedule{
		AssetID: a.ID, Name: "Belt tension", EveryDays: intp(1),
		LeadDays: 1, NextDueAt: days(-1),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	f.sweep(t)
	got, err := f.svc.Get(ctx, f.org, alice, sch.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Open == nil || got.Open.WorkOrderID == nil {
		t.Fatal("the sweep raised nothing, so there is nothing to complete")
	}
	first := *got.Open.WorkOrderID

	// Finish it, the way a person does.
	done := docs.StatusDone
	if _, err := f.docs.Update(ctx, f.org, alice, first, docs.UpdateParams{
		Task: &docs.TaskPatch{Status: &done},
	}); err != nil {
		t.Fatalf("complete the work order: %v", err)
	}
	f.drain(t)

	got, err = f.svc.Get(ctx, f.org, alice, sch.ID)
	if err != nil {
		t.Fatalf("get after completion: %v", err)
	}
	if got.Open != nil {
		t.Error("the occurrence was not closed when its work order was completed")
	}
	// Forward from the completion, not from the due date. Yesterday's job
	// finished today is due again tomorrow.
	if !got.NextDueAt.After(time.Now()) {
		t.Errorf("next due is %s, which is not in the future.\n"+
			"Computing it from the due date rather than the completion is how a\n"+
			"job finished late becomes due again the moment it is closed.",
			got.NextDueAt)
	}

	// And the successor: one, however many times the sweep runs.
	for i := 0; i < 3; i++ {
		f.sweep(t)
	}
	if n := f.countOccurrences(t, sch.ID); n != 2 {
		t.Errorf("after one completion there are %d occurrences, want 2 "+
			"(the one that was done, and its single successor)", n)
	}
	if n := f.countWorkOrders(t, a.ID); n != 2 {
		t.Errorf("after one completion there are %d work orders, want 2", n)
	}
}

// The unique index is the guarantee, so prove the guard guards: two raisers
// racing for the same occurrence write one row between them.
func TestRaisingTwiceForTheSameOccurrenceWritesOneRow(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	a := f.asset(t, "Chiller 1")

	sch, err := f.svc.Save(ctx, f.org, alice, maint.Schedule{
		AssetID: a.ID, Name: "Filter change", EveryDays: intp(30),
		LeadDays: 0, NextDueAt: days(-1),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	first, err := f.gen.RaiseNow(ctx, f.org, alice, sch.ID)
	if err != nil {
		t.Fatalf("raise: %v", err)
	}
	if first == "" {
		t.Fatal("the first raise produced nothing")
	}
	second, err := f.gen.RaiseNow(ctx, f.org, alice, sch.ID)
	if err != nil {
		t.Fatalf("second raise: %v", err)
	}
	if second != "" {
		t.Errorf("pressing the button twice produced a second work order %s.\n"+
			"One open occurrence per schedule is the whole defence against a pile.",
			second)
	}
	if n := f.countOccurrences(t, sch.ID); n != 1 {
		t.Errorf("two raises produced %d occurrences, want 1", n)
	}
}

// A meter reading moves the date. That is the difference between a maintenance
// system and a calendar with opinions.
func TestAMeterReadingMovesTheNextDueDate(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	a := f.asset(t, "Generator 2")

	key := "run_hours"
	units := 500.0
	sch, err := f.svc.Save(ctx, f.org, alice, maint.Schedule{
		AssetID: a.ID, Name: "500 hour service",
		// A calendar clock far enough out that the meter is what decides,
		// which is what "whichever comes first" looks like on a machine that
		// runs hard.
		EveryDays: intp(3650), MeterKey: &key, EveryUnits: &units,
		LeadDays: 7, NextDueAt: days(3650),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	before, err := f.svc.Get(ctx, f.org, alice, sch.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if before.ProjectedMeterDue != nil {
		t.Errorf("a schedule with no readings projected a date of %s.\n"+
			"A projection built from nothing is a number with the shape of an\n"+
			"answer and none of the content.", before.ProjectedMeterDue)
	}

	// Two readings, thirty days apart, at ten hours a day. Backdated with SQL
	// because a trend needs time to have passed and a test cannot wait a month.
	f.reading(t, a.ID, key, 0, days(-30))
	f.reading(t, a.ID, key, 300, days(0))

	after, err := f.svc.Get(ctx, f.org, alice, sch.ID)
	if err != nil {
		t.Fatalf("get after readings: %v", err)
	}
	if after.ProjectedMeterDue == nil {
		t.Fatal("two readings and a rate produced no projection")
	}
	// 300 hours in 30 days is ten a day, 200 hours left, so about twenty days.
	wantAbout := time.Now().AddDate(0, 0, 20)
	if diff := after.ProjectedMeterDue.Sub(wantAbout); diff > 48*time.Hour || diff < -48*time.Hour {
		t.Errorf("projected due %s, want about %s.\n"+
			"The rate is 300 units over 30 days with 200 to go.",
			after.ProjectedMeterDue.Format(time.DateOnly), wantAbout.Format(time.DateOnly))
	}
	if !after.DueAt().Before(before.DueAt()) {
		t.Errorf("the schedule is still due %s after the readings, and was due %s before.\n"+
			"A meter that cannot move the date is a column nobody fills in.",
			after.DueAt(), before.DueAt())
	}

	// A faster machine is due sooner. Same schedule, more hours.
	f.reading(t, a.ID, key, 450, days(0))
	faster, err := f.svc.Get(ctx, f.org, alice, sch.ID)
	if err != nil {
		t.Fatalf("get after the third reading: %v", err)
	}
	if !faster.DueAt().Before(after.DueAt()) {
		t.Errorf("running the machine harder did not bring the service forward: "+
			"%s then %s", after.DueAt(), faster.DueAt())
	}
}

// reading backdates a meter value, which the service deliberately will not do.
func (f *fx) reading(t *testing.T, assetID, key string, value float64, at time.Time) {
	t.Helper()
	_, err := f.pools.Web.Exec(context.Background(), `
		INSERT INTO meter_readings (id, org_id, asset_id, key, value, read_at, read_by)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,$5,$6)`,
		f.org, assetID, key, value, at, f.alice)
	if err != nil {
		t.Fatalf("insert reading: %v", err)
	}
}

// Crossing the threshold raises the job, and the sweep says so.
func TestCrossingTheMeterThresholdRaisesTheJob(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	a := f.asset(t, "Compressor 7")

	key := "cycles"
	units := 1000.0
	sch, err := f.svc.Save(ctx, f.org, alice, maint.Schedule{
		AssetID: a.ID, Name: "Thousand cycle inspection",
		MeterKey: &key, EveryUnits: &units, LeadDays: 0, NextDueAt: days(3650),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	f.reading(t, a.ID, key, 100, days(-10))
	f.reading(t, a.ID, key, 1200, days(0))

	f.sweep(t)
	got, err := f.svc.Get(ctx, f.org, alice, sch.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Open == nil {
		t.Fatal("the meter went past the threshold and nothing was raised")
	}
	if got.Open.Reason != maint.ReasonMeter {
		t.Errorf("occurrence reason is %q, want %q.\n"+
			"A compliance report that cannot say which clock fired is a report\n"+
			"that cannot explain a short interval.", got.Open.Reason, maint.ReasonMeter)
	}
}

// A template makes the job an instruction rather than a reminder.
func TestAScheduleWithAProcedureCarriesItsChecklist(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	a := f.asset(t, "Boiler 1")

	tmpl, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindTemplate, Title: "Annual boiler service",
		Body: "Isolate, drain, inspect.",
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	for _, step := range []string{"Isolate the supply", "Drain the vessel", "Inspect the seals"} {
		if _, err := f.docs.AddChecklistItem(ctx, f.org, alice, tmpl.ID, step, ""); err != nil {
			t.Fatalf("add step: %v", err)
		}
	}

	sch, err := f.svc.Save(ctx, f.org, alice, maint.Schedule{
		AssetID: a.ID, Name: "Annual service", TemplateID: &tmpl.ID,
		EveryDays: intp(365), LeadDays: 0, NextDueAt: days(-1),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	f.sweep(t)
	got, err := f.svc.Get(ctx, f.org, alice, sch.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Open == nil || got.Open.WorkOrderID == nil {
		t.Fatal("nothing was raised")
	}
	items, err := f.docs.Checklist(ctx, f.org, alice, *got.Open.WorkOrderID)
	if err != nil {
		t.Fatalf("checklist: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("the generated work order has %d steps, want 3.\n"+
			"A job raised without its procedure is a reminder, and the crew has\n"+
			"plenty of those.", len(items))
	}
	if items[0].DoneAt != nil {
		t.Error("the copied checklist arrived already ticked")
	}
}

// The compliance number has to match a direct count, or it is decoration.
func TestComplianceMatchesADirectCount(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	a := f.asset(t, "Line 3")

	sch, err := f.svc.Save(ctx, f.org, alice, maint.Schedule{
		AssetID: a.ID, Name: "Weekly greasing", EveryDays: intp(7),
		LeadDays: 0, NextDueAt: days(7),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// A quarter of history, written directly, because the point of this test is
	// that the report reads the table honestly rather than that the generator
	// can be driven for ninety days.
	type row struct {
		due       int
		completed *int
	}
	hist := []row{
		{due: -80, completed: intp(-80)}, // on time
		{due: -70, completed: intp(-69)}, // late
		{due: -60, completed: intp(-60)}, // on time
		{due: -50, completed: nil},       // missed
		{due: -40, completed: intp(-30)}, // late
		{due: -30, completed: nil},       // missed
		{due: -20, completed: intp(-20)}, // on time
		{due: 5, completed: nil},         // still open, not a breach
	}
	for _, h := range hist {
		var completed *time.Time
		if h.completed != nil {
			c := days(*h.completed)
			completed = &c
		}
		if _, err := f.pools.Web.Exec(ctx, `
			INSERT INTO pm_occurrences (id, schedule_id, org_id, due_at, completed_at)
			VALUES (gen_random_uuid(),$1,$2,$3,$4)`,
			sch.ID, f.org, days(h.due), completed); err != nil {
			t.Fatalf("insert occurrence: %v", err)
		}
	}

	from, to := days(-90), days(10)
	rep, err := f.svc.Report(ctx, f.org, alice, from, to)
	if err != nil {
		t.Fatalf("report: %v", err)
	}

	// The count the auditor would run. Whole days, because that is what the
	// report grades on: due Tuesday and done Tuesday is on time, and a job due
	// this morning is not a breach this afternoon.
	var missed int
	err = f.pools.Web.QueryRow(ctx, `
		SELECT count(*) FROM pm_occurrences
		WHERE org_id=$1 AND completed_at IS NULL
		  AND date_trunc('day', due_at) < date_trunc('day', now())
		  AND due_at >= $2 AND due_at < $3`, f.org, from, to).Scan(&missed)
	if err != nil {
		t.Fatalf("direct count: %v", err)
	}
	if rep.Missed != missed {
		t.Errorf("the report says %d missed, a direct count says %d.\n"+
			"A compliance view that disagrees with the table is worse than none.",
			rep.Missed, missed)
	}
	if rep.Missed != 2 {
		t.Errorf("missed = %d, want 2", rep.Missed)
	}
	if rep.OnTime != 3 {
		t.Errorf("on time = %d, want 3", rep.OnTime)
	}
	if rep.Late != 2 {
		t.Errorf("late = %d, want 2", rep.Late)
	}
	if rep.Open != 1 {
		t.Errorf("still open = %d, want 1", rep.Open)
	}
	if rep.Due != 8 {
		t.Errorf("due in the window = %d, want 8", rep.Due)
	}
	// Five of the seven settled were done, so 71 percent; three of seven on
	// time, so 42. The open one is in neither number.
	if got := rep.Percent(); got != 71 {
		t.Errorf("completed = %d%%, want 71", got)
	}
	if got := rep.OnTimePercent(); got != 42 {
		t.Errorf("on time = %d%%, want 42", got)
	}
}

// Visibility follows the asset. A schedule is not a second answer to who can
// see a machine.
func TestSchedulesAreInvisibleWithoutTheAsset(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)
	a := f.asset(t, "Restricted press")

	sch, err := f.svc.Save(ctx, f.org, alice, maint.Schedule{
		AssetID: a.ID, Name: "Guard inspection", EveryDays: intp(30),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	list, err := f.svc.List(ctx, f.org, bob)
	if err != nil {
		t.Fatalf("list as bob: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("Bob cannot see the asset but sees %d of its schedules", len(list))
	}
	if _, err := f.svc.Get(ctx, f.org, bob, sch.ID); !errors.Is(err, maint.ErrNotFound) {
		t.Errorf("Get as Bob returned %v, want not-found.\n"+
			"Telling somebody an id is real is itself a disclosure.", err)
	}
	if _, err := f.svc.Save(ctx, f.org, bob, maint.Schedule{
		AssetID: a.ID, Name: "Bob's schedule", EveryDays: intp(1),
	}); err == nil {
		t.Error("Bob set a schedule on an asset he cannot even see")
	}

	// And once he can see it, he still cannot change it: view is not edit.
	if err := f.az.Grant(ctx, f.org, a.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessView, f.alice); err != nil {
		t.Fatalf("grant view: %v", err)
	}
	if _, err := f.svc.Get(ctx, f.org, bob, sch.ID); err != nil {
		t.Errorf("Bob can view the asset but not its schedule: %v", err)
	}
	if err := f.svc.SetEnabled(ctx, f.org, bob, sch.ID, false); err == nil {
		t.Error("a viewer paused a schedule, which is a change to what work happens")
	}
}

// Deleting a machine has to stop the work for it.
//
// Deleting a document is a soft delete, so nothing cascades. Without a filter
// the schedule outlives the thing it maintains, and the sweep keeps raising
// jobs for a press that left the building three months ago.
func TestDeletingTheAssetStopsItsSchedule(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	a := f.asset(t, "Press being scrapped")

	sch, err := f.svc.Save(ctx, f.org, alice, maint.Schedule{
		AssetID: a.ID, Name: "Guard check", EveryDays: intp(1),
		LeadDays: 0, NextDueAt: days(-1),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := f.docs.Delete(ctx, f.org, alice, a.ID); err != nil {
		t.Fatalf("delete asset: %v", err)
	}

	f.sweep(t)
	if n := f.countOccurrences(t, sch.ID); n != 0 {
		t.Errorf("a scrapped machine produced %d work orders, want 0", n)
	}
	list, err := f.svc.List(ctx, f.org, alice)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, s := range list {
		if s.ID == sch.ID {
			t.Error("the schedule for a deleted asset is still on the list")
		}
	}
	rep, err := f.svc.Report(ctx, f.org, alice, days(-30), days(30))
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep.Due != 0 {
		t.Errorf("compliance counts %d occurrences for a deleted asset, want 0", rep.Due)
	}
}

// A schedule with neither clock never comes round, which is worse than no
// schedule: it looks like cover and is not.
func TestAScheduleWithNoIntervalIsRefused(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a := f.asset(t, "Hoist 2")

	_, err := f.svc.Save(ctx, f.org, f.subj(t, f.alice), maint.Schedule{
		AssetID: a.ID, Name: "Some day",
	})
	if !errors.Is(err, maint.ErrInvalid) {
		t.Errorf("Save returned %v, want ErrInvalid", err)
	}
}

// Recording a reading is doing the work, not changing what the work is.
func TestCommentAccessIsEnoughToReadAGauge(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)
	a := f.asset(t, "Tank 5")

	if _, err := f.svc.RecordReading(ctx, f.org, bob, a.ID, "level", 42, ""); err == nil {
		t.Error("somebody with no access recorded a reading")
	}
	if err := f.az.Grant(ctx, f.org, a.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessComment, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := f.svc.RecordReading(ctx, f.org, bob, a.ID, "level", 42, "morning round"); err != nil {
		t.Errorf("comment access was not enough to record a reading: %v", err)
	}

	got, err := f.svc.LatestReading(ctx, a.ID, "level")
	if err != nil || got == nil {
		t.Fatalf("latest reading: %v %v", got, err)
	}
	if got.Value != 42 {
		t.Errorf("latest reading is %v, want 42", got.Value)
	}
	_ = alice
}
