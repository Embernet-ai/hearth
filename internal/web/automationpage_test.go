package web

// Automation is for the Sales team and super admins; the dashboard section appears for them and for nobody else, and its
// numbers come from Cascade.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/cascade"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
)

type salesFor struct {
	crm.Sales
	yes bool
}

func (f salesFor) HasSalesAccess(context.Context, string, core.Subjects) (bool, error) {
	return f.yes, nil
}

func TestTheAutomationSectionIsForSalesAndNumbersComeFromCascade(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/insights":
			_, _ = w.Write([]byte(`{"totals":{"total":12,"succeeded":10,"failed":2,"unfinished":1},"workflows":[{"workflowId":"w1","workflowName":"Contact Form to Sales","total":12,"failed":2}]}`))
		case "/api/v1/approvals":
			_, _ = w.Write([]byte(`{"items":[{"id":"a1","title":"ok?","status":"pending"}]}`))
		case "/api/v1/workflows":
			_, _ = w.Write([]byte(`{"items":[{"id":"w1","name":"x","active":true},{"id":"w2","name":"y"}]}`))
		}
	}))
	defer srv.Close()
	id := &auth.Identity{OrgID: "o", PrincipalID: "me"}
	ctx := context.Background()
	yes := &Server{sales: salesFor{yes: true}, cascade: cascade.New(srv.URL, "hlc_k")}
	sec := yes.automationSection(ctx, id)
	if sec == nil || len(sec.Tiles) != 4 || sec.Tiles[0].Value != "12" || sec.Tiles[1].Value != "2" || sec.Tiles[2].Value != "1" || sec.Tiles[3].Value != "1" || sec.Tiles[3].Period != "of 2" {
		t.Fatalf("section = %+v", sec)
	}
	if len(sec.Rows) != 1 || sec.Rows[0].Title != "Contact Form to Sales" {
		t.Fatalf("rows = %+v", sec.Rows)
	}
	if !yes.automationAllowed(ctx, id) {
		t.Error("sales is allowed")
	}
	no := &Server{sales: salesFor{yes: false}, cascade: cascade.New(srv.URL, "hlc_k")}
	if no.automationSection(ctx, id) != nil || no.automationAllowed(ctx, id) {
		t.Error("somebody with no sales access gets the section")
	}
	if (&Server{sales: salesFor{yes: true}}).automationAllowed(ctx, id) {
		t.Error("with no Cascade configured there is nothing to allow")
	}
}
