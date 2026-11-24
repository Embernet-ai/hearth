package web

// The sales desk routes against a real Postgres: who may open them, what they write, and that an outsider is told
// there is nothing there.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/connect"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
)

type deskFx struct {
	t      *testing.T
	srv    *Server
	mux    *http.ServeMux
	az     *authz.Service
	org    string
	member string
	other  string
	out    string
	admin  string
	deal   string
}

func newDeskFx(t *testing.T) *deskFx {
	t.Helper()
	dsn := pgtest.Database(t, "hearth_test_webdesk")
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	if err := pg.Migrate(ctx, pools, log); err != nil {
		t.Fatal(err)
	}
	az := authz.New(pools.Web)
	svc := crm.New(pools.Web, az)
	srv, err := New(Deps{Log: log, Sales: svc, CRM: svc, Authz: az})
	if err != nil {
		t.Fatal(err)
	}
	f := &deskFx{t: t, srv: srv, az: az, org: uuid.NewString(), member: uuid.NewString(), other: uuid.NewString(), out: uuid.NewString(), admin: uuid.NewString()}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Desk Test')`, f.org, "dt-"+f.org[:8])
	for id, n := range map[string]string{f.member: "Mia", f.other: "Omar", f.out: "Outside", f.admin: "Ada"} {
		exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user',$3)`, id, f.org, n)
	}
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$2)`, f.org, f.admin)
	team := uuid.NewString()
	exec(`INSERT INTO teams (id, org_id, name) VALUES ($1,$2,'Sales')`, team, f.org)
	for _, p := range []string{f.member, f.other} {
		exec(`INSERT INTO team_members (team_id, principal_id) VALUES ($1,$2)`, team, p)
	}
	t.Cleanup(func() {
		c := context.Background()
		for _, q := range []string{`DELETE FROM crm_followups WHERE org_id=$1`, `DELETE FROM crm_activities WHERE org_id=$1`,
			`DELETE FROM deals WHERE org_id=$1`, `DELETE FROM capability_grants WHERE org_id=$1`, `DELETE FROM teams WHERE org_id=$1`} {
			_, _ = pools.Web.Exec(c, q, f.org)
		}
	})
	d, err := svc.CreateDeal(ctx, f.org, f.subj(f.member), crm.Deal{Name: "Desk deal", AmountCents: 50000, Stage: crm.StageProposal})
	if err != nil {
		t.Fatal(err)
	}
	f.deal = d.ID
	f.mux = http.NewServeMux()
	srv.salesRoutes(f.mux)
	srv.deskRoutes(f.mux)
	return f
}

func (f *deskFx) subj(p string) core.Subjects {
	s, err := f.az.SubjectsFor(context.Background(), f.org, p)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *deskFx) do(as, method, path string, form url.Values) *httptest.ResponseRecorder {
	f.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("Accept", "application/json")
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r = r.WithContext(auth.WithIdentity(r.Context(), &auth.Identity{PrincipalID: as, OrgID: f.org, Subjects: f.subj(as)}))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func TestDeskRoutesAreForSalesAndSuperAdminsOnly(t *testing.T) {
	f := newDeskFx(t)
	gets := []string{"/crm/unassigned", "/crm/followups", "/crm/followups.csv", "/crm/forecast", "/crm/forecast.csv", "/crm/reports"}
	for _, p := range gets {
		if w := f.do(f.out, "GET", p, nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %s as an outsider = %d, want 404", p, w.Code)
		}
		for _, who := range []string{f.member, f.admin} {
			if w := f.do(who, "GET", p, nil); w.Code != http.StatusOK {
				t.Errorf("GET %s as sales or a super admin = %d, want 200: %s", p, w.Code, w.Body.String())
			}
		}
	}
	posts := map[string]url.Values{
		"/crm/followups":                       {"target_type": {"deal"}, "target_id": {f.deal}, "title": {"x"}},
		"/crm/followups/" + f.deal + "/done":   {},
		"/crm/followups/" + f.deal + "/due":    {},
		"/crm/followups/" + f.deal + "/delete": {},
		"/crm/bulk":                            {"type": {"deal"}, "ids": {f.deal}, "stage": {"won"}},
		"/crm/email":                           {"target_type": {"deal"}, "target_id": {f.deal}, "provider": {"google"}, "to": {"a@b.c"}, "body": {"hi"}},
		"/crm/meeting":                         {"target_type": {"deal"}, "target_id": {f.deal}, "provider": {"google"}, "title": {"m"}, "when": {"2026-12-01T10:00"}},
	}
	for p, form := range posts {
		if w := f.do(f.out, "POST", p, form); w.Code != http.StatusNotFound {
			t.Errorf("POST %s as an outsider = %d, want 404", p, w.Code)
		}
	}
	// Nothing the outsider tried changed the deal.
	d, _ := f.srv.sales.GetDeal(context.Background(), f.org, f.subj(f.member), f.deal)
	if d.Stage != crm.StageProposal {
		t.Errorf("the deal moved to %s", d.Stage)
	}
}

func TestFollowupsThroughTheRoutes(t *testing.T) {
	f := newDeskFx(t)
	w := f.do(f.member, "POST", "/crm/followups", url.Values{"target_type": {"deal"}, "target_id": {f.deal}, "title": {"Send the quote"}, "due": {"2026-12-01"}, "assignee": {f.other}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/deals/"+f.deal+"#followups" {
		t.Fatalf("add = %d to %q", w.Code, w.Header().Get("Location"))
	}
	// A person who did not make it sees it, with nobody's filter on.
	w = f.do(f.other, "GET", "/crm/followups", nil)
	var env struct {
		Page struct {
			Desk struct {
				Followups []struct{ ID, Title, AssigneeName string }
			}
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || len(env.Page.Desk.Followups) != 1 || env.Page.Desk.Followups[0].AssigneeName != "Omar" {
		t.Fatalf("queue = %s (%v)", w.Body.String(), err)
	}
	id := env.Page.Desk.Followups[0].ID
	if w := f.do(f.out, "POST", "/crm/followups/"+id+"/done", url.Values{}); w.Code != http.StatusNotFound {
		t.Errorf("an outsider completing it = %d, want 404", w.Code)
	}
	if w := f.do(f.member, "POST", "/crm/followups/"+id+"/done", url.Values{"back": {"https://evil.example/"}}); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/crm/followups" {
		t.Errorf("done = %d to %q, want a local redirect", w.Code, w.Header().Get("Location"))
	}
	if w := f.do(f.member, "POST", "/crm/followups", url.Values{"target_type": {"document"}, "target_id": {f.deal}, "title": {"x"}}); w.Code != http.StatusBadRequest {
		t.Errorf("a follow-up on a document = %d, want 400", w.Code)
	}
	if w := f.do(f.member, "POST", "/crm/followups", url.Values{"target_type": {"deal"}, "target_id": {f.deal}, "title": {" "}}); w.Code != http.StatusBadRequest {
		t.Errorf("an empty title = %d, want 400", w.Code)
	}
	csvw := f.do(f.admin, "GET", "/crm/followups.csv?status=done", nil)
	if !strings.Contains(csvw.Body.String(), "Send the quote") {
		t.Errorf("the csv of done follow-ups: %s", csvw.Body.String())
	}
}

func TestBulkForecastAndReportThroughTheRoutes(t *testing.T) {
	f := newDeskFx(t)
	w := f.do(f.other, "POST", "/crm/bulk", url.Values{"type": {"deal"}, "ids": {f.deal}, "owner": {"me"}, "stage": {"meeting"}, "back": {"/deals?view=table"}})
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/deals?view=table&notice=Changed+1.") {
		t.Fatalf("bulk = %d to %q", w.Code, w.Header().Get("Location"))
	}
	d, _ := f.srv.sales.GetDeal(context.Background(), f.org, f.subj(f.member), f.deal)
	if d.Stage != crm.StageMeeting || d.OwnerID == nil || *d.OwnerID != f.other {
		t.Errorf("deal = %s owner %v", d.Stage, d.OwnerID)
	}
	if w := f.do(f.other, "POST", "/crm/bulk", url.Values{"type": {"deal"}}); w.Code != http.StatusBadRequest {
		t.Errorf("bulk with nothing = %d, want 400", w.Code)
	}
	var fc struct {
		Page struct {
			Desk struct{ Forecast struct{ Count int } }
		}
	}
	// Everybody's by default, not the caller's.
	if err := json.Unmarshal(f.do(f.member, "GET", "/crm/forecast", nil).Body.Bytes(), &fc); err != nil || fc.Page.Desk.Forecast.Count != 1 {
		t.Errorf("forecast for a person who does not own it: %+v %v", fc, err)
	}
	var rp struct {
		Page struct {
			Desk struct{ Report struct{ OpenCount int } }
		}
	}
	if err := json.Unmarshal(f.do(f.member, "GET", "/crm/reports?from=2026-01-01&to=2026-12-31", nil).Body.Bytes(), &rp); err != nil || rp.Page.Desk.Report.OpenCount != 1 {
		t.Errorf("report: %+v %v", rp, err)
	}
	// Mail and meetings need the connectors, which this deployment does not have.
	if w := f.do(f.member, "POST", "/crm/email", url.Values{"target_type": {"deal"}, "target_id": {f.deal}, "provider": {"google"}, "to": {"a@b.c"}, "body": {"hi"}}); w.Code != http.StatusNotFound {
		t.Errorf("email without connectors = %d, want 404", w.Code)
	}
}

func TestTheRailOffersSalesOnlyToSales(t *testing.T) {
	f := newDeskFx(t)
	nav := func(who string) bool {
		r := httptest.NewRequest("GET", "/", nil)
		r = r.WithContext(auth.WithIdentity(r.Context(), &auth.Identity{PrincipalID: who, OrgID: f.org, Subjects: f.subj(who)}))
		return f.srv.salesNav(r)
	}
	if !nav(f.member) || !nav(f.admin) || nav(f.out) {
		t.Errorf("nav: member %v admin %v outsider %v, want true true false", nav(f.member), nav(f.admin), nav(f.out))
	}
}

// A person whose connected account is missing or revoked is told to connect it, not that the provider failed.
func TestAMailActionOnAnAccountThatIsNotConnectedSaysToConnectIt(t *testing.T) {
	srv, err := New(Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	for name, e := range map[string]error{"not connected": connect.ErrNotConnected, "revoked": connect.ErrReconnect,
		"wrapped": fmt.Errorf("google: this account is not connected: %w", connect.ErrNotConnected)} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/crm/email", nil)
		r.Header.Set("Accept", "application/json")
		srv.connectFail(w, r, e, "sending mail")
		if w.Code != http.StatusConflict || !strings.Contains(w.Header().Get("X-Hearth-Reason"), "Connect your account first") {
			t.Errorf("%s: status %d reason %q, want 409 Connect your account first", name, w.Code, w.Header().Get("X-Hearth-Reason"))
		}
	}
	w := httptest.NewRecorder()
	srv.connectFail(w, httptest.NewRequest("POST", "/crm/email", nil), errors.New("quota exceeded"), "sending mail")
	if w.Code != http.StatusBadGateway {
		t.Errorf("a real provider failure = %d, want 502", w.Code)
	}
}
