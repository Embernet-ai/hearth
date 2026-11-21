package google

// The one test that talks to the real Google. It is skipped unless it is handed a real account's access token, so
// CI and a laptop with no account never run it, and the recorded-shape tests in workspace_integration_test.go are
// what runs everywhere else.
//
// To run it, sign in as a Workspace user whose account may use these scopes and take an access token for it, for
// example:
//
//	gcloud auth application-default login --scopes=openid,https://www.googleapis.com/auth/calendar,\
//	  https://www.googleapis.com/auth/contacts.readonly,https://www.googleapis.com/auth/drive.readonly
//	export HEARTH_LIVE_GOOGLE_TOKEN=$(gcloud auth application-default print-access-token)
//	export HEARTH_LIVE_GOOGLE_SHARE_WITH=second.user@example.com     # optional: a second account in the domain
//	export HEARTH_LIVE_GOOGLE_SECOND_TOKEN=...                        # optional: that account's token
//	go test ./internal/google -run Live -v
//
// It makes one throwaway calendar, works in it, and deletes it. Nothing is read from or written to the account's
// own primary calendar, and the token is never printed.

import (
	"context"
	"os"
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
		rule, err := cal.Acl.Insert(created.Id, &gcal.AclRule{Role: "reader", Scope: &gcal.AclRuleScope{Type: "user", Value: with}}).Context(ctx).Do()
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

	// Contacts and Drive, read only.
	cs, err := contactsAdapter{s}.Contacts(ctx)
	t.Logf("contacts: %d (%v)", len(cs), err)
	drives, err := s.Drives(ctx)
	t.Logf("drives: %d (%v)", len(drives), err)
	if err == nil {
		items, err := s.List(ctx, "root", "")
		t.Logf("My Drive root: %d items (%v)", len(items), err)
	}
}

func evt(title string, start, end time.Time) connect.Event {
	return connect.Event{Title: title, Start: start, End: end}
}
