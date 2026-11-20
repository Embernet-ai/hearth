package plant

import (
	"context"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Store is what the rest of Hearth calls on this domain. *Service, backed by
// Postgres, is the standalone implementation and is unchanged; with
// HEARTH_BACKEND=emberops the same calls are served from EmberOPS
// (internal/emberops/stores). Callers hold a Store, never a *Service.
type Store interface {
	Ancestors(ctx context.Context, orgID string, actor core.Subjects, id string) ([]*Asset, error)
	AvailabilityFor(ctx context.Context, orgID string, actor core.Subjects,
		assetID string, from, to time.Time) (*Availability, error)
	ByCode(ctx context.Context, orgID string, actor core.Subjects, code string) (string, error)
	Children(ctx context.Context, orgID string, actor core.Subjects, id string) ([]*Asset, error)
	ComeUp(ctx context.Context, orgID string, actor core.Subjects,
		assetID string, at time.Time) (*Downtime, error)
	CreateSpace(ctx context.Context, orgID string, actor core.Subjects,
		name, kind string, parentID *string) (*Space, error)
	DeleteSpace(ctx context.Context, orgID string, actor core.Subjects, id string) error
	DowntimeFor(ctx context.Context, orgID string, actor core.Subjects,
		assetID string, limit int) ([]Downtime, error)
	FieldsFor(ctx context.Context, orgID, assetID string) (Fields, error)
	FindSpaceByName(ctx context.Context, orgID, name string) (string, error)
	Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*Asset, error)
	GetSpace(ctx context.Context, orgID string, actor core.Subjects, id string) (*Space, error)
	GoDown(ctx context.Context, orgID string, actor core.Subjects,
		assetID string, at time.Time, planned bool, workOrderID *string, note string) (*Downtime, error)
	LogDowntime(ctx context.Context, orgID string, actor core.Subjects,
		assetID string, from, to time.Time, planned bool, workOrderID *string, note string) (*Downtime, error)
	ManageableSpaces(ctx context.Context, orgID string, actor core.Subjects, tree []*Space) (map[string]bool, error)
	MayAdminSpaces(ctx context.Context, orgID string, actor core.Subjects) (bool, error)
	MayManageAnySpace(ctx context.Context, orgID string, actor core.Subjects) (bool, error)
	MoveDocument(ctx context.Context, orgID string, actor core.Subjects,
		docID string, spaceID *string) error
	MoveSpace(ctx context.Context, orgID string, actor core.Subjects,
		id string, parentID *string) error
	RemoveDowntime(ctx context.Context, orgID string, actor core.Subjects, id string) error
	RenameSpace(ctx context.Context, orgID string, actor core.Subjects,
		id, name string) error
	ReorderSpaces(ctx context.Context, orgID string, actor core.Subjects, parentID string, orderedIDs []string) error
	SaveSpaceOrder(ctx context.Context, orgID string, actor core.Subjects, orderedIDs []string) error
	SetFields(ctx context.Context, orgID string, actor core.Subjects,
		assetID string, f Fields) error
	SpaceChildren(ctx context.Context, orgID string, actor core.Subjects, parentID string) ([]*Space, error)
	SpaceTree(ctx context.Context, orgID string, actor core.Subjects) ([]*Space, error)
	Tree(ctx context.Context, orgID string, actor core.Subjects) ([]*Asset, error)
}

var _ Store = (*Service)(nil)
