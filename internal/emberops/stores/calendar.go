package stores

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/calendar"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
)

// CalendarPeople is what Hearth knows about whose calendar is whose: which
// departments a person is in or manages, who a feed token belongs to, and the
// Google events cached for them. *calendar.Service provides it.
type CalendarPeople interface {
	FeedOwner(ctx context.Context, token string) (orgID, principalID, name string, err error)
	SpacesOf(ctx context.Context, principalID string) ([]string, error)
	Between(ctx context.Context, orgID, principalID string, from, to time.Time) ([]calendar.Entry, error)
}

var _ CalendarPeople = (*calendar.Service)(nil)

// Calendar keeps events in EmberOPS (Event). A personal event is Private and
// owned by the person; a department's is Public with its hearth_space. The
// work due on someone's calendar is read from their Tasks and Work Orders in
// EmberOPS. Feed tokens, and Google events cached for a person, stay in
// Hearth (Google is not in the addon, and a token is Hearth's own credential).
//
// Which department events a person sees is Hearth's rule (the departments they
// are in or manage, read through PG), so the page and the feed agree. Deleting
// is the person's own EmberOPS permission: they can remove an event they own;
// a department manager removing someone else's event is refused unless
// EmberOPS lets them.
type Calendar struct {
	calendar.Store
	Base
	Dir Directory
	PG  CalendarPeople
}

var _ calendar.Store = (*Calendar)(nil)

var eventFields = []string{"name", "hearth_id", "subject", "description", "hearth_location", "starts_on", "ends_on",
	"all_day", "event_type", "hearth_space", "owner"}

func (s *Calendar) Create(ctx context.Context, orgID, actorID string, p calendar.Params) (*calendar.Entry, error) {
	if strings.TrimSpace(p.Title) == "" {
		return nil, errors.New("calendar: an event needs a title")
	}
	if p.StartsAt.IsZero() {
		return nil, errors.New("calendar: an event needs a start")
	}
	if p.EndsAt.Before(p.StartsAt) {
		return nil, errors.New("calendar: it cannot finish before it starts")
	}
	end := p.EndsAt
	if end.IsZero() {
		end = p.StartsAt.Add(time.Hour)
	}
	e := &calendar.Entry{ID: uuid.NewString(), Kind: calendar.KindEvent, Title: strings.TrimSpace(p.Title),
		Notes: strings.TrimSpace(p.Notes), Place: strings.TrimSpace(p.Place), StartsAt: p.StartsAt, EndsAt: end, AllDay: p.AllDay}
	rec := mapping.Rec{"id": e.ID, "title": e.Title, "notes": e.Notes, "location": e.Place,
		"starts_at": e.StartsAt.UTC().Format(time.RFC3339), "ends_at": e.EndsAt.UTC().Format(time.RFC3339), "all_day": e.AllDay}
	if p.SpaceID == "" {
		rec["principal_id"] = actorID // personal
	} else {
		rec["space_id"] = p.SpaceID
	}
	doc := mapping.TZ{Loc: s.loc()}.EventDoc(rec)
	delete(doc, "doctype")
	delete(doc, "hearth_id")
	delete(doc, "owner") // the person writing it owns it
	if _, err := s.C.Upsert(ctx, "Event", e.ID, doc); err != nil {
		return nil, err
	}
	return e, nil
}

func (s *Calendar) Delete(ctx context.Context, orgID, actorID, id string) error {
	err := s.C.DeleteByHearthID(ctx, "Event", id)
	if errors.Is(err, emberops.ErrNotFound) || isForbidden(err) {
		// Not an EmberOPS event: it may be one a connector brought into Hearth's own table.
		if s.Store != nil {
			if perr := s.Store.Delete(ctx, orgID, actorID, id); perr == nil {
				return nil
			}
		}
		return calendar.ErrNotFound
	}
	return err
}

// Between is a person's own events, their departments' events, the work
// assigned to them that falls due in the window, and their Google events.
func (s *Calendar) Between(ctx context.Context, orgID, principalID string, from, to time.Time) ([]calendar.Entry, error) {
	if to.Before(from) {
		from, to = to, from
	}
	emails, err := s.Dir.Emails(ctx)
	if err != nil {
		return nil, err
	}
	me := emails[principalID]
	spaces, err := s.PG.SpacesOf(ctx, principalID)
	if err != nil {
		return nil, err
	}
	var out []calendar.Entry

	// Events: owned by them, or in one of their departments.
	var spaceNames []any
	nameOf := map[string]string{} // Frappe name -> space name
	nameID := map[string]string{} // Frappe name -> hearth id
	if len(spaces) > 0 {
		var ids []any
		for _, id := range spaces {
			ids = append(ids, id)
		}
		rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Hearth Space", Fields: []string{"name", "hearth_id", "space_name"}, Limit: 500, Reference: true,
			Filters: []emberops.Filter{{Field: "hearth_id", Op: "in", Value: ids}}})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			spaceNames = append(spaceNames, r.S("name"))
			nameOf[r.S("name")], nameID[r.S("name")] = r.S("space_name"), r.S("hearth_id")
		}
	}
	var or []emberops.Filter
	if me != "" {
		or = append(or, emberops.Filter{Field: "owner", Op: "=", Value: me})
	}
	if len(spaceNames) > 0 {
		or = append(or, emberops.Filter{Field: "hearth_space", Op: "in", Value: spaceNames})
	}
	if len(or) > 0 {
		evs, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Event", Fields: eventFields, OrderBy: "starts_on asc",
			Filters:   []emberops.Filter{{Field: "starts_on", Op: "<", Value: s.tsv(to)}, {Field: "ends_on", Op: ">=", Value: s.tsv(from)}},
			OrFilters: or}, 2000)
		if err != nil {
			return nil, err
		}
		for _, r := range evs {
			sp := r.S("hearth_space")
			if sp != "" {
				if _, mine := nameOf[sp]; !mine {
					continue // a department they do not belong to
				}
			} else if r.S("owner") != me {
				continue
			}
			out = append(out, calendar.Entry{ID: idOf(r), Kind: calendar.KindEvent, Title: r.S("subject"), Notes: r.S("description"),
				Place: r.S("hearth_location"), StartsAt: r.T("starts_on", s.loc()), EndsAt: r.T("ends_on", s.loc()), AllDay: r.B("all_day"),
				OwnerName: nameOf[sp]})
		}
	}

	// Work due, assigned to them and not finished: two reads, tasks and work orders.
	if me != "" {
		for _, q := range []struct{ dt, title, due string }{{"Task", "subject", "hearth_due_at"}, {"Maintenance Work Order", "title", "planned_end"}} {
			rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: q.dt, Fields: []string{"hearth_id", q.title, q.due}, OrderBy: q.due + " asc",
				Filters: []emberops.Filter{emberops.F("hearth_primary_assignee", me),
					{Field: "status", Op: "not in", Value: []any{"Completed", "Cancelled"}},
					{Field: "hearth_deleted_at", Op: "is", Value: "not set"},
					{Field: q.due, Op: ">=", Value: s.tsv(from)}, {Field: q.due, Op: "<", Value: s.tsv(to)}}}, 2000)
			if err != nil {
				return nil, err
			}
			for _, r := range rows {
				title := r.S(q.title)
				if title == "" {
					title = "Untitled"
				}
				at := r.T(q.due, s.loc())
				out = append(out, calendar.Entry{ID: "due-" + r.S("hearth_id"), Kind: calendar.KindDue, Title: title, AllDay: true,
					StartsAt: at, EndsAt: at, DocumentID: r.S("hearth_id")})
			}
		}
	}

	// Google events are cached in Hearth, and so are the events the Microsoft 365 and Google Workspace connectors bring in
	// (ConnectEvents), which Hearth keeps in its own table.
	if all, err := s.PG.Between(ctx, orgID, principalID, from, to); err == nil {
		// An event Hearth made is in EmberOps and, from before the move (and from time off approved while both were
		// written), in Hearth's own table too. The same event twice read as two rows, "Out: Jeff Ryan" twice on one
		// day, so a Postgres copy is dropped when EmberOps already gave the same title, window and all-day flag.
		seen := map[string]bool{}
		key := func(e calendar.Entry) string {
			return fmt.Sprintf("%s|%d|%d|%t", strings.ToLower(strings.TrimSpace(e.Title)), e.StartsAt.Unix(), e.EndsAt.Unix(), e.AllDay)
		}
		for _, e := range out {
			if e.Kind == calendar.KindEvent {
				seen[key(e)] = true
			}
		}
		for _, e := range all {
			if e.Kind == calendar.KindGoogle || e.Kind == calendar.KindEvent {
				if e.Kind == calendar.KindEvent && seen[key(e)] {
					continue
				}
				out = append(out, e)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartsAt.Before(out[j].StartsAt) })
	return out, nil
}

// Feed answers a subscribed calendar. The token is Hearth's; the events are
// read as the service client, because nobody is signed in, and limited to the
// token owner's by Between.
func (s *Calendar) Feed(ctx context.Context, token string, now time.Time) (string, []calendar.Entry, error) {
	orgID, principalID, name, err := s.PG.FeedOwner(ctx, token)
	if err != nil {
		return "", nil, err
	}
	entries, err := s.Between(ctx, orgID, principalID, now.AddDate(0, -1, 0), now.AddDate(1, 0, 0))
	if err != nil {
		return "", nil, err
	}
	return name, entries, nil
}
