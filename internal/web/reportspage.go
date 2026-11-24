package web

// Billing's own report, separate from Workload. Workload is a planning tool
// for whoever schedules the week; this is what turns logged hours into an
// invoice, and the two are gated on different capabilities on purpose -- see
// timelog.TimeReport.
//
// Two views behind one route. The default is the whole-org roll-up -- who
// logged what, before anybody reads a single entry -- because a flat list of
// every entry from every person is not a report, it is a dump. Clicking a
// name drills into that one person's entries via ?person=, which is also the
// page somebody wants when they only care about their own team lead's week.

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// hoursAndMinutes formats a duration the way billing needs to read it. Pay is
// per hour, not per minute, so the column billing actually does math against
// is "1h 30m", not a raw integer somebody has to divide by 60 themselves.
// Same wording as the "hours" template func, kept separate because a CSV or
// xlsx cell is not a template context.
func hoursAndMinutes(m int) string {
	switch {
	case m <= 0:
		return "0m"
	case m < 60:
		return fmt.Sprintf("%dm", m)
	case m%60 == 0:
		return fmt.Sprintf("%dh", m/60)
	default:
		return fmt.Sprintf("%dh %dm", m/60, m%60)
	}
}

// writeXLSX is what a CSV cannot be: a real grid, with a header row that
// actually looks like one and a total row that is visibly not just another
// entry. Same data every CSV branch already writes, addressed to somebody
// who is going to build an invoice from it rather than pipe it into a script.
func writeXLSX(w http.ResponseWriter, filename string, headers []string, rows [][]string, totalLabel, totalValue string) error {
	f := excelize.NewFile()
	defer f.Close()
	const sheet = "Time report"
	if err := f.SetSheetName("Sheet1", sheet); err != nil {
		return err
	}

	headerStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"2F5597"}},
		Alignment: &excelize.Alignment{
			Vertical: "center",
		},
	})
	if err != nil {
		return err
	}
	totalStyle, err := f.NewStyle(&excelize.Style{
		Font:   &excelize.Font{Bold: true},
		Border: []excelize.Border{{Type: "top", Color: "000000", Style: 2}},
	})
	if err != nil {
		return err
	}

	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellValue(sheet, cell, h); err != nil {
			return err
		}
	}
	if last, _ := excelize.CoordinatesToCellName(len(headers), 1); last != "" {
		if err := f.SetCellStyle(sheet, "A1", last, headerStyle); err != nil {
			return err
		}
	}

	for r, row := range rows {
		for c, v := range row {
			cell, _ := excelize.CoordinatesToCellName(c+1, r+2)
			if err := f.SetCellValue(sheet, cell, v); err != nil {
				return err
			}
		}
	}

	// One blank row before the total, so it reads as a summary rather than
	// one more entry somebody has to notice is different.
	totalRow := len(rows) + 3
	labelCell, _ := excelize.CoordinatesToCellName(1, totalRow)
	valueCell, _ := excelize.CoordinatesToCellName(2, totalRow)
	if err := f.SetCellValue(sheet, labelCell, totalLabel); err != nil {
		return err
	}
	if err := f.SetCellValue(sheet, valueCell, totalValue); err != nil {
		return err
	}
	lastCell, _ := excelize.CoordinatesToCellName(max(len(headers), 2), totalRow)
	if err := f.SetCellStyle(sheet, labelCell, lastCell, totalStyle); err != nil {
		return err
	}

	// Wide enough to read a full name or a task title without Excel's own
	// "###" overflow marker, which is a worse first impression than a plain
	// CSV would have given.
	for i := range headers {
		col, _ := excelize.ColumnNumberToName(i + 1)
		if err := f.SetColWidth(sheet, col, col, 22); err != nil {
			return err
		}
	}
	_ = f.SetPanes(sheet, &excelize.Panes{
		Freeze: true, Split: false, XSplit: 0, YSplit: 1,
		TopLeftCell: "A2", ActivePane: "bottomLeft",
	})

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(filename)+`.xlsx"`)
	return f.Write(w)
}

func (s *Server) getTimeReport(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())

	// The calendar month to date by default -- the window a billing run
	// actually covers, unlike Workload's Monday-start week.
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	if v := r.URL.Query().Get("from"); v != "" {
		if d, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
			from = d
		}
	}
	to := from.AddDate(0, 1, 0)
	if v := r.URL.Query().Get("to"); v != "" {
		if d, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
			// The field the reader sees is inclusive of its last day; the query
			// below is half-open, so it needs to reach one day past it.
			to = d.AddDate(0, 0, 1)
		}
	}
	reportFrom := from.Format("2006-01-02")
	reportTo := to.AddDate(0, 0, -1).Format("2006-01-02")
	format := r.URL.Query().Get("format")
	person := r.URL.Query().Get("person")
	stamp := time.Now().Format("2006-01-02")

	fail := func(err error) bool {
		switch {
		case errors.Is(err, timelog.ErrNotAllowed):
			// Same discipline as Workload: told plainly this page is not
			// theirs, not shown an empty table that reads as "nobody logged
			// anything".
			s.fail(w, r, http.StatusForbidden,
				"Time reports are a billing tool, and that is not something your account can see.")
			return true
		case err != nil:
			s.log.ErrorContext(r.Context(), "time report", "error", err)
			s.fail(w, r, http.StatusInternalServerError, "could not build the report")
			return true
		}
		return false
	}

	// One line per request for the report, so what somebody asked for and what came back can be read from the log afterwards:
	// who, the dates, which shape (totals, one person, details), the format and how many people were chosen.
	picked, all := 0, r.URL.Query().Get("all") == "1"
	if ids, ok := detailPeople(r); ok && !all {
		picked = len(ids)
	}
	s.log.InfoContext(r.Context(), "time report", "principal", id.PrincipalID, "from", reportFrom, "to", reportTo,
		"detail", r.URL.Query().Get("detail") == "1", "format", format, "person", person, "people", picked, "all", all)

	// Details for a set of people, in one file. A separate choice from the totals
	// and the per-person export below, which are untouched.
	if r.URL.Query().Get("detail") == "1" && (format == "csv" || format == "xlsx") {
		s.timeDetailExport(w, r, format, from, to)
		return
	}

	if person == "" {
		totals, err := s.time.TimeReportSummary(r.Context(), id.OrgID, id.PrincipalID, from, to)
		if fail(err) {
			return
		}
		grand := 0
		for _, t := range totals {
			grand += t.Minutes
		}

		switch format {
		case "xlsx":
			rows := make([][]string, 0, len(totals))
			for _, t := range totals {
				rows = append(rows, []string{t.PersonName, hoursAndMinutes(t.Minutes)})
			}
			if err := writeXLSX(w, "whole org time "+stamp, []string{"Person", "Hours"},
				rows, "Total", hoursAndMinutes(grand)); err != nil {
				s.log.ErrorContext(r.Context(), "write xlsx", "error", err)
			}
			return
		case "csv":
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="`+
				safeFilename("whole org time "+stamp)+`.csv"`)
			cw := csv.NewWriter(w)
			_ = cw.Write([]string{"person", "hours"})
			for _, t := range totals {
				_ = cw.Write([]string{t.PersonName, hoursAndMinutes(t.Minutes)})
			}
			_ = cw.Write([]string{})
			_ = cw.Write([]string{"total", hoursAndMinutes(grand)})
			cw.Flush()
			return
		}

		s.render(w, r, "timereport", page{
			Title: "Time report", Totals: totals, ReportTotal: grand,
			ReportPeople: s.reportPeople(r.Context(), id.OrgID, totals),
			ReportNotice: r.URL.Query().Get("notice"),
			ReportFrom:   reportFrom, ReportTo: reportTo,
		})
		return
	}

	rows, err := s.time.TimeReport(r.Context(), id.OrgID, id.PrincipalID, person, from, to)
	if fail(err) {
		return
	}
	total := 0
	name := ""
	for _, row := range rows {
		total += row.Minutes
		name = row.PersonName
	}
	who := name
	if who == "" {
		who = "person"
	}

	switch format {
	case "xlsx":
		out := make([][]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, []string{
				row.StartedAt.Format("2006-01-02"), hoursAndMinutes(row.Minutes),
				row.SpaceName, row.DocTitle, strings.Join(row.Tags, "; "), row.Note,
			})
		}
		if err := writeXLSX(w, who+" time "+stamp,
			[]string{"Date", "Hours", "Space", "Work", "Tags", "Note"},
			out, "Total", hoursAndMinutes(total)); err != nil {
			s.log.ErrorContext(r.Context(), "write xlsx", "error", err)
		}
		return
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+
			safeFilename(who+" time "+stamp)+`.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"date", "hours", "space", "work", "tags", "note"})
		for _, row := range rows {
			_ = cw.Write([]string{
				row.StartedAt.Format("2006-01-02"), hoursAndMinutes(row.Minutes),
				row.SpaceName, row.DocTitle, strings.Join(row.Tags, "; "), row.Note,
			})
		}
		_ = cw.Write([]string{})
		_ = cw.Write([]string{"total", hoursAndMinutes(total)})
		cw.Flush()
		return
	}

	s.render(w, r, "timereport", page{
		Title: "Time report", Report: rows, ReportTotal: total,
		ReportFrom: reportFrom, ReportTo: reportTo,
		ReportPerson: person, ReportPersonName: name,
	})
}

// reportPeople is who the details export can be made for: every active person, in the order of their time in the range and
// then by name, with their minutes (0 for somebody who logged none). Only people with time in the range used to be offered,
// so a range with no time in it offered nobody and the panel was not there at all. The report is Finance's, and a
// Finance person already sees everybody's totals, so listing the rest of the people shows them nothing new.
func (s *Server) reportPeople(ctx context.Context, orgID string, totals []timelog.ReportTotal) []timelog.ReportTotal {
	out := append([]timelog.ReportTotal{}, totals...)
	have := map[string]bool{}
	for _, t := range totals {
		have[t.PrincipalID] = true
	}
	if s.directory != nil {
		if list, err := s.directory.People(ctx, orgID); err == nil {
			for _, p := range list {
				if (p.Status != "" && p.Status != "active") || have[p.PrincipalID] {
					continue
				}
				out = append(out, timelog.ReportTotal{PrincipalID: p.PrincipalID, PersonName: p.DisplayName})
			}
		} else {
			s.log.WarnContext(ctx, "time report: list people", "error", err)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Minutes != out[j].Minutes {
			return out[i].Minutes > out[j].Minutes
		}
		return strings.ToLower(out[i].PersonName) < strings.ToLower(out[j].PersonName)
	})
	return out
}
