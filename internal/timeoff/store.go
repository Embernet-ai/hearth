package timeoff

import (
	"context"
)

// Store is what the rest of Hearth calls on this domain. *Service, backed by
// Postgres, is the standalone implementation and is unchanged; with
// HEARTH_BACKEND=emberops the same calls are served from EmberOPS
// (internal/emberops/stores). Callers hold a Store, never a *Service.
type Store interface {
	Decidable(ctx context.Context, orgID, actorID string, requesterIDs []string) (map[string]bool, error)
	Deciders(ctx context.Context, orgID, requesterID string) ([]string, error)
	DepartmentSpace(ctx context.Context, orgID, principalID string) (string, error)
	Ask(ctx context.Context, orgID, actorID string, p Params) (*Request, error)
	Cancel(ctx context.Context, orgID, actorID, id string) error
	Decide(ctx context.Context, orgID, actorID, id string, approve bool, note string) error
	Mine(ctx context.Context, orgID, actorID string) ([]*Request, error)
	PendingCount(ctx context.Context, orgID, actorID string) (int, error)
	ToDecide(ctx context.Context, orgID, actorID string) ([]*Request, error)
}

var _ Store = (*Service)(nil)
