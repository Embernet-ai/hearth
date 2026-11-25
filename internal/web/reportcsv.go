package web

// The sales report as CSV, one table at a time: /crm/reports.csv?section=owners (the default), summary, stages, activity, lost, age, or
// created, over the same period and owner the page is showing.

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/crm"
)

func amtText(a []crm.Amt) string {
	out := ""
	for i, x := range a {
		if i > 0 {
			out += "; "
		}
		out += fmt.Sprintf("%s %.2f", x.Currency, float64(x.Cents)/100)
	}
	return out
}

func (s *Server) reportsCSV(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	now := time.Now()
	to := now
	if t := dayOf(r.URL.Query().Get("to")); t != nil {
		to = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, now.Location())
	}
	from := to.AddDate(0, 0, -29)
	if t := dayOf(r.URL.Query().Get("from")); t != nil {
		from = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, now.Location())
	}
	if from.After(to) {
		from, to = to, from
	}
	section := r.URL.Query().Get("section")
	switch section {
	case "", "owners", "summary", "stages", "activity", "lost", "age", "created":
	default:
		s.fail(w, r, http.StatusBadRequest, "no such section of the report")
		return
	}
	if section == "" {
		section = "owners"
	}
	rep, err := desk.SalesReport(r.Context(), id.OrgID, id.Subjects, from, to, ownerParam(r))
	if err != nil {
		s.log.ErrorContext(r.Context(), "sales report export", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not export the report")
		return
	}
	cw := s.csvHead(w, "sales-report-"+section)
	n := strconv.Itoa
	switch section {
	case "summary":
		_ = cw.Write([]string{"Period from", "Period to", "Open deals", "Open value", "Weighted", "Won", "Won value", "Lost", "Lost value", "Win rate percent", "Average won deal", "Days to win"})
		win := ""
		if rep.WinRate >= 0 {
			win = n(rep.WinRate)
		}
		_ = cw.Write([]string{from.Format("2006-01-02"), to.Format("2006-01-02"), n(rep.OpenCount), amtText(rep.Open), amtText(rep.Weighted), n(rep.WonCount),
			amtText(rep.Won), n(rep.LostCount), amtText(rep.Lost), win, amtText(rep.AvgWon), n(rep.AvgCycleDays)})
	case "stages":
		_ = cw.Write([]string{"Stage", "Chance percent", "Deals", "Value", "Weighted"})
		for _, st := range rep.Pipeline {
			_ = cw.Write([]string{st.Label, n(st.Probability), n(st.Count), amtText(st.Value), amtText(st.Weighted)})
		}
	case "activity":
		_ = cw.Write([]string{"Person", "Notes", "Calls", "Meetings", "Emails", "Total"})
		for _, a := range rep.Activity {
			_ = cw.Write([]string{safeCell(a.Name), n(a.Notes), n(a.Calls), n(a.Meetings), n(a.Emails), n(a.Total)})
		}
	case "lost":
		_ = cw.Write([]string{"Reason", "Deals", "Value"})
		for _, l := range rep.LostReasons {
			_ = cw.Write([]string{safeCell(l.Reason), n(l.Count), amtText(l.Value)})
		}
	case "age":
		_ = cw.Write([]string{"Stage", "Open deals", "Average days", "Oldest days"})
		for _, a := range rep.StageAge {
			_ = cw.Write([]string{a.Label, n(a.Count), n(a.AvgDays), n(a.OldestDays)})
		}
	case "created":
		_ = cw.Write([]string{"Month", "Deals made", "Value"})
		for _, m := range rep.Created {
			_ = cw.Write([]string{m.Month, n(m.Count), amtText(m.Value)})
		}
	default:
		_ = cw.Write([]string{"Person", "Open deals", "Open value", "Weighted", "Won", "Won value", "Lost"})
		for _, o := range rep.Owners {
			_ = cw.Write([]string{safeCell(o.Name), n(o.OpenCount), amtText(o.Open), amtText(o.Weighted), n(o.WonCount), amtText(o.Won), n(o.LostCount)})
		}
	}
	cw.Flush()
}
