package stores_test

import (
	"context"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/calendar"
	"github.com/Embernet-ai/hearth/internal/connect"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
)

// pgEvents is Hearth's own events table, as the composite store sees it.
type pgEvents struct {
	rows    map[string]connect.LocalEvent
	deleted []string
}

func (p *pgEvents) Load(_ context.Context, _, id string) (*connect.LocalEvent, error) {
	if e, ok := p.rows[id]; ok {
		return &e, nil
	}
	return nil, nil
}
func (p *pgEvents) Candidates(context.Context, string, string, string, time.Time, time.Time) ([]string, error) {
	var ids []string
	for id := range p.rows {
		ids = append(ids, id)
	}
	return ids, nil
}
func (p *pgEvents) Create(_ context.Context, _, _, _ string, e connect.LocalEvent) (string, error) {
	id := "11111111-aaaa-0000-0000-000000000001"
	p.rows[id] = e
	return id, nil
}
func (p *pgEvents) Update(_ context.Context, _, id string, e connect.LocalEvent) error {
	p.rows[id] = e
	return nil
}
func (p *pgEvents) Delete(_ context.Context, _, id string) error {
	delete(p.rows, id)
	p.deleted = append(p.deleted, id)
	return nil
}

// With EmberOPS holding the records, the calendar sync still finds, sends, changes and removes the events a person made in
// Hearth, which live in EmberOPS, and keeps what a provider brings in in Hearth's own table.
func TestConnectEventsReadAndChangeEmberOPSEvents(t *testing.T) {
	e := newEnv(t)
	dept := "dddddddd-0000-0000-0000-000000000001"
	e.f.Put("Hearth Space", map[string]any{"name": "HSP-1", "hearth_id": dept, "space_name": "Maintenance"})
	pg := &pgEvents{rows: map[string]connect.LocalEvent{"pg-1": {ID: "pg-1", Title: "From Google"}}}
	s := &stores.ConnectEvents{EventStore: pg, Base: e.base, Dir: e.dir}
	cal := &stores.Calendar{Base: e.base, Dir: e.dir, PG: calPeople{}}
	ctx := e.as("ann-tok")
	d := time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)
	mine, err := cal.Create(ctx, org, ann, calendar.Params{Title: "Standup", StartsAt: d, EndsAt: d.Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := cal.Create(ctx, org, ann, calendar.Params{Title: "Shutdown", StartsAt: d.Add(24 * time.Hour), EndsAt: d.Add(26 * time.Hour), SpaceID: dept})
	if err != nil {
		t.Fatal(err)
	}
	// Someone else's event, and an EmberOPS event Hearth never made (no hearth_id).
	e.f.Put("Event", map[string]any{"name": "EV-bob", "hearth_id": "b1", "subject": "Bob's", "owner": "bob@example.com", "event_type": "Private", "starts_on": "2026-03-03 10:00:00", "ends_on": "2026-03-03 11:00:00"})
	e.f.Put("Event", map[string]any{"name": "EV-native", "subject": "Made in EmberOPS", "owner": "ann@example.com", "event_type": "Private", "starts_on": "2026-03-03 12:00:00", "ends_on": "2026-03-03 13:00:00"})
	svc := context.Background() // the background sync has nobody signed in
	from, to := d.Add(-time.Hour), d.Add(7*24*time.Hour)

	// Load: an EmberOPS event, one in Hearth's table, and one that is nowhere.
	got, err := s.Load(svc, org, mine.ID)
	if err != nil || got == nil || got.Title != "Standup" || !got.Start.Equal(d) || got.AllDay {
		t.Fatalf("load EmberOPS event = %+v, %v", got, err)
	}
	if got, _ := s.Load(svc, org, "pg-1"); got == nil || got.Title != "From Google" {
		t.Fatalf("load Hearth event = %+v", got)
	}
	if got, err := s.Load(svc, org, "nowhere"); err != nil || got != nil {
		t.Fatalf("load missing = %+v, %v", got, err)
	}

	// Candidates for a person's own calendar: their Hearth-made EmberOPS event and Hearth's own rows, not Bob's, not a department's,
	// not one Hearth never made.
	ids, err := s.Candidates(svc, org, ann, "", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if !has(ids, mine.ID) || !has(ids, "pg-1") || has(ids, "b1") || has(ids, shared.ID) || len(ids) != 2 {
		t.Fatalf("own candidates = %v", ids)
	}
	// And for the department's calendar.
	ids, err = s.Candidates(svc, org, ann, dept, from, to)
	if err != nil || !has(ids, shared.ID) || has(ids, mine.ID) {
		t.Fatalf("department candidates = %v, %v", ids, err)
	}

	// Update an EmberOPS event as the provider holds it; the owner stays Ann's. An event in Hearth's table is updated there.
	if err := s.Update(svc, org, mine.ID, connect.LocalEvent{Title: "Standup (moved)", Notes: "n", Place: "Bay 2", Start: d.Add(time.Hour), End: d.Add(2 * time.Hour), AllDay: false}); err != nil {
		t.Fatal(err)
	}
	for _, ev := range e.f.All("Event") {
		if ev["hearth_id"] == mine.ID && (ev["subject"] != "Standup (moved)" || ev["hearth_location"] != "Bay 2" || ev["starts_on"] != "2026-03-03 10:00:00" || ev["owner"] != "ann@example.com") {
			t.Fatalf("updated event = %v", ev)
		}
	}
	if err := s.Update(svc, org, "pg-1", connect.LocalEvent{Title: "Renamed"}); err != nil || pg.rows["pg-1"].Title != "Renamed" {
		t.Fatalf("update Hearth row: %v %+v", err, pg.rows["pg-1"])
	}

	// Delete: an EmberOPS event goes from EmberOPS, one in Hearth's table from there, one already gone is no error.
	if err := s.Delete(svc, org, mine.ID); err != nil {
		t.Fatal(err)
	}
	for _, ev := range e.f.All("Event") {
		if ev["hearth_id"] == mine.ID {
			t.Fatal("EmberOPS event still there")
		}
	}
	if err := s.Delete(svc, org, "pg-1"); err != nil || len(pg.deleted) != 1 {
		t.Fatalf("delete Hearth row: %v %v", err, pg.deleted)
	}
	if err := s.Delete(svc, org, "nowhere"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
}

func has(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// fakePGCalendar is Hearth's own calendar store, for the events a connector brought in.
type fakePGCalendar struct {
	calendar.Store
	deleted []string
}

func (f *fakePGCalendar) Delete(_ context.Context, _, _, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

// An event a connector brought into Hearth's own table shows on the calendar, and deleting it from the calendar reaches that
// table when EmberOPS has no such event.
func TestCalendarShowsAndDeletesEventsFromConnectors(t *testing.T) {
	e := newEnv(t)
	d := time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)
	synced := calendar.Entry{ID: "pg-9", Kind: calendar.KindEvent, Title: "Synced from Outlook", StartsAt: d, EndsAt: d.Add(time.Hour)}
	pg := &fakePGCalendar{}
	s := &stores.Calendar{Store: pg, Base: e.base, Dir: e.dir, PG: calPeople{google: []calendar.Entry{synced}}}
	got, err := s.Between(e.as("ann-tok"), org, ann, d.Add(-24*time.Hour), d.Add(24*time.Hour))
	if err != nil || len(got) != 1 || got[0].Title != "Synced from Outlook" {
		t.Fatalf("between = %+v, %v", got, err)
	}
	if err := s.Delete(e.as("ann-tok"), org, ann, "pg-9"); err != nil || len(pg.deleted) != 1 || pg.deleted[0] != "pg-9" {
		t.Fatalf("delete = %v %v", err, pg.deleted)
	}
}

// A calendar sync started from the page ("Sync now") carries the signed-in person, and EmberOPS gives a person no delete on an
// Event, so an event deleted at the provider stayed in Hearth. The sync removes it as the service identity, and a personal event
// (Private, owned by the person) goes too.
func TestConnectEventsDeleteWhenThePersonHasNoDelete(t *testing.T) {
	e := newEnv(t)
	e.f.PersonCannotDeleteEvents = true
	pg := &pgEvents{rows: map[string]connect.LocalEvent{}}
	s := &stores.ConnectEvents{EventStore: pg, Base: e.base, Dir: e.dir}
	cal := &stores.Calendar{Base: e.base, Dir: e.dir, PG: calPeople{}}
	d := time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)
	mine, err := cal.Create(e.as("ann-tok"), org, ann, calendar.Params{Title: "Made in Hearth", StartsAt: d, EndsAt: d.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(e.as("ann-tok"), org, mine.ID); err != nil {
		t.Fatalf("delete as the signed-in person: %v", err)
	}
	if got, _ := s.Load(context.Background(), org, mine.ID); got != nil {
		t.Fatalf("the event is still there: %+v", got)
	}
}
