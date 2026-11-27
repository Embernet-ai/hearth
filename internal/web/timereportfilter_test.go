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

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/teams"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// approvalFixture is four entries across two people in three approval states, two spaces (one under the other) and a tag.
func approvalFixture() []timelog.ReportRow {
	at := time.Date(2026, 11, 3, 9, 30, 0, 0, time.Local)
	dec := time.Date(2026, 11, 5, 0, 0, 0, 0, time.Local)
	return []timelog.ReportRow{
		{ID: "a1", PrincipalID: "p1", PersonName: "Ann Lee", SpaceID: "sp-eng", SpaceName: "Engineering", StartedAt: at, Minutes: 60, Tags: []string{"Billable"}, Approval: timelog.ApprovalApproved, DecidedAt: &dec, DeciderName: "Ann Lee"},
		{ID: "a2", PrincipalID: "p1", PersonName: "Ann Lee", SpaceID: "sp-line", SpaceName: "Line 3", StartedAt: at.AddDate(0, 0, 1), Minutes: 120, Approval: timelog.ApprovalPending},
		{ID: "b1", PrincipalID: "p2", PersonName: "Bo Diaz", SpaceID: "sp-eng", SpaceName: "Engineering", StartedAt: at, Minutes: 30, Approval: timelog.ApprovalRejected, DecidedAt: &dec, DeciderName: "Cy Boss"},
		{ID: "b2", PrincipalID: "p2", PersonName: "Bo Diaz", SpaceID: "", StartedAt: at.AddDate(0, 0, 2), Minutes: 45, Tags: []string{"billable"}, Approval: timelog.ApprovalPending},
	}
}

type reportTeams struct {
	teams.Store
	members map[string][]teams.Member
}

func (f reportTeams) Members(_ context.Context, _, id string) ([]teams.Member, error) {
	return f.members[id], nil
}
func (f reportTeams) List(context.Context, string) ([]*teams.Team, error) { return nil, nil }

func csvOf(t *testing.T, rec *httptest.ResponseRecorder) [][]string {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	recs, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func ids(recs [][]string) string {
	var out []string
	for _, r := range recs[1:] {
		out = append(out, r[12])
	}
	return strings.Join(out, ",")
}

func TestDetailRowsSayWhereEachHourStands(t *testing.T) {
	recs := csvOf(t, runDetail(t, &detailStore{rows: approvalFixture()}, "detail=1&format=csv&all=1"))
	if got := strings.Join(recs[0][13:], ","); got != "approval_status,decided_by,decided_on" {
		t.Fatalf("the file ends in %q", got)
	}
	want := map[string][3]string{
		"a1": {"approved", "Ann Lee", "2026-11-05"},
		"a2": {"pending", "", ""},
		"b1": {"rejected", "Cy Boss", "2026-11-05"},
		"b2": {"pending", "", ""},
	}
	for _, r := range recs[1:] {
		w := want[r[12]]
		if r[13] != w[0] || r[14] != w[1] || r[15] != w[2] {
			t.Errorf("%s reads %v, want %v", r[12], r[13:], w)
		}
	}
}

func TestDetailApprovalFilter(t *testing.T) {
	for q, want := range map[string]string{
		"approval=approved":                   "a1",
		"approval=pending":                    "a2,b2",
		"approval=rejected":                   "b1",
		"approval=approved&approval=rejected": "a1,b1",
		"approval=pending,rejected":           "a2,b1,b2",
		"approval=all":                        "a1,a2,b1,b2",
		"approval=PENDING":                    "a2,b2",
	} {
		st := &detailStore{rows: approvalFixture()}
		recs := csvOf(t, runDetail(t, st, "detail=1&format=csv&all=1&"+q))
		if got := ids(recs); got != want {
			t.Errorf("%s: got %s, want %s", q, got, want)
		}
	}
}

func TestDetailApprovalFilterRejectsAWordItDoesNotKnow(t *testing.T) {
	rec := runDetail(t, &detailStore{rows: approvalFixture()}, "detail=1&format=csv&all=1&approval=maybe")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestDetailFilterByTagIsAnyCase(t *testing.T) {
	recs := csvOf(t, runDetail(t, &detailStore{rows: approvalFixture()}, "detail=1&format=csv&all=1&tag=billable"))
	if got := ids(recs); got != "a1,b2" {
		t.Errorf("billable: %s", got)
	}
	recs = csvOf(t, runDetail(t, &detailStore{rows: approvalFixture()}, "detail=1&format=csv&all=1&tag=billable&approval=pending"))
	if got := ids(recs); got != "b2" {
		t.Errorf("billable and pending: %s", got)
	}
}

func TestDetailFilterByTeamAndSpace(t *testing.T) {
	s := &Server{
		time:  &detailStore{rows: approvalFixture()},
		teams: reportTeams{members: map[string][]teams.Member{"t1": {{PrincipalID: "p2"}}}},
		log:   slog.New(slog.NewTextHandler(&strings.Builder{}, nil)),
	}
	run := func(q string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/reports/time?detail=1&format=csv&all=1&"+q, nil)
		req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{OrgID: "o", PrincipalID: "me"}))
		rec := httptest.NewRecorder()
		s.getTimeReport(rec, req)
		return rec
	}
	if got := ids(csvOf(t, run("team=t1"))); got != "b1,b2" {
		t.Errorf("team: %s", got)
	}
	if got := ids(csvOf(t, run("space=sp-eng"))); got != "a1,b1" {
		t.Errorf("space: %s", got)
	}
	if got := ids(csvOf(t, run("team=t1&space=sp-eng&approval=rejected"))); got != "b1" {
		t.Errorf("all three: %s", got)
	}
}

func TestSpaceFilterTakesTheSpacesUnderIt(t *testing.T) {
	parent := "sp-eng"
	tree := []*plant.Space{{ID: "sp-eng"}, {ID: "sp-line", ParentID: &parent}, {ID: "sp-other"}}
	got := withDescendants(tree, map[string]bool{"sp-eng": true})
	if len(got) != 2 || got[0] != "sp-eng" || got[1] != "sp-line" {
		t.Errorf("under Engineering: %v", got)
	}
}

func TestDetailFilterThatMatchesNothingSaysSoAndKeepsTheFilters(t *testing.T) {
	rec := runDetail(t, &detailStore{rows: approvalFixture()}, "detail=1&format=csv&all=1&approval=rejected&tag=nope")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "matches") || !strings.Contains(loc, "approval=rejected") || !strings.Contains(loc, "tag=nope") {
		t.Errorf("redirect %q should explain and carry the filters", loc)
	}
}

func TestFilteredTotalsAreBuiltFromTheEntriesThatSurvive(t *testing.T) {
	rows := approvalFixture()
	f := reportFilter{approvals: map[string]bool{timelog.ApprovalPending: true}}
	var kept []timelog.ReportRow
	for _, r := range rows {
		if f.keep(r) {
			kept = append(kept, r)
		}
	}
	tot := totalsFromRows(kept)
	if len(tot) != 2 || tot[0].PrincipalID != "p1" || tot[0].Minutes != 120 || tot[1].Minutes != 45 {
		t.Errorf("pending totals %+v", tot)
	}
	if (reportFilter{}).active() || !f.active() {
		t.Error("active() is wrong")
	}
}

func TestFilterQueryIsCarriedOnLinks(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/reports/time?approval=pending&approval=approved&tag=on+site", nil)
	s := &Server{log: slog.New(slog.NewTextHandler(&strings.Builder{}, nil))}
	f, msg := s.parseReportFilter(context.Background(), r)
	if msg != "" {
		t.Fatal(msg)
	}
	if got := f.query(); got != "&approval=pending&approval=approved&tag=on+site" {
		t.Errorf("query %q", got)
	}
}

func TestDetailApprovalFilterWithChosenPeople(t *testing.T) {
	recs := csvOf(t, runDetail(t, &detailStore{rows: approvalFixture()}, "detail=1&format=csv&approval=pending&people=p2"))
	if got := ids(recs); got != "b2" {
		t.Errorf("pending for p2: %s", got)
	}
}

// The People selector's picks are the report's `people` filter: they narrow the entries, and they ride on every link the page builds.
func TestPeopleFilterNarrowsRowsAndRidesOnTheLinks(t *testing.T) {
	st := &detailStore{rows: approvalFixture()}
	got := ids(csvOf(t, runDetail(t, st, "detail=1&format=csv&people=p2")))
	if got != "b1,b2" {
		t.Fatalf("people=p2 exported %q, want only Bo Diaz's entries b1,b2", got)
	}
	// nobody picked is everyone in the view
	if got := ids(csvOf(t, runDetail(t, &detailStore{rows: approvalFixture()}, "detail=1&format=csv"))); got != "a1,a2,b1,b2" {
		t.Fatalf("nobody picked exported %q", got)
	}
	// the picks are in the query the page puts on links, with the other filters
	f := reportFilter{whoQ: []string{"p1", "p2"}, approvalQ: []string{"pending"}}
	if q := f.query(); !strings.Contains(q, "people=p1") || !strings.Contains(q, "people=p2") || !strings.Contains(q, "approval=pending") {
		t.Errorf("the filter query lost the people: %q", q)
	}
	// and they are an active filter, so the totals are rebuilt from the entries that survive
	if !(reportFilter{who: map[string]bool{"p1": true}}).active() {
		t.Error("picked people did not make the filter active")
	}
}
