package web

// The time report's details export: every entry, for the people somebody picked,
// in one file.
//
// The totals export gives one line per person, and the per-person export gives
// one person's entries, so a billing run that needed six people's details meant
// six downloads and a paste. This is the third shape: GET /reports/time with
// detail=1, people=<id> repeated (or all=1), the usual from and to, and format
// csv or xlsx. Every row carries the person's name and id, so the file sorts and
// filters by person, and there is no total line in it for the same reason: a
// total in the middle of a column is the first thing a pivot trips on.
//
// The gate is the report's own. The store refuses anybody without Finance before
// it reads a row, so a refusal is a plain 403 and not half a file. The CSV is
// written as the rows come off the database and flushed as it goes, so a year of
// everybody's time starts downloading at once and never sits whole in memory.

import (
	"encoding/csv"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// detailHeaders are the columns of the details file. date, hours, space, work,
// tags and note are the per-person export's own columns, in its order and under
// its names; the person and the entry's identity are added around them.
var detailHeaders = []string{"employee", "employee_id", "date", "start", "hours", "minutes", "space", "work", "tags", "note", "entry_id"}

func detailRow(row timelog.ReportRow) []string {
	return []string{
		row.PersonName, row.PrincipalID,
		row.StartedAt.Format("2006-01-02"), row.StartedAt.Format("15:04"),
		hoursAndMinutes(row.Minutes), strconv.Itoa(row.Minutes),
		row.SpaceName, row.DocTitle, strings.Join(row.Tags, "; "), row.Note, row.ID,
	}
}

// detailPeople reads who was picked. all=1 means everybody, which the store takes
// as an empty list; otherwise at least one id is needed.
func detailPeople(r *http.Request) (ids []string, ok bool) {
	q := r.URL.Query()
	if q.Get("all") == "1" {
		return nil, true
	}
	seen := map[string]bool{}
	for _, v := range q["people"] {
		for _, id := range strings.Split(v, ",") {
			if id = strings.TrimSpace(id); id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids, len(ids) > 0
}

func (s *Server) timeDetailExport(w http.ResponseWriter, r *http.Request, format string, from, to time.Time) {
	id := auth.From(r.Context())
	people, ok := detailPeople(r)
	if !ok {
		s.fail(w, r, http.StatusBadRequest, "Choose at least one person to export, or choose everybody.")
		return
	}
	name := "time details " + from.Format("2006-01-02") + " to " + to.AddDate(0, 0, -1).Format("2006-01-02")

	refuse := func(err error) {
		switch {
		case errors.Is(err, timelog.ErrNotAllowed):
			s.fail(w, r, http.StatusForbidden,
				"Time reports are a billing tool, and that is not something your account can see.")
		default:
			s.log.ErrorContext(r.Context(), "time details export", "error", err)
			s.fail(w, r, http.StatusInternalServerError, "could not build the export")
		}
	}

	if format == "xlsx" {
		// A workbook is built as a stream into the library's own temporary storage and
		// sent when it is whole; the rows themselves are never held as a slice.
		f := excelize.NewFile()
		defer f.Close()
		const sheet = "Time details"
		if err := f.SetSheetName("Sheet1", sheet); err != nil {
			refuse(err)
			return
		}
		sw, err := f.NewStreamWriter(sheet)
		if err != nil {
			refuse(err)
			return
		}
		head := make([]any, len(detailHeaders))
		for i, h := range detailHeaders {
			head[i] = h
		}
		if err := sw.SetRow("A1", head); err != nil {
			refuse(err)
			return
		}
		for i := range detailHeaders {
			_ = sw.SetColWidth(i+1, i+1, 22)
		}
		n := 1
		err = s.time.TimeReportRows(r.Context(), id.OrgID, id.PrincipalID, people, from, to, func(row timelog.ReportRow) error {
			n++
			cells := detailRow(row)
			out := make([]any, len(cells))
			for i, c := range cells {
				out[i] = c
			}
			cell, _ := excelize.CoordinatesToCellName(1, n)
			return sw.SetRow(cell, out)
		})
		if err != nil {
			refuse(err)
			return
		}
		if n == 1 {
			// Only the header row: say so on the page instead of handing over a file that looks empty.
			s.detailNothing(w, r, from, to)
			return
		}
		if err := sw.Flush(); err != nil {
			refuse(err)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(name)+`.xlsx"`)
		if err := f.Write(w); err != nil {
			s.log.ErrorContext(r.Context(), "write xlsx", "error", err)
		}
		return
	}

	// CSV: headers go out with the first row, after the gate has answered, and the
	// writer is flushed every few hundred rows so the download is moving the whole time.
	rc := http.NewResponseController(w)
	var cw *csv.Writer
	begin := func() {
		if cw != nil {
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(name)+`.csv"`)
		w.Header().Set("Cache-Control", "no-store")
		cw = csv.NewWriter(w)
		_ = cw.Write(detailHeaders)
	}
	n := 0
	err := s.time.TimeReportRows(r.Context(), id.OrgID, id.PrincipalID, people, from, to, func(row timelog.ReportRow) error {
		begin()
		if err := cw.Write(detailRow(row)); err != nil {
			return err
		}
		if n++; n%500 == 0 {
			cw.Flush()
			if err := cw.Error(); err != nil {
				return err
			}
			_ = rc.Flush()
		}
		return nil
	})
	if err != nil {
		if cw == nil {
			refuse(err)
			return
		}
		// Rows are already on the wire; the status cannot change, so the file ends short
		// and the log says why.
		s.log.ErrorContext(r.Context(), "time details export", "error", err, "rows", n)
		return
	}
	if cw == nil {
		// No row at all: the page says so, with the range, instead of a file that is only a header.
		s.detailNothing(w, r, from, to)
		return
	}
	cw.Flush()
}

// detailNothing sends the person back to the report with a note that the export would have been empty and why it may be:
// nobody they chose logged finished time inside these dates.
func (s *Server) detailNothing(w http.ResponseWriter, r *http.Request, from, to time.Time) {
	last := to.AddDate(0, 0, -1).Format("2006-01-02")
	msg := "Nothing to export: the people you chose have no finished time between " + from.Format("2006-01-02") + " and " + last + ". Widen the dates above, press Show, and try again."
	q := url.Values{"from": {from.Format("2006-01-02")}, "to": {last}, "notice": {msg}}
	http.Redirect(w, r, "/reports/time?"+q.Encode(), http.StatusSeeOther)
}
