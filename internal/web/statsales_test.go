package web

// The dashboard's sales parts follow the viewer's sales permission, not how many deals they can see: a sales user
// and a super admin get the Deals group with every deal, and everybody else gets no Deals group at all.

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
)

type fakeSales struct {
	crm.Sales
	access bool
	asked  string
}

func (f *fakeSales) HasSalesAccess(context.Context, string, core.Subjects) (bool, error) {
	return f.access, nil
}

func (f *fakeSales) PersonStats(_ context.Context, _ string, _ core.Subjects, owner string, _, _, _ time.Time) (*crm.PersonStats, error) {
	f.asked = owner
	return &crm.PersonStats{OpenCount: 3, Open: []crm.Sum{{Currency: "USD", Count: 3, Cents: 175000}},
		Stages: []crm.StageRow{{Stage: crm.StageNew, Label: "New", Count: 3, Sums: []crm.Sum{{Currency: "USD", Count: 3, Cents: 175000}}}}}, nil
}

func (f *fakeSales) Deals(context.Context, string, core.Subjects, crm.ListQuery) ([]*crm.Deal, int, error) {
	return nil, 0, nil
}

func dealsGroup(p *statsPanel) *statGroup {
	for i := range p.Groups {
		if p.Groups[i].Key == "deals" {
			return &p.Groups[i]
		}
	}
	return nil
}

func TestDealsGroupFollowsSalesPermission(t *testing.T) {
	id := &auth.Identity{OrgID: "o", PrincipalID: "me", DisplayName: "Me"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// A sales user (or a super admin, which HasSalesAccess answers yes for) sees the group, with every owner's deals.
	yes := &fakeSales{access: true}
	s := &Server{sales: yes, log: log}
	p, status, _ := s.statsFor(context.Background(), id, "", false)
	if status != 0 || p == nil {
		t.Fatalf("stats for a sales user: %d", status)
	}
	g := dealsGroup(p)
	if g == nil || len(g.Tiles) != 4 {
		t.Fatalf("a sales user should get the Deals group: %+v", p.Groups)
	}
	if yes.asked != "" {
		t.Errorf("deals default to all owners, but the stats were asked for owner %q", yes.asked)
	}
	if len(p.Stages) == 0 {
		t.Errorf("a sales user should get the pipeline by stage")
	}
	// Only mine is a filter somebody turns on.
	p, _, _ = s.statsFor(context.Background(), id, "", true)
	if yes.asked != "me" || dealsGroup(p) == nil {
		t.Errorf("only mine should ask for the viewer's own deals: asked %q", yes.asked)
	}

	// Somebody outside sales gets no Deals group, no pipeline and no closed deals, even though a count would not be zero.
	no := &fakeSales{access: false}
	s = &Server{sales: no, log: log}
	p, status, _ = s.statsFor(context.Background(), id, "", false)
	if status != 0 || p == nil {
		t.Fatalf("stats for a non-sales user: %d", status)
	}
	if dealsGroup(p) != nil || len(p.Stages) != 0 {
		t.Errorf("a non-sales viewer got sales on the dashboard: %+v stages %d", p.Groups, len(p.Stages))
	}
	for _, r := range p.Recent {
		if r.Kind != "Task done" {
			t.Errorf("a non-sales viewer got %q in Recently closed", r.Kind)
		}
	}
	if no.asked != "" {
		t.Errorf("a non-sales viewer's deals were counted at all (asked %q)", no.asked)
	}
}
