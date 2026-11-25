package mautic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Mautic takes segment lists as plain id arrays, wants a campaign to carry an email event, and answers collections
// either as id-keyed objects or as empty arrays.
func TestMarketingBodiesAndDecoders(t *testing.T) {
	var emailBody, campaignBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/oauth/v2/token":
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
		case r.URL.Path == "/api/emails/new":
			_ = json.Unmarshal(b, &emailBody)
			_, _ = w.Write([]byte(`{"email":{"id":11}}`))
		case r.URL.Path == "/api/campaigns/new":
			_ = json.Unmarshal(b, &campaignBody)
			_, _ = w.Write([]byte(`{"campaign":{"id":12}}`))
		case r.URL.Path == "/api/emails":
			_, _ = w.Write([]byte(`{"total":"1","emails":{"11":{"id":11,"name":"Spring","subject":"Hi","emailType":"list","isPublished":true,"sentCount":5,"readCount":2,"lists":[{"id":3}]}}}`))
		case r.URL.Path == "/api/campaigns":
			_, _ = w.Write([]byte(`{"total":0,"campaigns":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "id", "secret")
	ctx := context.Background()
	if id, err := c.SaveEmail(ctx, 0, EmailInput{Name: "Spring", Subject: "Hi", Type: "list", Segments: []int64{3}}); err != nil || id != 11 {
		t.Fatalf("SaveEmail = %d, %v", id, err)
	}
	if l, ok := emailBody["lists"].([]any); !ok || len(l) != 1 || l[0] != float64(3) {
		t.Errorf("lists sent as %v, want a plain id array", emailBody["lists"])
	}
	if id, err := c.SaveCampaign(ctx, 0, CampaignInput{Name: "Onboarding", EmailID: 11, Segments: []int64{3}}); err != nil || id != 12 {
		t.Fatalf("SaveCampaign = %d, %v", id, err)
	}
	if ev, ok := campaignBody["events"].([]any); !ok || len(ev) != 1 || campaignBody["canvasSettings"] == nil {
		t.Errorf("campaign sent %v, want one email event and a canvas", campaignBody)
	}
	es, total, err := c.Emails(ctx, "", 30, 0)
	if err != nil || total != 1 || len(es) != 1 || es[0].Sent != 5 || es[0].Type != "list" || len(es[0].Segments) != 1 {
		t.Errorf("Emails = %+v, %d, %v", es, total, err)
	}
	cs, _, err := c.Campaigns(ctx, "", 30, 0)
	if err != nil || len(cs) != 0 {
		t.Errorf("an empty array collection is no campaigns, got %+v, %v", cs, err)
	}
}

// Contacts: listed from Mautic's id-keyed answer, found by address, made, changed, marked do not email and taken off, sent
// an example email, and deleted, each as the call Mautic documents.
func TestContactsReadAndWrite(t *testing.T) {
	var calls []string
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/oauth/v2/token" {
			calls = append(calls, r.Method+" "+r.URL.Path)
		}
		switch {
		case r.URL.Path == "/oauth/v2/token":
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
		case r.URL.Path == "/api/contacts" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"total":"2","contacts":{"7":{"id":7,"points":12,"lastActive":"2026-11-20 10:00:00","fields":{"all":{"firstname":"Ann","lastname":"Lee","email":"ann@example.com","company":"Acme"}},"tags":[{"tag":"vip"}],"doNotContact":[{"channel":"email"}]},"5":{"id":5,"fields":{"all":{"email":"bob@example.com"}}}}}`))
		case r.URL.Path == "/api/contacts/new":
			_ = json.Unmarshal(b, &created)
			_, _ = w.Write([]byte(`{"contact":{"id":9}}`))
		case r.URL.Path == "/api/contacts/9/edit":
			_, _ = w.Write([]byte(`{"contact":{"id":9}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "id", "secret")
	ctx := context.Background()
	list, total, err := c.Contacts(ctx, "", 30, 0)
	if err != nil || total != 2 || len(list) != 2 || list[0].ID != 7 || list[0].Name() != "Ann Lee" || !list[0].DoNotEmail || list[0].Tags[0] != "vip" || list[1].Name() != "bob@example.com" || list[1].DoNotEmail {
		t.Fatalf("Contacts = %+v, %d, %v", list, total, err)
	}
	if got, err := c.FindContactByEmail(ctx, "ANN@example.com"); err != nil || got == nil || got.ID != 7 {
		t.Errorf("FindContactByEmail = %+v, %v", got, err)
	}
	if got, _ := c.FindContactByEmail(ctx, "nobody@example.com"); got != nil {
		t.Errorf("found a contact that is not there: %+v", got)
	}
	if id, err := c.SaveContact(ctx, 0, ContactInput{First: "Cy", Email: "cy@example.com", Tags: []string{"test"}}); err != nil || id != 9 || created["email"] != "cy@example.com" {
		t.Fatalf("SaveContact new = %d, %v, %v", id, err, created)
	}
	if id, err := c.SaveContact(ctx, 9, ContactInput{First: "Cyrus"}); err != nil || id != 9 {
		t.Fatalf("SaveContact edit = %d, %v", id, err)
	}
	for _, f := range []func() error{
		func() error { return c.SetDoNotEmail(ctx, 9, true, "asked") },
		func() error { return c.SetDoNotEmail(ctx, 9, false, "") },
		func() error { return c.SegmentAdd(ctx, 3, 9) },
		func() error { return c.SegmentRemove(ctx, 3, 9) },
		func() error { return c.SendEmailTo(ctx, 11, 9) },
		func() error { return c.DeleteContact(ctx, 9) },
	} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"POST /api/contacts/9/dnc/email/add", "POST /api/contacts/9/dnc/email/remove", "POST /api/segments/3/contact/9/add", "POST /api/segments/3/contact/9/remove",
		"POST /api/emails/11/contact/9/send", "DELETE /api/contacts/9/delete"}
	got := calls[len(calls)-len(want):]
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, got[i], want[i])
		}
	}
}
