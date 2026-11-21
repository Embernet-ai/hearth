package connect

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAWebhookIsAcceptedOnlyAtTheVendorsOwnHosts(t *testing.T) {
	for _, c := range []struct {
		p    Provider
		addr string
		ok   bool
	}{
		{Google, "https://chat.googleapis.com/v1/spaces/AAAA/messages?key=k&token=t", true},
		{Google, "http://chat.googleapis.com/v1/spaces/AAAA/messages", false},
		{Google, "https://chat.googleapis.com.evil.test/v1/spaces/AAAA/messages", false},
		{Google, "https://chat.googleapis.com/other", false},
		{Google, "https://user:pw@chat.googleapis.com/v1/spaces/A/messages", false},
		{Google, "https://10.0.0.5/v1/spaces/A/messages", false},
		{Microsoft, "https://contoso.webhook.office.com/webhookb2/abc", true},
		{Microsoft, "https://prod-12.westus.logic.azure.com:443/workflows/abc", true},
		{Microsoft, "https://webhook.office.com.evil.test/x", false},
		{Microsoft, "https://169.254.169.254/latest", false},
		{Microsoft, "https://localhost/x", false},
		{Google, "not a url", false},
	} {
		_, err := ValidateWebhook(c.p, c.addr)
		if (err == nil) != c.ok {
			t.Errorf("%s %q: error %v, want ok=%v", c.p, c.addr, err, c.ok)
		}
	}
}

func TestTheChatPostIsTheShapeEachProductTakes(t *testing.T) {
	g, _ := chatBody(Google, "Dana assigned you Quote 118", "https://hearth.example.com/documents/1")
	var gm map[string]any
	_ = json.Unmarshal(g, &gm)
	if txt, _ := gm["text"].(string); !strings.Contains(txt, "Quote 118") || !strings.Contains(txt, "hearth.example.com/documents/1") {
		t.Errorf("Google Chat body %s", g)
	}

	m, _ := chatBody(Microsoft, "Dana assigned you Quote 118", "https://hearth.example.com/documents/1")
	var mm struct {
		Type        string
		Attachments []struct {
			ContentType string
			Content     struct {
				Type    string
				Version string
				Body    []struct{ Type, Text string }
				Actions []struct{ Type, URL string }
			}
		}
	}
	if err := json.Unmarshal(m, &mm); err != nil || mm.Type != "message" || len(mm.Attachments) != 1 {
		t.Fatalf("Teams body %s (%v)", m, err)
	}
	a := mm.Attachments[0]
	if a.ContentType != "application/vnd.microsoft.card.adaptive" || a.Content.Type != "AdaptiveCard" ||
		a.Content.Body[0].Text != "Dana assigned you Quote 118" || a.Content.Actions[0].URL != "https://hearth.example.com/documents/1" {
		t.Errorf("Teams card %+v", a)
	}
}

func TestAChatPostReportsFailureWithoutTheAddress(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		if strings.Contains(r.URL.Path, "bad") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	n := &ChatNotifier{http: &http.Client{Timeout: 2 * time.Second}}
	if err := n.post(context.Background(), Google, srv.URL+"/ok", "hello", ""); err != nil || !strings.Contains(got, "hello") {
		t.Fatalf("post: %v body %q", err, got)
	}
	err := n.post(context.Background(), Google, srv.URL+"/bad?key=SECRET", "hello", "")
	if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("a failed post gave %v, which must say what happened and never the address", err)
	}
}
