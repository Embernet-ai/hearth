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
