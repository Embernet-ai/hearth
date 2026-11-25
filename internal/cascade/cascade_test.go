package cascade

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

type seen struct{ method, path, query, auth, body string }

func fake(t *testing.T, reply func(r *http.Request) (int, string)) (*Client, *[]seen) {
	t.Helper()
	var calls []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(b)})
		code, body := reply(r)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL+"/", "hlc_key"), &calls
}

func TestReadsSendTheKeyAsABearerAndParseTheListShape(t *testing.T) {
	c, calls := fake(t, func(r *http.Request) (int, string) {
		switch r.URL.Path {
		case "/api/v1/workflows":
			return 200, `{"items":[{"id":"w1","name":"Contact Form to Sales","active":true,"version":3}]}`
		case "/api/v1/executions":
			return 200, `{"items":[{"id":"e1","workflowId":"w1","workflowName":"Contact Form to Sales","status":"failed","error":"boom","createdAt":"2026-11-23T21:34:34Z"},{"id":"e2","status":"running"}]}`
		case "/api/v1/approvals":
			return 200, `{"items":[{"id":"a1","executionId":"e2","title":"Send the quote?","status":"pending"}]}`
		case "/api/v1/insights":
			return 200, `{"totals":{"total":29,"succeeded":27,"failed":2,"unfinished":0,"failureRate":0.07},"workflows":[{"workflowId":"w1","workflowName":"X","total":9,"failed":2}]}`
		}
		return 404, `{"error":"no"}`
	})
	ctx := context.Background()
	if !c.Enabled() || (*Client)(nil).Enabled() {
		t.Fatal("Enabled")
	}
	ws, err := c.Workflows(ctx)
	if err != nil || len(ws) != 1 || !ws[0].Active || ws[0].Version != 3 {
		t.Fatalf("workflows %+v %v", ws, err)
	}
	ex, err := c.Executions(ctx, "failed", "deal-42", 25)
	if err != nil || len(ex) != 2 || ex[0].Error != "boom" || !ex[0].Finished() || ex[1].Finished() {
		t.Fatalf("executions %+v %v", ex, err)
	}
	ap, err := c.Approvals(ctx, "pending", 10)
	if err != nil || len(ap) != 1 || ap[0].Title != "Send the quote?" {
		t.Fatalf("approvals %+v %v", ap, err)
	}
	in, err := c.Insights(ctx, 24*time.Hour)
	if err != nil || in.Totals.Total != 29 || in.Totals.Failed != 2 || in.Workflows[0].Failed != 2 {
		t.Fatalf("insights %+v %v", in, err)
	}
	for _, s := range *calls {
		if s.auth != "Bearer hlc_key" || s.method != "GET" {
			t.Errorf("call %+v", s)
		}
	}
	if q := (*calls)[1].query; !strings.Contains(q, "status=failed") || !strings.Contains(q, "search=deal-42") || !strings.Contains(q, "limit=25") {
		t.Errorf("executions query %q", q)
	}
}

func TestTheWritesUseTheRoutesCascadeDocuments(t *testing.T) {
	c, calls := fake(t, func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/run") || strings.HasSuffix(r.URL.Path, "/retry") {
			return 201, `{"executionId":"new1"}`
		}
		return 200, `{}`
	})
	ctx := context.Background()
	if err := c.Activate(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Deactivate(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	id, err := c.Run(ctx, "w1", []map[string]any{{"deal": "d1"}})
	if err != nil || id != "new1" {
		t.Fatalf("run %q %v", id, err)
	}
	if id, err = c.Retry(ctx, "e1"); err != nil || id != "new1" {
		t.Fatalf("retry %q %v", id, err)
	}
	if err := c.Stop(ctx, "e2"); err != nil {
		t.Fatal(err)
	}
	want := []string{"/api/v1/workflows/w1/activate", "/api/v1/workflows/w1/deactivate", "/api/v1/workflows/w1/run", "/api/v1/executions/e1/retry", "/api/v1/executions/e2/stop"}
	for i, w := range want {
		if (*calls)[i].method != "POST" || (*calls)[i].path != w {
			t.Errorf("call %d = %+v, want POST %s", i, (*calls)[i], w)
		}
	}
	var b struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte((*calls)[2].body), &b); err != nil || b.Items[0]["deal"] != "d1" {
		t.Errorf("run body %q", (*calls)[2].body)
	}
	if err := c.Stop(ctx, " "); err == nil {
		t.Error("an empty id is refused before any call")
	}
}

func TestARefusedKeyAndAServerErrorReadInWords(t *testing.T) {
	c, _ := fake(t, func(r *http.Request) (int, string) {
		if r.URL.Path == "/api/v1/workflows" {
			return 401, `{"error":"bad key"}`
		}
		return 409, `{"error":"already running"}`
	})
	if _, err := c.Workflows(context.Background()); err == nil || !strings.Contains(err.Error(), "refused the key") {
		t.Errorf("err = %v", err)
	}
	if err := c.Stop(context.Background(), "e"); err == nil || !strings.Contains(err.Error(), "409 already running") {
		t.Errorf("err = %v", err)
	}
}
