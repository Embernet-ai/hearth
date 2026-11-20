package stores

import (
	"context"
	"testing"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/emberops"
)

type pgCRM struct {
	crm.Store
	asked int
}

func (p *pgCRM) Companies(context.Context, string, core.Subjects, string, string) ([]*crm.Company, error) {
	p.asked++
	return []*crm.Company{{Name: "From Postgres"}}, nil
}

// In emberops mode the companies stay in Postgres unless somebody asks for them
// to move, because moving them turns the whole sales side off. That default is
// what this holds.
func TestCompaniesStayInPostgresInEmberopsModeByDefault(t *testing.T) {
	c := emberops.New(emberops.Config{})
	set := EmberOPS(c, PG{}, Options{})
	if _, ok := set.CRM.(*partiesInPostgres); !ok {
		t.Fatalf("by default the CRM store is %T, want the one that keeps companies and contacts in Postgres", set.CRM)
	}
	moved := EmberOPS(c, PG{}, Options{CRMPartiesInEmberOPS: true})
	if _, ok := moved.CRM.(*CRM); !ok {
		t.Fatalf("asked to move them, the CRM store is %T", moved.CRM)
	}

	// And the wrapper answers company questions from Postgres, never EmberOps.
	pg := &pgCRM{}
	w := &partiesInPostgres{CRM: &CRM{}, pg: pg}
	got, err := w.Companies(context.Background(), "o", core.Subjects{}, "", "")
	if err != nil || len(got) != 1 || got[0].Name != "From Postgres" || pg.asked != 1 {
		t.Fatalf("companies came from %+v, postgres asked %d times, err=%v", got, pg.asked, err)
	}
}
