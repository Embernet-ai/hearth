package web

// The page somebody lands on.
//
// Until now signing in redirected to /documents?kind=work_order: a filtered
// list, chosen because it was the first thing built rather than because it
// answers anything. The question a person actually arrives with is "what needs
// me today", and a list of every work order in the company does not answer it.
//
// Four regions, each answering one question:
//
//	mine      what is assigned to me, and what is due this week
//	numbers   hours, work and deals, per person, each linking to its records
//	activity  what has my department and my channels been saying
//	watching  what moved on the things I asked to be told about
//
// Every region is built from the query compiler and the services that already
// exist. Nothing here writes new aggregate SQL: a dashboard with its own
// hand-rolled counting is a dashboard that disagrees with the list it links to,
// and the first time those two numbers differ nobody trusts either again.

import (
	"context"
	"net/http"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	cal "github.com/Embernet-ai/hearth/internal/calendar"
	"github.com/Embernet-ai/hearth/internal/docs"
)

// dashboard is everything the landing page shows.
type dashboard struct {
	// ReportDay is today in the office, and ReportID the signed-in person's
	// daily report for it, empty until they file one.
	ReportDay string
	ReportID  string

	// Mine is what is assigned to this person and still open.
	Mine []*docs.Row
	// DueThisWeek is the same set narrowed to the next seven days, which is
	// the horizon somebody plans in.
	DueThisWeek []*docs.Row
	// Week is seven days of what is due, for the strip that expands.
	Week []dashDay

	// Stats is the numbers section: one person's hours, work and deals.
	Stats *statsPanel

	// Watching is recent movement on documents this person follows.
	Watching []*docs.Row

	// Upcoming is what is actually on: meetings, leave, shifts, and the work
	// falling due, for the next fortnight.
	//
	// A fortnight rather than the seven days the strip above covers, because
	// the question "is there anything I have forgotten" reaches further than
	// the question "what is due". Long enough to catch next week's shutdown,
	// short enough that it is still a list rather than a calendar.
	Upcoming []cal.Entry

	// Month is the full calendar, and WeekTitle, PrevWeek and NextWeek name the strip's week and the days either side.
	Month     *dashMonth
	WeekTitle string
	PrevWeek  string
	NextWeek  string
	Today     string
}

// dashMonth is the month calendar: six weeks at most, Sunday first, each day linking to its own week.
type dashMonth struct {
	Title string
	Prev  string
	Next  string
	Names []string
	Weeks [][]monthCell
}

type monthCell struct {
	Day      int
	InMonth  bool
	Today    bool
	Selected bool
	Weekend  bool
	Count    int
	Href     string
	Titles   []string
}

// dashDay is one column of the week strip.
type dashDay struct {
	Date    time.Time
	Label   string
	Weekday string
	Today   bool
	// Selected is the day the person picked on the month calendar (or today), Weekend is Saturday or Sunday, and
	// Href is the address that selects this day.
	Selected bool
	Weekend  bool
	Href     string
	Count    int
	Items    []*docs.Row
	// Events is what is on that day as opposed to what is due. The strip
	// answered only "what is due", which meant a shutdown window or somebody's
	// leave was invisible on the one page everybody opens.
	Events []cal.Entry
}

func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		// A form's own address is not a page; send the person to the page it belongs to rather than a 404.
		if to, ok := postOnlyParent(s.authed, r.Host, r.URL); ok {
			http.Redirect(w, r, to, http.StatusSeeOther)
			return
		}
		s.fail(w, r, http.StatusNotFound, "no such page")
		return
	}
	id := auth.From(r.Context())
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	d := s.buildDashboard(r.Context(), id, pickedDay(r.URL.Query().Get("day")))
	stats, status, why := s.statsFor(r.Context(), id, r.URL.Query().Get("person"))
	if stats == nil {
		s.fail(w, r, status, why)
		return
	}
	d.Stats = stats
	s.render(w, r, "dashboard", page{Title: "Dashboard", Dash: d})
}

// buildDashboard gathers the regions.
//
// Each one degrades to empty rather than failing the page. A dashboard that
// will not render because one count timed out is worse than one that renders
// with a gap: the other three regions still answer their question.
func (s *Server) buildDashboard(ctx context.Context, id *auth.Identity, picked time.Time) *dashboard {
	d := &dashboard{}
	me := id.PrincipalID

	d.ReportDay = docs.ReportDay(time.Now())
	if rid, err := s.docs.ReportOn(ctx, id.OrgID, me, d.ReportDay); err == nil {
		d.ReportID = rid
	}

	open := []string{docs.StatusTodo, docs.StatusInProgress, docs.StatusBlocked}

	// Assigned to me and not finished.
	mine := docs.Query{
		Filters: []docs.Filter{
			{Field: docs.Col("assignee"), Op: docs.OpIs, Values: []string{me}},
			{Field: docs.Col("status"), Op: docs.OpAnyOf, Values: open},
		},
		Sorts: []docs.Sort{{Field: docs.Col("due"), Desc: false}},
		Limit: 50,
	}
	if rows, err := s.docs.Search(ctx, id.OrgID, id.Subjects, mine); err == nil {
		d.Mine = rows
	}

	// The week strip always runs Sunday to Saturday around the picked day (today until somebody picks another on
	// the month calendar), and every day carries what is on it and what is due, so nothing hides behind a click.
	today := startOfDay(time.Now())
	d.Week = sundayWeek(picked, today, d.Mine)
	for _, day := range d.Week {
		d.DueThisWeek = append(d.DueThisWeek, day.Items...)
	}
	d.Today = dayLink(today)
	d.WeekTitle = rangeLabel(d.Week[0].Date, d.Week[6].Date)
	d.PrevWeek = dayLink(d.Week[0].Date.AddDate(0, 0, -7))
	d.NextWeek = dayLink(d.Week[0].Date.AddDate(0, 0, 7))
	d.Month = &dashMonth{Names: []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}}

	// The calendar, on the page everybody actually opens. One read covers the month grid, the week strip (a week
	// can straddle two months) and the fortnight list; degrades to nothing, like every other region here.
	gridFrom := sundayOf(time.Date(picked.Year(), picked.Month(), 1, 0, 0, 0, 0, time.Local))
	gridTo := sundayOf(time.Date(picked.Year(), picked.Month()+1, 0, 0, 0, 0, 0, time.Local)).AddDate(0, 0, 7)
	from, to := gridFrom, gridTo
	if d.Week[0].Date.Before(from) {
		from = d.Week[0].Date
	}
	if end := d.Week[6].Date.AddDate(0, 0, 1); end.After(to) {
		to = end
	}
	if t14 := today.AddDate(0, 0, 14); t14.After(to) {
		to = t14
	}
	if today.Before(from) {
		from = today
	}
	var entries []cal.Entry
	if s.calendar != nil {
		if got, err := s.calendar.Between(ctx, id.OrgID, me, from, to); err == nil {
			entries = got
			for _, e := range entries {
				if e.EndsAt.After(today) && e.StartsAt.Before(today.AddDate(0, 0, 14)) {
					d.Upcoming = append(d.Upcoming, e)
				}
			}
			for i := range d.Week {
				for _, e := range entries {
					// Work already appears in the strip through Items, so only real events go here. Showing a
					// due date twice on one day is how somebody concludes they have two jobs.
					if e.Kind != cal.KindEvent && e.Kind != cal.KindGoogle {
						continue
					}
					if eventOn(e, d.Week[i].Date) {
						d.Week[i].Events = append(d.Week[i].Events, e)
					}
				}
				d.Week[i].Count += len(d.Week[i].Events)
			}
		}
	}
	d.Month = buildMonth(picked, today, entries, d.Mine)

	// Things I follow. The watch table already holds 5,000+ rows imported from
	// ClickUp, so this has content on the first morning rather than after
	// somebody has clicked follow on things for a week.
	watching := docs.Query{
		Sorts: []docs.Sort{{Field: docs.Col("updated"), Desc: true}},
		Limit: 12,
	}
	if rows, err := s.docs.SearchWatched(ctx, id.OrgID, id.Subjects, me, watching); err == nil {
		d.Watching = rows
	}
	return d
}

// pickedDay reads the day chosen on the month calendar, today when it is missing or not a date.
func pickedDay(v string) time.Time {
	if t, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
		return t
	}
	return startOfDay(time.Now())
}

func dayLink(t time.Time) string { return "/?day=" + t.Format("2006-01-02") }

// sundayOf is the Sunday on or before t, at midnight.
func sundayOf(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
	return d.AddDate(0, 0, -int(d.Weekday()))
}

// eventOn reports whether an event touches a calendar day. An all-day event ends at the next midnight, so the
// end is exclusive for it.
func eventOn(e cal.Entry, day time.Time) bool {
	start, end := startOfDay(day), startOfDay(day).AddDate(0, 0, 1)
	if e.AllDay || !e.EndsAt.After(e.StartsAt) {
		return e.StartsAt.Before(end) && (e.EndsAt.After(start) || sameDay(e.StartsAt, day))
	}
	return e.StartsAt.Before(end) && e.EndsAt.After(start)
}

// sundayWeek is the seven days, Sunday first, of the week holding picked, with the work due on each. Built from rows
// already fetched rather than seven more queries: the strip is a different arrangement of "what is assigned to me",
// not a different question, and asking the database again would let the two disagree.
func sundayWeek(picked, today time.Time, rows []*docs.Row) []dashDay {
	start := sundayOf(picked)
	days := make([]dashDay, 7)
	for i := range days {
		day := start.AddDate(0, 0, i)
		days[i] = dashDay{
			Date: day, Label: day.Format("2 Jan"), Weekday: day.Format("Mon"),
			Today: sameDay(day, today), Selected: sameDay(day, picked), Href: dayLink(day),
			Weekend: day.Weekday() == time.Saturday || day.Weekday() == time.Sunday,
		}
	}
	for _, row := range rows {
		if row.Task == nil || row.Task.DueAt == nil {
			continue
		}
		// The due date's own calendar day (it is stored as midnight UTC), placed on the same day of the local strip.
		du := row.Task.DueAt.UTC()
		due := time.Date(du.Year(), du.Month(), du.Day(), 0, 0, 0, 0, time.Local)
		for i := range days {
			if sameDay(due, days[i].Date) {
				days[i].Items = append(days[i].Items, row)
				days[i].Count++
				break
			}
		}
	}
	return days
}

// buildMonth lays out the month of picked as weeks that start on Sunday, each day with a count and its first
// titles, and a link that selects it.
func buildMonth(picked, today time.Time, entries []cal.Entry, mine []*docs.Row) *dashMonth {
	first := time.Date(picked.Year(), picked.Month(), 1, 0, 0, 0, 0, time.Local)
	m := &dashMonth{
		Title: picked.Format("January 2006"),
		Prev:  dayLink(first.AddDate(0, -1, 0)), Next: dayLink(first.AddDate(0, 1, 0)),
		Names: []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
	}
	last := time.Date(picked.Year(), picked.Month()+1, 0, 0, 0, 0, 0, time.Local)
	for w := sundayOf(first); !w.After(last); w = w.AddDate(0, 0, 7) {
		var week []monthCell
		for i := 0; i < 7; i++ {
			day := w.AddDate(0, 0, i)
			c := monthCell{Day: day.Day(), InMonth: day.Month() == picked.Month(), Today: sameDay(day, today),
				Selected: sameDay(day, picked), Weekend: i == 0 || i == 6, Href: dayLink(day)}
			for _, e := range entries {
				if e.Kind == cal.KindDue {
					continue
				}
				if eventOn(e, day) {
					c.Count++
					if len(c.Titles) < 3 {
						c.Titles = append(c.Titles, e.Title)
					}
				}
			}
			for _, row := range mine {
				if row.Task == nil || row.Task.DueAt == nil {
					continue
				}
				du := row.Task.DueAt.UTC()
				if sameDay(time.Date(du.Year(), du.Month(), du.Day(), 0, 0, 0, 0, time.Local), day) {
					c.Count++
					if len(c.Titles) < 3 {
						c.Titles = append(c.Titles, row.Title)
					}
				}
			}
			week = append(week, c)
		}
		m.Weeks = append(m.Weeks, week)
	}
	return m
}

func sameDay(a, b time.Time) bool {
	y1, m1, d1 := a.Date()
	y2, m2, d2 := b.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}
