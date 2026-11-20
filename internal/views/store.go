package views

import (
	"context"
)

// Store is what the rest of Hearth calls on this domain. *Service, backed by
// Postgres, is the standalone implementation and is unchanged; with
// HEARTH_BACKEND=emberops the same calls are served from EmberOPS
// (internal/emberops/stores). Callers hold a Store, never a *Service.
type Store interface {
	Delete(ctx context.Context, orgID, principalID, id string) error
	Get(ctx context.Context, orgID, principalID, id string) (*View, error)
	List(ctx context.Context, orgID, principalID string) ([]*View, error)
	Save(ctx context.Context, orgID, ownerID string, v View) (*View, error)
}

var _ Store = (*Service)(nil)
