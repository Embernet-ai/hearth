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
	"github.com/Embernet-ai/hearth/internal/plant"
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

func (d *detailStore) TimeReport(_ context.Context, _, _, person string, _, _ time.Time) ([]timelog.ReportRow, error) {
	var out []timelog.ReportRow
	for _, r := range d.rows {
		if person == "" || r.PrincipalID == person {
			out = append(out, r)
		}
	}
	return out, nil
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
		first[4] != "1h 30m" || first[5] != "90" || first[6] != "Acme" || first[7] != "" || first[8] != "" || first[9] != "Wire the panel" ||
		first[10] != "billable; onsite" || first[11] != `day one, "tight"` || first[12] != "e1" {
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

// A details button with nobody ticked used to be disabled, and a request that arrived without people was refused with a 400.
// Yessid pressed it and got nothing. Nobody ticked now means everybody in the report's current view.
func TestDetailWithNobodyTickedExportsEveryoneInTheView(t *testing.T) {
	for _, format := range []string{"csv", "xlsx"} {
		st := &detailStore{rows: detailFixture()}
		rec := runDetail(t, st, "detail=1&format="+format+"&from=2026-11-01&to=2026-11-30")
		if rec.Code != http.StatusOK || len(st.gotPeop) != 0 {
			t.Fatalf("%s: status %d, store asked for %v", format, rec.Code, st.gotPeop)
		}
		if format == "csv" {
			recs, _ := csv.NewReader(rec.Body).ReadAll()
			if len(recs) != 4 {
				t.Fatalf("csv: %d records, want the header and 3 entries", len(recs))
			}
		}
	}
}

func TestDetailWithNobodyTickedStillAppliesTheFilters(t *testing.T) {
	st := &detailStore{rows: approvalFixture()}
	got := ids(csvOf(t, runDetail(t, st, "detail=1&format=csv&approval=pending")))
	if got != "a2,b2" {
		t.Fatalf("pending with nobody ticked exported %q", got)
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
	for cell, want := range map[string]string{"A1": "employee", "B1": "employee_id", "A2": "Ann Lee", "B2": "p1", "A4": "Bo Diaz", "M4": "e3", "G1": "space", "H1": "customer_project", "I1": "service_project_phase", "J1": "task"} {
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

// Every export leaves one log line saying who asked, for which dates and how many people, and how many rows came back, so a
// report of "it came back empty" can be answered from the log.
func TestDetailExportLeavesALogLine(t *testing.T) {
	var buf strings.Builder
	st := &detailStore{rows: detailFixture()}
	s := &Server{time: st, log: slog.New(slog.NewTextHandler(&buf, nil))}
	req := httptest.NewRequest(http.MethodGet, "/reports/time?detail=1&format=csv&from=2026-11-01&to=2026-11-30&people=p1", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{OrgID: "o", PrincipalID: "me"}))
	s.getTimeReport(httptest.NewRecorder(), req)
	out := buf.String()
	for _, want := range []string{`msg="time report"`, `principal=me`, `from=2026-11-01`, `to=2026-11-30`, `detail=true`, `people=1`, `msg="time details export"`, `rows=2`} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %s:\n%s", want, out)
		}
	}
}

// chainStore stands in for the plant service's tree read.
type chainPlant struct {
	plant.Store
	chains map[string][]plant.SpaceLink
}

func (c chainPlant) SpaceChains(context.Context, string) (map[string][]plant.SpaceLink, error) {
	return c.chains, nil
}

func levelFixture() ([]timelog.ReportRow, chainPlant) {
	at := time.Date(2026, 11, 4, 9, 0, 0, 0, time.Local)
	rows := []timelog.ReportRow{
		{ID: "l1", PrincipalID: "p1", PersonName: "Ann Lee", DocTitle: "On-Site Testing at Mt. Clemens", SpaceID: "crane", SpaceName: "Expert Crane", StartedAt: at, Minutes: 195},
		{ID: "l2", PrincipalID: "p1", PersonName: "Ann Lee", DocTitle: "Alarm map", SpaceID: "cny", SpaceName: "CNY0324 Process Update", StartedAt: at, Minutes: 60},
		{ID: "l3", PrincipalID: "p1", PersonName: "Ann Lee", StartedAt: at, Minutes: 30},
	}
	chains := map[string][]plant.SpaceLink{
		"crane": {{Name: "Engineering", Kind: "area"}, {Name: "White Team Service", Kind: "folder"}, {Name: "Expert Crane", Kind: "folder"}},
		"cny":   {{Name: "Engineering", Kind: "area"}, {Name: "White Team Service", Kind: "folder"}, {Name: "Cast Nylon", Kind: "folder"}, {Name: "CNY0324 Process Update", Kind: "project"}},
	}
	return rows, chainPlant{chains: chains}
}

// livePlant is how the live server holds its plant store: the EmberOps store wraps the Postgres one and embeds the
// interface, so only what the interface itself names reaches the report. 2.33.0 asked for the tree through an optional
// method the wrapper hid, and every live row fell back to its single space name with Customer and Phase empty.
type livePlant struct{ plant.Store }

func TestTimeReportLevelsReachThroughAWrappedPlantStore(t *testing.T) {
	rows, inner := levelFixture()
	st := &detailStore{rows: rows}
	s := &Server{time: st, plant: livePlant{inner}, log: slog.New(slog.NewTextHandler(&strings.Builder{}, nil))}
	req := httptest.NewRequest(http.MethodGet, "/reports/time?detail=1&format=csv&all=1&from=2026-11-01&to=2026-11-30", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{OrgID: "o", PrincipalID: "me"}))
	rec := httptest.NewRecorder()
	s.getTimeReport(rec, req)
	recs, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(recs[1][6:10], "|"); got != "White Team Service|Expert Crane||On-Site Testing at Mt. Clemens" {
		t.Errorf("a wrapped store gave %q", got)
	}
}

func TestTimeReportFilesCarryTheFourColumns(t *testing.T) {
	rows, pl := levelFixture()
	run := func(query string) [][]string {
		st := &detailStore{rows: rows}
		s := &Server{time: st, plant: pl, log: slog.New(slog.NewTextHandler(&strings.Builder{}, nil))}
		req := httptest.NewRequest(http.MethodGet, "/reports/time?"+query, nil)
		req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{OrgID: "o", PrincipalID: "me"}))
		rec := httptest.NewRecorder()
		s.getTimeReport(rec, req)
		recs, err := csv.NewReader(rec.Body).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		return recs
	}
	want := [][]string{
		{"White Team Service", "Expert Crane", "", "On-Site Testing at Mt. Clemens"},
		{"White Team Service", "Cast Nylon", "CNY0324 Process Update", "Alarm map"},
		{"", "", "", ""},
	}
	// details file, filtered by a space the way the report's filter does
	d := run("detail=1&format=csv&all=1&from=2026-11-01&to=2026-11-30")
	if strings.Join(d[0][6:10], ",") != "space,customer_project,service_project_phase,task" {
		t.Fatalf("details header %v", d[0])
	}
	for i, w := range want {
		if got := d[i+1][6:10]; strings.Join(got, "|") != strings.Join(w, "|") {
			t.Errorf("details row %d reads %v, want %v", i, got, w)
		}
	}
	// the per-person file
	st := &detailStore{rows: rows}
	sv := &Server{time: st, plant: pl, log: slog.New(slog.NewTextHandler(&strings.Builder{}, nil))}
	req := httptest.NewRequest(http.MethodGet, "/reports/time?person=p1&format=csv&from=2026-11-01&to=2026-11-30", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{OrgID: "o", PrincipalID: "me"}))
	rec := httptest.NewRecorder()
	sv.getTimeReport(rec, req)
	cr := csv.NewReader(rec.Body)
	cr.FieldsPerRecord = -1
	recs, err := cr.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(recs[0][:6], ",") != "date,hours,space,customer_project,service_project_phase,task" {
		t.Fatalf("person header %v", recs[0])
	}
	if got := strings.Join(recs[2][2:6], "|"); got != "White Team Service|Cast Nylon|CNY0324 Process Update|Alarm map" {
		t.Errorf("person row reads %q", got)
	}
}
