package teams

import (
	"context"
)

// Store is what the rest of Hearth calls on this domain. *Service, backed by
// Postgres, is the standalone implementation and is unchanged; with
// HEARTH_BACKEND=emberops the same calls are served from EmberOPS
// (internal/emberops/stores). Callers hold a Store, never a *Service.
type Store interface {
	Add(ctx context.Context, orgID, actorID, teamID, principalID string) error
	ByExternalID(ctx context.Context, orgID, externalID string) (*Team, error)
	Create(ctx context.Context, orgID, actorID, name, description string) (*Team, error)
	CreateManaged(ctx context.Context, orgID, actorID, name, externalID string) (*Team, error)
	Delete(ctx context.Context, orgID, actorID, id string) error
	Get(ctx context.Context, orgID, id string) (*Team, error)
	List(ctx context.Context, orgID string) ([]*Team, error)
	Members(ctx context.Context, orgID, teamID string) ([]Member, error)
	Of(ctx context.Context, orgID, principalID string) ([]*Team, error)
	Remove(ctx context.Context, orgID, actorID, teamID, principalID string) error
	Rename(ctx context.Context, orgID, actorID, id, name string) error
	RenameManaged(ctx context.Context, orgID, actorID, id, name string) error
	SetMembers(ctx context.Context, orgID, actorID, teamID string, principalIDs []string) (added, removed int, err error)
}

var _ Store = (*Service)(nil)
