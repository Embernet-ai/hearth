package mautic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The calls the CRM makes about one contact read Mautic's answers, which are an object keyed by id when there are rows and
// [] when there are none, and move the contact with Mautic's own add and remove routes.
func TestTheContactCallsReadAndMove(t *testing.T) {
	var posted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/oauth/v2/token":
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
		case r.URL.Path == "/api/contacts/5/segments":
			_, _ = w.Write([]byte(`{"total":2,"lists":{"3":{"id":3,"name":"Expo"},"9":{"id":9,"name":"Hearth: all contacts"}}}`))
		case r.URL.Path == "/api/contacts/5/campaigns":
			_, _ = w.Write([]byte(`{"total":0,"campaigns":[]}`))
		case r.URL.Path == "/api/segments":
			_, _ = w.Write([]byte(`{"total":2,"lists":{"3":{"id":3,"name":"Expo","alias":"expo","isPublished":true},"9":{"id":9,"name":"Hearth: all contacts","alias":"hearth-all","isPublished":true}}}`))
		case r.Method == http.MethodPost:
			posted = append(posted, r.URL.Path)
			_, _ = w.Write([]byte(`{"success":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "id", "secret")
	ctx := context.Background()
	if got, err := c.ContactSegments(ctx, 5); err != nil || len(got) != 2 {
		t.Errorf("segments = %v (%v), want two", got, err)
	}
	if got, err := c.ContactCampaigns(ctx, 5); err != nil || len(got) != 0 {
		t.Errorf("campaigns = %v (%v), want none from []", got, err)
	}
	if rows, err := c.SegmentsBrief(ctx); err != nil || len(rows) != 2 || rows[0].Name != "Expo" {
		t.Errorf("segments brief = %+v (%v)", rows, err)
	}
	if err := c.CampaignAdd(ctx, 4, 5); err != nil {
		t.Fatal(err)
	}
	if err := c.CampaignRemove(ctx, 4, 5); err != nil {
		t.Fatal(err)
	}
	if len(posted) != 2 || posted[0] != "/api/campaigns/4/contact/5/add" || posted[1] != "/api/campaigns/4/contact/5/remove" {
		t.Errorf("posted %v", posted)
	}
}
