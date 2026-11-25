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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/calls"
	"github.com/Embernet-ai/hearth/internal/connect"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/crmforms"
	"github.com/Embernet-ai/hearth/internal/crmmarketing"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/fake"
	"github.com/Embernet-ai/hearth/internal/forms"
	"github.com/Embernet-ai/hearth/internal/mautic"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
)

type fakeForms struct{}

func (fakeForms) Forms(context.Context) ([]forms.Form, error) {
	return []forms.Form{{ID: "f1", Name: "Expo lead form", Status: forms.InProgress, Questions: []forms.Question{{ID: "q1", Headline: "Your email"}, {ID: "q2", Headline: "What do you need?"}}}}, nil
}
func (fakeForms) Responses(context.Context, string, int, int) ([]forms.Response, error) {
	return []forms.Response{{ID: "r1", Created: "2026-11-01T10:00:00Z", Finished: true, Answers: map[string]any{"q1": "in@m.example", "q2": "Rewinds"}},
		{ID: "r2", Created: "2026-11-02T10:00:00Z", Answers: map[string]any{"q1": "other@x.example", "q2": "Nope"}}}, nil
}

type fakeMautic struct{ calls []string }

func (f *fakeMautic) SegmentsBrief(context.Context) ([]mautic.Segment, error) {
	return []mautic.Segment{{ID: 1, Name: "Expo"}, {ID: 2, Name: "Hearth: all contacts"}}, nil
}
func (f *fakeMautic) CampaignsBrief(context.Context) ([]mautic.Campaign, error) {
	return []mautic.Campaign{{ID: 7, Name: "Welcome", Published: true}}, nil
}
func (f *fakeMautic) ContactSegments(context.Context, int64) ([]int64, error)  { return []int64{2}, nil }
func (f *fakeMautic) ContactCampaigns(context.Context, int64) ([]int64, error) { return nil, nil }
func (f *fakeMautic) SegmentAdd(context.Context, int64, int64) error {
	f.calls = append(f.calls, "seg+")
	return nil
}
func (f *fakeMautic) SegmentRemove(context.Context, int64, int64) error {
	f.calls = append(f.calls, "seg-")
	return nil
}
func (f *fakeMautic) CampaignAdd(context.Context, int64, int64) error {
	f.calls = append(f.calls, "camp+")
	return nil
}
func (f *fakeMautic) CampaignRemove(context.Context, int64, int64) error {
	f.calls = append(f.calls, "camp-")
	return nil
}

type deskFx struct {
	mkt    *fakeMautic
	ops    *fake.Frappe
	t      *testing.T
	srv    *Server
	pool   *pgxpool.Pool
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
	ops := fake.New(map[string]string{"svc": "hearth-service"})
	t.Cleanup(ops.Close)
	opsURL, _ := url.Parse(ops.URL)
	mkt := &fakeMautic{}
	srv, err := New(Deps{CRMForms: crmforms.New(fakeForms{}), Calls: calls.New(pools.Web, az, nil), Docs: docs.New(pools.Web, az), CRMMarketing: crmmarketing.New(pools.Web, mkt), Log: log, Sales: svc, CRM: svc, Authz: az, Ops: emberops.New(emberops.Config{BaseURL: opsURL, Service: emberops.StaticToken("svc")})})
	if err != nil {
		t.Fatal(err)
	}
	f := &deskFx{mkt: mkt, pool: pools.Web, ops: ops, t: t, srv: srv, az: az, org: uuid.NewString(), member: uuid.NewString(), other: uuid.NewString(), out: uuid.NewString(), admin: uuid.NewString()}
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
			`DELETE FROM mautic_links WHERE org_id=$1`, `DELETE FROM contacts WHERE org_id=$1`, `DELETE FROM external_refs WHERE org_id=$1`, `DELETE FROM deals WHERE org_id=$1`, `DELETE FROM capability_grants WHERE org_id=$1`, `DELETE FROM teams WHERE org_id=$1`} {
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
	f.mux.HandleFunc("GET /contacts/{id}", srv.getContact)
	f.mux.HandleFunc("GET /contacts", func(w http.ResponseWriter, r *http.Request) { srv.contactsPage(w, r) })
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
	gets := []string{"/crm/properties", "/crm/duplicates", "/crm/unassigned", "/crm/followups", "/crm/followups.csv", "/crm/forecast", "/crm/forecast.csv", "/crm/reports"}
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

func TestMergeThroughTheRoutes(t *testing.T) {
	f := newDeskFx(t)
	ctx := context.Background()
	t.Cleanup(func() {})
	svc := f.srv.sales.(*crm.Service)
	a, err := svc.CreateContact(ctx, f.org, f.subj(f.member), crm.Contact{Name: "Dana Route", Email: "dana@route.example"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.CreateContact(ctx, f.org, f.subj(f.member), crm.Contact{Name: "dana route", Email: "d.route@route.example"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*crm.Contact{a, b} {
		if err := svc.Share(ctx, f.org, f.subj(f.member), c.Ref()); err != nil {
			t.Fatal(err)
		}
	}
	var env struct {
		Page struct {
			Desk struct{ Dups []struct{ Reason string } }
		}
	}
	if err := json.Unmarshal(f.do(f.other, "GET", "/crm/duplicates", nil).Body.Bytes(), &env); err != nil || len(env.Page.Desk.Dups) != 1 {
		t.Fatalf("duplicates page = %+v (%v)", env, err)
	}
	if w := f.do(f.out, "POST", "/crm/merge", url.Values{"type": {"contact"}, "survivor": {a.ID}, "merge": {b.ID}}); w.Code != http.StatusNotFound {
		t.Errorf("an outsider merging = %d, want 404", w.Code)
	}
	if w := f.do(f.other, "POST", "/crm/merge", url.Values{"type": {"contact"}, "survivor": {a.ID}}); w.Code != http.StatusBadRequest {
		t.Errorf("a merge with nothing to merge = %d, want 400", w.Code)
	}
	w := f.do(f.other, "POST", "/crm/merge", url.Values{"type": {"contact"}, "survivor": {a.ID}, "merge": {b.ID}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/contacts/"+a.ID {
		t.Fatalf("merge = %d to %q", w.Code, w.Header().Get("Location"))
	}
	if _, err := svc.GetContact(ctx, f.org, f.subj(f.member), b.ID); err == nil {
		t.Error("the merged contact still opens")
	}
}

func TestADealPageShowsItsEmberOpsQuotesAndReportsTotalQuoted(t *testing.T) {
	f := newDeskFx(t)
	ctx := context.Background()
	f.ops.Put("Quotation", map[string]any{"name": "SAL-QTN-9", "quotation_to": "Lead", "party_name": "LEAD-9", "customer_name": "Desk Co", "status": "Open",
		"currency": "USD", "grand_total": 4200, "transaction_date": time.Now().Format("2006-01-02"), "opportunity": "CRM-OPP-9"})
	f.ops.Put("Quotation", map[string]any{"name": "SAL-QTN-10", "customer_name": "Elsewhere", "status": "Open", "currency": "USD", "grand_total": 1, "transaction_date": time.Now().Format("2006-01-02"), "opportunity": "CRM-OPP-10"})
	if _, err := f.pool.Exec(ctx, `INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id, external_url) VALUES ($1,'emberops-record','Opportunity/'||$2::text,'deal',$2::uuid,'https://ops.example/app/opportunity/CRM-OPP-9')`, f.org, f.deal); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Page struct {
			Desk struct {
				EmberOps struct {
					Quotes []struct {
						Name, Status string
						Total        float64
					}
					NewQuoteURL string
				}
			}
		}
	}
	if err := json.Unmarshal(f.do(f.other, "GET", "/deals/"+f.deal, nil).Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	q := env.Page.Desk.EmberOps
	if len(q.Quotes) != 1 || q.Quotes[0].Name != "SAL-QTN-9" || q.Quotes[0].Total != 4200 || !strings.Contains(q.NewQuoteURL, "opportunity=CRM-OPP-9") {
		t.Errorf("deal page EmberOps panel = %+v, want only this opportunity's quote and a link to make another", q)
	}
	var rep struct {
		Page struct {
			Desk struct{ Quoted struct{ Count int } }
		}
	}
	if err := json.Unmarshal(f.do(f.member, "GET", "/crm/reports", nil).Body.Bytes(), &rep); err != nil || rep.Page.Desk.Quoted.Count != 2 {
		t.Errorf("report quoted = %+v (%v), want both quotations of the period", rep, err)
	}
	// A person outside sales gets the page refused, and so none of EmberOps's data about it.
	if w := f.do(f.out, "GET", "/deals/"+f.deal, nil); strings.Contains(w.Body.String(), "SAL-QTN-9") {
		t.Error("a person outside sales was shown the quote")
	}
}

func TestAContactsMauticPanelAndMovingThem(t *testing.T) {
	f := newDeskFx(t)
	ctx := context.Background()
	svc := f.srv.sales.(*crm.Service)
	linked, err := svc.CreateContact(ctx, f.org, f.subj(f.member), crm.Contact{Name: "In Mautic", Email: "in@m.example"})
	if err != nil {
		t.Fatal(err)
	}
	loose, err := svc.CreateContact(ctx, f.org, f.subj(f.member), crm.Contact{Name: "Not In Mautic", Email: "not@m.example"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*crm.Contact{linked, loose} {
		if err := svc.Share(ctx, f.org, f.subj(f.member), c.Ref()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO mautic_links (contact_id, org_id, mautic_id, payload_hash, segments, tags) VALUES ($1,$2,77,'h','{}','{}')`, linked.ID, f.org); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Page struct {
			Desk struct {
				Mautic struct {
					Segments []struct {
						Name           string
						Member, Locked bool
					}
				}
				MauticNote string
			}
		}
	}
	if err := json.Unmarshal(f.do(f.other, "GET", "/contacts/"+linked.ID, nil).Body.Bytes(), &env); err != nil || len(env.Page.Desk.Mautic.Segments) != 2 || !env.Page.Desk.Mautic.Segments[1].Locked || !env.Page.Desk.Mautic.Segments[1].Member {
		t.Fatalf("panel = %+v (%v)", env, err)
	}
	var fr struct {
		Page struct {
			Desk struct {
				Forms []struct {
					Form    string
					Answers []struct{ Question, Answer string }
				}
			}
		}
	}
	if err := json.Unmarshal(f.do(f.other, "GET", "/contacts/"+linked.ID, nil).Body.Bytes(), &fr); err != nil || len(fr.Page.Desk.Forms) != 1 || fr.Page.Desk.Forms[0].Answers[1].Answer != "Rewinds" {
		t.Errorf("form responses = %+v (%v), want only the contact's own", fr, err)
	}
	env.Page.Desk.MauticNote = ""
	if err := json.Unmarshal(f.do(f.other, "GET", "/contacts/"+loose.ID, nil).Body.Bytes(), &env); err != nil || !strings.Contains(env.Page.Desk.MauticNote, "not in Mautic yet") {
		t.Errorf("a contact not in Mautic: note %q (%v)", env.Page.Desk.MauticNote, err)
	}
	form := func(id string, seg string, on string) url.Values {
		return url.Values{"contact_id": {id}, "id": {seg}, "on": {on}}
	}
	if w := f.do(f.out, "POST", "/crm/mautic/segment", form(linked.ID, "1", "1")); w.Code != http.StatusNotFound {
		t.Errorf("a person outside sales moving a contact = %d, want 404", w.Code)
	}
	if w := f.do(f.other, "POST", "/crm/mautic/segment", form(linked.ID, "2", "0")); w.Code != http.StatusConflict {
		t.Errorf("a segment the sync keeps = %d, want 409", w.Code)
	}
	if w := f.do(f.other, "POST", "/crm/mautic/segment", form(loose.ID, "1", "1")); w.Code != http.StatusConflict {
		t.Errorf("a contact not in Mautic = %d, want 409", w.Code)
	}
	if w := f.do(f.other, "POST", "/crm/mautic/segment", form(linked.ID, "1", "1")); w.Code != http.StatusSeeOther {
		t.Errorf("add to a segment = %d, want 303", w.Code)
	}
	if w := f.do(f.other, "POST", "/crm/mautic/campaign", form(linked.ID, "7", "1")); w.Code != http.StatusSeeOther {
		t.Errorf("add to a campaign = %d, want 303", w.Code)
	}
	if w := f.do(f.other, "POST", "/crm/mautic/campaign", form(linked.ID, "x", "1")); w.Code != http.StatusBadRequest {
		t.Errorf("a campaign id that is not a number = %d, want 400", w.Code)
	}
	if len(f.mkt.calls) != 2 || f.mkt.calls[0] != "seg+" || f.mkt.calls[1] != "camp+" {
		t.Errorf("Mautic was asked %v, want seg+ then camp+ only", f.mkt.calls)
	}
}

func TestEditingATimelineEntryThroughTheRoute(t *testing.T) {
	f := newDeskFx(t)
	ctx := context.Background()
	svc := f.srv.sales.(*crm.Service)
	a, err := svc.AddActivity(ctx, f.org, f.subj(f.member), crm.Activity{Kind: crm.ActCall, Title: "Rang", Body: "no answer", Targets: []core.Ref{{Type: core.EntityDeal, ID: f.deal}}})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"title": {"Rang Dana"}, "body": {"left a voicemail"}, "direction": {"outbound"}, "disposition": {"voicemail"}, "when": {"2026-11-20T14:30"}, "minutes": {"2"}, "back": {"/deals/" + f.deal}}
	if w := f.do(f.out, "POST", "/activities/"+a.ID+"/edit", form); w.Code != http.StatusNotFound {
		t.Errorf("an outsider editing = %d, want 404", w.Code)
	}
	w := f.do(f.other, "POST", "/activities/"+a.ID+"/edit", form)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/deals/"+f.deal+"#timeline" {
		t.Fatalf("edit = %d to %q", w.Code, w.Header().Get("Location"))
	}
	acts, _ := svc.ActivitiesFor(ctx, f.org, f.subj(f.member), core.Ref{Type: core.EntityDeal, ID: f.deal})
	if len(acts) != 1 || acts[0].Title != "Rang Dana" || acts[0].Disposition != "voicemail" || acts[0].Direction != "outbound" || *acts[0].DurationMs != 120000 {
		t.Errorf("entry after the edit = %+v", acts)
	}
	for name, v := range map[string]url.Values{"a bad date": {"when": {"yesterday"}}, "bad minutes": {"minutes": {"lots"}}, "emptied": {"title": {""}, "body": {""}}} {
		if w := f.do(f.other, "POST", "/activities/"+a.ID+"/edit", v); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, w.Code)
		}
	}
	if w := f.do(f.other, "POST", "/activities/"+uuid.NewString()+"/edit", url.Values{"title": {"x"}}); w.Code != http.StatusNotFound {
		t.Errorf("an entry that is not there = %d, want 404", w.Code)
	}
	if w := f.do(f.other, "POST", "/activities/"+a.ID+"/edit", url.Values{"title": {"x"}, "back": {"https://evil.example/"}}); w.Header().Get("Location") != "/deals#timeline" {
		t.Errorf("an off-site back = %q, want a local one", w.Header().Get("Location"))
	}
}

func TestEnrolingManyContactsInMauticThroughTheRoute(t *testing.T) {
	f := newDeskFx(t)
	ctx := context.Background()
	svc := f.srv.sales.(*crm.Service)
	mid := 100
	mk := func(name, email string, linked bool) string {
		c, err := svc.CreateContact(ctx, f.org, f.subj(f.member), crm.Contact{Name: name, Email: email})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Share(ctx, f.org, f.subj(f.member), c.Ref()); err != nil {
			t.Fatal(err)
		}
		if linked {
			if _, err := f.pool.Exec(ctx, `INSERT INTO mautic_links (contact_id, org_id, mautic_id, payload_hash, segments, tags) VALUES ($1,$2,$3,'h','{}','{}')`, c.ID, f.org, mid); err != nil {
				t.Fatal(err)
			}
			mid++
		}
		return c.ID
	}
	a, b, loose := mk("En A", "ea@m.example", true), mk("En B", "eb@m.example", true), mk("En C", "ec@m.example", false)
	form := func(target string, ids ...string) url.Values {
		return url.Values{"ids": ids, "target": {target}, "back": {"/contacts"}}
	}
	if w := f.do(f.out, "POST", "/crm/mautic/enrol", form("segment:1", a)); w.Code != http.StatusNotFound {
		t.Errorf("an outsider enrolling = %d, want 404", w.Code)
	}
	if w := f.do(f.other, "POST", "/crm/mautic/enrol", url.Values{"target": {"segment:1"}}); w.Code != http.StatusBadRequest {
		t.Errorf("enrolling nobody = %d, want 400", w.Code)
	}
	if w := f.do(f.other, "POST", "/crm/mautic/enrol", form("nonsense", a)); w.Code != http.StatusBadRequest {
		t.Errorf("a target that is not a number = %d, want 400", w.Code)
	}
	if w := f.do(f.other, "POST", "/crm/mautic/enrol", form("segment:2", a)); w.Code != http.StatusConflict {
		t.Errorf("a segment the sync keeps = %d, want 409", w.Code)
	}
	if w := f.do(f.other, "POST", "/crm/mautic/enrol", form("segment:99", a)); w.Code != http.StatusBadRequest {
		t.Errorf("a segment that is not there = %d, want 400", w.Code)
	}
	w := f.do(f.other, "POST", "/crm/mautic/enrol", form("segment:1", a, b, loose))
	loc := w.Header().Get("Location")
	if w.Code != http.StatusSeeOther || !strings.Contains(loc, "notice=Enrolled+2.+1+are+not+in+Mautic+yet") {
		t.Fatalf("enrol = %d to %q", w.Code, loc)
	}
	if w := f.do(f.other, "POST", "/crm/mautic/enrol", form("campaign:7", a)); w.Code != http.StatusSeeOther {
		t.Errorf("campaign enrol = %d, want 303", w.Code)
	}
	if len(f.mkt.calls) != 3 || f.mkt.calls[0] != "seg+" || f.mkt.calls[2] != "camp+" {
		t.Errorf("Mautic was asked %v, want seg+ seg+ camp+", f.mkt.calls)
	}
	var env struct {
		Page struct {
			Desk struct{ MauticChoices []struct{ Kind, Name string } }
		}
	}
	if err := json.Unmarshal(f.do(f.member, "GET", "/contacts", nil).Body.Bytes(), &env); err != nil || len(env.Page.Desk.MauticChoices) != 2 {
		t.Errorf("choices on the contacts list = %+v (%v), want Expo and Welcome", env, err)
	}
}

func TestCustomFieldsThroughTheRoutes(t *testing.T) {
	f := newDeskFx(t)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.pool.Exec(c, `DELETE FROM property_values WHERE def_id IN (SELECT id FROM property_defs WHERE org_id=$1)`, f.org)
		_, _ = f.pool.Exec(c, `DELETE FROM property_defs WHERE org_id=$1`, f.org)
	})
	add := url.Values{"type": {"deal"}, "label": {"Line speed"}, "data_type": {"number"}}
	if w := f.do(f.out, "POST", "/crm/properties", add); w.Code != http.StatusNotFound {
		t.Errorf("an outsider defining a field = %d, want 404", w.Code)
	}
	if w := f.do(f.member, "POST", "/crm/properties", add); w.Code != http.StatusForbidden {
		t.Errorf("a salesperson defining a field = %d, want 403", w.Code)
	}
	if w := f.do(f.admin, "POST", "/crm/properties", add); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/crm/properties?type=deal" {
		t.Fatalf("define = %d to %q", w.Code, w.Header().Get("Location"))
	}
	if w := f.do(f.admin, "POST", "/crm/properties", url.Values{"type": {"deal"}, "label": {"Line Speed"}, "data_type": {"text"}}); w.Code != http.StatusConflict {
		t.Errorf("the same key twice = %d, want 409", w.Code)
	}
	var env struct {
		Page struct {
			Desk struct {
				PropDefs  []struct{ ID, Key string }
				CanManage bool
			}
		}
	}
	if err := json.Unmarshal(f.do(f.other, "GET", "/crm/properties?type=deal", nil).Body.Bytes(), &env); err != nil || len(env.Page.Desk.PropDefs) != 1 || env.Page.Desk.CanManage {
		t.Fatalf("page for a salesperson = %+v (%v), want the field and no management", env, err)
	}
	if err := json.Unmarshal(f.do(f.admin, "GET", "/crm/properties?type=deal", nil).Body.Bytes(), &env); err != nil || !env.Page.Desk.CanManage {
		t.Errorf("page for an admin = %+v (%v), want management", env, err)
	}
	// Values on a record.
	set := func(who string, v url.Values) *httptest.ResponseRecorder {
		v.Set("target_type", "deal")
		v.Set("target_id", f.deal)
		return f.do(who, "POST", "/crm/properties/values", v)
	}
	if w := set(f.out, url.Values{"p_line_speed": {"10"}}); w.Code != http.StatusNotFound {
		t.Errorf("an outsider setting a value = %d, want 404", w.Code)
	}
	if w := set(f.other, url.Values{"p_line_speed": {"fast"}}); w.Code != http.StatusBadRequest {
		t.Errorf("a bad number = %d, want 400", w.Code)
	}
	if w := set(f.other, url.Values{"p_nope": {"x"}}); w.Code != http.StatusBadRequest {
		t.Errorf("a field that is not there = %d, want 400", w.Code)
	}
	if w := f.do(f.other, "POST", "/crm/properties/values", url.Values{"target_type": {"document"}, "target_id": {f.deal}}); w.Code != http.StatusBadRequest {
		t.Errorf("a target that is not CRM = %d, want 400", w.Code)
	}
	w := set(f.other, url.Values{"p_line_speed": {"1,250"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/deals/"+f.deal+"#fields" {
		t.Fatalf("set = %d to %q", w.Code, w.Header().Get("Location"))
	}
	var rec struct {
		Page struct {
			Desk struct{ Properties []struct{ Key, Text string } }
		}
	}
	if err := json.Unmarshal(f.do(f.member, "GET", "/deals/"+f.deal, nil).Body.Bytes(), &rec); err != nil || len(rec.Page.Desk.Properties) != 1 || rec.Page.Desk.Properties[0].Text != "1250" {
		t.Errorf("deal page fields = %+v (%v), want line speed 1250", rec, err)
	}
	if w := f.do(f.other, "POST", "/crm/properties/"+env.Page.Desk.PropDefs[0].ID+"/delete", url.Values{"type": {"deal"}}); w.Code != http.StatusForbidden {
		t.Errorf("a salesperson deleting a field = %d, want 403", w.Code)
	}
	if w := f.do(f.admin, "POST", "/crm/properties/"+env.Page.Desk.PropDefs[0].ID+"/delete", url.Values{"type": {"deal"}}); w.Code != http.StatusSeeOther {
		t.Errorf("delete = %d, want 303", w.Code)
	}
}
