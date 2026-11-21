package google

// The Workspace connectors against recorded Google API shapes, through Google's own Go clients.
//
// The bodies below are written in the shapes Google publishes in its API references (Calendar v3 events.list and
// events.insert, People v1 people.connections.list, Gmail v1 users.messages.list and get with format=metadata,
// Drive v3 files.list, get and export), with every value invented. They are not captured from a live domain.
// What the tests check is that the official clients parse those shapes, and that what is sent is what the
// references say: the scopes, the force-sent empty fields on a patch, the conference request, the label ids.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/connect"
)

type wsFake struct {
	srv     *httptest.Server
	mu      sync.Mutex
	calls   []string
	bodies  map[string]string
	hd      string
	revoked int
	status  int
}

func hdToken(sub, email, hd string) string {
	m := map[string]string{"sub": sub, "email": email}
	if hd != "" {
		m["hd"] = hd
	}
	b, _ := json.Marshal(m)
	return "x." + base64.RawURLEncoding.EncodeToString(b) + ".y"
}

const (
	calList = `{"kind":"calendar#calendarList","items":[
	  {"id":"dana@example.com","summary":"dana@example.com","primary":true,"accessRole":"owner"},
	  {"id":"c_shared1@group.calendar.google.com","summary":"Plant 3 shifts","summaryOverride":"Shifts","accessRole":"reader"}]}`
	evPage1 = `{"kind":"calendar#events","nextPageToken":"p2","items":[
	  {"id":"e1","etag":"\"3181161784712000\"","status":"confirmed","summary":"Plant review","description":"agenda","location":"Room 4",
	   "start":{"dateTime":"2026-12-02T09:00:00-05:00"},"end":{"dateTime":"2026-12-02T10:00:00-05:00"},
	   "hangoutLink":"https://meet.google.com/abc-defg-hij"},
	  {"id":"e2","etag":"\"x\"","status":"cancelled","summary":"Dropped","start":{"dateTime":"2026-12-02T11:00:00Z"},"end":{"dateTime":"2026-12-02T12:00:00Z"}}]}`
	evPage2 = `{"kind":"calendar#events","items":[
	  {"id":"e3","etag":"\"y\"","status":"confirmed","summary":"Shutdown","start":{"date":"2026-12-03"},"end":{"date":"2026-12-05"}},
	  {"id":"e4","etag":"\"z\"","status":"confirmed","summary":"Vendor call",
	   "start":{"dateTime":"2026-12-04T15:00:00Z"},"end":{"dateTime":"2026-12-04T16:00:00Z"},
	   "conferenceData":{"entryPoints":[{"entryPointType":"phone","uri":"tel:+1"},{"entryPointType":"video","uri":"https://meet.google.com/zzz"}]}}]}`
	conns1 = `{"connections":[
	  {"resourceName":"people/c1","etag":"%etag1","names":[{"displayName":"Priya Shah"}],
	   "emailAddresses":[{"value":"priya@acme.test"}],"phoneNumbers":[{"value":"+1 555 0100"}],
	   "organizations":[{"name":"Acme","title":"Buyer"}]}],"nextPageToken":"n2","totalPeople":2}`
	conns2  = `{"connections":[{"resourceName":"people/c2","etag":"%etag2","names":[{"displayName":"Marcus Lee"}]}],"totalPeople":2}`
	msgList = `{"messages":[{"id":"m1","threadId":"t1"},{"id":"m2","threadId":"t2"}],"resultSizeEstimate":2}`
	driveL  = `{"files":[
	  {"id":"fo1","name":"Quotes","mimeType":"application/vnd.google-apps.folder","webViewLink":"https://drive.google.com/drive/folders/fo1","modifiedTime":"2026-11-30T16:00:00.000Z"},
	  {"id":"fi1","name":"quote-118.pdf","mimeType":"application/pdf","size":"11","webViewLink":"https://drive.google.com/file/d/fi1/view","modifiedTime":"2026-11-30T16:00:00.000Z"}]}`
)

func msgMeta(id string, ms int64, from, to, subj string) string {
	body := base64.URLEncoding.EncodeToString([]byte("Hi Dana,\n\nPlease find the quote.\n\nPriya"))
	html := base64.URLEncoding.EncodeToString([]byte("<p>Hi Dana</p>"))
	return `{"id":"` + id + `","threadId":"th-` + id + `","snippet":"Please find the quote","internalDate":"` + itoa(ms) + `","labelIds":["INBOX","UNREAD"],
	  "payload":{"mimeType":"multipart/alternative","headers":[{"name":"From","value":"` + from + `"},{"name":"To","value":"` + to + `"},
	    {"name":"Cc","value":"ops@example.com"},{"name":"Subject","value":"` + subj + `"},{"name":"Message-ID","value":"<` + id + `@mail.example>"},
	    {"name":"References","value":"<earlier@mail.example>"}],
	    "parts":[{"mimeType":"text/plain","body":{"data":"` + body + `"}},{"mimeType":"text/html","body":{"data":"` + html + `"}},
	             {"mimeType":"application/pdf","filename":"quote-118.pdf","body":{"attachmentId":"a1"}}]}}`
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func newWSFake(t *testing.T) *wsFake {
	f := &wsFake{bodies: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "refresh_token": "rt-1", "expires_in": 3600,
			"scope": ScopeCalendarWrite + " " + ScopeCalendarList + " " + ScopeContacts, "id_token": hdToken("sub-1", "dana@example.com", f.hd)})
	})
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) { f.revoked++ })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		key := r.Method + " " + r.URL.Path
		f.calls = append(f.calls, key+"?"+r.URL.RawQuery)
		f.bodies[key] = string(b)
		st := f.status
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer at-1" {
			w.WriteHeader(401)
			return
		}
		if st != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(st)
			_, _ = w.Write([]byte(`{"error":{"code":` + itoa(int64(st)) + `,"message":"x","status":"X"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		p := r.URL.Path
		switch {
		case p == "/v1/people:createContact" && r.Method == "POST":
			_, _ = w.Write([]byte(`{"resourceName":"people/cNEW","etag":"etag-new","names":[{"displayName":"Dana Park"}],"emailAddresses":[{"value":"dana.park@acme.test"}]}`))
		case strings.HasSuffix(p, ":updateContact") && r.Method == "PATCH":
			_, _ = w.Write([]byte(`{"resourceName":"people/c1","etag":"etag-patched","names":[{"displayName":"Priya Shah"}]}`))
		case strings.HasSuffix(p, ":deleteContact") && r.Method == "DELETE":
			if strings.Contains(p, "/gone:") {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":{"code":404,"message":"x"}}`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(p, "/users/me/profile"):
			_, _ = w.Write([]byte(`{"emailAddress":"dana@example.com"}`))
		case strings.HasSuffix(p, "/users/me/messages/send") && r.Method == "POST":
			_, _ = w.Write([]byte(`{"id":"sent1","threadId":"th-m1"}`))
		case strings.HasSuffix(p, "/modify") && r.Method == "POST":
			_, _ = w.Write([]byte(`{"id":"m1"}`))
		case strings.HasSuffix(p, "/trash") && r.Method == "POST":
			_, _ = w.Write([]byte(`{"id":"m1"}`))
		case strings.HasSuffix(p, "/users/me/labels") && r.Method == "GET":
			_, _ = w.Write([]byte(`{"labels":[{"id":"INBOX","name":"INBOX","type":"system"},{"id":"Label_1","name":"Quotes","type":"user"},{"id":"Label_2","name":"Old","type":"user"}]}`))
		case strings.HasSuffix(p, "/users/me/labels") && r.Method == "POST":
			_, _ = w.Write([]byte(`{"id":"Label_9","name":"Urgent","type":"user"}`))
		case strings.HasSuffix(p, "/upload/drive/v3/files") && r.Method == "POST", strings.Contains(p, "/drive/v3/files") && r.Method == "POST":
			_, _ = w.Write([]byte(`{"id":"newfile","name":"plan.txt","mimeType":"text/plain","size":"5"}`))
		case strings.Contains(p, "/files/") && r.Method == "PATCH":
			_, _ = w.Write([]byte(`{"id":"fi1","name":"renamed.pdf","mimeType":"application/pdf","size":"11"}`))
		case strings.HasSuffix(p, "/users/me/calendarList"):
			_, _ = w.Write([]byte(calList))
		case strings.HasSuffix(p, "/events") && r.Method == "GET":
			if q.Get("pageToken") == "p2" {
				_, _ = w.Write([]byte(evPage2))
			} else {
				_, _ = w.Write([]byte(evPage1))
			}
		case strings.HasSuffix(p, "/events") && r.Method == "POST":
			_, _ = w.Write([]byte(`{"id":"new1","etag":"\"n\"","summary":"Safety walk","start":{"dateTime":"2026-12-04T13:00:00Z"},"end":{"dateTime":"2026-12-04T14:00:00Z"},"hangoutLink":"https://meet.google.com/new"}`))
		case strings.Contains(p, "/events/") && r.Method == "PATCH":
			_, _ = w.Write([]byte(`{"id":"e1","etag":"\"p\"","summary":"t","start":{"dateTime":"2026-12-04T13:00:00Z"},"end":{"dateTime":"2026-12-04T14:00:00Z"}}`))
		case strings.Contains(p, "/events/") && r.Method == "DELETE":
			if strings.HasSuffix(p, "/gone") {
				w.WriteHeader(410)
				_, _ = w.Write([]byte(`{"error":{"code":410,"message":"Resource has been deleted"}}`))
				return
			}
			w.WriteHeader(204)
		case strings.HasSuffix(p, "/people/me/connections"):
			if q.Get("pageToken") == "n2" {
				_, _ = w.Write([]byte(strings.ReplaceAll(conns2, "%etag2", "e2")))
			} else {
				_, _ = w.Write([]byte(strings.ReplaceAll(conns1, "%etag1", "e1")))
			}
		case strings.HasSuffix(p, "/users/me/messages"):
			_, _ = w.Write([]byte(msgList))
		case strings.HasSuffix(p, "/users/me/messages/m1"):
			_, _ = w.Write([]byte(msgMeta("m1", time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC).UnixMilli(), "Priya Shah <priya@acme.test>", "dana@example.com, ops@example.com", "Quote 118")))
		case strings.HasSuffix(p, "/users/me/messages/m2"):
			_, _ = w.Write([]byte(msgMeta("m2", time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC).UnixMilli(), "old@acme.test", "dana@example.com", "Old")))
		case strings.HasSuffix(p, "/drives"):
			_, _ = w.Write([]byte(`{"drives":[{"id":"sd1","name":"Plant 3 drive"}]}`))
		case strings.HasSuffix(p, "/files") && r.Method == "GET":
			_, _ = w.Write([]byte(driveL))
		case strings.HasSuffix(p, "/files/fi1") && q.Get("alt") == "media":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("hello world"))
		case strings.HasSuffix(p, "/files/fi1"):
			_, _ = w.Write([]byte(`{"id":"fi1","name":"quote-118.pdf","mimeType":"application/pdf","size":"11","webViewLink":"https://drive.google.com/file/d/fi1/view"}`))
		case strings.HasSuffix(p, "/files/doc1"):
			_, _ = w.Write([]byte(`{"id":"doc1","name":"Plant plan","mimeType":"application/vnd.google-apps.document","webViewLink":"https://docs.google.com/document/d/doc1"}`))
		case strings.HasSuffix(p, "/files/doc1/export"):
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("docx-bytes"))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"no"}}`))
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func wsSetup(t *testing.T, hd string) (*fx, *wsFake, connect.Session) {
	t.Helper()
	f := setup(t)
	w := newWSFake(t)
	w.hd = hd
	f.svc.tokenURL = w.srv.URL + "/token"
	f.svc.revokeURL = w.srv.URL + "/revoke"
	f.svc.http = w.srv.Client()
	f.svc.api = apiEndpoints{
		calendar: w.srv.URL + "/calendar/v3/", people: w.srv.URL + "/", gmail: w.srv.URL + "/", drive: w.srv.URL + "/drive/v3/",
	}
	ctx := context.Background()
	if _, err := f.svc.ConnectIn(ctx, f.org, f.dana, "code", "example.com"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	s, err := f.svc.Session(ctx, f.dana)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	return f, w, s
}

func TestAWorkspaceAccountMustBelongToTheConnectedDomain(t *testing.T) {
	f := setup(t)
	w := newWSFake(t)
	f.svc.tokenURL, f.svc.revokeURL, f.svc.http = w.srv.URL+"/token", w.srv.URL+"/revoke", w.srv.Client()
	ctx := context.Background()

	w.hd = "" // a personal Gmail account has no hd claim
	if _, err := f.svc.ConnectIn(ctx, f.org, f.dana, "code", "example.com"); !errors.Is(err, connect.ErrWrongTenant) {
		t.Fatalf("a personal account got %v", err)
	}
	w.hd = "other.com"
	if _, err := f.svc.ConnectIn(ctx, f.org, f.dana, "code", "example.com"); !errors.Is(err, connect.ErrWrongTenant) {
		t.Fatalf("another company's domain got %v", err)
	}
	if w.revoked != 2 {
		t.Errorf("%d grants were withdrawn at Google, want 2: a refused account must not keep a live grant", w.revoked)
	}
	if _, err := f.svc.Get(ctx, f.dana); !errors.Is(err, ErrNotConnected) {
		t.Fatal("a refused account was stored")
	}
	w.hd = "EXAMPLE.com"
	if _, err := f.svc.ConnectIn(ctx, f.org, f.dana, "code", "example.com"); err != nil {
		t.Fatalf("the right domain, in another case, got %v", err)
	}
}

func TestTheDomainHintAndTheScopesOfTheConnectorsAreWhatTheURLAsksFor(t *testing.T) {
	f := setup(t)
	u, _ := url.Parse(f.svc.AuthURLFor("s", f.svc.Scopes(connect.Calendar), "example.com"))
	q := u.Query()
	if q.Get("hd") != "example.com" || q.Get("access_type") != "offline" || q.Get("prompt") != "consent" {
		t.Errorf("query %v", q)
	}
	sc := q.Get("scope")
	if !strings.Contains(sc, ScopeCalendarFull) || strings.Contains(sc, "gmail") || strings.Contains(sc, "drive") {
		t.Errorf("scope %q", sc)
	}
}

func TestTheFullScopesCoverTheNarrowerOnesAndAReadOnlyGrantDoesNotCoverTheFullOnes(t *testing.T) {
	got := expand([]string{ScopeCalendarFull, ScopeContactsFull, ScopeMailFull, ScopeDriveFull})
	if !connect.HasScopes(got, []string{ScopeCalendarWrite, ScopeCalendarList, ScopeContacts, ScopeGmailSend, ScopeGmailMeta, ScopeDriveRead}) {
		t.Errorf("the full scopes should cover the narrower ones: %v", got)
	}
	old := expand([]string{ScopeCalendarRead, ScopeGmailRead, ScopeContacts, ScopeDriveRead})
	for _, full := range []string{ScopeCalendarFull, ScopeMailFull, ScopeContactsFull, ScopeDriveFull} {
		if connect.HasScopes(old, []string{full}) {
			t.Errorf("a read-only grant was treated as covering %s: the person has to connect again", full)
		}
	}
}

func TestCalendarEventsAcrossPagesWithMeetLinksAndAllDayDates(t *testing.T) {
	_, w, s := wsSetup(t, "example.com")
	ctx := context.Background()
	cals, err := s.Calendar().Calendars(ctx)
	if err != nil || len(cals) != 2 || !cals[0].Primary || !cals[0].Writable || cals[1].Name != "Shifts" || cals[1].Writable {
		t.Fatalf("calendars %+v %v", cals, err)
	}
	evs, err := s.Calendar().Events(ctx, "primary", time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC), time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(evs) != 3 {
		t.Fatalf("events %+v %v: want 3 over two pages with the cancelled one dropped", evs, err)
	}
	a := evs[0]
	if a.Title != "Plant review" || !a.Start.Equal(time.Date(2026, 12, 2, 14, 0, 0, 0, time.UTC)) || a.JoinURL != "https://meet.google.com/abc-defg-hij" || a.Version != `"3181161784712000"` {
		t.Errorf("event %+v", a)
	}
	if d := evs[1]; !d.AllDay || !d.Start.Equal(time.Date(2026, 12, 3, 0, 0, 0, 0, time.UTC)) || !d.End.Equal(time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("all-day %+v", d)
	}
	if evs[2].JoinURL != "https://meet.google.com/zzz" {
		t.Errorf("the video entry point should be the join link: %+v", evs[2])
	}
	q := w.calls[1]
	for _, want := range []string{"singleEvents=true", "showDeleted=false", "timeMin=", "timeMax="} {
		if !strings.Contains(q, want) {
			t.Errorf("events query %q lacks %s", q, want)
		}
	}
}

func TestCreateAsksForAMeetLinkOnlyWhenToldAndPatchSendsEmptyFields(t *testing.T) {
	_, w, s := wsSetup(t, "example.com")
	ctx := context.Background()
	ev := connect.Event{Title: "Safety walk", Notes: "east bay", Place: "Bay 2",
		Start: time.Date(2026, 12, 4, 13, 0, 0, 0, time.UTC), End: time.Date(2026, 12, 4, 14, 0, 0, 0, time.UTC)}
	out, err := s.Calendar().Create(ctx, "primary", ev, true)
	if err != nil || out.ID != "new1" || out.JoinURL != "https://meet.google.com/new" {
		t.Fatalf("create %+v %v", out, err)
	}
	if !strings.Contains(w.calls[len(w.calls)-1], "conferenceDataVersion=1") {
		t.Errorf("no conferenceDataVersion on %q: Google ignores the request without it", w.calls[len(w.calls)-1])
	}
	body := w.bodies["POST /calendar/v3/calendars/primary/events"]
	if !strings.Contains(body, `"hangoutsMeet"`) || !strings.Contains(body, `"createRequest"`) || !strings.Contains(body, `"summary":"Safety walk"`) {
		t.Errorf("body %s", body)
	}
	if _, err := s.Calendar().Create(ctx, "primary", ev, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.bodies["POST /calendar/v3/calendars/primary/events"], "conferenceData") {
		t.Error("a meeting was requested when nobody asked")
	}

	// A person who clears the notes and the place in Hearth has to clear them at Google, so empty strings are sent.
	ev.ID, ev.Notes, ev.Place = "e1", "", ""
	if _, err := s.Calendar().Update(ctx, "primary", ev); err != nil {
		t.Fatal(err)
	}
	pb := w.bodies["PATCH /calendar/v3/calendars/primary/events/e1"]
	if !strings.Contains(pb, `"description":""`) || !strings.Contains(pb, `"location":""`) {
		t.Errorf("patch body %s drops cleared fields", pb)
	}
	if err := s.Calendar().Delete(ctx, "primary", "gone"); err != nil {
		t.Errorf("deleting what is already gone should succeed, got %v", err)
	}
}

func TestContactsAcrossPages(t *testing.T) {
	_, _, s := wsSetup(t, "example.com")
	cs, err := s.Contacts().Contacts(context.Background())
	if err != nil || len(cs) != 2 {
		t.Fatalf("contacts %+v %v", cs, err)
	}
	if c := cs[0]; c.ID != "people/c1" || c.Version != "e1" || c.Name != "Priya Shah" || c.Email != "priya@acme.test" || c.Phone != "+1 555 0100" || c.Company != "Acme" || c.Title != "Buyer" {
		t.Errorf("contact %+v", c)
	}
}

func TestMailIsReadInFullFromTheInboxAndSentAndStopsAtTheCutoff(t *testing.T) {
	_, w, s := wsSetup(t, "example.com")
	ms, err := s.Mail().Messages(context.Background(), time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Fatalf("messages %+v: one from the inbox label and one from sent, the October one cut off", ms)
	}
	m := ms[0]
	if m.Subject != "Quote 118" || m.From != "Priya Shah <priya@acme.test>" || len(m.To) != 2 || len(m.Cc) != 1 || m.Snippet != "Please find the quote" ||
		m.Body != "Hi Dana,\n\nPlease find the quote.\n\nPriya" || m.HTML || !m.Unread || m.ThreadID != "th-m1" ||
		len(m.Attached) != 1 || m.Attached[0] != "quote-118.pdf" || !strings.Contains(m.WebLink, "m1") {
		t.Errorf("message %+v", m)
	}
	var sawInbox, sawSent, sawFull bool
	for _, c := range w.calls {
		sawInbox = sawInbox || strings.Contains(c, "labelIds=INBOX")
		sawSent = sawSent || strings.Contains(c, "labelIds=SENT")
		sawFull = sawFull || strings.Contains(c, "format=full")
	}
	if !sawInbox || !sawSent || !sawFull {
		t.Errorf("inbox=%v sent=%v full=%v", sawInbox, sawSent, sawFull)
	}
}

func rawOf(t *testing.T, body string) (headers map[string]string, text string) {
	t.Helper()
	var m struct{ Raw, ThreadId string }
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	raw, err := base64.URLEncoding.DecodeString(m.Raw)
	if err != nil {
		t.Fatalf("raw: %v", err)
	}
	head, rest, _ := strings.Cut(string(raw), "\r\n\r\n")
	headers = map[string]string{"threadId": m.ThreadId}
	for _, line := range strings.Split(head, "\r\n") {
		k, v, _ := strings.Cut(line, ": ")
		headers[k] = v
	}
	dec, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(rest, "\r\n", ""))
	return headers, string(dec)
}

func TestMailIsSentRepliedForwardedArchivedLabelledAndDeletedAsThePerson(t *testing.T) {
	_, w, s := wsSetup(t, "example.com")
	ctx := context.Background()
	mailbox := s.Mail()
	send := "POST /gmail/v1/users/me/messages/send"

	if err := mailbox.Send(ctx, connect.Outgoing{To: []string{"a@b.test"}, Cc: []string{"c@d.test"}, Bcc: []string{"e@f.test"}, Subject: "Café quote", Body: "héllo"}); err != nil {
		t.Fatal(err)
	}
	h, text := rawOf(t, w.bodies[send])
	if h["From"] != "dana@example.com" || h["To"] != "a@b.test" || h["Cc"] != "c@d.test" || h["Bcc"] != "e@f.test" || h["MIME-Version"] != "1.0" ||
		!strings.HasPrefix(h["Content-Type"], "text/plain; charset=UTF-8") || text != "héllo" {
		t.Errorf("sent %v %q", h, text)
	}
	if !strings.HasPrefix(h["Subject"], "=?utf-8?") {
		t.Errorf("a subject with a non-ASCII character has to be encoded: %q", h["Subject"])
	}

	// A reply keeps the thread and the references, goes to the sender, quotes the original.
	if err := mailbox.Reply(ctx, "m1", connect.Outgoing{Body: "Thanks Priya"}, false); err != nil {
		t.Fatal(err)
	}
	h, text = rawOf(t, w.bodies[send])
	if h["To"] != `"Priya Shah" <priya@acme.test>` && h["To"] != "Priya Shah <priya@acme.test>" {
		t.Errorf("reply to %q", h["To"])
	}
	if h["Subject"] != "Re: Quote 118" || h["In-Reply-To"] != "<m1@mail.example>" || h["References"] != "<earlier@mail.example> <m1@mail.example>" || h["threadId"] != "th-m1" {
		t.Errorf("reply headers %v", h)
	}
	if !strings.HasPrefix(text, "Thanks Priya") || !strings.Contains(text, "> Please find the quote.") {
		t.Errorf("reply body %q", text)
	}
	// Reply all keeps the other recipients but not the person.
	if err := mailbox.Reply(ctx, "m1", connect.Outgoing{Body: "all"}, true); err != nil {
		t.Fatal(err)
	}
	h, _ = rawOf(t, w.bodies[send])
	if strings.Contains(h["Cc"], "dana@example.com") || !strings.Contains(h["Cc"], "ops@example.com") {
		t.Errorf("reply all Cc %q: the other people stay and the person does not", h["Cc"])
	}

	if err := mailbox.Forward(ctx, "m1", connect.Outgoing{To: []string{"c@d.test"}, Body: "fyi"}); err != nil {
		t.Fatal(err)
	}
	h, text = rawOf(t, w.bodies[send])
	if h["Subject"] != "Fwd: Quote 118" || h["To"] != "c@d.test" || !strings.Contains(text, "---------- Forwarded message ---------") || !strings.Contains(text, "Please find the quote.") {
		t.Errorf("forward %v %q", h, text)
	}

	mod := "POST /gmail/v1/users/me/messages/m1/modify"
	if err := mailbox.Archive(ctx, "m1"); err != nil || !strings.Contains(w.bodies[mod], `"removeLabelIds":["INBOX"]`) {
		t.Errorf("archive %v %s", err, w.bodies[mod])
	}
	if err := mailbox.MarkRead(ctx, "m1", true); err != nil || !strings.Contains(w.bodies[mod], `"removeLabelIds":["UNREAD"]`) {
		t.Errorf("mark read %v %s", err, w.bodies[mod])
	}
	// Quotes exists, Urgent is made, Old is removed by name.
	if err := mailbox.Label(ctx, "m1", []string{"quotes", "Urgent"}, []string{"Old"}); err != nil {
		t.Fatal(err)
	}
	if b := w.bodies[mod]; !strings.Contains(b, `"addLabelIds":["Label_1","Label_9"]`) || !strings.Contains(b, `"removeLabelIds":["Label_2"]`) {
		t.Errorf("label %s", b)
	}
	if labels, err := mailbox.Labels(ctx); err != nil || len(labels) != 2 {
		t.Errorf("labels %v %v: user labels only", labels, err)
	}
	if err := mailbox.Delete(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	var trashed bool
	for _, c := range w.calls {
		trashed = trashed || strings.HasPrefix(c, "POST /gmail/v1/users/me/messages/m1/trash")
	}
	if !trashed {
		t.Error("delete should move to Trash")
	}

	list, _, err := mailbox.Inbox(ctx, connect.MailQuery{Folder: "archive", Search: "from:priya", Limit: 10})
	if err != nil || len(list) != 2 {
		t.Fatalf("inbox %+v %v", list, err)
	}
	var q string
	for _, c := range w.calls {
		if strings.HasPrefix(c, "GET /gmail/v1/users/me/messages?") && strings.Contains(c, "q=") {
			q, _ = url.QueryUnescape(c)
		}
	}
	if !strings.Contains(q, "-in:inbox") || !strings.Contains(q, "from:priya") {
		t.Errorf("archive search %q", q)
	}
}

func TestContactsAreCreatedEditedAndDeletedAtGoogle(t *testing.T) {
	_, w, s := wsSetup(t, "example.com")
	ctx := context.Background()
	api := s.Contacts()

	out, err := api.Create(ctx, connect.Contact{Name: "Dana Park", Email: "dana.park@acme.test", Phone: "555 0123", Title: "CFO", Company: "Acme"})
	if err != nil || out.ID != "people/cNEW" || out.Version != "etag-new" {
		t.Fatalf("create %+v %v", out, err)
	}
	var p struct {
		Names          []struct{ GivenName, FamilyName string }
		EmailAddresses []struct{ Value string }
		PhoneNumbers   []struct{ Value string }
		Organizations  []struct{ Name, Title string }
	}
	if err := json.Unmarshal([]byte(w.bodies["POST /v1/people:createContact"]), &p); err != nil || p.Names[0].GivenName != "Dana" || p.Names[0].FamilyName != "Park" ||
		p.EmailAddresses[0].Value != "dana.park@acme.test" || p.PhoneNumbers[0].Value != "555 0123" || p.Organizations[0].Title != "CFO" {
		t.Errorf("create body %s", w.bodies["POST /v1/people:createContact"])
	}
	upd, err := api.Update(ctx, connect.Contact{ID: "people/c1", Version: "etag-old", Name: "Priya Shah"})
	if err != nil || upd.Version != "etag-patched" {
		t.Fatalf("update %+v %v", upd, err)
	}
	if b := w.bodies["PATCH /v1/people/c1:updateContact"]; !strings.Contains(b, `"etag":"etag-old"`) {
		t.Errorf("update body %s: Google refuses an edit that does not carry the etag it was read at", b)
	}
	var sawFields bool
	for _, c := range w.calls {
		sawFields = sawFields || (strings.Contains(c, ":updateContact") && strings.Contains(c, "updatePersonFields="))
	}
	if !sawFields {
		t.Error("an update has to say which fields it changes")
	}
	if err := api.Delete(ctx, "people/c1"); err != nil {
		t.Fatal(err)
	}
	if err := api.Delete(ctx, "people/gone"); err != nil {
		t.Errorf("deleting what is already gone should succeed, got %v", err)
	}
}

func TestFilesAreUploadedReplacedRenamedMovedMadeAndTrashedAtGoogle(t *testing.T) {
	_, w, s := wsSetup(t, "example.com")
	ctx := context.Background()
	files := s.Files()

	it, err := files.Upload(ctx, "root", "fo1", "plan.txt", "text/plain", 5, strings.NewReader("hello"))
	if err != nil || it.ID != "newfile" {
		t.Fatalf("upload %+v %v", it, err)
	}
	var up string
	for k, b := range w.bodies {
		if strings.HasPrefix(k, "POST ") && strings.Contains(k, "/upload/drive/v3/files") {
			up = b
		}
	}
	if !strings.Contains(up, `"name":"plan.txt"`) || !strings.Contains(up, `"parents":["fo1"]`) || !strings.Contains(up, "hello") {
		t.Errorf("upload body %q: a multipart request carrying the metadata and the bytes", up)
	}
	if _, err := files.Replace(ctx, "root", "fi1", "application/pdf", 3, strings.NewReader("new")); err != nil {
		t.Fatal(err)
	}
	if r, err := files.Rename(ctx, "root", "fi1", "renamed.pdf"); err != nil || r.Name != "renamed.pdf" {
		t.Fatalf("rename %+v %v", r, err)
	}
	if b := w.bodies["PATCH /drive/v3/files/fi1"]; !strings.Contains(b, `"name":"renamed.pdf"`) {
		t.Errorf("rename body %s", b)
	}
	if _, err := files.Move(ctx, "root", "fi1", "fo2"); err != nil {
		t.Fatal(err)
	}
	var mv string
	for _, c := range w.calls {
		if strings.HasPrefix(c, "PATCH /drive/v3/files/fi1?") && strings.Contains(c, "addParents") {
			mv = c
		}
	}
	if !strings.Contains(mv, "addParents=fo2") || !strings.Contains(mv, "supportsAllDrives=true") {
		t.Errorf("move %q", mv)
	}
	if d, err := files.Mkdir(ctx, "root", "fo1", "Quotes"); err != nil || d.ID != "newfile" {
		t.Fatalf("mkdir %+v %v", d, err)
	}
	if b := w.bodies["POST /drive/v3/files"]; !strings.Contains(b, "application/vnd.google-apps.folder") || !strings.Contains(b, `"parents":["fo1"]`) {
		t.Errorf("mkdir body %s", b)
	}
	if err := files.Delete(ctx, "root", "fi1"); err != nil {
		t.Fatal(err)
	}
	if b := w.bodies["PATCH /drive/v3/files/fi1"]; !strings.Contains(b, `"trashed":true`) {
		t.Errorf("delete should move to the bin: %s", b)
	}
}

func TestDriveListsOpensAndExports(t *testing.T) {
	_, w, s := wsSetup(t, "example.com")
	ctx := context.Background()
	drives, err := s.Files().Drives(ctx)
	if err != nil || len(drives) != 2 || drives[0].ID != "root" || drives[1].Kind != "shared" {
		t.Fatalf("drives %+v %v", drives, err)
	}
	items, err := s.Files().List(ctx, "root", "")
	if err != nil || len(items) != 2 || !items[0].Folder || items[1].Size != 11 || items[1].MIME != "application/pdf" {
		t.Fatalf("items %+v %v", items, err)
	}
	if q := w.calls[len(w.calls)-1]; !strings.Contains(q, "supportsAllDrives=true") || !strings.Contains(q, "%27root%27+in+parents") {
		t.Errorf("list query %q", q)
	}
	it, body, err := s.Files().Open(ctx, "root", "fi1", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(body)
	body.Close()
	if string(data) != "hello world" || it.Name != "quote-118.pdf" {
		t.Errorf("download %q %+v", data, it)
	}
	if _, _, err := s.Files().Open(ctx, "root", "fi1", 5); err == nil {
		t.Error("a file over the limit was downloaded")
	}
	it, body, err = s.Files().Open(ctx, "root", "doc1", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(body)
	body.Close()
	if string(data) != "docx-bytes" || it.Name != "Plant plan.docx" || !strings.Contains(it.MIME, "wordprocessingml") {
		t.Errorf("a Google document should export as Word: %q %+v", data, it)
	}
}

func TestGoogleRefusalsBecomeTheErrorsHearthActsOn(t *testing.T) {
	f, w, s := wsSetup(t, "example.com")
	ctx := context.Background()
	w.status = http.StatusForbidden
	if _, err := s.Calendar().Calendars(ctx); !errors.Is(err, connect.ErrScope) {
		t.Errorf("403 gave %v, want ErrScope", err)
	}
	w.status = http.StatusUnauthorized
	if _, err := s.Calendar().Calendars(ctx); !errors.Is(err, connect.ErrReconnect) {
		t.Errorf("401 gave %v, want ErrReconnect", err)
	}
	if _, err := f.svc.Granted(ctx, f.dana); !errors.Is(err, connect.ErrReconnect) {
		t.Error("a 401 did not mark the connection revoked")
	}
}

func TestEveryConnectorAsksForGooglesFullReadWriteScope(t *testing.T) {
	f := setup(t)
	want := map[connect.Kind][]string{
		connect.Calendar: {"https://www.googleapis.com/auth/calendar"}, connect.Contacts: {"https://www.googleapis.com/auth/contacts"},
		connect.Mail: {"https://mail.google.com/"}, connect.Files: {"https://www.googleapis.com/auth/drive"}, connect.Chat: nil,
	}
	for k, w := range want {
		if got := f.svc.Scopes(k); strings.Join(got, ",") != strings.Join(w, ",") {
			t.Errorf("%s asks for %v, want %v", k, got, w)
		}
	}
}
