package google

// The one test that talks to the real Google. It is skipped unless it is handed a real account's access token, so
// CI and a laptop with no account never run it, and the recorded-shape tests in workspace_integration_test.go are
// what runs everywhere else.
//
// To run it, sign in as a Workspace user whose account may use these scopes and take an access token for it, for
// example:
//
//	secrets/access/google-dwd-mint.py --sub account@fireballz.ai \
//	  --scope "https://mail.google.com/ https://www.googleapis.com/auth/calendar https://www.googleapis.com/auth/contacts https://www.googleapis.com/auth/drive" \
//	  -- go test ./internal/google -run Live -v
//
// or take any access token for the four full scopes and set HEARTH_LIVE_GOOGLE_TOKEN yourself:
//	export HEARTH_LIVE_GOOGLE_SHARE_WITH=second.user@example.com     # optional: a second account in the domain
//	export HEARTH_LIVE_GOOGLE_SECOND_TOKEN=...                        # optional: that account's token
//	go test ./internal/google -run Live -v
//
// It is read and write: a throwaway calendar, a message sent to the account itself and replied to, labelled,
// archived and deleted, a throwaway contact, and a throwaway Drive folder. Each is deleted at the end, and the token
// is never printed.

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	gcal "google.golang.org/api/calendar/v3"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
	"google.golang.org/api/people/v1"

	"github.com/Embernet-ai/hearth/internal/connect"
)

func liveSession(t *testing.T, token string) (*session, *gcal.Service) {
	t.Helper()
	ctx := context.Background()
	hc := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token, TokenType: "Bearer"}))
	cal, err := gcal.NewService(ctx, option.WithHTTPClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	ppl, _ := people.NewService(ctx, option.WithHTTPClient(hc))
	gm, _ := gmail.NewService(ctx, option.WithHTTPClient(hc))
	dr, _ := drive.NewService(ctx, option.WithHTTPClient(hc))
	return &session{cal: cal, ppl: ppl, gm: gm, dr: dr}, cal
}

func TestLiveGoogleWorkspace(t *testing.T) {
	token := os.Getenv("HEARTH_LIVE_GOOGLE_TOKEN")
	if token == "" {
		t.Skip("HEARTH_LIVE_GOOGLE_TOKEN not set: this talks to the real Google")
	}
	ctx := context.Background()
	s, cal := liveSession(t, token)

	// A calendar of its own, so nothing real is touched.
	created, err := cal.Calendars.Insert(&gcal.Calendar{Summary: "Hearth live proof " + time.Now().UTC().Format("2006-01-02 15:04"), TimeZone: "UTC"}).Context(ctx).Do()
	if err != nil {
		t.Fatalf("make a throwaway calendar (the token needs the calendar scope): %v", err)
	}
	t.Cleanup(func() { _ = cal.Calendars.Delete(created.Id).Do() })

	// It shows in the calendar list the adapter reads.
	infos, err := s.Calendars(ctx)
	if err != nil {
		t.Fatalf("calendar list: %v", err)
	}
	found := false
	for _, ci := range infos {
		found = found || ci.ID == created.Id
	}
	if !found {
		t.Fatalf("the new calendar is not in the list the adapter returns (%d calendars)", len(infos))
	}

	// Two-way: create through the adapter, read it back, move it, delete it.
	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)
	ev, err := s.Create(ctx, created.Id, evt("Hearth live event", start, start.Add(time.Hour)), true)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Logf("created %s, Meet link present: %v", ev.ID, ev.JoinURL != "")
	got, err := s.Events(ctx, created.Id, start.Add(-time.Hour), start.Add(2*time.Hour))
	if err != nil || len(got) != 1 || got[0].Title != "Hearth live event" {
		t.Fatalf("read back %+v %v", got, err)
	}
	moved := got[0]
	moved.Start, moved.End = start.Add(2*time.Hour), start.Add(3*time.Hour)
	if _, err := s.Update(ctx, created.Id, moved); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, _ := s.Events(ctx, created.Id, start, start.Add(4*time.Hour))
	if len(again) != 1 || !again[0].Start.Equal(start.Add(2*time.Hour)) {
		t.Fatalf("the move did not take: %+v", again)
	}

	// Shared with a second account: visible to them, through their own token if given.
	if with := os.Getenv("HEARTH_LIVE_GOOGLE_SHARE_WITH"); with != "" {
		rule, err := cal.Acl.Insert(created.Id, &gcal.AclRule{Role: "reader", Scope: &gcal.AclRuleScope{Type: "user", Value: with}}).SendNotifications(false).Context(ctx).Do()
		if err != nil {
			t.Fatalf("share: %v", err)
		}
		t.Logf("shared with %s as %s", with, rule.Role)
		if second := os.Getenv("HEARTH_LIVE_GOOGLE_SECOND_TOKEN"); second != "" {
			s2, _ := liveSession(t, second)
			deadline := time.Now().Add(60 * time.Second)
			var seen []string
			for time.Now().Before(deadline) {
				if es, err := s2.Events(ctx, created.Id, start, start.Add(4*time.Hour)); err == nil && len(es) == 1 {
					seen = append(seen, es[0].Title)
					break
				}
				time.Sleep(5 * time.Second)
			}
			if len(seen) != 1 {
				t.Fatal("the second account could not read the shared calendar within a minute")
			}
			t.Logf("the second account reads the shared calendar's event: %s", seen[0])
		}
	}

	if err := s.Delete(ctx, created.Id, ev.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	liveMail(t, ctx, s)
	liveContacts(t, ctx, s)
	liveDrive(t, ctx, s)
}

// liveMail sends a message to the account itself, finds it, opens it in full, replies, labels, archives and deletes it.
func liveMail(t *testing.T, ctx context.Context, s *session) {
	t.Helper()
	mb := mailAdapter{s}
	me, err := s.self(ctx)
	if err != nil {
		t.Fatalf("who am I: %v", err)
	}
	subject := "Hearth live proof " + time.Now().UTC().Format("20060102T150405")
	if err := mb.Send(ctx, connect.Outgoing{To: []string{me}, Subject: subject, Body: "live proof body"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	var found connect.Message
	for i := 0; i < 12 && found.ID == ""; i++ {
		list, _, err := mb.Inbox(ctx, connect.MailQuery{Folder: "all", Search: "subject:\"" + subject + "\"", Limit: 5})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(list) > 0 {
			found = list[0]
			break
		}
		time.Sleep(5 * time.Second)
	}
	if found.ID == "" {
		t.Fatal("the message sent to myself never arrived")
	}
	full, err := mb.Get(ctx, found.ID)
	if err != nil || !strings.Contains(full.Body, "live proof body") {
		t.Fatalf("open %+v %v", full, err)
	}
	t.Logf("mail: sent, found and opened in full (%d chars)", len(full.Body))
	if err := mb.Reply(ctx, found.ID, connect.Outgoing{Body: "live proof reply"}, false); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if err := mb.Label(ctx, found.ID, []string{"Hearth live proof"}, nil); err != nil {
		t.Fatalf("label: %v", err)
	}
	if err := mb.Archive(ctx, found.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := mb.Delete(ctx, found.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	t.Log("mail: replied, labelled, archived and deleted")
}

func liveContacts(t *testing.T, ctx context.Context, s *session) {
	t.Helper()
	api := contactsAdapter{s}
	c, err := api.Create(ctx, connect.Contact{Name: "Hearth Liveproof", Email: "liveproof@example.invalid", Phone: "555 0100", Title: "Test"})
	if err != nil {
		// Reported, not fatal, so the rest of the proof still runs: the usual cause is the People API not being
		// enabled on the Google project, which is a console step and not a fault in Hearth.
		t.Errorf("contact create: %v", err)
		return
	}
	c.Title = "Test, edited"
	upd, err := api.Update(ctx, c)
	if err != nil {
		t.Fatalf("contact update: %v", err)
	}
	all, err := api.Contacts(ctx)
	if err != nil {
		t.Fatalf("contact list: %v", err)
	}
	seen := false
	for _, x := range all {
		seen = seen || (x.ID == upd.ID && x.Title == "Test, edited")
	}
	if !seen {
		t.Error("the edited contact is not in the list with its new title (Google can take a few seconds to show a new contact)")
	}
	if err := api.Delete(ctx, upd.ID); err != nil {
		t.Fatalf("contact delete: %v", err)
	}
	t.Logf("contacts: created, edited, listed (%d total) and deleted", len(all))
}

func liveDrive(t *testing.T, ctx context.Context, s *session) {
	t.Helper()
	fs := filesAdapter{s}
	dir, err := fs.Mkdir(ctx, "root", "", "Hearth live proof "+time.Now().UTC().Format("150405"))
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = fs.Delete(ctx, "root", dir.ID) })
	up, err := fs.Upload(ctx, "root", dir.ID, "proof.txt", "text/plain", 5, strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if _, err := fs.Replace(ctx, "root", up.ID, "text/plain", 6, strings.NewReader("hello!")); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if _, err := fs.Rename(ctx, "root", up.ID, "proof2.txt"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	sub, err := fs.Mkdir(ctx, "root", dir.ID, "inner")
	if err != nil {
		t.Fatalf("mkdir inner: %v", err)
	}
	if _, err := fs.Move(ctx, "root", up.ID, sub.ID); err != nil {
		t.Fatalf("move: %v", err)
	}
	items, err := fs.List(ctx, "root", sub.ID)
	if err != nil || len(items) != 1 || items[0].Name != "proof2.txt" {
		t.Fatalf("list inner %+v %v", items, err)
	}
	_, body, err := fs.Open(ctx, "root", up.ID, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	data, _ := io.ReadAll(body)
	body.Close()
	if string(data) != "hello!" {
		t.Fatalf("content %q", data)
	}
	if err := fs.Delete(ctx, "root", up.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	t.Log("drive: made folders, uploaded, replaced, renamed, moved, read back and deleted")
}

func evt(title string, start, end time.Time) connect.Event {
	return connect.Event{Title: title, Start: start, End: end}
}
