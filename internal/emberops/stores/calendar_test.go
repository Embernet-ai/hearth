package stores_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/calendar"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
)

type calPeople struct {
	spaces []string
	google []calendar.Entry
}

func (c calPeople) FeedOwner(_ context.Context, tok string) (string, string, string, error) {
	if tok != "good" {
		return "", "", "", calendar.ErrNoFeed
	}
	return org, ann, "Ann", nil
}
func (c calPeople) SpacesOf(context.Context, string) ([]string, error) { return c.spaces, nil }
func (c calPeople) Between(context.Context, string, string, time.Time, time.Time) ([]calendar.Entry, error) {
	return c.google, nil
}

func TestCalendarEventsDueWorkAndGoogle(t *testing.T) {
	e := newEnv(t)
	dept := "dddddddd-0000-0000-0000-000000000001"
	other := "dddddddd-0000-0000-0000-000000000002"
	e.f.Put("Hearth Space", map[string]any{"name": "HSP-1", "hearth_id": dept, "space_name": "Maintenance"})
	e.f.Put("Hearth Space", map[string]any{"name": "HSP-2", "hearth_id": other, "space_name": "Sales"})
	goog := calendar.Entry{ID: "google-1", Kind: calendar.KindGoogle, Title: "Dentist", StartsAt: time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC), EndsAt: time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)}
	s := &stores.Calendar{Base: e.base, Dir: e.dir, PG: calPeople{spaces: []string{dept}, google: []calendar.Entry{goog}}}
	ctx := e.as("ann-tok")

	d := time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)
	mine, err := s.Create(ctx, org, ann, calendar.Params{Title: " Standup ", StartsAt: d, EndsAt: d.Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := s.Create(ctx, org, ann, calendar.Params{Title: "Shutdown", StartsAt: d.Add(24 * time.Hour), EndsAt: d.Add(26 * time.Hour), SpaceID: dept})
	if _, err := s.Create(ctx, org, ann, calendar.Params{Title: "", StartsAt: d}); err == nil {
		t.Fatal("untitled event accepted")
	}
	evs := e.f.All("Event")
	if len(evs) != 2 {
		t.Fatalf("events = %v", evs)
	}
	for _, ev := range evs {
		switch ev["hearth_id"] {
		case mine.ID:
			if ev["event_type"] != "Private" || ev["subject"] != "Standup" || ev["starts_on"] != "2026-03-03 09:00:00" || ev["owner"] != "ann@example.com" {
				t.Fatalf("personal = %v", ev)
			}
		case shared.ID:
			if ev["event_type"] != "Public" || ev["hearth_space"] != "HSP-1" {
				t.Fatalf("department event = %v", ev)
			}
		}
	}
	// Someone else's personal event, and an event in a department ann is not in.
	e.f.Put("Event", map[string]any{"name": "EV-x", "hearth_id": "e1", "subject": "Bob's dentist", "owner": "bob@example.com", "event_type": "Private", "starts_on": "2026-03-03 10:00:00", "ends_on": "2026-03-03 11:00:00"})
	e.f.Put("Event", map[string]any{"name": "EV-y", "hearth_id": "e2", "subject": "Sales kickoff", "owner": "bob@example.com", "event_type": "Public", "hearth_space": "HSP-2", "starts_on": "2026-03-03 10:00:00", "ends_on": "2026-03-03 11:00:00"})
	// Work due: assigned to ann, open, in the window; and one that is done.
	e.f.Put("Task", map[string]any{"name": "T1", "hearth_id": taskID, "subject": "Fix belt", "status": "Open", "hearth_primary_assignee": "ann@example.com", "hearth_due_at": "2026-03-05 00:00:00"})
	e.f.Put("Task", map[string]any{"name": "T2", "hearth_id": "t2", "subject": "Old", "status": "Completed", "hearth_primary_assignee": "ann@example.com", "_assign": "[\"ann@example.com\"]", "hearth_due_at": "2026-03-05 00:00:00"})
	e.f.Put("Maintenance Work Order", map[string]any{"name": "W1", "hearth_id": woID, "title": "PM press", "status": "In Progress", "hearth_primary_assignee": "ann@example.com", "_assign": "[\"ann@example.com\"]", "planned_end": "2026-03-06 00:00:00"})

	before := len(e.f.Calls)
	got, err := s.Between(ctx, org, ann, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, g := range got {
		titles = append(titles, string(g.Kind)+":"+g.Title)
	}
	want := []string{"event:Standup", "event:Shutdown", "google:Dentist", "due:Fix belt", "due:PM press"}
	if len(got) != len(want) {
		t.Fatalf("calendar = %v, want %v", titles, want)
	}
	for _, w := range want {
		found := false
		for _, g := range titles {
			found = found || g == w
		}
		if !found {
			t.Fatalf("calendar = %v, missing %s", titles, w)
		}
	}
	for _, g := range got {
		if g.Title == "Shutdown" && g.OwnerName != "Maintenance" {
			t.Fatalf("department event = %+v", g)
		}
		if g.Kind == calendar.KindDue && (g.DocumentID == "" || !g.AllDay) {
			t.Fatalf("due = %+v", g)
		}
	}
	if n := len(e.f.Calls) - before; n > 6 {
		t.Fatalf("%d calls for a month: %v", n, e.f.Calls[before:])
	}

	// The feed reads the same, for the token's owner, with no one signed in.
	name, entries, err := s.Feed(e.as("svc-tok"), "good", time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC))
	if err != nil || name != "Ann" || len(entries) != len(want) {
		t.Fatalf("feed = %s %v %v", name, entries, err)
	}
	if _, _, err := s.Feed(ctx, "bad", time.Now()); !errors.Is(err, calendar.ErrNoFeed) {
		t.Fatalf("bad token: %v", err)
	}

	if err := s.Delete(ctx, org, ann, mine.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, org, ann, mine.ID); !errors.Is(err, calendar.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}
