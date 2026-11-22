package stores

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/connect"
	"github.com/Embernet-ai/hearth/internal/emberops"
)

// ConnectEvents is the calendar sync's event store when EmberOPS holds the records. Events a person makes in Hearth are
// EmberOPS Events (Calendar above), so the sync has to read, send and change those. Events that arrive from a provider are
// made in Hearth's own table, the same place the calendar already reads Google events from (Calendar.Between), so they
// never need a person's EmberOPS permission to be written while nobody is signed in.
//
// The sync runs in the background as the service client, with no signed-in person. Reading Events as the service client is
// what the subscribed feed already does; changing one keeps its owner, so a person's event stays theirs.
type ConnectEvents struct {
	connect.EventStore // Hearth's own table: where events from a provider are made.
	Base
	Dir Directory
}

var _ connect.EventStore = (*ConnectEvents)(nil)

func (s *ConnectEvents) fromRow(r emberops.Row) *connect.LocalEvent {
	return &connect.LocalEvent{ID: idOf(r), Title: r.S("subject"), Notes: r.S("description"), Place: r.S("hearth_location"),
		Start: r.T("starts_on", s.loc()).UTC(), End: r.T("ends_on", s.loc()).UTC(), AllDay: r.B("all_day")}
}

// opsEvent finds an EmberOPS Event by the id Hearth gave it.
func (s *ConnectEvents) opsEvent(ctx context.Context, id string) (*emberops.Row, error) {
	rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Event", Fields: eventFields,
		Filters: []emberops.Filter{{Field: "hearth_id", Op: "=", Value: id}}}, 1)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// Load reads an event from Hearth's table, then from EmberOPS.
func (s *ConnectEvents) Load(ctx context.Context, orgID, id string) (*connect.LocalEvent, error) {
	if e, err := s.EventStore.Load(ctx, orgID, id); err != nil || e != nil {
		return e, err
	}
	r, err := s.opsEvent(ctx, id)
	if err != nil || r == nil {
		return nil, err
	}
	return s.fromRow(*r), nil
}

// Candidates adds the person's own EmberOPS events (or the department's) in the window to Hearth's own.
func (s *ConnectEvents) Candidates(ctx context.Context, orgID, principalID, space string, from, to time.Time) ([]string, error) {
	ids, err := s.EventStore.Candidates(ctx, orgID, principalID, space, from, to)
	if err != nil {
		return nil, err
	}
	filters := []emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"},
		{Field: "starts_on", Op: ">=", Value: s.tsv(from)}, {Field: "starts_on", Op: "<", Value: s.tsv(to)}}
	if space == "" {
		emails, err := s.Dir.Emails(ctx)
		if err != nil {
			return nil, err
		}
		me := emails[principalID]
		if me == "" {
			return ids, nil
		}
		filters = append(filters, emberops.Filter{Field: "owner", Op: "=", Value: me}, emberops.Filter{Field: "hearth_space", Op: "is", Value: "not set"})
	} else {
		spaces, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Hearth Space", Fields: []string{"name"}, Limit: 1, Reference: true,
			Filters: []emberops.Filter{{Field: "hearth_id", Op: "=", Value: space}}})
		if err != nil {
			return nil, err
		}
		if len(spaces) == 0 {
			return ids, nil
		}
		filters = append(filters, emberops.Filter{Field: "hearth_space", Op: "=", Value: spaces[0].S("name")})
	}
	rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Event", Fields: eventFields, Filters: filters}, 2000)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		ids = append(ids, idOf(r))
	}
	return ids, nil
}

// Update changes an event wherever it lives.
func (s *ConnectEvents) Update(ctx context.Context, orgID, id string, e connect.LocalEvent) error {
	if cur, err := s.EventStore.Load(ctx, orgID, id); err != nil {
		return err
	} else if cur != nil {
		return s.EventStore.Update(ctx, orgID, id, e)
	}
	allDay := 0
	if e.AllDay {
		allDay = 1
	}
	_, err := s.C.Upsert(ctx, "Event", id, map[string]any{"subject": strings.TrimSpace(e.Title), "description": e.Notes,
		"hearth_location": e.Place, "starts_on": s.tsv(e.Start), "ends_on": s.tsv(e.End), "all_day": allDay})
	return err
}

// Delete removes an event wherever it lives. One that is already gone is not an error.
func (s *ConnectEvents) Delete(ctx context.Context, orgID, id string) error {
	if cur, err := s.EventStore.Load(ctx, orgID, id); err != nil {
		return err
	} else if cur != nil {
		return s.EventStore.Delete(ctx, orgID, id)
	}
	if err := s.C.DeleteByHearthID(ctx, "Event", id); err != nil && !errors.Is(err, emberops.ErrNotFound) {
		return err
	}
	return nil
}
