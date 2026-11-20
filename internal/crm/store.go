package crm

import (
	"context"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Store is what the rest of Hearth calls on this domain. *Service, backed by
// Postgres, is the standalone implementation and is unchanged; with
// HEARTH_BACKEND=emberops the same calls are served from EmberOPS
// (internal/emberops/stores). Callers hold a Store, never a *Service.
type Store interface {
	Adjust(ctx context.Context, orgID string, actor core.Subjects,
		partID string, delta float64, note string) (*Movement, error)
	ByEmail(ctx context.Context, orgID, email string) (*Contact, error)
	Companies(ctx context.Context, orgID string, actor core.Subjects,
		kind, search string) ([]*Company, error)
	Consume(ctx context.Context, orgID string, actor core.Subjects,
		docID, partID string, quantity float64, note string) (*Movement, error)
	Contacts(ctx context.Context, orgID string, actor core.Subjects,
		companyID, search string) ([]*Contact, error)
	CreateCompany(ctx context.Context, orgID string, actor core.Subjects,
		c Company) (*Company, error)
	CreateContact(ctx context.Context, orgID string, actor core.Subjects,
		c Contact) (*Contact, error)
	DeleteCompany(ctx context.Context, orgID string, actor core.Subjects, id string) error
	DeleteContact(ctx context.Context, orgID string, actor core.Subjects, id string) error
	DeletePart(ctx context.Context, orgID string, actor core.Subjects, id string) error
	GetCompany(ctx context.Context, orgID string, actor core.Subjects, id string) (*Company, error)
	GetContact(ctx context.Context, orgID string, actor core.Subjects, id string) (*Contact, error)
	GetPart(ctx context.Context, orgID, id string) (*Part, error)
	HistoryFor(ctx context.Context, orgID, partID string, limit int) ([]Movement, error)
	MovementsFor(ctx context.Context, orgID string, actor core.Subjects,
		docID string) ([]Movement, error)
	PartiesFor(ctx context.Context, orgID string, actor core.Subjects,
		docID string) ([]Party, error)
	Parts(ctx context.Context, orgID, search string) ([]*Part, error)
	PartsCost(ctx context.Context, orgID, docID string) (int, error)
	PartsCostForAsset(ctx context.Context, orgID, assetID string) (int, error)
	Receive(ctx context.Context, orgID string, actor core.Subjects,
		docID, partID string, quantity float64, unitCostCents int, note string) (*Movement, error)
	RemoveMovement(ctx context.Context, orgID string, actor core.Subjects, id string) error
	Return(ctx context.Context, orgID string, actor core.Subjects,
		docID, partID string, quantity float64, note string) (*Movement, error)
	SavePart(ctx context.Context, orgID string, actor core.Subjects, p Part) (*Part, error)
	UpdateCompany(ctx context.Context, orgID string, actor core.Subjects,
		c Company) error
	UpdateContact(ctx context.Context, orgID string, actor core.Subjects,
		c Contact) error
}

var _ Store = (*Service)(nil)
