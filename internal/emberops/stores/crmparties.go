package stores

import (
	"context"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
)

// partiesInPostgres is the EmberOPS CRM store with companies and contacts left
// where they are: in Postgres, with Hearth's grants, deals, owners, types and
// tags. Parts and stock still come from EmberOPS.
//
// This is the default in emberops mode. Moving companies and contacts into
// EmberOPS (the CRMPartiesInEmberOPS option) takes the whole sales side away,
// because a deal is a row that points at a company row and there is no deal in
// EmberOPS to point at, so it has to be asked for by name. The CRM reaches
// EmberOPS through crmsync instead, which sends only what accounting needs.
type partiesInPostgres struct {
	*CRM
	pg crm.Store
}

var _ crm.Store = (*partiesInPostgres)(nil)

func (p *partiesInPostgres) Companies(ctx context.Context, orgID string, a core.Subjects, kind, search string) ([]*crm.Company, error) {
	return p.pg.Companies(ctx, orgID, a, kind, search)
}
func (p *partiesInPostgres) Contacts(ctx context.Context, orgID string, a core.Subjects, companyID, search string) ([]*crm.Contact, error) {
	return p.pg.Contacts(ctx, orgID, a, companyID, search)
}
func (p *partiesInPostgres) CreateCompany(ctx context.Context, orgID string, a core.Subjects, c crm.Company) (*crm.Company, error) {
	return p.pg.CreateCompany(ctx, orgID, a, c)
}
func (p *partiesInPostgres) CreateContact(ctx context.Context, orgID string, a core.Subjects, c crm.Contact) (*crm.Contact, error) {
	return p.pg.CreateContact(ctx, orgID, a, c)
}
func (p *partiesInPostgres) UpdateCompany(ctx context.Context, orgID string, a core.Subjects, c crm.Company) error {
	return p.pg.UpdateCompany(ctx, orgID, a, c)
}
func (p *partiesInPostgres) UpdateContact(ctx context.Context, orgID string, a core.Subjects, c crm.Contact) error {
	return p.pg.UpdateContact(ctx, orgID, a, c)
}
func (p *partiesInPostgres) DeleteCompany(ctx context.Context, orgID string, a core.Subjects, id string) error {
	return p.pg.DeleteCompany(ctx, orgID, a, id)
}
func (p *partiesInPostgres) DeleteContact(ctx context.Context, orgID string, a core.Subjects, id string) error {
	return p.pg.DeleteContact(ctx, orgID, a, id)
}
func (p *partiesInPostgres) GetCompany(ctx context.Context, orgID string, a core.Subjects, id string) (*crm.Company, error) {
	return p.pg.GetCompany(ctx, orgID, a, id)
}
func (p *partiesInPostgres) GetContact(ctx context.Context, orgID string, a core.Subjects, id string) (*crm.Contact, error) {
	return p.pg.GetContact(ctx, orgID, a, id)
}
func (p *partiesInPostgres) ByEmail(ctx context.Context, orgID, email string) (*crm.Contact, error) {
	return p.pg.ByEmail(ctx, orgID, email)
}
