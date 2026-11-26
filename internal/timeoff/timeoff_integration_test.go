package timeoff_test

// Time off against a real Postgres. The checks that matter are about who may
// decide and who may see: every refusal is tested beside its permission,
// because a check that always says yes passes every happy-path test.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
	"github.com/Embernet-ai/hearth/internal/timeoff"
)

func TestTimeOffIsAskedDecidedAndPrivate(t *testing.T) {
	dsn := pgtest.Database(t, "hearth_timeoff_test")
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	if err := pg.Migrate(ctx, pools, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))); err != nil {
		t.Fatal(err)
	}
	db := pools.Web
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	org := uuid.NewString()
	ana, boss, hr, colleague, manager := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	dept := uuid.NewString()
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1, $2, 'Time Off Co')`, org, "to-"+org[:8])
	for id, name := range map[string]string{ana: "Ana", boss: "Boss", hr: "Hilda HR", colleague: "Colin", manager: "Mara"} {
		exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1, $2, 'user', $3)`, id, org, name)
	}
	exec(`UPDATE principals SET approver_id = $2 WHERE id = $1`, ana, boss)
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1, $2, 'people', $2)`, org, hr)
	exec(`INSERT INTO spaces (id, org_id, kind, name, created_by) VALUES ($1, $2, 'folder', 'Engineering', $3)`, dept, org, boss)
	exec(`INSERT INTO principal_departments (org_id, principal_id, space_id) VALUES ($1, $2, $3)`, org, ana, dept)
	exec(`INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1, $2, $3)`, org, dept, manager)

	svc := timeoff.New(db, nil)
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	// Refused without the acknowledgement, the dates backwards, or all weekend.
	for name, p := range map[string]timeoff.Params{
		"unacknowledged": {StartsOn: day("2030-03-04"), EndsOn: day("2030-03-08"), Kind: "vacation"},
		"backwards":      {StartsOn: day("2030-03-08"), EndsOn: day("2030-03-04"), Kind: "vacation", Acknowledged: true},
		"weekend":        {StartsOn: day("2030-03-09"), EndsOn: day("2030-03-10"), Kind: "vacation", Acknowledged: true},
		"no kind":        {StartsOn: day("2030-03-04"), EndsOn: day("2030-03-08"), Kind: "beach", Acknowledged: true},
	} {
		if _, err := svc.Ask(ctx, org, ana, p); !errors.Is(err, timeoff.ErrInvalid) {
			t.Errorf("%s request was accepted: %v", name, err)
		}
	}

	// Mon 4 to Mon 11 March 2030 is six working days.
	r, err := svc.Ask(ctx, org, ana, timeoff.Params{StartsOn: day("2030-03-04"), EndsOn: day("2030-03-11"),
		Kind: "medical", Reason: "Surgery", Acknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Days != 6 {
		t.Fatalf("counted %d working days, want 6", r.Days)
	}

	// Who sees it: the approver, the department manager and HR. Not a colleague.
	for who, want := range map[string]int{boss: 1, manager: 1, hr: 1, colleague: 0, ana: 0} {
		list, err := svc.ToDecide(ctx, org, who)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != want {
			t.Errorf("%s sees %d requests to decide, want %d", who, len(list), want)
		}
	}

	// Who may decide: never the requester, never a colleague.
	if err := svc.Decide(ctx, org, ana, r.ID, true, ""); !errors.Is(err, timeoff.ErrDenied) {
		t.Fatalf("the requester approved their own time off: %v", err)
	}
	if err := svc.Decide(ctx, org, colleague, r.ID, true, ""); !errors.Is(err, timeoff.ErrDenied) {
		t.Fatalf("a colleague approved somebody's time off: %v", err)
	}
	if err := svc.Decide(ctx, org, boss, r.ID, true, "Get well"); err != nil {
		t.Fatalf("the approver could not approve: %v", err)
	}
	if err := svc.Decide(ctx, org, hr, r.ID, false, ""); !errors.Is(err, timeoff.ErrDenied) {
		t.Fatalf("an already decided request was decided again: %v", err)
	}

	// The team's calendar says Out, with the name and never the reason.
	var title, space string
	if err := db.QueryRow(ctx, `SELECT title, COALESCE(space_id::text, '') FROM calendar_events WHERE org_id = $1`, org).Scan(&title, &space); err != nil {
		t.Fatalf("no calendar entry after approval: %v", err)
	}
	if title != "Out: Ana" || space != dept {
		t.Fatalf("calendar says %q on %q, want \"Out: Ana\" on the department", title, space)
	}

	mine, err := svc.Mine(ctx, org, ana)
	if err != nil || len(mine) != 1 || mine[0].Status != "approved" || mine[0].DeciderName != "Boss" {
		t.Fatalf("Ana's own view is wrong: %+v %v", mine, err)
	}

	// Cancelling a future approved request takes it off the calendar; only
	// the requester can.
	if err := svc.Cancel(ctx, org, boss, r.ID); !errors.Is(err, timeoff.ErrDenied) {
		t.Fatalf("somebody else cancelled Ana's time off: %v", err)
	}
	if err := svc.Cancel(ctx, org, ana, r.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM calendar_events WHERE org_id = $1`, org).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the calendar still shows the cancelled time off (%d, %v)", n, err)
	}
}

type told struct {
	changed []*timeoff.Request
	notes   []string
}

func (told) TimeOffRequested(context.Context, string, *timeoff.Request, []string) {}
func (told) TimeOffDecided(context.Context, string, string, *timeoff.Request)     {}
func (t *told) TimeOffChanged(_ context.Context, _, _ string, r *timeoff.Request, note string) {
	t.changed = append(t.changed, r)
	t.notes = append(t.notes, note)
}

func ptr[T any](v T) *T { return &v }

// An approver or an admin changes or cancels somebody else's request, and the answer is the
// same everywhere: only those who may decide, pending or approved only, audited, the person told,
// and the calendar's "Out" kept in step.
func TestApproversAndAdminsChangeAndCancelTimeOff(t *testing.T) {
	dsn := pgtest.Database(t, "hearth_timeoff_change_test")
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	if err := pg.Migrate(ctx, pools, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))); err != nil {
		t.Fatal(err)
	}
	db := pools.Web
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	org := uuid.NewString()
	ana, boss, admin, colleague := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1, $2, 'Change Co')`, org, "tc-"+org[:8])
	for id, name := range map[string]string{ana: "Ana", boss: "Boss", admin: "Ada Admin", colleague: "Colin"} {
		exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1, $2, 'user', $3)`, id, org, name)
	}
	exec(`UPDATE principals SET approver_id = $2 WHERE id = $1`, ana, boss)
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1, $2, 'super_admin', $2)`, org, admin)

	notice := &told{}
	svc := timeoff.New(db, notice)
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	ask := func() *timeoff.Request {
		r, err := svc.Ask(ctx, org, ana, timeoff.Params{StartsOn: day("2030-03-04"), EndsOn: day("2030-03-08"), Kind: "vacation",
			Reason: "private reason", Acknowledged: true})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	audits := func(action string) int {
		var n int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE org_id = $1 AND action = $2`, org, action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Pending: the person's approver changes it; dates, type, half day, note.
	r := ask()
	for name, who := range map[string]string{"the person": ana, "a colleague": colleague} {
		if _, err := svc.Change(ctx, org, who, r.ID, timeoff.Change{Kind: ptr("sick")}); !errors.Is(err, timeoff.ErrDenied) {
			t.Errorf("%s changed the request: %v", name, err)
		}
		if err := svc.CancelFor(ctx, org, who, r.ID, ""); !errors.Is(err, timeoff.ErrDenied) {
			t.Errorf("%s cancelled the request: %v", name, err)
		}
	}
	if audits("time_off.change")+audits("time_off.cancel") != 0 || len(notice.changed) != 0 {
		t.Fatal("a refused change left an audit row or a notice")
	}
	if _, err := svc.Change(ctx, org, boss, r.ID, timeoff.Change{Kind: ptr("vacation")}); !errors.Is(err, timeoff.ErrInvalid) {
		t.Fatalf("a change that changes nothing: %v", err)
	}
	if _, err := svc.Change(ctx, org, boss, r.ID, timeoff.Change{StartsOn: ptr(day("2030-03-09")), EndsOn: ptr(day("2030-03-10"))}); !errors.Is(err, timeoff.ErrInvalid) {
		t.Fatalf("a weekend: %v", err)
	}
	got, err := svc.Change(ctx, org, boss, r.ID, timeoff.Change{StartsOn: ptr(day("2030-03-05")), EndsOn: ptr(day("2030-03-12")),
		Kind: ptr("personal"), HalfDay: ptr(true), Comments: ptr("moved a day"), Note: "the team needs Monday"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" || got.Days != 6 || got.Length() != 5.5 || !got.HalfDay || got.Kind != "personal" || got.Comments != "moved a day" {
		t.Fatalf("changed = %+v", got)
	}
	mine, _ := svc.Mine(ctx, org, ana)
	if len(mine) != 1 || mine[0].StartsOn != day("2030-03-05") || !mine[0].HalfDay || mine[0].Reason != "private reason" {
		t.Fatalf("Ana's own view after the change: %+v", mine)
	}
	if len(notice.changed) != 1 || notice.changed[0].RequesterID != ana || notice.notes[0] != "the team needs Monday" {
		t.Fatalf("Ana was told: %+v %v", notice.changed, notice.notes)
	}
	var before, after, actor, entity string
	if err := db.QueryRow(ctx, `SELECT before::text, after::text, actor_id::text, entity_id::text FROM audit_log WHERE org_id = $1 AND action = 'time_off.change'`, org).
		Scan(&before, &after, &actor, &entity); err != nil {
		t.Fatalf("no audit row for the change: %v", err)
	}
	if actor != boss || entity != r.ID || !strings.Contains(before, `"2030-03-04"`) || !strings.Contains(after, `"2030-03-05"`) ||
		!strings.Contains(after, "the team needs Monday") || strings.Contains(before+after, "private reason") {
		t.Fatalf("audit row: actor %s entity %s\nbefore %s\nafter %s", actor, entity, before, after)
	}

	// Approved: an admin changes it, the calendar follows, and an admin cancels it off the calendar.
	if err := svc.Decide(ctx, org, boss, r.ID, true, "approved"); err != nil {
		t.Fatal(err)
	}
	if got, err = svc.Change(ctx, org, admin, r.ID, timeoff.Change{EndsOn: ptr(day("2030-03-14")), HalfDay: ptr(false), Note: "extended by HR"}); err != nil {
		t.Fatal(err)
	}
	if got.Status != "approved" || got.Days != 8 || got.HalfDay || got.DeciderName != "Ada Admin" || got.DecisionNote != "extended by HR" {
		t.Fatalf("approved, changed = %+v", got)
	}
	var starts, ends time.Time
	if err := db.QueryRow(ctx, `SELECT starts_at, ends_at FROM calendar_events WHERE org_id = $1`, org).Scan(&starts, &ends); err != nil {
		t.Fatal(err)
	}
	if !starts.Equal(day("2030-03-05")) || !ends.Equal(day("2030-03-15")) {
		t.Fatalf("the Out says %v to %v, want 5 to 14 March", starts, ends)
	}
	if err := svc.CancelFor(ctx, org, admin, r.ID, "the project was cancelled"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM calendar_events WHERE org_id = $1`, org).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the Out stayed after the cancel (%d, %v)", n, err)
	}
	mine, _ = svc.Mine(ctx, org, ana)
	if mine[0].Status != "cancelled" || mine[0].DecisionNote != "Cancelled by Ada Admin: the project was cancelled" {
		t.Fatalf("after the cancel Ana sees %+v", mine[0])
	}
	if audits("time_off.change") != 2 || audits("time_off.cancel") != 1 || len(notice.changed) != 3 || notice.changed[2].Status != "cancelled" {
		t.Fatalf("audit rows %d+%d, notices %d", audits("time_off.change"), audits("time_off.cancel"), len(notice.changed))
	}
	// Finished is finished, even for an admin.
	if _, err := svc.Change(ctx, org, admin, r.ID, timeoff.Change{Kind: ptr("sick")}); !errors.Is(err, timeoff.ErrDenied) {
		t.Fatalf("changed a cancelled request: %v", err)
	}

	// An approved one that started long ago is still the admin's to fix, and the list an approver
	// opens carries approved requests still to come however long ago they were decided.
	r2 := ask()
	if err := svc.Decide(ctx, org, boss, r2.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE time_off_requests SET decided_at = now() - interval '90 days' WHERE id = $1`, r2.ID)
	list, err := svc.ToDecide(ctx, org, boss)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range list {
		found = found || x.ID == r2.ID
	}
	if !found {
		t.Fatalf("an approved request still to come is not on the approver's list: %+v", list)
	}
	if g, err := svc.Get(ctx, org, boss, r2.ID); err != nil || g.ID != r2.ID {
		t.Fatalf("approver Get: %v", err)
	}
	if _, err := svc.Get(ctx, org, colleague, r2.ID); !errors.Is(err, timeoff.ErrNotFound) {
		t.Fatalf("a colleague read the request: %v", err)
	}
}

type countedNotes struct{ asked, decided int }

func (c *countedNotes) TimeOffRequested(context.Context, string, *timeoff.Request, []string) {
	c.asked++
}
func (c *countedNotes) TimeOffDecided(context.Context, string, string, *timeoff.Request) { c.decided++ }
func (c *countedNotes) TimeOffChanged(context.Context, string, string, *timeoff.Request, string) {
}

// A salaried person's vacation approves itself on submit, up to five working days a calendar month, with the Out on the
// calendar, the notices and an audit row a manual approval would give. Over the week it waits for a person.
func TestSalariedVacationApprovesItselfUpToAWeekAMonth(t *testing.T) {
	dsn := pgtest.Database(t, "hearth_timeoff_auto_test")
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	if err := pg.Migrate(ctx, pools, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))); err != nil {
		t.Fatal(err)
	}
	db := pools.Web
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	org, sal, other, boss := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1, $2, 'Auto Co')`, org, "au-"+org[:8])
	for id, name := range map[string]string{sal: "Sal", other: "Otto", boss: "Boss"} {
		exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1, $2, 'user', $3)`, id, org, name)
	}
	exec(`UPDATE principals SET approver_id = $2 WHERE id IN ($1, $3)`, sal, boss, other)
	exec(`UPDATE principals SET time_auto_approve_since = '2026-01-01' WHERE id = $1`, sal)

	notes := &countedNotes{}
	svc := timeoff.New(db, notes)
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	ask := func(who, kind, from, to string) *timeoff.Request {
		t.Helper()
		r, err := svc.Ask(ctx, org, who, timeoff.Params{StartsOn: day(from), EndsOn: day(to), Kind: kind, Acknowledged: true})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Mon 4 to Wed 6 March 2030: three days, approved on the way in by the setting.
	r := ask(sal, "vacation", "2030-03-04", "2030-03-06")
	if r.Status != "approved" || r.DecisionNote != timeoff.AutoApprovedNote || r.DecidedAt == nil {
		t.Fatalf("a fitting vacation was not approved on submit: %+v", r)
	}
	if n := count(`SELECT count(*) FROM calendar_events WHERE org_id=$1 AND title='Out: Sal'`, org); n != 1 {
		t.Fatalf("%d Out events, want 1", n)
	}
	if n := count(`SELECT count(*) FROM time_off_requests WHERE id=$1 AND status='approved' AND decided_by=$2 AND calendar_event_id IS NOT NULL`, r.ID, sal); n != 1 {
		t.Fatal("the stored request is not approved with its Out linked")
	}
	if n := count(`SELECT count(*) FROM audit_log WHERE org_id=$1 AND action='time_off.auto_approve' AND entity_id=$2 AND actor_id IS NULL`, org, r.ID); n != 1 {
		t.Fatalf("%d audit rows, want 1", n)
	}
	if notes.asked != 1 || notes.decided != 1 {
		t.Fatalf("notices: asked %d, decided %d", notes.asked, notes.decided)
	}

	// Two more days make five and fit; a sixth waits for a person, who can still decide it.
	if r2 := ask(sal, "vacation", "2030-03-18", "2030-03-19"); r2.Status != "approved" {
		t.Fatalf("five days: %+v", r2)
	}
	over := ask(sal, "vacation", "2030-03-25", "2030-03-25")
	if over.Status != "pending" {
		t.Fatalf("a sixth day approved itself: %+v", over)
	}
	if n := count(`SELECT count(*) FROM calendar_events WHERE org_id=$1`, org); n != 2 {
		t.Fatalf("%d Out events, want 2", n)
	}
	if err := svc.Decide(ctx, org, boss, over.ID, true, "ok"); err != nil {
		t.Fatalf("a person deciding the over-cap request: %v", err)
	}
	// A cancelled one frees its days; other kinds and people without the setting always wait.
	if err := svc.Cancel(ctx, org, sal, r.ID); err != nil {
		t.Fatal(err)
	}
	if r3 := ask(sal, "vacation", "2030-03-11", "2030-03-12"); r3.Status != "approved" {
		t.Fatalf("days freed by cancelling: %+v", r3)
	}
	if s := ask(sal, "sick", "2030-04-01", "2030-04-01"); s.Status != "pending" {
		t.Fatalf("sick leave approved itself: %+v", s)
	}
	if o := ask(other, "vacation", "2030-04-01", "2030-04-02"); o.Status != "pending" {
		t.Fatalf("somebody without the setting was auto approved: %+v", o)
	}
}
