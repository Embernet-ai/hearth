package stores_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
	"github.com/Embernet-ai/hearth/internal/timeoff"
)

// structure stands in for Hearth's approver/department tables: bob approves ann. It keeps
// the audit rows the store records, which the real one writes to Hearth's database.
type structure struct {
	dept  string
	audit *[]audit.Entry
}

func (s structure) Record(_ context.Context, e audit.Entry) error {
	if s.audit != nil {
		*s.audit = append(*s.audit, e)
	}
	return nil
}

func (structure) Decidable(_ context.Context, _, actor string, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = actor == bob && id == ann
	}
	return out, nil
}
func (structure) Deciders(context.Context, string, string) ([]string, error) {
	return []string{bob}, nil
}
func (s structure) DepartmentSpace(context.Context, string, string) (string, error) {
	return s.dept, nil
}

type notified struct {
	asked, decided, changed int
	last                    *timeoff.Request
	lastNote                string
}

func (n *notified) TimeOffChanged(_ context.Context, _, _ string, r *timeoff.Request, note string) {
	n.changed++
	n.last, n.lastNote = r, note
}

func (n *notified) TimeOffRequested(context.Context, string, *timeoff.Request, []string) { n.asked++ }
func (n *notified) TimeOffDecided(context.Context, string, string, *timeoff.Request)     { n.decided++ }

func TestTimeOffAskDecideCancel(t *testing.T) {
	e := newEnv(t)
	e.f.Put("Hearth Space", map[string]any{"name": "Ops", "hearth_id": "space-ops"})
	n := &notified{}
	s := &stores.TimeOff{Base: e.base, Dir: e.dir, PG: structure{dept: "space-ops"}, Notify: n}

	mon := time.Date(2030, 1, 7, 0, 0, 0, 0, time.UTC)
	r, err := s.Ask(e.as("ann-tok"), org, ann, timeoff.Params{StartsOn: mon, EndsOn: mon.AddDate(0, 0, 4), Kind: "vacation", Acknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "pending" || r.Days != 5 || r.RequesterID != ann {
		t.Fatalf("asked = %+v", r)
	}
	if n.asked != 1 {
		t.Fatalf("approver not told")
	}
	if _, err := s.Ask(e.ctx, org, ann, timeoff.Params{StartsOn: mon, EndsOn: mon, Kind: "vacation"}); !errors.Is(err, timeoff.ErrInvalid) {
		t.Fatalf("unacknowledged: %v", err)
	}

	// ann cannot decide her own; bob can.
	if err := s.Decide(e.as("ann-tok"), org, ann, r.ID, true, ""); !errors.Is(err, timeoff.ErrDenied) {
		t.Fatalf("self-approval: %v", err)
	}
	if c, _ := s.PendingCount(e.as("bob-tok"), org, bob); c != 1 {
		t.Fatalf("pending = %d", c)
	}
	if err := s.Decide(e.as("bob-tok"), org, bob, r.ID, true, "enjoy"); err != nil {
		t.Fatal(err)
	}
	mine, _ := s.Mine(e.as("ann-tok"), org, ann)
	if len(mine) != 1 || mine[0].Status != "approved" || mine[0].DecisionNote != "enjoy" {
		t.Fatalf("mine = %+v", mine)
	}
	ev := e.f.All("Event")
	if len(ev) != 1 || ev[0]["event_type"] != "Public" || ev[0]["hearth_space"] != "Ops" {
		t.Fatalf("calendar event = %v", ev)
	}
	if n.decided != 1 {
		t.Fatal("requester not told")
	}
	if err := s.Decide(e.as("bob-tok"), org, bob, r.ID, false, ""); !errors.Is(err, timeoff.ErrDenied) {
		t.Fatalf("decided twice: %v", err)
	}

	if err := s.Cancel(e.as("ann-tok"), org, ann, r.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.f.All("Event")) != 0 {
		t.Fatal("Out stayed on the calendar after cancelling")
	}
	if err := s.Cancel(e.as("bob-tok"), org, bob, r.ID); !errors.Is(err, timeoff.ErrDenied) {
		t.Fatalf("bob cancelled ann's: %v", err)
	}
}

const carl = "33333333-3333-3333-3333-333333333333"

// approved files a request for ann on Monday 7 January 2030 to Friday the 11th and has bob approve it.
func approved(t *testing.T, e *env, s *stores.TimeOff) *timeoff.Request {
	t.Helper()
	mon := time.Date(2030, 1, 7, 0, 0, 0, 0, time.UTC)
	r, err := s.Ask(e.as("ann-tok"), org, ann, timeoff.Params{StartsOn: mon, EndsOn: mon.AddDate(0, 0, 4), Kind: "vacation", Reason: "trip", Acknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Decide(e.as("bob-tok"), org, bob, r.ID, true, "enjoy"); err != nil {
		t.Fatal(err)
	}
	return r
}

func ptr[T any](v T) *T { return &v }

func TestTimeOffChangedByTheApproverOnTheOwnDoctype(t *testing.T) {
	e := newEnv(t)
	e.f.Put("Hearth Space", map[string]any{"name": "Ops", "hearth_id": "space-ops"})
	var rows []audit.Entry
	n := &notified{}
	s := &stores.TimeOff{Base: e.base, Dir: e.dir, PG: structure{dept: "space-ops", audit: &rows}, Notify: n}
	r := approved(t, e, s)
	tue := time.Date(2030, 1, 8, 0, 0, 0, 0, time.UTC)

	// Only somebody who may decide: not the person, not a stranger.
	for who, id := range map[string]string{"ann-tok": ann, "bob-tok": carl} {
		if _, err := s.Change(e.as(who), org, id, r.ID, timeoff.Change{Kind: ptr("sick")}); !errors.Is(err, timeoff.ErrDenied) {
			t.Fatalf("%s changed ann's request: %v", id, err)
		}
		if err := s.CancelFor(e.as(who), org, id, r.ID, ""); !errors.Is(err, timeoff.ErrDenied) {
			t.Fatalf("%s cancelled ann's request: %v", id, err)
		}
	}
	if len(rows) != 0 || n.changed != 0 {
		t.Fatalf("a refused change left %d audit rows and %d notices", len(rows), n.changed)
	}
	// Nothing different is not a change.
	if _, err := s.Change(e.as("bob-tok"), org, bob, r.ID, timeoff.Change{Kind: ptr("vacation")}); !errors.Is(err, timeoff.ErrInvalid) {
		t.Fatalf("no change: %v", err)
	}

	got, err := s.Change(e.as("bob-tok"), org, bob, r.ID, timeoff.Change{
		StartsOn: &tue, EndsOn: ptr(tue.AddDate(0, 0, 1)), Kind: ptr("sick"), HalfDay: ptr(true), Comments: ptr("moved"), Note: "covering a shift",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != r.ID || got.Status != "approved" || got.Kind != "sick" || got.Days != 2 || !got.HalfDay || got.Length() != 1.5 || got.Comments != "moved" {
		t.Fatalf("changed = %+v", got)
	}
	if got.DeciderName != "Bob" || got.DecisionNote != "covering a shift" {
		t.Fatalf("decided by %q with %q", got.DeciderName, got.DecisionNote)
	}
	mine, _ := s.Mine(e.as("ann-tok"), org, ann)
	if len(mine) != 1 || mine[0].StartsOn != tue || mine[0].Status != "approved" || !mine[0].HalfDay {
		t.Fatalf("ann's view = %+v", mine)
	}
	ev := e.f.All("Event")
	if len(ev) != 1 || ev[0]["starts_on"] != "2030-01-08 00:00:00" || ev[0]["ends_on"] != "2030-01-10 00:00:00" {
		t.Fatalf("the Out did not move: %v", ev)
	}
	if n.changed != 1 || n.last.RequesterID != ann || n.lastNote != "covering a shift" {
		t.Fatalf("ann was told %d times: %+v %q", n.changed, n.last, n.lastNote)
	}
	if len(rows) != 1 || rows[0].Action != audit.ActionTimeOffChange || rows[0].ActorID != bob || rows[0].EntityID != r.ID {
		t.Fatalf("audit = %+v", rows)
	}
	after := rows[0].After.(timeoff.Snapshot)
	before := rows[0].Before.(timeoff.Snapshot)
	if before.StartsOn != "2030-01-07" || after.StartsOn != "2030-01-08" || before.Kind != "vacation" || after.Kind != "sick" || after.Note != "covering a shift" || !after.CommentsChanged {
		t.Fatalf("before %+v after %+v", before, after)
	}

	if err := s.CancelFor(e.as("bob-tok"), org, bob, r.ID, "the trip is off"); err != nil {
		t.Fatal(err)
	}
	mine, _ = s.Mine(e.as("ann-tok"), org, ann)
	if mine[0].Status != "cancelled" || mine[0].DecisionNote != "Cancelled by Bob: the trip is off" {
		t.Fatalf("after cancel: %+v", mine[0])
	}
	if len(e.f.All("Event")) != 0 {
		t.Fatal("the Out stayed on the calendar after the request was cancelled")
	}
	if len(rows) != 2 || rows[1].Action != audit.ActionTimeOffCancel || n.changed != 2 || n.last.Status != "cancelled" {
		t.Fatalf("audit %d, told %d, last %+v", len(rows), n.changed, n.last)
	}
	// Finished is finished.
	if _, err := s.Change(e.as("bob-tok"), org, bob, r.ID, timeoff.Change{Kind: ptr("vacation")}); !errors.Is(err, timeoff.ErrDenied) {
		t.Fatalf("changed a cancelled request: %v", err)
	}
	if err := s.CancelFor(e.as("bob-tok"), org, bob, r.ID, ""); !errors.Is(err, timeoff.ErrDenied) {
		t.Fatalf("cancelled twice: %v", err)
	}
}

func TestPendingTimeOffIsChangedWhereItIs(t *testing.T) {
	e := newEnv(t)
	s := &stores.TimeOff{Base: e.base, Dir: e.dir, PG: structure{}}
	mon := time.Date(2030, 1, 7, 0, 0, 0, 0, time.UTC)
	r, err := s.Ask(e.as("ann-tok"), org, ann, timeoff.Params{StartsOn: mon, EndsOn: mon.AddDate(0, 0, 4), Kind: "vacation", Acknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Change(e.as("bob-tok"), org, bob, r.ID, timeoff.Change{EndsOn: ptr(mon.AddDate(0, 0, 1))})
	if err != nil || got.ID != r.ID || got.Status != "pending" || got.Days != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if len(e.f.All("Hearth Time Off Request")) != 1 || len(e.f.All("Event")) != 0 {
		t.Fatal("a pending change filed something else")
	}
	if _, err := s.Change(e.as("bob-tok"), org, bob, r.ID, timeoff.Change{StartsOn: ptr(mon.AddDate(0, 0, 5)), EndsOn: ptr(mon.AddDate(0, 0, 6))}); !errors.Is(err, timeoff.ErrInvalid) {
		t.Fatalf("a weekend: %v", err)
	}
	if err := s.CancelFor(e.as("bob-tok"), org, bob, r.ID, ""); err != nil {
		t.Fatal(err)
	}
	// The ones to decide include a cancelled-by-approver one only while it is recent; what matters
	// here is that an approver also sees upcoming approved requests however long ago they were decided.
}

func TestApprovedLeaveApplicationIsCancelledAndFiledAgain(t *testing.T) {
	e := newEnv(t)
	e.f.HRMS = true
	e.f.Put("Hearth Space", map[string]any{"name": "Ops", "hearth_id": "space-ops"})
	var rows []audit.Entry
	n := &notified{}
	s := &stores.TimeOff{Base: e.base, Dir: e.dir, PG: structure{dept: "space-ops", audit: &rows}, Notify: n}
	r := approved(t, e, s)
	tue := time.Date(2030, 1, 8, 0, 0, 0, 0, time.UTC)

	got, err := s.Change(e.as("bob-tok"), org, bob, r.ID, timeoff.Change{StartsOn: &tue, EndsOn: ptr(tue.AddDate(0, 0, 1)), HalfDay: ptr(true), Note: "covering"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == r.ID || got.Status != "approved" || got.StartsOn != tue || !got.HalfDay || got.DeciderName != "Bob" {
		t.Fatalf("refiled = %+v", got)
	}
	docs := e.f.All("Hearth Time Off Request")
	if len(docs) != 2 {
		t.Fatalf("EmberOPS holds %d applications, want the cancelled one and the new one", len(docs))
	}
	var old, fresh map[string]any
	for _, d := range docs {
		if d["hearth_id"] == r.ID {
			old = d
		} else {
			fresh = d
		}
	}
	if old["status"] != "cancelled" || fresh["status"] != "approved" || fresh["starts_on"] != "2030-01-08" {
		t.Fatalf("old %v new %v", old, fresh)
	}
	if ev := e.f.All("Event"); len(ev) != 1 || ev[0]["starts_on"] != "2030-01-08 00:00:00" {
		t.Fatalf("calendar = %v", ev)
	}
	mine, _ := s.Mine(e.as("ann-tok"), org, ann)
	statuses := map[string]string{}
	for _, m := range mine {
		statuses[m.ID] = m.Status
	}
	if len(mine) != 2 || statuses[r.ID] != "cancelled" || statuses[got.ID] != "approved" {
		t.Fatalf("ann sees %v", statuses)
	}
	if len(rows) != 1 || rows[0].After.(timeoff.Snapshot).FiledAs != got.ID || n.changed != 1 || n.last.ID != got.ID {
		t.Fatalf("audit %+v, told %d", rows, n.changed)
	}

	// A decided Leave Application cannot be edited in place; this is the rule the change works round.
	if _, err := e.c.Upsert(context.Background(), "Hearth Time Off Request", got.ID, map[string]any{"starts_on": "2030-02-04"}); err == nil {
		t.Fatal("the fake lets a decided application change, so this test proves nothing")
	}

	// Cancelling it keeps it on the list, cancelled.
	if err := s.CancelFor(e.as("bob-tok"), org, bob, got.ID, ""); err != nil {
		t.Fatal(err)
	}
	if len(e.f.All("Hearth Time Off Request")) != 2 || len(e.f.All("Event")) != 0 {
		t.Fatal("cancelling should leave both applications, cancelled, and no Out")
	}
}

func TestARefusedChangeFilesTheOriginalAgain(t *testing.T) {
	e := newEnv(t)
	e.f.HRMS = true
	s := &stores.TimeOff{Base: e.base, Dir: e.dir, PG: structure{}}
	r := approved(t, e, s)
	tue := time.Date(2030, 1, 8, 0, 0, 0, 0, time.UTC)
	e.f.RejectNew = func(_ string, vals map[string]any) string {
		if vals["starts_on"] == "2030-01-08" {
			return "overlaps another leave application"
		}
		return ""
	}
	if _, err := s.Change(e.as("bob-tok"), org, bob, r.ID, timeoff.Change{StartsOn: &tue, EndsOn: ptr(tue.AddDate(0, 0, 1))}); !errors.Is(err, timeoff.ErrInvalid) {
		t.Fatalf("refused change: %v", err)
	}
	mine, _ := s.Mine(e.as("ann-tok"), org, ann)
	live := 0
	for _, m := range mine {
		if m.Status == "approved" {
			live++
			if m.StartsOn != time.Date(2030, 1, 7, 0, 0, 0, 0, time.UTC) || m.Days != 5 {
				t.Fatalf("restored request = %+v", m)
			}
		}
	}
	if live != 1 {
		t.Fatalf("ann has %d approved requests after a refused change, want her original", live)
	}
	if len(e.f.All("Event")) != 1 {
		t.Fatal("the Out is gone after a refused change")
	}
}

func TestApproversSeeUpcomingApprovedRequests(t *testing.T) {
	e := newEnv(t)
	s := &stores.TimeOff{Base: e.base, Dir: e.dir, PG: structure{}}
	r := approved(t, e, s)
	// Decided long ago, still to come: it is on bob's list so it can be changed.
	e.f.Put("Hearth Time Off Request", map[string]any{"name": "X", "hearth_id": r.ID, "decided_at": "2020-01-01 00:00:00"})
	list, err := s.ToDecide(e.as("bob-tok"), org, bob)
	if err != nil || len(list) != 1 || list[0].ID != r.ID {
		t.Fatalf("%+v %v", list, err)
	}
}
