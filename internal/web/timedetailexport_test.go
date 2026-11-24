package web

import (
	"context"
	"encoding/csv"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// detailStore answers the details read from a fixed list, applying the same
// "these people, or everybody when none" rule the real stores do, and refusing
// anybody who is not allowed.
type detailStore struct {
	timelog.Store
	rows    []timelog.ReportRow
	refuse  bool
	gotPeop []string
}

func (d *detailStore) TimeReportRows(_ context.Context, _, _ string, ids []string, _, _ time.Time, fn func(timelog.ReportRow) error) error {
	d.gotPeop = ids
	if d.refuse {
		return timelog.ErrNotAllowed
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	for _, r := range d.rows {
		if len(want) > 0 && !want[r.PrincipalID] {
			continue
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

func detailFixture() []timelog.ReportRow {
	at := time.Date(2026, 11, 3, 9, 30, 0, 0, time.Local)
	return []timelog.ReportRow{
		{ID: "e1", PrincipalID: "p1", PersonName: "Ann Lee", DocTitle: "Wire the panel", SpaceName: "Acme", StartedAt: at, Minutes: 90, Note: "day one, \"tight\"", Tags: []string{"billable", "onsite"}},
		{ID: "e2", PrincipalID: "p1", PersonName: "Ann Lee", DocTitle: "Wire the panel", SpaceName: "Acme", StartedAt: at.AddDate(0, 0, 1), Minutes: 60},
		{ID: "e3", PrincipalID: "p2", PersonName: "Bo Diaz", DocTitle: "Commission", SpaceName: "Zed", StartedAt: at, Minutes: 480, Note: "all day"},
	}
}

func runDetail(t *testing.T, st *detailStore, query string) *httptest.ResponseRecorder {
	t.Helper()
	s := &Server{time: st, log: slog.New(slog.NewTextHandler(&strings.Builder{}, nil))}
	req := httptest.NewRequest(http.MethodGet, "/reports/time?"+query, nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{OrgID: "o", PrincipalID: "me"}))
	rec := httptest.NewRecorder()
	s.getTimeReport(rec, req)
	return rec
}

func TestDetailCSVForChosenPeople(t *testing.T) {
	st := &detailStore{rows: detailFixture()}
	rec := runDetail(t, st, "detail=1&format=csv&from=2026-11-01&to=2026-11-30&people=p1&people=p2")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
		t.Errorf("content type %q", got)
	}
	recs, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 4 {
		t.Fatalf("want header plus 3 rows, got %d: %v", len(recs), recs)
	}
	if strings.Join(recs[0], ",") != strings.Join(detailHeaders, ",") {
		t.Errorf("header %v", recs[0])
	}
	first := recs[1]
	if first[0] != "Ann Lee" || first[1] != "p1" || first[2] != "2026-11-03" || first[3] != "09:30" ||
		first[4] != "1h 30m" || first[5] != "90" || first[6] != "Acme" || first[7] != "Wire the panel" ||
		first[8] != "billable; onsite" || first[9] != `day one, "tight"` || first[10] != "e1" {
		t.Errorf("first row %v", first)
	}
	if recs[3][0] != "Bo Diaz" || recs[3][1] != "p2" {
		t.Errorf("last row %v", recs[3])
	}
	for _, r := range recs {
		if r[0] == "total" {
			t.Error("a total line would break sorting and filtering")
		}
	}
}

func TestDetailCSVOnlyTheChosenPeople(t *testing.T) {
	st := &detailStore{rows: detailFixture()}
	rec := runDetail(t, st, "detail=1&format=csv&people=p2")
	recs, _ := csv.NewReader(rec.Body).ReadAll()
	if len(recs) != 2 || recs[1][1] != "p2" {
		t.Fatalf("want only p2, got %v", recs)
	}
	if len(st.gotPeop) != 1 || st.gotPeop[0] != "p2" {
		t.Errorf("store asked for %v", st.gotPeop)
	}
}

func TestDetailAllAsksForEverybody(t *testing.T) {
	st := &detailStore{rows: detailFixture()}
	rec := runDetail(t, st, "detail=1&format=csv&all=1")
	recs, _ := csv.NewReader(rec.Body).ReadAll()
	if len(recs) != 4 || len(st.gotPeop) != 0 {
		t.Fatalf("all=1: %d records, store asked for %v", len(recs), st.gotPeop)
	}
}

func TestDetailNeedsSomebody(t *testing.T) {
	rec := runDetail(t, &detailStore{rows: detailFixture()}, "detail=1&format=csv")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestDetailRefusedIsA403NotHalfAFile(t *testing.T) {
	for _, f := range []string{"csv", "xlsx"} {
		rec := runDetail(t, &detailStore{rows: detailFixture(), refuse: true}, "detail=1&all=1&format="+f)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", f, rec.Code)
		}
		if strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
			t.Errorf("%s: a refusal must not offer a download", f)
		}
	}
}

func TestDetailXLSXRoundTrips(t *testing.T) {
	rec := runDetail(t, &detailStore{rows: detailFixture()}, "detail=1&format=xlsx&all=1")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	f, err := excelize.OpenReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for cell, want := range map[string]string{"A1": "employee", "B1": "employee_id", "A2": "Ann Lee", "B2": "p1", "A4": "Bo Diaz", "K4": "e3"} {
		if got, _ := f.GetCellValue("Time details", cell); got != want {
			t.Errorf("%s = %q, want %q", cell, got, want)
		}
	}
}

func TestDetailLeavesTheTotalsExportAlone(t *testing.T) {
	// Without detail=1 the handler must not enter the details path at all: it goes on to
	// the totals read, which this store does not have, so it would panic on the nil embed.
	st := &detailStore{rows: detailFixture()}
	defer func() { _ = recover() }()
	runDetail(t, st, "format=csv")
	if st.gotPeop != nil {
		t.Error("the totals export reached the details read")
	}
}

// A range with nobody's time in it used to hand back a file that was only a header, and the picker was missing from the page
// altogether because it listed only people who had logged time in the range. An empty export now says so on the report.
func TestDetailExportWithNoRowsSaysSoInsteadOfAnEmptyFile(t *testing.T) {
	for _, format := range []string{"csv", "xlsx"} {
		st := &detailStore{} // no rows at all
		rec := runDetail(t, st, "detail=1&format="+format+"&from=2026-11-01&to=2026-11-30&people=p1")
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s with no rows: status %d, want a redirect back to the report", format, rec.Code)
		}
		loc := rec.Header().Get("Location")
		if !strings.HasPrefix(loc, "/reports/time?") || !strings.Contains(loc, "notice=Nothing+to+export") || !strings.Contains(loc, "from=2026-11-01") {
			t.Errorf("%s redirect %q should return to the report with the dates and a note", format, loc)
		}
		if rec.Header().Get("Content-Disposition") != "" {
			t.Errorf("%s: an attachment was started for an export with nothing in it", format)
		}
	}
}

type fakeDirectory struct{ list []auth.Person }

func (f fakeDirectory) People(context.Context, string) ([]auth.Person, error) { return f.list, nil }

// Every active person is offered, whether or not they logged time in the range, and the ones with time come first.
func TestReportPeopleOffersEveryActivePerson(t *testing.T) {
	s := &Server{directory: fakeDirectory{list: []auth.Person{
		{PrincipalID: "p1", DisplayName: "Ann Lee", Status: "active"},
		{PrincipalID: "p2", DisplayName: "Bo Diaz", Status: "active"},
		{PrincipalID: "p3", DisplayName: "Cy Moved On", Status: "suspended"},
		{PrincipalID: "p4", DisplayName: "Al Able", Status: ""},
	}}, log: slog.New(slog.NewTextHandler(&strings.Builder{}, nil))}
	// Nobody logged anything: the picker still offers the three active people, by name.
	got := s.reportPeople(context.Background(), "o", nil)
	if len(got) != 3 || got[0].PersonName != "Al Able" || got[1].PersonName != "Ann Lee" || got[2].PersonName != "Bo Diaz" {
		t.Fatalf("with no time logged the picker offers %+v, want Al Able, Ann Lee, Bo Diaz", got)
	}
	// Bo logged time: Bo is first, with the minutes, and is not listed twice.
	got = s.reportPeople(context.Background(), "o", []timelog.ReportTotal{{PrincipalID: "p2", PersonName: "Bo Diaz", Minutes: 480}})
	if len(got) != 3 || got[0].PrincipalID != "p2" || got[0].Minutes != 480 || got[1].PersonName != "Al Able" {
		t.Fatalf("with Bo's time the picker offers %+v", got)
	}
	// Nobody is hidden by a missing directory: the people with time are still there.
	got = (&Server{log: slog.New(slog.NewTextHandler(&strings.Builder{}, nil))}).reportPeople(context.Background(), "o", []timelog.ReportTotal{{PrincipalID: "p2", PersonName: "Bo Diaz", Minutes: 5}})
	if len(got) != 1 {
		t.Errorf("without a directory: %+v", got)
	}
}
