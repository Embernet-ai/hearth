package web

import (
	"context"
	"net/url"
	"testing"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/crmops"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/fake"
)

type opsSales struct {
	crm.Sales
	yes bool
}

func (f opsSales) HasSalesAccess(context.Context, string, core.Subjects) (bool, error) {
	return f.yes, nil
}

func TestTheEmberOpsPartIsForSalesAndReadsZerosWithAnEmptyState(t *testing.T) {
	f := fake.New(map[string]string{"svc": "hearth-service"})
	defer f.Close()
	u, _ := url.Parse(f.URL)
	ops := crmops.New(emberops.New(emberops.Config{BaseURL: u, Service: emberops.StaticToken("svc")}))
	id := &auth.Identity{OrgID: "o", PrincipalID: "me"}
	ctx := context.Background()

	s := &Server{sales: opsSales{yes: true}, crmOps: ops}
	sec := s.opsSection(ctx, id)
	if sec == nil || len(sec.Tiles) != 4 || sec.Tiles[0].Value != "0" || sec.Tiles[2].Value != "0" || sec.Notice != "" {
		t.Fatalf("section = %+v", sec)
	}
	if len(sec.Rows) != 1 || sec.Rows[0].Title != "No quotations yet" || sec.Rows[0].Detail != "Create one from a deal." {
		t.Fatalf("empty state = %+v", sec.Rows)
	}

	f.Put("Sales Order", map[string]any{"name": "SO-1", "customer": "C", "status": "To Bill", "docstatus": float64(1), "grand_total": 10})
	f.Put("Sales Invoice", map[string]any{"name": "SI-1", "customer": "C", "status": "Overdue", "docstatus": float64(1), "outstanding_amount": 250, "currency": "USD", "due_date": "2020-01-01"})
	(&s.opsNums).mu.Lock()
	s.opsNums.sec = nil
	(&s.opsNums).mu.Unlock()
	sec = s.opsSection(ctx, id)
	if sec.Tiles[1].Value != "1" || sec.Tiles[2].Value != "1" || sec.Tiles[2].Detail != "$250 owing" || sec.Tiles[2].Tone != "bad" || len(sec.Rows) != 0 {
		t.Fatalf("section with sales = %+v", sec)
	}

	if (&Server{sales: opsSales{yes: false}, crmOps: ops}).opsSection(ctx, id) != nil {
		t.Error("somebody with no sales access sees EmberOps numbers")
	}
	if (&Server{sales: opsSales{yes: true}}).opsSection(ctx, id) != nil {
		t.Error("with no EmberOps there is no part")
	}
}
