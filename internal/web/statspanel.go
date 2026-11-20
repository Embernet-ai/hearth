package web

// Per person numbers on the dashboard.
//
// Each tile is one query over records Hearth already holds: time entries, tasks,
// deals. Nothing is typed in, estimated, or carried over from last week. Every
// tile names its period and links to the exact records it counts, because the
// filters in the link are the filters in the count: work comes from the same
// query compiler the document list uses, deals from the same list filters the
// deals page uses, hours from the week the time page opens on.
//
// Somebody sees their own. Somebody who holds everybody's time (a super admin,
// or the "everyone's time" grant) can pick a colleague, and then still sees only
// the tasks and deals their own access reaches, because those queries run as the
// viewer. A metric with no data behind it on this deployment (no sales pages, no
// permission to read hours) is left out, not shown empty.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/docs"
)

type personRef struct {
	ID   string
	Name string
}

// statTile is one number: what it is, its value, the period it covers, and the
// records behind it.
type statTile struct {
	Label  string
	Value  string
	Detail string
	Period string
	Href   string
	// Tone is "bad" when the number is something to act on, and empty
	// otherwise: a panel where everything is coloured has nothing to show.
	Tone string
	// Percent is Value against Of, set only when there is a target to measure
	// against.
	Percent    int
	HasPercent bool
	Of         string
	Note       string
}

type statGroup struct {
	Key   string
	Title string
	Tiles []statTile
}

// barRow is one bar of a small chart: a week of hours, or a pipeline stage.
type barRow struct {
	Label   string
	Value   string
	Percent int
	Href    string
	Current bool
}

type recentRow struct {
	Kind   string
	Title  string
	Detail string
	When   string
	Href   string
}

// statsPanel is everything the numbers section of the dashboard shows.
type statsPanel struct {
	Person personRef
	Self   bool
	// People is the picker, filled only for somebody who may look at others.
	People []personRef

	Groups []statGroup
	// Weeks is hours per week for the last eight, and WeeksTarget what a week
	// is measured against.
	Weeks       []barRow
	WeeksTarget string
	// Stages is the open pipeline by stage.
	Stages []barRow
	Recent []recentRow
}

func dayStr(t time.Time) string { return t.Format("2006-01-02") }

func rangeLabel(from, to time.Time) string {
	if from.Month() == to.Month() && from.Year() == to.Year() {
		return fmt.Sprintf("%d to %s", from.Day(), to.Format("2 Jan"))
	}
	return from.Format("2 Jan") + " to " + to.Format("2 Jan")
}

func percentOf(n, of int) int {
	if of <= 0 {
		return 0
	}
	return n * 100 / of
}

// sums renders a set of per-currency totals as one string, "$4,200 + EUR 900".
func sums(in []crm.Sum) string {
	if len(in) == 0 {
		return crm.Money(0, "USD")
	}
	parts := make([]string, len(in))
	for i, s := range in {
		parts[i] = s.Amount()
	}
	return strings.Join(parts, " + ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// statsFor builds the panel for the person asked about. It returns an HTTP
// status and message instead of a panel when the viewer may not ask.
func (s *Server) statsFor(ctx context.Context, id *auth.Identity, asked string) (*statsPanel, int, string) {
	me := id.PrincipalID
	person := me
	if asked != "" {
		person = asked
	}
	var held authz.Held
	if s.az != nil {
		if h, err := s.az.Capabilities(ctx, id.OrgID, me); err == nil {
			held = h
		}
	}
	seeAll := held.Has(authz.TimeAll)

	p := &statsPanel{Self: person == me, Person: personRef{ID: person, Name: id.DisplayName}}
	if seeAll && s.auth != nil {
		if list, err := s.auth.People(ctx, id.OrgID); err == nil {
			found := person == me
			for _, pe := range list {
				if pe.Status != "" && pe.Status != "active" {
					continue
				}
				p.People = append(p.People, personRef{ID: pe.PrincipalID, Name: pe.DisplayName})
				if pe.PrincipalID == person {
					p.Person.Name = pe.DisplayName
					found = true
				}
			}
			if !found {
				return nil, http.StatusNotFound, "there is nobody by that id here"
			}
		}
	}
	if person != me && !seeAll {
		return nil, http.StatusForbidden, "you can see your own numbers. Seeing a colleague's takes the everyone's time permission."
	}

	now := time.Now()
	today := startOfDay(now)
	weekFrom := startOfWeek(now)
	weekTo := weekFrom.AddDate(0, 0, 6)
	monthFrom := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	monthTo := monthFrom.AddDate(0, 1, -1)
	weekLabel := rangeLabel(weekFrom, weekTo)
	monthLabel := now.Format("January 2006")
	monthSoFar := rangeLabel(monthFrom, today)
	who := ""
	if person != me {
		who = "&person=" + url.QueryEscape(person)
	}

	// Hours.
	if s.time != nil {
		if h, err := s.time.PersonHours(ctx, id.OrgID, me, person, weekFrom, monthFrom, today); err != nil {
			s.log.WarnContext(ctx, "stats: hours", "error", err)
		} else {
			note := ""
			if !h.Set {
				note = "No weekly hours are set for this person, so the standard 40 hour week is used."
			}
			weekHref := "/time?from=" + dayStr(weekFrom) + who
			monthHref := "/time?from=" + dayStr(monthFrom) + "&span=month" + who
			g := statGroup{Key: "hours", Title: "Hours", Tiles: []statTile{
				{Label: "Logged this week", Value: hoursAndMinutes(h.Week), Period: weekLabel,
					Detail: plural(h.WeekEntries, "entry", "entries"), Href: weekHref,
					Of: hoursAndMinutes(h.WeekTarget), Percent: percentOf(h.Week, h.WeekTarget),
					HasPercent: h.WeekTarget > 0, Note: note},
				{Label: "Logged this month", Value: hoursAndMinutes(h.Month), Period: monthSoFar,
					Detail: plural(h.MonthEntries, "entry", "entries"), Href: monthHref,
					Of: hoursAndMinutes(h.MonthTarget), Percent: percentOf(h.Month, h.MonthTarget),
					HasPercent: h.MonthTarget > 0, Note: note},
				{Label: "Waiting for approval", Value: hoursAndMinutes(h.Waiting), Period: monthSoFar, Href: monthHref},
				{Label: "Sent back", Value: hoursAndMinutes(h.SentBack), Period: monthSoFar, Href: monthHref,
					Tone: toneIf(h.SentBack > 0)},
			}}
			p.Groups = append(p.Groups, g)
			peak := 0
			for _, w := range h.Weeks {
				peak = max(peak, w.Minutes)
			}
			peak = max(peak, h.WeekTarget)
			for i, w := range h.Weeks {
				p.Weeks = append(p.Weeks, barRow{
					Label: w.From.Format("2 Jan"), Value: hoursAndMinutes(w.Minutes),
					Percent: percentOf(w.Minutes, peak),
					Href:    "/time?from=" + dayStr(w.From) + who, Current: i == len(h.Weeks)-1})
			}
			p.WeeksTarget = hoursAndMinutes(h.WeekTarget)
		}
	}

	// Work, through the compiler the document list uses.
	if s.docs != nil {
		open := []string{docs.StatusTodo, docs.StatusInProgress, docs.StatusBlocked}
		mine := docs.Filter{Field: docs.Col("assignee"), Op: docs.OpIs, Values: []string{person}}
		isOpen := docs.Filter{Field: docs.Col("status"), Op: docs.OpAnyOf, Values: open}
		isDone := docs.Filter{Field: docs.Col("status"), Op: docs.OpIs, Values: []string{docs.StatusDone}}
		tile := func(label, period string, tone string, f ...docs.Filter) statTile {
			q := docs.Query{Filters: f}
			n, err := s.docs.Count(ctx, id.OrgID, id.Subjects, q)
			if err != nil {
				s.log.WarnContext(ctx, "stats: work", "error", err, "tile", label)
				return statTile{}
			}
			t := statTile{Label: label, Value: fmt.Sprint(n), Period: period,
				Href: "/documents?" + q.Encode().Encode()}
			if n > 0 {
				t.Tone = tone
			}
			return t
		}
		cut := func(t time.Time) string { return t.Add(-time.Second).UTC().Format(time.RFC3339) }
		end := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
		doneIn := func(from, to time.Time) []docs.Filter {
			return []docs.Filter{mine, isDone,
				{Field: docs.Col("completed"), Op: docs.OpAfter, Values: []string{cut(from)}},
				{Field: docs.Col("completed"), Op: docs.OpBefore, Values: []string{end(to.AddDate(0, 0, 1))}}}
		}
		next7 := today.AddDate(0, 0, 6)
		tiles := []statTile{
			tile("Open and assigned", "right now", "", mine, isOpen),
			tile("Overdue", "due before "+today.Format("2 Jan"), "bad", mine, isOpen,
				docs.Filter{Field: docs.Col("due"), Op: docs.OpBefore, Values: []string{dayStr(today)}}),
			tile("Due soon", rangeLabel(today, next7), "", mine, isOpen,
				docs.Filter{Field: docs.Col("due"), Op: docs.OpAfter, Values: []string{dayStr(today.AddDate(0, 0, -1))}},
				docs.Filter{Field: docs.Col("due"), Op: docs.OpBefore, Values: []string{dayStr(next7.AddDate(0, 0, 1))}}),
			tile("Completed this week", weekLabel, "", doneIn(weekFrom, weekTo)...),
			tile("Completed this month", monthLabel, "", doneIn(monthFrom, monthTo)...),
		}
		// Everyone files a daily report: how many of this month's working days have one.
		if entries, err := s.docs.Reports(ctx, id.OrgID, id.Subjects, docs.ReportFilter{
			From: docs.ReportDay(monthFrom), To: docs.ReportDay(now), PersonID: person}); err == nil {
			filed := map[string]bool{}
			for _, e := range entries {
				filed[e.Day] = true
			}
			days := workingDaysBetween(monthFrom, today)
			t := statTile{Label: "Daily reports filed", Value: fmt.Sprint(len(filed)), Period: monthSoFar,
				Href: "/reports/daily?from=" + docs.ReportDay(monthFrom) + "&to=" + docs.ReportDay(now) + "&person=" + url.QueryEscape(person)}
			if days > 0 {
				t.Of, t.Percent, t.HasPercent = fmt.Sprintf("%d working days", days), percentOf(len(filed), days), true
			}
			tiles = append(tiles, t)
		} else {
			s.log.WarnContext(ctx, "stats: daily reports", "error", err)
		}
		g := statGroup{Key: "work", Title: "Work"}
		for _, t := range tiles {
			if t.Label != "" {
				g.Tiles = append(g.Tiles, t)
			}
		}
		if len(g.Tiles) > 0 {
			p.Groups = append(p.Groups, g)
		}

		recent := docs.Query{
			Filters: []docs.Filter{mine, isDone,
				{Field: docs.Col("completed"), Op: docs.OpNotEmpty}},
			Sorts: []docs.Sort{{Field: docs.Col("completed"), Desc: true}},
			Limit: 5,
		}
		if rows, err := s.docs.Search(ctx, id.OrgID, id.Subjects, recent); err == nil {
			for _, r := range rows {
				when := ""
				if r.Task != nil && r.Task.CompletedAt != nil {
					when = r.Task.CompletedAt.In(time.Local).Format("2 Jan")
				}
				p.Recent = append(p.Recent, recentRow{Kind: "Task done", Title: r.Title, When: when,
					Href: "/documents/" + r.ID})
			}
		}
	}

	// Deals.
	if s.sales != nil {
		st, err := s.sales.PersonStats(ctx, id.OrgID, id.Subjects, person, today,
			monthFrom, monthFrom.AddDate(0, 1, 0))
		if err != nil {
			s.log.WarnContext(ctx, "stats: deals", "error", err)
		} else {
			own := "/deals?view=table&owner=" + url.QueryEscape(person)
			closed := own + "&closedfrom=" + dayStr(monthFrom) + "&closedto=" + dayStr(monthTo)
			g := statGroup{Key: "deals", Title: "Deals", Tiles: []statTile{
				{Label: "Open pipeline", Value: sums(st.Open), Detail: plural(st.OpenCount, "open deal", "open deals"),
					Period: "right now", Href: own + "&open=1"},
				{Label: "Past expected close", Value: fmt.Sprint(st.Overdue), Period: "open, close date before " + today.Format("2 Jan"),
					Href: own + "&overdue=1", Tone: toneIf(st.Overdue > 0)},
				{Label: "Won this month", Value: sums(st.Won), Detail: plural(st.WonCount, "deal", "deals"),
					Period: monthLabel, Href: closed + "&stage=won"},
				{Label: "Lost this month", Value: fmt.Sprint(st.LostCount), Detail: sums(st.Lost),
					Period: monthLabel, Href: closed + "&stage=lost"},
			}}
			p.Groups = append(p.Groups, g)
			peak := 0
			for _, r := range st.Stages {
				peak = max(peak, r.Count)
			}
			for _, r := range st.Stages {
				p.Stages = append(p.Stages, barRow{
					Label: r.Label, Value: fmt.Sprintf("%d, %s", r.Count, sums(r.Sums)),
					Percent: percentOf(r.Count, peak), Href: own + "&stage=" + r.Stage})
			}
			cf := today.AddDate(0, 0, -90)
			rows, _, err := s.sales.Deals(ctx, id.OrgID, id.Subjects, crm.ListQuery{
				Owner: person, ClosedFrom: &cf, Sort: "closed", Desc: true, Limit: 5})
			if err == nil {
				for _, d := range rows {
					if d.Stage != crm.StageWon && d.Stage != crm.StageLost {
						continue
					}
					p.Recent = append(p.Recent, recentRow{Kind: "Deal " + strings.ToLower(crm.StageLabel(d.Stage)),
						Title: d.Name, Detail: d.Amount(), Href: "/deals/" + d.ID})
				}
			}
		}
	}
	return p, 0, ""
}

// workingDaysBetween counts Monday to Friday from first to last, both included.
func workingDaysBetween(first, last time.Time) int {
	n := 0
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			n++
		}
	}
	return n
}

func toneIf(b bool) string {
	if b {
		return "bad"
	}
	return ""
}
