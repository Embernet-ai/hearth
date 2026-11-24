package web

// The calendar, and the feed a phone subscribes to.
//
// The feed endpoint is the interesting one, and it is the reason this file has
// a handler registered outside the authenticated mux. A calendar application
// fetches a URL on a timer forever: it holds no cookie, it cannot be redirected
// to a sign-in page, and there is nobody sitting in front of it to type a
// password. So the URL carries a token and the handler resolves it itself.

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	cal "github.com/Embernet-ai/hearth/internal/calendar"
	"github.com/Embernet-ai/hearth/internal/plant"
)

// monthOf reads the month being viewed, defaulting to the one containing today.
func monthOf(r *http.Request) time.Time {
	now := time.Now()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	if v := r.URL.Query().Get("month"); v != "" {
		if d, err := time.ParseInLocation("2006-01", v, time.Local); err == nil {
			first = d
		}
	}
	return first
}

func (s *Server) getCalendar(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())

	from := monthOf(r)
	to := from.AddDate(0, 1, 0)

	entries, err := s.calendar.Between(r.Context(), id.OrgID, id.PrincipalID, from, to)
	if err != nil {
		s.log.ErrorContext(r.Context(), "calendar", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not read the calendar")
		return
	}

	// Whether they already have a feed. The token itself is never shown again,
	// because only its hash is stored, so the page offers to replace rather
	// than to reveal.
	hasFeed, feedMade, err := s.calendar.HasFeed(r.Context(), id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "calendar feed state", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not read your feed")
		return
	}

	// The departments an event can belong to. A failure here costs the picker,
	// not the page: somebody reading their month should not lose it because a
	// dropdown they were not going to use did not load.
	var departments []*plant.Space
	if s.plant != nil {
		if tree, err := s.plant.SpaceTree(r.Context(), id.OrgID, id.Subjects); err == nil {
			for _, sp := range tree {
				if sp.Depth == 0 {
					departments = append(departments, sp)
				}
			}
		}
	}

	s.render(w, r, "calendar", page{
		Title:       "Calendar",
		Entries:     entries,
		Month:       from,
		Days:        monthGrid(from, entries),
		PrevMonth:   from.AddDate(0, -1, 0).Format("2006-01"),
		NextMonth:   from.AddDate(0, 1, 0).Format("2006-01"),
		Departments: departments,
		HasFeed:     hasFeed,
		FeedMadeAt:  feedMade,
		// Set only by the handler that issues one, and only for that one
		// render. It is never read back from anywhere.
		FeedToken: strings.TrimSpace(r.URL.Query().Get("token")),
		BaseURL:   s.baseURL,
	})
}

// calendarDay is one cell of the month grid.
type calendarDay struct {
	Date    time.Time
	InMonth bool
	Today   bool
	Entries []cal.Entry
}

// monthGrid lays the month out as whole weeks, Sunday first.
//
// Built here rather than in the template because a template that does date
// arithmetic is a template nobody can test. The leading and trailing days
// belong to the neighbouring months and are marked, so the grid is rectangular
// and the eye is not asked to reflow it.
// Returned as whole weeks rather than a flat run of days, so the template
// ranges twice and needs no arithmetic at all. A flat list would need the
// template to work out where every seventh cell falls, which means a helper
// doing modulo in a template, which is the point at which the layout stops
// being reviewable.
func monthGrid(first time.Time, entries []cal.Entry) [][]calendarDay {
	start := first
	for start.Weekday() != time.Sunday {
		start = start.AddDate(0, 0, -1)
	}
	end := first.AddDate(0, 1, 0)
	for end.Weekday() != time.Sunday {
		end = end.AddDate(0, 0, 1)
	}

	today := time.Now()
	var weeks [][]calendarDay
	var week []calendarDay
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		day := calendarDay{
			Date:    d,
			InMonth: d.Month() == first.Month(),
			Today:   sameDay(d, today),
		}
		for _, e := range entries {
			// An event spans days, so a cell shows it when the day falls
			// anywhere inside it rather than only on the day it began.
			if !e.StartsAt.After(endOfDay(d)) && !e.EndsAt.Before(startOfDay(d)) {
				day.Entries = append(day.Entries, e)
			}
		}
		week = append(week, day)
		if len(week) == 7 {
			weeks = append(weeks, week)
			week = nil
		}
	}
	if len(week) > 0 {
		weeks = append(weeks, week)
	}
	return weeks
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
func endOfDay(t time.Time) time.Time {
	return startOfDay(t).AddDate(0, 0, 1).Add(-time.Nanosecond)
}

// sameDay lives in dashboard.go. Two of it would be two answers to the same
// question, which is exactly the sort of thing that drifts.

func (s *Server) postCalendarEvent(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	allDay := r.FormValue("all_day") != ""
	start, err := parseWhen(r.FormValue("starts"), allDay)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "that is not a date and time")
		return
	}
	end := start.Add(time.Hour)
	if v := strings.TrimSpace(r.FormValue("ends")); v != "" {
		if parsed, err := parseWhen(v, allDay); err == nil {
			end = parsed
		}
	} else if allDay {
		end = start
	}

	if _, err := s.calendar.Create(r.Context(), id.OrgID, id.PrincipalID, cal.Params{
		Title:    r.FormValue("title"),
		Notes:    r.FormValue("notes"),
		Place:    r.FormValue("location"),
		StartsAt: start, EndsAt: end, AllDay: allDay,
		SpaceID: strings.TrimSpace(r.FormValue("department")),
	}); err != nil {
		s.log.WarnContext(r.Context(), "create event refused", "error", err)
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "calendar: "))
		return
	}
	http.Redirect(w, r, "/calendar?month="+start.Format("2006-01"), http.StatusSeeOther)
}

// parseWhen accepts the two shapes the form produces: a date for an all-day
// event, and a local date and time for anything else.
func parseWhen(v string, allDay bool) (time.Time, error) {
	v = strings.TrimSpace(v)
	if allDay {
		return time.ParseInLocation("2006-01-02", v, time.Local)
	}
	// datetime-local, which sends no zone. Parsed in the server's zone, which
	// is the estate's, because the alternative is guessing at the browser's.
	return time.ParseInLocation("2006-01-02T15:04", v, time.Local)
}

func (s *Server) deleteCalendarEvent(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	err := s.calendar.Delete(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id"))
	switch {
	case errors.Is(err, cal.ErrNotFound):
		s.fail(w, r, http.StatusForbidden, "that is not yours to remove")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "delete event", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not remove it")
		return
	}
	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

// postCalendarFeed issues or rotates the subscribable URL.
//
// The token comes back in the redirect exactly once, because only its hash is
// stored and there is no way to show it again. Rotating is what revoking a
// leaked URL looks like: the previous one stops resolving immediately.
func (s *Server) postCalendarFeed(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	if r.FormValue("action") == "revoke" {
		if err := s.calendar.RevokeFeed(r.Context(), id.PrincipalID); err != nil {
			s.log.ErrorContext(r.Context(), "revoke feed", "error", err)
			s.fail(w, r, http.StatusInternalServerError, "could not revoke it")
			return
		}
		http.Redirect(w, r, "/calendar", http.StatusSeeOther)
		return
	}

	token, err := s.calendar.IssueFeed(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "issue feed", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not make a feed")
		return
	}
	http.Redirect(w, r, "/calendar?token="+token, http.StatusSeeOther)
}

// getCalendarFeed serves the .ics. No session, by necessity.
//
// Registered outside the authenticated mux, and it must stay there: a redirect
// to /login is not something a calendar application can follow, so putting this
// behind auth turns every subscription into a silent failure.
func (s *Server) getCalendarFeed(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSuffix(r.PathValue("token"), ".ics")

	name, entries, err := s.calendar.Feed(r.Context(), token, time.Now())
	switch {
	case errors.Is(err, cal.ErrNoFeed):
		// Flat 404 with no detail. A token is a credential, and telling the
		// holder of a wrong one whether they were close is telling them
		// something.
		http.NotFound(w, r)
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "calendar feed", "error", err)
		http.Error(w, "could not build the calendar", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="`+cal.FeedFilename(name)+`"`)
	// Never cached by anything in between. This URL carries a credential and
	// its content is somebody's movements.
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write([]byte(cal.ICS(name+" · Hearth", s.baseURL, entries, time.Now())))
}
