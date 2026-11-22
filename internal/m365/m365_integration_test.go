package m365

// Microsoft 365 against a fake Entra and a fake Graph, and a real Postgres.
//
// The fake Graph serves the recorded shapes in testdata (written from Microsoft's published examples, see its
// README) and records each request, so the tests check both what the SDK parses and what it sends: the Prefer
// header that asks for UTC times, the bearer token, the body of an event that is created.

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/connect"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
	"github.com/Embernet-ai/hearth/internal/platform/secret"
)

const tenant = "8f1c2b7e-0000-4a1d-9c3e-5d2f7a6b1c90"

type call struct {
	method, path, query, auth, prefer string
	body                              string
}

type fakeMS struct {
	srv        *httptest.Server
	mu         sync.Mutex
	calls      []call
	grantValid bool
	refreshes  int
	status     int // when set, every Graph call answers this
	uploaded   []string
	tenantOfID string
}

func recorded(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func idToken(tid, oid, upn string) string {
	body, _ := json.Marshal(map[string]string{"tid": tid, "oid": oid, "preferred_username": upn})
	return "h." + base64.RawURLEncoding.EncodeToString(body) + ".s"
}

func newFakeMS(t *testing.T) *fakeMS {
	f := &fakeMS{grantValid: true, tenantOfID: tenant}
	mux := http.NewServeMux()
	token := func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code_verifier") == "" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"no PKCE verifier"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at-1", "refresh_token": "rt-1", "expires_in": 3600, "token_type": "Bearer",
				"scope":    "openid profile email https://graph.microsoft.com/User.Read https://graph.microsoft.com/Calendars.ReadWrite",
				"id_token": idToken(f.tenantOfID, "oid-dana", "Dana@Contoso.test"),
			})
		case "refresh_token":
			f.mu.Lock()
			f.refreshes++
			ok := f.grantValid
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"AADSTS70008: expired or revoked"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at-2", "refresh_token": "rt-2", "expires_in": 3600, "token_type": "Bearer",
				"scope": "openid profile email User.Read Calendars.ReadWrite",
			})
		}
	}
	graph := func(w http.ResponseWriter, r *http.Request) {
		var rd io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			// The SDK compresses what it sends.
			if zr, err := gzip.NewReader(r.Body); err == nil {
				rd = zr
			}
		}
		b, _ := io.ReadAll(rd)
		f.mu.Lock()
		f.calls = append(f.calls, call{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"),
			strings.Join(r.Header.Values("Prefer"), ","), string(b)})
		status := f.status
		f.mu.Unlock()
		if status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":"Denied","message":"x"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		p := strings.TrimPrefix(r.URL.Path, "/v1.0")
		switch {
		case p == "/me/calendarView" && r.URL.Query().Get("page") == "2":
			_, _ = w.Write([]byte(recorded(t, "calendarview-page2.json")))
		case p == "/me/calendarView":
			_, _ = w.Write([]byte(strings.ReplaceAll(recorded(t, "calendarview-page1.json"), "NEXTLINK",
				f.srv.URL+"/v1.0/me/calendarView?page=2")))
		case p == "/me/calendars":
			_, _ = w.Write([]byte(recorded(t, "calendars.json")))
		case p == "/me/events" && r.Method == http.MethodPost:
			w.WriteHeader(201)
			_, _ = w.Write([]byte(recorded(t, "event-created.json")))
		case strings.HasPrefix(p, "/me/events/") && r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(recorded(t, "event-created.json")))
		case strings.HasPrefix(p, "/me/events/") && r.Method == http.MethodDelete:
			w.WriteHeader(204)
		case p == "/me/contacts" && r.Method == http.MethodPost:
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"AAMkCONNEW","changeKey":"ck-new","displayName":"Dana Park","emailAddresses":[{"address":"dana.park@acme.test"}]}`))
		case strings.HasPrefix(p, "/me/contacts/") && r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"id":"AAMkCON1","changeKey":"ck-patched","displayName":"Priya Shah"}`))
		case strings.HasPrefix(p, "/me/contacts/") && r.Method == http.MethodDelete:
			if strings.HasSuffix(p, "/gone") {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":{"code":"ErrorItemNotFound","message":"x"}}`))
				return
			}
			w.WriteHeader(204)
		case p == "/me/contacts":
			_, _ = w.Write([]byte(recorded(t, "contacts.json")))
		case p == "/me/sendMail", strings.HasSuffix(p, "/reply"), strings.HasSuffix(p, "/replyAll"), strings.HasSuffix(p, "/forward"):
			w.WriteHeader(202)
		case strings.HasSuffix(p, "/move"):
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"AAMkMSG1moved"}`))
		case p == "/me/mailFolders/inbox/messages", p == "/me/mailFolders/sentitems/messages", p == "/me/mailFolders/archive/messages":
			_, _ = w.Write([]byte(strings.Replace(recorded(t, "messages.json"), `"value": [`, `"@odata.nextLink": "`+f.srv.URL+`/v1.0/me/messages?page=2", "value": [`, 1)))
		case p == "/me/messages/AAMkMSG1" && r.Method == http.MethodGet && r.URL.Query().Get("$select") == "categories":
			_, _ = w.Write([]byte(`{"id":"AAMkMSG1","categories":["Old","Keep"]}`))
		case p == "/me/messages/AAMkMSG1" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(recorded(t, "message-full.json")))
		case strings.HasPrefix(p, "/me/messages/") && r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"id":"AAMkMSG1"}`))
		case strings.HasPrefix(p, "/me/messages/") && r.Method == http.MethodDelete:
			w.WriteHeader(204)
		case p == "/me/outlook/masterCategories":
			_, _ = w.Write([]byte(`{"value":[{"displayName":"Quotes","color":"preset0"},{"displayName":"Urgent","color":"preset1"}]}`))
		case strings.HasSuffix(p, "/createUploadSession"):
			_, _ = w.Write([]byte(`{"uploadUrl":"` + f.srv.URL + `/upload/session-1","expirationDateTime":"2026-12-02T00:00:00Z"}`))
		case strings.HasPrefix(p, "/upload/"):
			f.mu.Lock()
			f.uploaded = append(f.uploaded, r.Header.Get("Content-Range"))
			f.mu.Unlock()
			if r.Header.Get("Authorization") != "" {
				t.Errorf("the upload session got a bearer token: its address carries its own")
			}
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"nextExpectedRanges":[]}`))
		case strings.HasSuffix(p, "/children") && r.Method == http.MethodPost:
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"01NEW","name":"made","size":0}`))
		case strings.HasPrefix(p, "/drives/") && strings.Contains(p, "/items/") && r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"id":"01FILE","name":"renamed.pdf","size":11,"file":{"mimeType":"application/pdf"}}`))
		case strings.HasPrefix(p, "/drives/") && strings.Contains(p, "/items/") && r.Method == http.MethodDelete:
			w.WriteHeader(204)
		case strings.HasSuffix(p, "/content") && r.Method == http.MethodPut:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"01NEW","name":"made","size":5,"file":{"mimeType":"text/plain"}}`))
		case p == "/me/messages":
			_, _ = w.Write([]byte(recorded(t, "messages.json")))
		case p == "/me/drive":
			_, _ = w.Write([]byte(recorded(t, "drive.json")))
		case p == "/me/followedSites":
			_, _ = w.Write([]byte(recorded(t, "followedsites.json")))
		case strings.HasSuffix(p, "/drives") && strings.HasPrefix(p, "/sites/"):
			_, _ = w.Write([]byte(recorded(t, "site-drives.json")))
		case strings.HasSuffix(p, "/root"):
			_, _ = w.Write([]byte(recorded(t, "drive-root.json")))
		case strings.HasSuffix(p, "/children"):
			_, _ = w.Write([]byte(recorded(t, "children.json")))
		case strings.HasSuffix(p, "/content"):
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("hello world"))
		case strings.HasPrefix(p, "/drives/") && strings.Contains(p, "/items/"):
			_, _ = w.Write([]byte(`{"id":"01FILE","name":"quote-118.pdf","size":11,"file":{"mimeType":"application/pdf"},"webUrl":"https://x/f"}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":"itemNotFound","message":"no"}}`))
		}
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
			token(w, r)
			return
		}
		graph(w, r)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMS) last() call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

type fx struct {
	svc  *Service
	db   *pgxpool.Pool
	fake *fakeMS
	org  string
	dana string
}

func setup(t *testing.T) *fx {
	t.Helper()
	dsn := pgtest.Database(t, "hearth_test_m365")
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	key := make([]byte, secret.KeyBytes)
	for i := range key {
		key[i] = byte(i + 1)
	}
	box, err := secret.New(key)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeMS(t)
	f := &fx{db: pools.Web, fake: fake, org: uuid.NewString(), dana: uuid.NewString()}
	f.svc = New(pools.Web, Config{ClientID: "cid", ClientSecret: "csecret",
		RedirectURL: "https://hearth.example.com/integrations/microsoft/callback"}, box)
	f.svc.authBase = fake.srv.URL
	f.svc.graphBase = fake.srv.URL + "/v1.0"
	f.svc.http = fake.srv.Client()
	for _, q := range [][]any{
		{`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'MS Test')`, f.org, "m-" + f.org[:8]},
		{`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Dana')`, f.dana, f.org},
	} {
		if _, err := pools.Web.Exec(ctx, q[0].(string), q[1:]...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM microsoft_connections WHERE org_id=$1`, `SELECT audit_purge_org($1)`,
			`DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`} {
			_, _ = pools.Web.Exec(context.Background(), q, f.org)
		}
	})
	return f
}

func (f *fx) connect(t *testing.T) {
	t.Helper()
	if err := f.svc.Connect(context.Background(), f.org, f.dana, tenant, "the-code", "verifier-123"); err != nil {
		t.Fatalf("connect: %v", err)
	}
}

func (f *fx) session(t *testing.T) connect.Session {
	t.Helper()
	s, err := f.svc.Session(context.Background(), f.dana)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	return s
}

// ---- sign-in and tokens ---------------------------------------------------

func TestTheAuthorisationURLGoesToTheTenantWithPKCEAndOnlyTheScopesAskedFor(t *testing.T) {
	f := setup(t)
	raw := f.svc.AuthURL(tenant, "state-1", "verifier-1", []string{ScopeCalendar})
	u, _ := url.Parse(raw)
	if !strings.HasSuffix(u.Path, "/"+tenant+"/oauth2/v2.0/authorize") {
		t.Errorf("the sign-in goes to %s, not the connected tenant's authority", u.Path)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Error("no PKCE challenge")
	}
	sc := strings.Fields(q.Get("scope"))
	for _, want := range []string{"offline_access", "User.Read", ScopeCalendar} {
		if !contains(sc, want) {
			t.Errorf("scope %q is missing from %v", want, sc)
		}
	}
	for _, not := range []string{ScopeMail, ScopeMailSend, ScopeContacts, ScopeFiles} {
		if contains(sc, not) {
			t.Errorf("scope %q was asked for and the person only chose the calendar", not)
		}
	}
	if q.Get("redirect_uri") != "https://hearth.example.com/integrations/microsoft/callback" {
		t.Errorf("redirect_uri %q", q.Get("redirect_uri"))
	}
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func TestConnectingStoresTokensSealedAndTheGrantAsMicrosoftReportedIt(t *testing.T) {
	f := setup(t)
	f.connect(t)
	var access, refresh []byte
	var email, tid string
	var scopes []string
	if err := f.db.QueryRow(context.Background(),
		`SELECT access_token, refresh_token, email::text, tenant_id, scopes FROM microsoft_connections WHERE principal_id=$1`, f.dana).
		Scan(&access, &refresh, &email, &tid, &scopes); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(access), "at-1") || strings.Contains(string(refresh), "rt-1") {
		t.Fatal("a token is stored in the clear")
	}
	if email != "dana@contoso.test" || tid != tenant {
		t.Errorf("email %q tenant %q", email, tid)
	}
	if !contains(scopes, "Calendars.ReadWrite") || contains(scopes, ScopeMail) {
		t.Errorf("granted scopes %v: Microsoft's full-URI spelling should be normalised, and mail was never granted", scopes)
	}
	got, err := f.svc.Granted(context.Background(), f.dana)
	if err != nil || !connect.HasScopes(got, f.svc.Scopes(connect.Calendar)) || connect.HasScopes(got, f.svc.Scopes(connect.Mail)) {
		t.Fatalf("granted %v %v", got, err)
	}
}

func TestAnAccountFromAnotherTenantIsRefused(t *testing.T) {
	f := setup(t)
	f.fake.tenantOfID = "11111111-2222-3333-4444-555555555555"
	err := f.svc.Connect(context.Background(), f.org, f.dana, tenant, "the-code", "v")
	if !errors.Is(err, connect.ErrWrongTenant) {
		t.Fatalf("got %v, want ErrWrongTenant", err)
	}
	if _, err := f.svc.Granted(context.Background(), f.dana); !errors.Is(err, connect.ErrNotConnected) {
		t.Fatal("a refused account was stored anyway")
	}
}

func TestAnExpiredTokenIsRefreshedAndMicrosoftsRotatedRefreshTokenReplacesTheOld(t *testing.T) {
	f := setup(t)
	f.connect(t)
	ctx := context.Background()
	if _, err := f.db.Exec(ctx, `UPDATE microsoft_connections SET expires_at = now() - interval '1 minute' WHERE principal_id=$1`, f.dana); err != nil {
		t.Fatal(err)
	}
	tok, err := f.svc.token(ctx, f.dana)
	if err != nil || tok != "at-2" {
		t.Fatalf("token %q %v, want the refreshed one", tok, err)
	}
	var sealed []byte
	_ = f.db.QueryRow(ctx, `SELECT refresh_token FROM microsoft_connections WHERE principal_id=$1`, f.dana).Scan(&sealed)
	if got, _ := f.svc.box.OpenString(sealed, "m365:"+f.dana); got != "rt-2" {
		t.Fatalf("the stored refresh token is %q, want the rotated rt-2", got)
	}
	// A fresh token is not refreshed again.
	if tok, _ := f.svc.token(ctx, f.dana); tok != "at-2" || f.fake.refreshes != 1 {
		t.Fatalf("refreshes %d", f.fake.refreshes)
	}
}

func TestARevokedGrantIsMarkedAndAsksForReconnection(t *testing.T) {
	f := setup(t)
	f.connect(t)
	ctx := context.Background()
	_, _ = f.db.Exec(ctx, `UPDATE microsoft_connections SET expires_at = now() - interval '1 minute' WHERE principal_id=$1`, f.dana)
	f.fake.grantValid = false
	if _, err := f.svc.token(ctx, f.dana); !errors.Is(err, connect.ErrReconnect) {
		t.Fatalf("got %v", err)
	}
	if _, err := f.svc.Granted(ctx, f.dana); !errors.Is(err, connect.ErrReconnect) {
		t.Fatal("a revoked connection still reports as granted")
	}
	conns, _ := f.svc.Connections(ctx, f.org)
	if len(conns) != 1 || !conns[0].Revoked || conns[0].LastError == "" {
		t.Fatalf("connections %+v", conns)
	}
}

func TestDisconnectingDeletesTheTokens(t *testing.T) {
	f := setup(t)
	f.connect(t)
	if err := f.svc.Disconnect(context.Background(), f.dana); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Granted(context.Background(), f.dana); !errors.Is(err, connect.ErrNotConnected) {
		t.Fatal("tokens survived a disconnect")
	}
}

func TestAdminConsentGrantsOnlyTheScopesOfTheConnectorsThatAreOn(t *testing.T) {
	f := setup(t)
	u, _ := url.Parse(f.svc.AdminConsentURL(tenant, "s", []string{ScopeMail}))
	if !strings.HasSuffix(u.Path, "/"+tenant+"/v2.0/adminconsent") {
		t.Errorf("path %s", u.Path)
	}
	sc := u.Query().Get("scope")
	if !strings.Contains(sc, ScopeMail) || strings.Contains(sc, ScopeCalendar) || strings.Contains(sc, ScopeFiles) {
		t.Errorf("admin consent scope %q", sc)
	}
}

// ---- Graph ----------------------------------------------------------------

func TestCalendarEventsAreReadInUTCAcrossPagesAndCancelledOnesDropped(t *testing.T) {
	f := setup(t)
	f.connect(t)
	ctx := context.Background()
	evs, err := f.session(t).Calendar().Events(ctx, "primary",
		time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC), time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2: page one has a real one and a cancelled one, page two has the all-day", len(evs))
	}
	a := evs[0]
	if a.Title != "Plant review" || a.Notes != "Agenda: line 3 downtime" || a.Place != "Room 4" || a.Version != "DfUb2BvDcEmM7UUQc3a2xwAAAAAgSw==" ||
		!a.Start.Equal(time.Date(2026, 12, 2, 9, 0, 0, 0, time.UTC)) || !strings.Contains(a.JoinURL, "teams.microsoft.com") {
		t.Errorf("event %+v", a)
	}
	d := evs[1]
	if !d.AllDay || !d.Start.Equal(time.Date(2026, 12, 3, 0, 0, 0, 0, time.UTC)) || !d.End.Equal(time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("all-day event %+v", d)
	}
	first := f.fake.calls[0]
	if first.auth != "Bearer at-1" {
		t.Errorf("Authorization %q", first.auth)
	}
	if !strings.Contains(first.prefer, `outlook.timezone="UTC"`) || !strings.Contains(first.prefer, `outlook.body-content-type="text"`) {
		t.Errorf("Prefer %q: without these Graph answers in the mailbox's time zone and in HTML", first.prefer)
	}
	if !strings.Contains(first.query, "startDateTime") || !strings.Contains(first.query, "endDateTime") {
		t.Errorf("query %q has no time window", first.query)
	}
}

func TestACreatedEventIsSentInTheShapeGraphDocumentsWithATeamsLinkWhenAsked(t *testing.T) {
	f := setup(t)
	f.connect(t)
	ctx := context.Background()
	out, err := f.session(t).Calendar().Create(ctx, "primary", connect.Event{Title: "Safety walk", Notes: "east bay", Place: "Bay 2",
		Start: time.Date(2026, 12, 4, 13, 0, 0, 0, time.UTC), End: time.Date(2026, 12, 4, 14, 0, 0, 0, time.UTC)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != "AAMkNEW=" || out.Version != "ck-new" || !strings.Contains(out.JoinURL, "meeting_new") {
		t.Errorf("created %+v", out)
	}
	c := f.fake.last()
	if c.method != "POST" || c.path != "/v1.0/me/events" {
		t.Fatalf("%s %s", c.method, c.path)
	}
	var body struct {
		Subject                   string
		Body                      struct{ ContentType, Content string }
		Start                     struct{ DateTime, TimeZone string }
		End                       struct{ DateTime, TimeZone string }
		Loc                       struct{ DisplayName string } `json:"location"`
		IsAllDay, IsOnlineMeeting bool
		OnlineMeetingProvider     string
	}
	if err := json.Unmarshal([]byte(c.body), &body); err != nil {
		t.Fatalf("body %s: %v", c.body, err)
	}
	if body.Subject != "Safety walk" || body.Body.ContentType != "text" || body.Body.Content != "east bay" ||
		body.Start.DateTime != "2026-12-04T13:00:00.0000000" || body.Start.TimeZone != "UTC" || body.Loc.DisplayName != "Bay 2" ||
		!body.IsOnlineMeeting || body.OnlineMeetingProvider != "teamsForBusiness" {
		t.Errorf("event body %s", c.body)
	}
	// Without the request, no meeting.
	if _, err := f.session(t).Calendar().Create(ctx, "primary", connect.Event{Title: "x", Start: time.Now(), End: time.Now().Add(time.Hour)}, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.fake.last().body, "isOnlineMeeting\":true") {
		t.Error("a meeting link was added when nobody asked")
	}
}

func TestUpdateDeleteAndTheCalendarList(t *testing.T) {
	f := setup(t)
	f.connect(t)
	ctx := context.Background()
	cal := f.session(t).Calendar()
	cals, err := cal.Calendars(ctx)
	if err != nil || len(cals) != 2 || !cals[0].Primary || !cals[0].Writable || cals[1].Writable || cals[1].Name != "Plant 3 shifts" {
		t.Fatalf("calendars %+v %v", cals, err)
	}
	if _, err := cal.Update(ctx, "primary", connect.Event{ID: "AAMkX=", Title: "t", Start: time.Now(), End: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if c := f.fake.last(); c.method != "PATCH" || !strings.HasSuffix(c.path, "/me/events/AAMkX=") {
		t.Errorf("update went to %s %s", c.method, c.path)
	}
	if err := cal.Delete(ctx, "primary", "AAMkX="); err != nil {
		t.Fatal(err)
	}
	if c := f.fake.last(); c.method != "DELETE" {
		t.Errorf("delete was %s", c.method)
	}
}

func TestContactsMessagesAndFiles(t *testing.T) {
	f := setup(t)
	f.connect(t)
	ctx := context.Background()
	s := f.session(t)

	cs, err := s.Contacts().Contacts(ctx)
	if err != nil || len(cs) != 2 {
		t.Fatalf("contacts %+v %v", cs, err)
	}
	if cs[0].Name != "Priya Shah" || cs[0].Email != "priya@acme.test" || cs[0].Phone != "+1 555 0100" || cs[0].Title != "Buyer" || cs[0].Company != "Acme" || cs[0].Version != "ck1" {
		t.Errorf("contact %+v", cs[0])
	}
	if cs[1].Name != "Marcus Lee" || cs[1].Phone != "+1 555 0122" || cs[1].Email != "" {
		t.Errorf("a contact with no display name, a business phone and no address: %+v", cs[1])
	}

	ms, err := s.Mail().Messages(ctx, time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC), 50)
	if err != nil || len(ms) != 1 {
		t.Fatalf("messages %+v %v", ms, err)
	}
	m := ms[0]
	if m.Subject != "Quote 118" || m.From != "priya@acme.test" || len(m.To) != 1 || m.To[0] != "dana@contoso.test" ||
		m.Snippet != "Please find the quote attached" || !m.Received.Equal(time.Date(2026, 12, 1, 8, 15, 0, 0, time.UTC)) {
		t.Errorf("message %+v", m)
	}
	q, _ := url.ParseQuery(f.fake.last().query)
	if !strings.Contains(q.Get("$filter"), "receivedDateTime ge 2026-11-20") || !strings.Contains(q.Get("$select"), "body") {
		t.Errorf("message query %v: it filters by date and asks for the body", q)
	}

	drives, err := s.Files().Drives(ctx)
	if err != nil || len(drives) != 2 || drives[0].Kind != "personal" || drives[1].Name != "Plant 3 Team / Documents" || drives[1].Kind != "site" {
		t.Fatalf("drives %+v %v", drives, err)
	}
	items, err := s.Files().List(ctx, "b!personal", "")
	if err != nil || len(items) != 2 || !items[0].Folder || items[1].Folder || items[1].MIME != "application/pdf" || items[1].Size != 11 {
		t.Fatalf("items %+v %v", items, err)
	}
	it, body, err := s.Files().Open(ctx, "b!personal", "01FILE", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(body)
	if string(data) != "hello world" || it.Name != "quote-118.pdf" {
		t.Errorf("download %q %+v", data, it)
	}
	if _, _, err := s.Files().Open(ctx, "b!personal", "01FILE", 5); err == nil {
		t.Error("a file over the limit was downloaded")
	}
}

func TestGraphRefusalsBecomeTheErrorsHearthActsOn(t *testing.T) {
	f := setup(t)
	f.connect(t)
	ctx := context.Background()
	cal := f.session(t).Calendar()

	f.fake.status = http.StatusForbidden
	if _, err := cal.Calendars(ctx); !errors.Is(err, connect.ErrScope) {
		t.Errorf("403 gave %v, want ErrScope", err)
	}
	f.fake.status = http.StatusUnauthorized
	if _, err := cal.Calendars(ctx); !errors.Is(err, connect.ErrReconnect) {
		t.Errorf("401 gave %v, want ErrReconnect", err)
	}
	if _, err := f.svc.Granted(ctx, f.dana); !errors.Is(err, connect.ErrReconnect) {
		t.Error("a 401 did not mark the connection revoked")
	}
}

func TestEveryConnectorAsksForReadWriteDelegatedPermissions(t *testing.T) {
	f := setup(t)
	want := map[connect.Kind][]string{
		connect.Calendar: {"Calendars.ReadWrite"}, connect.Contacts: {"Contacts.ReadWrite"},
		connect.Mail: {"Mail.ReadWrite", "Mail.Send"}, connect.Files: {"Files.ReadWrite.All", "Sites.Read.All"}, connect.Chat: nil,
	}
	for k, w := range want {
		got := f.svc.Scopes(k)
		if strings.Join(got, ",") != strings.Join(w, ",") {
			t.Errorf("%s asks for %v, want %v", k, got, w)
		}
	}
}

// ---- writes ---------------------------------------------------------------

func TestContactsAreCreatedEditedAndDeletedAtMicrosoft(t *testing.T) {
	f := setup(t)
	f.connect(t)
	ctx := context.Background()
	api := f.session(t).Contacts()

	out, err := api.Create(ctx, connect.Contact{Name: "Dana Park", Email: "dana.park@acme.test", Phone: "555 0123", Title: "CFO", Company: "Acme"})
	if err != nil || out.ID != "AAMkCONNEW" || out.Version != "ck-new" {
		t.Fatalf("create %+v %v", out, err)
	}
	c := f.fake.last()
	var body struct {
		GivenName, Surname, DisplayName, MobilePhone, JobTitle, CompanyName string
		EmailAddresses                                                      []struct{ Address, Name string }
	}
	if c.method != "POST" || c.path != "/v1.0/me/contacts" || json.Unmarshal([]byte(c.body), &body) != nil {
		t.Fatalf("%s %s %s", c.method, c.path, c.body)
	}
	if body.GivenName != "Dana" || body.Surname != "Park" || body.DisplayName != "Dana Park" || body.MobilePhone != "555 0123" ||
		body.JobTitle != "CFO" || body.CompanyName != "Acme" || len(body.EmailAddresses) != 1 || body.EmailAddresses[0].Address != "dana.park@acme.test" {
		t.Errorf("contact body %s", c.body)
	}
	if upd, err := api.Update(ctx, connect.Contact{ID: "AAMkCON1", Name: "Priya Shah"}); err != nil || upd.Version != "ck-patched" {
		t.Fatalf("update %+v %v", upd, err)
	}
	if c := f.fake.last(); c.method != "PATCH" || !strings.HasSuffix(c.path, "/me/contacts/AAMkCON1") {
		t.Errorf("update went to %s %s", c.method, c.path)
	}
	if err := api.Delete(ctx, "AAMkCON1"); err != nil {
		t.Fatal(err)
	}
	if err := api.Delete(ctx, "gone"); err != nil {
		t.Errorf("deleting what is already gone should succeed, got %v", err)
	}
}

func TestMailIsReadInFullAndSentRepliedForwardedArchivedAndLabelledAsThePerson(t *testing.T) {
	f := setup(t)
	f.connect(t)
	ctx := context.Background()
	mailbox := f.session(t).Mail()

	m, err := mailbox.Get(ctx, "AAMkMSG1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Body != "Hi Dana,\r\n\r\nPlease find the quote.\r\n\r\nPriya" || m.HTML || m.Unread != true || len(m.Cc) != 1 || m.ThreadID != "conv-1" ||
		len(m.Attached) != 1 || m.Attached[0] != "quote-118.pdf" || m.Labels[0] != "Quotes" {
		t.Errorf("message %+v", m)
	}
	if c := f.fake.last(); !strings.Contains(c.prefer, `outlook.body-content-type="text"`) {
		t.Errorf("Prefer %q: without it Graph answers in HTML", c.prefer)
	}

	list, next, err := mailbox.Inbox(ctx, connect.MailQuery{Folder: "sent", Limit: 10})
	if err != nil || len(list) != 1 || !strings.Contains(next, "page=2") {
		t.Fatalf("inbox %+v %q %v", list, next, err)
	}
	if c := f.fake.last(); c.path != "/v1.0/me/mailFolders/sentitems/messages" {
		t.Errorf("the sent folder is read from %s", c.path)
	}
	if _, _, err := mailbox.Inbox(ctx, connect.MailQuery{Cursor: "https://evil.example/steal"}); err == nil {
		t.Error("a cursor that does not point at Graph was followed")
	}
	if _, _, err := mailbox.Inbox(ctx, connect.MailQuery{Search: "invoice"}); err != nil {
		t.Fatal(err)
	}
	if q, _ := url.ParseQuery(f.fake.last().query); q.Get("$search") != `"invoice"` || q.Get("$orderby") != "" {
		t.Errorf("a search is quoted and carries no $orderby, which Graph refuses beside it: %v", q)
	}

	if err := mailbox.Send(ctx, connect.Outgoing{To: []string{"Priya Shah <priya@acme.test>"}, Cc: []string{"ops@contoso.test"}, Subject: "Hello", Body: "<p>hi</p>", HTML: true}); err != nil {
		t.Fatal(err)
	}
	var sm struct {
		Message struct {
			Subject                    string
			Body                       struct{ ContentType, Content string }
			ToRecipients, CcRecipients []struct {
				EmailAddress struct{ Address, Name string }
			}
		}
		SaveToSentItems bool
	}
	c := f.fake.last()
	if c.path != "/v1.0/me/sendMail" || json.Unmarshal([]byte(c.body), &sm) != nil {
		t.Fatalf("%s %s", c.path, c.body)
	}
	if sm.Message.Subject != "Hello" || sm.Message.Body.ContentType != "html" || sm.Message.ToRecipients[0].EmailAddress.Address != "priya@acme.test" ||
		sm.Message.ToRecipients[0].EmailAddress.Name != "Priya Shah" || sm.Message.CcRecipients[0].EmailAddress.Address != "ops@contoso.test" || !sm.SaveToSentItems {
		t.Errorf("sendMail body %s", c.body)
	}

	if err := mailbox.Reply(ctx, "AAMkMSG1", connect.Outgoing{Body: "thanks"}, false); err != nil {
		t.Fatal(err)
	}
	if c := f.fake.last(); !strings.HasSuffix(c.path, "/AAMkMSG1/reply") || !strings.Contains(strings.ToLower(c.body), `"comment":"thanks"`) {
		t.Errorf("reply %s %s", c.path, c.body)
	}
	if err := mailbox.Reply(ctx, "AAMkMSG1", connect.Outgoing{Body: "thanks all"}, true); err != nil {
		t.Fatal(err)
	}
	if c := f.fake.last(); !strings.HasSuffix(c.path, "/replyAll") {
		t.Errorf("reply all went to %s", c.path)
	}
	if err := mailbox.Forward(ctx, "AAMkMSG1", connect.Outgoing{To: []string{"c@d.test"}, Body: "fyi"}); err != nil {
		t.Fatal(err)
	}
	if c := f.fake.last(); !strings.HasSuffix(c.path, "/forward") || !strings.Contains(c.body, "c@d.test") || !strings.Contains(strings.ToLower(c.body), `"comment":"fyi"`) {
		t.Errorf("forward %s %s", c.path, c.body)
	}
	if err := mailbox.Archive(ctx, "AAMkMSG1"); err != nil {
		t.Fatal(err)
	}
	if c := f.fake.last(); !strings.HasSuffix(c.path, "/move") || !strings.Contains(strings.ToLower(c.body), `"destinationid":"archive"`) {
		t.Errorf("archive %s %s", c.path, c.body)
	}
	if err := mailbox.Label(ctx, "AAMkMSG1", []string{"Urgent", "keep"}, []string{"old"}); err != nil {
		t.Fatal(err)
	}
	var lb struct{ Categories []string }
	c = f.fake.last()
	if c.method != "PATCH" || json.Unmarshal([]byte(c.body), &lb) != nil || strings.Join(lb.Categories, ",") != "Keep,Urgent" {
		t.Errorf("categories patch %s %s: Old removed, Keep not duplicated, Urgent added", c.method, c.body)
	}
	if labels, err := mailbox.Labels(ctx); err != nil || len(labels) != 2 || labels[0] != "Quotes" {
		t.Errorf("labels %v %v", labels, err)
	}
	if err := mailbox.MarkRead(ctx, "AAMkMSG1", true); err != nil || !strings.Contains(f.fake.last().body, `"isRead":true`) {
		t.Errorf("mark read: %v %s", err, f.fake.last().body)
	}
	if err := mailbox.Delete(ctx, "AAMkMSG1"); err != nil || f.fake.last().method != "DELETE" {
		t.Errorf("delete: %v", err)
	}
}

func TestFilesAreUploadedReplacedRenamedMovedMadeAndDeletedAtMicrosoft(t *testing.T) {
	f := setup(t)
	f.connect(t)
	ctx := context.Background()
	files := f.session(t).Files()

	it, err := files.Upload(ctx, "b!personal", "01FOLDER", "made", "text/plain", 5, strings.NewReader("hello"))
	if err != nil || it.ID != "01NEW" {
		t.Fatalf("upload %+v %v", it, err)
	}
	f.fake.mu.Lock()
	var sawCreate, sawPut bool
	for _, c := range f.fake.calls {
		if c.method == "POST" && strings.HasSuffix(c.path, "/items/01FOLDER/children") {
			sawCreate = strings.Contains(c.body, `"name":"made"`) && strings.Contains(c.body, `"file"`) && strings.Contains(c.body, "conflictBehavior")
		}
		if c.method == "PUT" && strings.HasSuffix(c.path, "/items/01NEW/content") {
			sawPut = c.body == "hello"
		}
	}
	f.fake.mu.Unlock()
	if !sawCreate || !sawPut {
		t.Errorf("an upload makes the item in the folder, then puts the bytes to its id: create=%v put=%v", sawCreate, sawPut)
	}

	big := strings.Repeat("x", smallUpload+1)
	if _, err := files.Replace(ctx, "b!personal", "01FILE", "application/pdf", int64(len(big)), strings.NewReader(big)); err != nil {
		t.Fatal(err)
	}
	if len(f.fake.uploaded) == 0 || !strings.HasPrefix(f.fake.uploaded[0], "bytes 0-") || !strings.HasSuffix(f.fake.uploaded[len(f.fake.uploaded)-1], "/"+itoa(len(big))) {
		t.Errorf("a file over 4 MB goes through an upload session in ranged pieces: %v", f.fake.uploaded)
	}

	if r, err := files.Rename(ctx, "b!personal", "01FILE", "renamed.pdf"); err != nil || r.Name != "renamed.pdf" {
		t.Fatalf("rename %+v %v", r, err)
	}
	if c := f.fake.last(); c.method != "PATCH" || !strings.Contains(c.body, `"name":"renamed.pdf"`) {
		t.Errorf("rename %s %s", c.method, c.body)
	}
	if _, err := files.Move(ctx, "b!personal", "01FILE", "01FOLDER"); err != nil {
		t.Fatal(err)
	}
	if c := f.fake.last(); c.method != "PATCH" || !strings.Contains(c.body, `"parentReference"`) || !strings.Contains(c.body, `"id":"01FOLDER"`) {
		t.Errorf("move %s %s", c.method, c.body)
	}
	if d, err := files.Mkdir(ctx, "b!personal", "01FOLDER", "Quotes"); err != nil || d.ID != "01NEW" {
		t.Fatalf("mkdir %+v %v", d, err)
	}
	if c := f.fake.last(); !strings.Contains(c.body, `"folder"`) || !strings.Contains(c.body, `"name":"Quotes"`) {
		t.Errorf("mkdir body %s", c.body)
	}
	if err := files.Delete(ctx, "b!personal", "01FILE"); err != nil || f.fake.last().method != "DELETE" {
		t.Fatalf("delete: %v", err)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// A message with no Cc and no Bcc must not carry them as null: Graph answers 400 "A null value was found for the
// property named 'ccRecipients'", found against a real tenant on 2026-11-21.
func TestAMessageWithNoCcOrBccSendsNoNullLists(t *testing.T) {
	f := setup(t)
	f.connect(t)
	if err := f.session(t).Mail().Send(context.Background(), connect.Outgoing{To: []string{"a@b.test"}, Subject: "x", Body: "y"}); err != nil {
		t.Fatal(err)
	}
	body := f.fake.last().body
	if strings.Contains(body, "null") || strings.Contains(body, "ccRecipients") || strings.Contains(body, "bccRecipients") {
		t.Errorf("body %s carries a null or an empty optional recipient list", body)
	}
}
