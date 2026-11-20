package stores_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/emberops/stores"
	"github.com/Embernet-ai/hearth/internal/timeoff"
)

// structure stands in for Hearth's approver/department tables: bob approves ann.
type structure struct{ dept string }

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

type notified struct{ asked, decided int }

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
