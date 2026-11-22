package m365

// The one test that talks to the real Microsoft 365. It is skipped unless it is handed a real account's Graph access
// token, so CI and a laptop with no account never run it, and the recorded-shape tests in m365_integration_test.go are
// what runs everywhere else.
//
// It uses a delegated token for a licensed user (Exchange Online and OneDrive) with the connectors' scopes:
//
//	secrets/access/ms-liveproof-token.py delegated -- go test ./internal/m365 -run Live -v
//
// It is read and write: a throwaway calendar event with a Teams link, a throwaway contact, a message sent to the account
// itself and replied to, labelled, archived and deleted, and a throwaway OneDrive folder with files in it, including one
// over 4 MB so the upload session is exercised. Each is deleted at the end, and the token is never printed.

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	absauth "github.com/microsoft/kiota-abstractions-go/authentication"
	msgraphsdk "github.com/microsoftgraph/msgraph-sdk-go"
	"net/url"

	"github.com/Embernet-ai/hearth/internal/connect"
)

type staticToken struct{ tok string }

func (s staticToken) GetAuthorizationToken(context.Context, *url.URL, map[string]interface{}) (string, error) {
	return s.tok, nil
}
func (s staticToken) GetAllowedHostsValidator() *absauth.AllowedHostsValidator {
	v := absauth.NewAllowedHostsValidator([]string{"graph.microsoft.com"})
	return &v
}

func liveSession(t *testing.T, token string) *session {
	t.Helper()
	adapter, err := msgraphsdk.NewGraphRequestAdapter(absauth.NewBaseBearerTokenAuthenticationProvider(staticToken{token}))
	if err != nil {
		t.Fatal(err)
	}
	return &session{s: &Service{graphBase: "https://graph.microsoft.com/v1.0"}, client: msgraphsdk.NewGraphServiceClient(adapter)}
}

func TestLiveMicrosoft365(t *testing.T) {
	token := os.Getenv("HEARTH_LIVE_MS_TOKEN")
	if token == "" || os.Getenv("HEARTH_LIVE_MS_MODE") != "delegated" {
		t.Skip("HEARTH_LIVE_MS_TOKEN (delegated) not set: this talks to the real Microsoft 365")
	}
	ctx := context.Background()
	s := liveSession(t, token)
	liveCalendar(t, ctx, s)
	liveContacts(t, ctx, s)
	liveMail(t, ctx, s)
	liveFiles(t, ctx, s)
}

func liveCalendar(t *testing.T, ctx context.Context, s *session) {
	t.Helper()
	cals, err := s.Calendars(ctx)
	if err != nil || len(cals) == 0 {
		t.Fatalf("calendars %v %v", cals, err)
	}
	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)
	ev, err := s.Create(ctx, "primary", connect.Event{Title: "Hearth live event", Notes: "n", Place: "Bay 2", Start: start, End: start.Add(time.Hour)}, true)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = s.Delete(ctx, "primary", ev.ID) }()
	got, err := s.Events(ctx, "primary", start.Add(-time.Hour), start.Add(2*time.Hour))
	found := false
	for _, e := range got {
		if e.ID == ev.ID {
			found = true
			t.Logf("calendar: created; Teams link on create: %v, on read back: %v", ev.JoinURL != "", e.JoinURL != "")
		}
	}
	if err != nil || !found {
		t.Fatalf("read back: %v found=%v", err, found)
	}
	moved := ev
	moved.Title, moved.Start, moved.End = "Hearth live event, moved", start.Add(2*time.Hour), start.Add(3*time.Hour)
	if _, err := s.Update(ctx, "primary", moved); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := s.Delete(ctx, "primary", ev.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	t.Log("calendar: read back, moved, deleted")
}

func liveContacts(t *testing.T, ctx context.Context, s *session) {
	t.Helper()
	api := contactsAdapter{s}
	c, err := api.Create(ctx, connect.Contact{Name: "Hearth Liveproof", Email: "liveproof@example.invalid", Phone: "555 0100", Title: "Test", Company: "Acme"})
	if err != nil {
		t.Fatalf("contact create: %v", err)
	}
	c.Title = "Test, edited"
	upd, err := api.Update(ctx, c)
	if err != nil {
		t.Fatalf("contact update: %v", err)
	}
	all, err := api.Contacts(ctx)
	seen := false
	for _, x := range all {
		seen = seen || (x.ID == upd.ID && x.Title == "Test, edited")
	}
	if err != nil || !seen {
		t.Errorf("edited contact not listed with its new title: %v", err)
	}
	if err := api.Delete(ctx, upd.ID); err != nil {
		t.Fatalf("contact delete: %v", err)
	}
	t.Logf("contacts: created, edited, listed (%d) and deleted", len(all))
}

func liveMail(t *testing.T, ctx context.Context, s *session) {
	t.Helper()
	mb := mailAdapter{s}
	me := os.Getenv("HEARTH_LIVE_MS_USER")
	subject := "Hearth live proof " + time.Now().UTC().Format("20060102T150405")
	if err := mb.Send(ctx, connect.Outgoing{To: []string{me}, Subject: subject, Body: "live proof body"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	var found connect.Message
	for i := 0; i < 20 && found.ID == ""; i++ {
		list, _, err := mb.Inbox(ctx, connect.MailQuery{Folder: "inbox", Limit: 10})
		if err != nil {
			t.Fatalf("inbox: %v", err)
		}
		for _, m := range list {
			if m.Subject == subject {
				found = m
			}
		}
		if found.ID == "" {
			time.Sleep(5 * time.Second)
		}
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
	if err := mb.MarkRead(ctx, found.ID, true); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if err := mb.Archive(ctx, found.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	t.Log("mail: replied, labelled, marked read, archived")
}

func liveFiles(t *testing.T, ctx context.Context, s *session) {
	t.Helper()
	fs := filesAdapter{s}
	drives, err := fs.Drives(ctx)
	if err != nil || len(drives) == 0 {
		t.Fatalf("drives %v %v", drives, err)
	}
	d := drives[0].ID
	dir, err := fs.Mkdir(ctx, d, "", "Hearth live proof "+time.Now().UTC().Format("150405"))
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = fs.Delete(ctx, d, dir.ID) })
	up, err := fs.Upload(ctx, d, dir.ID, "proof.txt", "text/plain", 5, strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if _, err := fs.Replace(ctx, d, up.ID, "text/plain", 6, strings.NewReader("hello!")); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if _, err := fs.Rename(ctx, d, up.ID, "proof2.txt"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	sub, err := fs.Mkdir(ctx, d, dir.ID, "inner")
	if err != nil {
		t.Fatalf("mkdir inner: %v", err)
	}
	if _, err := fs.Move(ctx, d, up.ID, sub.ID); err != nil {
		t.Fatalf("move: %v", err)
	}
	items, err := fs.List(ctx, d, sub.ID)
	if err != nil || len(items) != 1 || items[0].Name != "proof2.txt" {
		t.Fatalf("list inner %+v %v", items, err)
	}
	_, body, err := fs.Open(ctx, d, up.ID, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	data, _ := io.ReadAll(body)
	body.Close()
	if string(data) != "hello!" {
		t.Fatalf("content %q", data)
	}
	big := strings.Repeat("x", smallUpload+1024)
	if _, err := fs.Upload(ctx, d, dir.ID, "big.bin", "application/octet-stream", int64(len(big)), strings.NewReader(big)); err != nil {
		t.Fatalf("upload over 4 MB (upload session): %v", err)
	}
	if err := fs.Delete(ctx, d, up.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	t.Log("files: made folders, uploaded small and over 4 MB, replaced, renamed, moved, read back, deleted")
}
