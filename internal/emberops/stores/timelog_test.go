package stores_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// pgTime is the Postgres side of time: who may approve whom, and the estimate
// on the spine.
type pgTime struct {
	timelog.Store
	approves map[string]bool // "actor>subject"
	estimate map[string]*int
	removed  []string // entry ids AuditRemoval was told about
}

func (p *pgTime) CanApprove(_ context.Context, _, actor, subject string) (bool, error) {
	return actor != subject && p.approves[actor+">"+subject], nil
}

func (p *pgTime) SetEstimate(_ context.Context, _ string, _ core.Subjects, id string, m *int) error {
	p.estimate[id] = m
	return nil
}

func (p *pgTime) AuditRemoval(_ context.Context, _, _, entryID, _, _ string, _ timelog.RemovedEntry) error {
	p.removed = append(p.removed, entryID)
	return nil
}

type fakeAuthz struct{ caps map[string]bool }

func (fakeAuthz) Require(context.Context, core.Subjects, core.Ref, core.AccessLevel) error {
	return nil
}
func (a fakeAuthz) HasCapability(_ context.Context, _, _, c string) (bool, error) {
	return a.caps[c], nil
}

type docLookup struct {
	docs.Store
	byID map[string]*docs.Document
}

func (d docLookup) Get(_ context.Context, _ string, _ core.Subjects, id string) (*docs.Document, error) {
	if x, ok := d.byID[id]; ok {
		return x, nil
	}
	return nil, docs.ErrNotFound
}
func (d docLookup) Linked(context.Context, string, core.Subjects, string) ([]*docs.Document, error) {
	return nil, nil
}
func (d docLookup) AssignedTo(context.Context, string) ([]string, string, error) {
	return []string{bob}, bob, nil
}

const (
	taskID  = "33333333-3333-3333-3333-333333333333"
	woID    = "44444444-4444-4444-4444-444444444444"
	assetID = "55555555-5555-5555-5555-555555555555"
)

type timeEnv struct {
	*env
	s  *stores.TimeLog
	pg *pgTime
	az fakeAuthz
}

func newTimeEnv(t *testing.T, caps ...string) *timeEnv {
	t.Helper()
	e := newEnv(t)
	e.f.Put("Task", map[string]any{"name": "TASK-1", "hearth_id": taskID, "subject": "Fix belt", "status": "Open",
		"expected_time": 2.0, "hearth_primary_assignee": "bob@example.com", "hearth_space": "SP-1"})
	e.f.Put("Maintenance Work Order", map[string]any{"name": "MWO-1", "hearth_id": woID, "title": "PM press", "status": "In Progress",
		"estimated_hours": 1.0, "hearth_primary_assignee": "ann@example.com", "asset": "AST-1"})
	e.f.Put("Asset", map[string]any{"name": "AST-1", "hearth_id": assetID, "asset_name": "Press 4"})
	e.f.Put("Hearth Space", map[string]any{"name": "SP-1", "space_name": "Customer A"})
	cs := map[string]bool{}
	for _, c := range caps {
		cs[c] = true
	}
	az := fakeAuthz{caps: cs}
	pg := &pgTime{approves: map[string]bool{ann + ">" + bob: true}, estimate: map[string]*int{}}
	s := &stores.TimeLog{Store: pg, Base: e.base, Dir: e.dir, Authz: az, Docs: docLookup{byID: map[string]*docs.Document{
		taskID:  {ID: taskID, Kind: docs.KindTask, Title: "Fix belt"},
		woID:    {ID: woID, Kind: docs.KindWorkOrder, Title: "PM press"},
		assetID: {ID: assetID, Kind: docs.KindAsset, Title: "Press 4"},
	}}}
	return &timeEnv{env: e, s: s, pg: pg, az: az}
}

var monday = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func week() (time.Time, time.Time) { return monday.Add(-9 * time.Hour), monday.AddDate(0, 0, 7) }

func TestTimeLogWritesTimesheetRowsAndReadsThemBack(t *testing.T) {
	e := newTimeEnv(t)
	ctx := e.as("ann-tok")
	en, err := e.s.Log(ctx, org, actor(ann), taskID, 90, monday, " belt ")
	if err != nil {
		t.Fatal(err)
	}
	rows := e.f.All("Timesheet Detail")
	if len(rows) != 1 || rows[0]["hearth_id"] != en.ID || rows[0]["task"] != "TASK-1" || rows[0]["hours"] != 1.5 || rows[0]["description"] != "belt" {
		t.Fatalf("row = %v", rows)
	}
	if _, has := rows[0]["employee"]; has {
		t.Fatal("a Timesheet Detail row does not carry the employee; its Timesheet does")
	}
	if sheets := e.f.All("Timesheet"); len(sheets) != 1 || sheets[0]["employee"] != "EMP-ann" {
		t.Fatalf("timesheets = %v", sheets)
	}
	if _, err := e.s.LogOwn(ctx, org, actor(ann), "", 30, monday.Add(24*time.Hour), "admin"); err != nil {
		t.Fatal(err)
	}

	from, to := week()
	before := len(e.f.Calls)
	mine, err := e.s.Mine(ctx, org, ann, from, to, false)
	if err != nil || len(mine) != 2 {
		t.Fatalf("mine = %v %v", mine, err)
	}
	var got *timelog.Entry
	for i := range mine {
		if mine[i].ID == en.ID {
			got = &mine[i]
		}
	}
	if got == nil || got.DocumentID != taskID || got.DocTitle != "Fix belt" || got.PrincipalID != ann || got.PersonName == "" ||
		got.Minutes == nil || *got.Minutes != 90 || got.Note != "belt" || !got.Pending() {
		t.Fatalf("entry = %+v", got)
	}
	// Bounded however many entries: timesheets, rows, parents, directory, a target batch, tags.
	if n := len(e.f.Calls) - before; n > 9 {
		t.Fatalf("%d calls for the week: %v", n, e.f.Calls[before:])
	}
	total, pending, err := e.s.MineTotal(ctx, org, ann, from, to)
	if err != nil || total != 120 || pending != 120 {
		t.Fatalf("total %d pending %d %v", total, pending, err)
	}
	if es, err := e.s.Entries(ctx, org, actor(ann), taskID); err != nil || len(es) != 1 {
		t.Fatalf("entries for the task: %v %v", es, err)
	}
	if _, err := e.s.Log(ctx, org, actor(ann), taskID, 0, monday, ""); err == nil {
		t.Fatal("zero minutes accepted")
	}
}

func TestTimeLogTimer(t *testing.T) {
	e := newTimeEnv(t)
	ctx := e.as("ann-tok")
	if r, err := e.s.Running(ctx, org, ann); err != nil || r != nil {
		t.Fatalf("running before start: %v %v", r, err)
	}
	if _, err := e.s.Stop(ctx, org, ann); !errors.Is(err, timelog.ErrNotRunning) {
		t.Fatalf("stop with nothing running: %v", err)
	}
	r, err := e.s.Start(ctx, org, actor(ann), woID, "pm")
	if err != nil || r == nil || r.DocumentID != woID || r.DocTitle != "PM press" || r.Note != "pm" {
		t.Fatalf("start = %+v %v", r, err)
	}
	if _, err := e.s.Start(ctx, org, actor(ann), taskID, ""); !errors.Is(err, timelog.ErrAlreadyRunning) {
		t.Fatalf("second timer: %v", err)
	}
	if r, _ := e.s.Running(ctx, org, ann); r == nil || r.DocumentID != woID {
		t.Fatalf("running = %+v", r)
	}
	// Bob's clock is his own.
	if r, _ := e.s.Running(e.as("bob-tok"), org, bob); r != nil {
		t.Fatalf("bob sees ann's timer: %+v", r)
	}
	en, err := e.s.Stop(ctx, org, ann)
	if err != nil || en.EndedAt == nil || en.DocumentID != woID || en.PrincipalID != ann {
		t.Fatalf("stop = %+v %v", en, err)
	}
	if rows := e.f.All("Timesheet Detail"); len(rows) != 1 || rows[0]["maintenance_work_order"] != "MWO-1" {
		t.Fatalf("rows = %v", rows)
	}
	if n := len(e.f.All("Hearth Timer")); n != 0 {
		t.Fatalf("%d timers left", n)
	}
}

func TestTimeLogCorrectRemoveRestoreAndWhoMay(t *testing.T) {
	e := newTimeEnv(t)
	ctx := e.as("ann-tok")
	en, _ := e.s.Log(ctx, org, actor(ann), taskID, 60, monday, "x")

	if err := e.s.Correct(ctx, org, actor(ann), en.ID, 45, nil, "fixed"); err != nil {
		t.Fatal(err)
	}
	row := e.f.All("Timesheet Detail")[0]
	if row["hours"] != 0.75 || row["description"] != "fixed" || row["to_time"] != "2026-03-02 09:45:00" {
		t.Fatalf("row = %v", row)
	}
	if err := e.s.Remove(ctx, org, actor(ann), en.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.pg.removed) != 1 || e.pg.removed[0] != en.ID {
		t.Fatalf("removing an entry must write its audit row, got %v", e.pg.removed)
	}
	from, to := week()
	if mine, _ := e.s.Mine(ctx, org, ann, from, to, false); len(mine) != 0 {
		t.Fatalf("removed entry still listed: %v", mine)
	}
	if mine, _ := e.s.Mine(ctx, org, ann, from, to, true); len(mine) != 1 || !mine[0].Deleted() {
		t.Fatalf("showRemoved: %v", mine)
	}
	if err := e.s.Correct(ctx, org, actor(ann), en.ID, 30, nil, ""); !errors.Is(err, timelog.ErrDeleted) {
		t.Fatalf("correcting a removed entry: %v", err)
	}
	if err := e.s.Remove(ctx, org, actor(ann), en.ID); err != nil {
		t.Fatalf("removing twice: %v", err)
	}
	if len(e.pg.removed) != 1 {
		t.Fatalf("removing twice wrote %d audit rows, want 1", len(e.pg.removed))
	}
	if err := e.s.Restore(ctx, org, actor(ann), en.ID); err != nil {
		t.Fatal(err)
	}
	if mine, _ := e.s.Mine(ctx, org, ann, from, to, false); len(mine) != 1 {
		t.Fatalf("restored entry missing: %v", mine)
	}
	// Bob is neither the author nor... the fake authorizer lets admin through,
	// so check the unattached case: nobody but the author may touch it.
	own, _ := e.s.LogOwn(ctx, org, actor(ann), "", 15, monday, "")
	if err := e.s.Remove(e.as("bob-tok"), org, actor(bob), own.ID); !errors.Is(err, timelog.ErrForbidden) {
		t.Fatalf("bob removing ann's unattached entry: %v", err)
	}
	if err := e.s.Remove(ctx, org, actor(ann), "99999999-9999-9999-9999-999999999999"); !errors.Is(err, timelog.ErrNotFound) {
		t.Fatalf("unknown entry: %v", err)
	}
}

func TestTimeLogApprovalFollowsHearthsStructure(t *testing.T) {
	e := newTimeEnv(t)
	bobCtx := e.as("bob-tok")
	a, _ := e.s.Log(bobCtx, org, actor(bob), taskID, 60, monday, "")
	b, _ := e.s.Log(bobCtx, org, actor(bob), woID, 30, monday, "")
	annCtx := e.as("ann-tok")
	e.s.Log(annCtx, org, actor(ann), taskID, 10, monday, "")

	// Ann approves bob (the structure says so), not herself; bob approves nobody.
	pend, err := e.s.Pending(annCtx, org, ann)
	if err != nil || len(pend) != 2 {
		t.Fatalf("pending = %v %v", pend, err)
	}
	if n, _ := e.s.PendingCount(annCtx, org, ann); n != 2 {
		t.Fatalf("count %d", n)
	}
	if pend, _ := e.s.Pending(bobCtx, org, bob); len(pend) != 0 {
		t.Fatalf("bob can decide ann's time: %v", pend)
	}
	if err := e.s.Decide(bobCtx, org, bob, a.ID, true, ""); !errors.Is(err, timelog.ErrNotYours) {
		t.Fatalf("bob deciding: %v", err)
	}
	if err := e.s.Decide(annCtx, org, ann, a.ID, false, " too long "); err != nil {
		t.Fatal(err)
	}
	from, to := week()
	mine, _ := e.s.Mine(bobCtx, org, bob, from, to, false)
	var sentBack timelog.Entry
	for _, m := range mine {
		if m.ID == a.ID {
			sentBack = m
		}
	}
	if !sentBack.SentBack() || sentBack.DecidedNote != "too long" || sentBack.DecidedBy != ann || sentBack.DeciderName == "" || sentBack.DecidedAt == nil {
		t.Fatalf("decision = %+v", sentBack)
	}
	if err := e.s.Decide(annCtx, org, ann, a.ID, true, ""); !errors.Is(err, timelog.ErrNotYours) {
		t.Fatalf("deciding twice: %v", err)
	}
	if err := e.s.Reopen(annCtx, org, ann, a.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.s.PendingCount(annCtx, org, ann); n != 2 {
		t.Fatalf("reopened entry not pending: %d", n)
	}

	// All or nothing: one entry she may not decide refuses the batch untouched.
	own, _ := e.s.Log(annCtx, org, actor(ann), taskID, 5, monday, "")
	if _, err := e.s.ApproveMany(annCtx, org, ann, []string{a.ID, own.ID}); !errors.Is(err, timelog.ErrBatchRefused) {
		t.Fatalf("batch with her own entry: %v", err)
	}
	if n, _ := e.s.PendingCount(annCtx, org, ann); n != 2 {
		t.Fatal("a refused batch changed something")
	}
	if n, err := e.s.ApproveMany(annCtx, org, ann, []string{a.ID, b.ID}); err != nil || n != 2 {
		t.Fatalf("approve many: %d %v", n, err)
	}
	if n, _ := e.s.PendingCount(annCtx, org, ann); n != 0 {
		t.Fatalf("pending after approval: %d", n)
	}
	if done, err := e.s.Decided(annCtx, org, ann, from, to, false); err != nil || len(done) != 2 {
		t.Fatalf("decided = %v %v", done, err)
	}
	if done, _ := e.s.Decided(bobCtx, org, bob, from, to, false); len(done) != 0 {
		t.Fatalf("bob sees decisions he did not make: %v", done)
	}
	if done, _ := e.s.Decided(bobCtx, org, bob, from, to, true); len(done) != 2 {
		t.Fatalf("seeAll: %v", done)
	}
}

func TestTimeLogWorkloadIsBatchedAndGatedByCapability(t *testing.T) {
	e := newTimeEnv(t)
	from, to := week()
	if _, err := e.s.Workload(e.as("ann-tok"), org, ann, from, to); !errors.Is(err, timelog.ErrNotAllowed) {
		t.Fatalf("without time_all: %v", err)
	}
	e = newTimeEnv(t, "time_all")
	e.f.Put("Employee", map[string]any{"name": "EMP-x", "hearth_id": "77777777-7777-7777-7777-777777777777"})
	for _, emp := range e.f.All("Employee") {
		if emp["name"] == "EMP-ann" {
			emp["hearth_minutes_per_week"] = 1200.0
		}
	}
	e.f.Put("Activity Cost", map[string]any{"employee": "EMP-ann", "costing_rate": 40.0})
	ctx := e.as("ann-tok")
	e.s.Log(ctx, org, actor(ann), taskID, 120, monday, "")
	e.s.Log(e.as("bob-tok"), org, actor(bob), woID, 60, monday, "")

	before := len(e.f.Calls)
	loads, err := e.s.Workload(ctx, org, ann, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(e.f.Calls) - before; n > 12 {
		t.Fatalf("workload took %d calls however many people: %v", n, e.f.Calls[before:])
	}
	by := map[string]timelog.Load{}
	for _, l := range loads {
		by[l.PrincipalID] = l
	}
	// Bob is assigned the 2 hour task; ann the 1 hour work order.
	if l := by[bob]; l.Assigned != 120 || l.Open != 1 || l.Logged != 60 || l.Capacity != 2400 {
		t.Fatalf("bob = %+v", l)
	}
	if l := by[ann]; l.Assigned != 60 || l.Logged != 120 || l.Capacity != 1200 || l.Percent() != 5 {
		t.Fatalf("ann = %+v", l)
	}
	if by[ann].RateCents != nil {
		t.Fatal("pay rate shown without the finance capability")
	}
	if loads[0].PrincipalID != bob {
		t.Fatalf("most loaded first: %+v", loads)
	}

	e2 := newTimeEnv(t, "time_all", "finance")
	e2.f.Put("Activity Cost", map[string]any{"employee": "EMP-ann", "costing_rate": 40.0})
	l2, _ := e2.s.Workload(e2.as("ann-tok"), org, ann, from, to)
	for _, l := range l2 {
		if l.PrincipalID == ann && (l.RateCents == nil || *l.RateCents != 4000) {
			t.Fatalf("rate = %v", l.RateCents)
		}
	}
}

func TestTimeLogReportsNeedFinanceAndSayWhichSpace(t *testing.T) {
	e := newTimeEnv(t)
	from, to := week()
	if _, err := e.s.TimeReport(e.as("ann-tok"), org, ann, "", from, to); !errors.Is(err, timelog.ErrNotAllowed) {
		t.Fatalf("report without finance: %v", err)
	}
	e = newTimeEnv(t, "finance")
	e.s.Log(e.as("ann-tok"), org, actor(ann), taskID, 60, monday, "belt")
	e.s.Log(e.as("ann-tok"), org, actor(ann), woID, 30, monday, "")
	e.s.Log(e.as("bob-tok"), org, actor(bob), taskID, 120, monday, "")

	sum, err := e.s.TimeReportSummary(e.as("ann-tok"), org, ann, from, to)
	if err != nil || len(sum) != 2 || sum[0].PrincipalID != bob || sum[0].Minutes != 120 || sum[1].Minutes != 90 {
		t.Fatalf("summary = %+v %v", sum, err)
	}
	rep, err := e.s.TimeReport(e.as("ann-tok"), org, ann, ann, from, to)
	if err != nil || len(rep) != 2 {
		t.Fatalf("report = %+v %v", rep, err)
	}
	var task timelog.ReportRow
	for _, r := range rep {
		if r.DocTitle == "Fix belt" {
			task = r
		}
	}
	if task.SpaceName != "Customer A" || task.Minutes != 60 || task.Note != "belt" {
		t.Fatalf("row = %+v", task)
	}
}

func TestTimeLogTagsAreActivityTypes(t *testing.T) {
	e := newTimeEnv(t, "people")
	ctx := e.as("ann-tok")
	if _, err := newTimeEnv(t).s.CreateTag(ctx, org, ann, "Repair", ""); !errors.Is(err, timelog.ErrNotAllowed) {
		t.Fatalf("tag admin without people: %v", err)
	}
	repair, err := e.s.CreateTag(ctx, org, ann, " Repair ", "fixing")
	if err != nil {
		t.Fatal(err)
	}
	inspect, _ := e.s.CreateTag(ctx, org, ann, "Inspect", "")
	if at := e.f.All("Activity Type"); len(at) != 2 || at[0]["hearth_id"] == "" {
		t.Fatalf("activity types = %v", at)
	}
	en, _ := e.s.Log(ctx, org, actor(ann), taskID, 60, monday, "")
	if err := e.s.TagEntry(ctx, org, actor(ann), en.ID, repair.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.s.TagEntry(ctx, org, actor(ann), en.ID, repair.ID); err != nil {
		t.Fatal("tagging twice must be a no-op")
	}
	if err := e.s.TagEntry(ctx, org, actor(ann), en.ID, inspect.ID); err != nil {
		t.Fatal(err)
	}
	row := e.f.All("Timesheet Detail")[0]
	if row["hearth_tags"] != "Inspect,Repair" && row["hearth_tags"] != "Repair,Inspect" {
		t.Fatalf("tags = %v", row["hearth_tags"])
	}
	if row["activity_type"] != "Repair" {
		t.Fatalf("the first tag is the Activity Type: %v", row["activity_type"])
	}
	tags, _ := e.s.EntryTags(ctx, org, en.ID)
	if len(tags) != 2 || tags[0].Name != "Inspect" {
		t.Fatalf("entry tags = %v", tags)
	}
	if err := e.s.UntagEntry(ctx, org, actor(ann), en.ID, repair.ID); err != nil {
		t.Fatal(err)
	}
	// Batch: tag the rest of the week; only entries without it change.
	e.s.Log(ctx, org, actor(ann), woID, 30, monday.Add(time.Hour), "")
	from, to := week()
	if n, err := e.s.BatchTag(ctx, org, actor(ann), ann, inspect.ID, from, to); err != nil || n != 1 {
		t.Fatalf("batch tag = %d %v", n, err)
	}
	if _, err := e.s.BatchTag(ctx, org, actor(ann), bob, inspect.ID, from, to); !errors.Is(err, timelog.ErrForbidden) {
		t.Fatalf("tagging someone else's week: %v", err)
	}
	if err := e.s.DeleteTag(ctx, org, ann, repair.ID); err != nil {
		t.Fatal(err)
	}
	if ts, _ := e.s.ListTags(ctx, org); len(ts) != 1 {
		t.Fatalf("tags after delete: %v", ts)
	}
}

func TestTimeLogEffortCapacityAndAssetCost(t *testing.T) {
	e := newTimeEnv(t)
	ctx := e.as("ann-tok")
	e.f.Put("Hearth Settings", map[string]any{"name": "Hearth Settings", "default_activity_type": "Execution"})
	e.s.Log(ctx, org, actor(ann), taskID, 90, monday, "")
	e.s.Log(e.as("bob-tok"), org, actor(bob), taskID, 30, monday, "")
	e.s.Log(ctx, org, actor(ann), woID, 60, monday, "")

	eff, err := e.s.EffortFor(ctx, org, actor(ann), taskID)
	if err != nil || eff.ActualMinutes != 120 || eff.Entries != 2 || eff.EstimateMinutes == nil || *eff.EstimateMinutes != 120 || eff.CostCents != nil {
		t.Fatalf("effort = %+v %v", eff, err)
	}
	rate := 6000
	if err := e.s.SetCapacity(ctx, org, ann, 1800, &rate); err != nil {
		t.Fatal(err)
	}
	if err := e.s.SetCapacity(ctx, org, ann, 1800, &rate); err != nil {
		t.Fatal(err)
	}
	if costs := e.f.All("Activity Cost"); len(costs) != 1 || costs[0]["costing_rate"] != 60.0 || costs[0]["activity_type"] != "Execution" {
		t.Fatalf("activity costs = %v", costs)
	}
	for _, emp := range e.f.All("Employee") {
		if emp["name"] == "EMP-ann" && emp["hearth_minutes_per_week"] != 1800.0 {
			t.Fatalf("capacity = %v", emp["hearth_minutes_per_week"])
		}
	}
	eff, _ = e.s.EffortFor(ctx, org, actor(ann), taskID)
	if eff.CostCents == nil || *eff.CostCents != 9000 { // ann's 90 minutes at 60.00/h; bob has no rate
		t.Fatalf("cost = %v", eff.CostCents)
	}
	ae, err := e.s.ForAsset(ctx, org, actor(ann), assetID)
	if err != nil || ae.Minutes != 60 || ae.WorkOrders != 1 || ae.AssetTitle != "Press 4" || ae.CostCents == nil || *ae.CostCents != 6000 {
		t.Fatalf("asset effort = %+v %v", ae, err)
	}

	m := 180
	if err := e.s.SetEstimate(ctx, org, actor(ann), taskID, &m); err != nil {
		t.Fatal(err)
	}
	if e.pg.estimate[taskID] == nil || *e.pg.estimate[taskID] != 180 {
		t.Fatal("the spine did not get the estimate")
	}
	if got := e.f.All("Task")[0]["expected_time"]; got != 3.0 {
		t.Fatalf("EmberOPS estimate = %v", got)
	}
}

func TestTimeLogWeekLoadHelper(t *testing.T) {
	e := newTimeEnv(t)
	ctx := e.as("ann-tok")
	e.s.Log(ctx, org, actor(ann), taskID, 90, monday, "")
	e.s.Log(ctx, org, actor(ann), taskID, 30, monday.AddDate(0, 0, 9), "") // next week
	r, err := e.c.WeekLoad(ctx, "EMP-ann", "2026-03-02")
	if err != nil || r.Minutes != 90 || r.CapacityMinutes != 2400 || !strings.HasPrefix(r.WeekStart, "2026-03-02") {
		t.Fatalf("week load = %+v %v", r, err)
	}
	if e.f.CallCount("week_load") != 1 {
		t.Fatal("expected one call")
	}
}
