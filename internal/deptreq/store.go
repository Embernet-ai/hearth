package deptreq

import (
	"context"
)

// Store is what the rest of Hearth calls on this domain. *Service, backed by
// Postgres, is the standalone implementation and is unchanged; with
// HEARTH_BACKEND=emberops the same calls are served from EmberOPS
// (internal/emberops/stores). Callers hold a Store, never a *Service.
type Store interface {
	Ask(ctx context.Context, orgID, actorID string, spaceIDs []string, note string) (*Request, error)
	Cancel(ctx context.Context, orgID, actorID string) error
	Current(ctx context.Context, orgID, principalID string) ([]Department, error)
	Decide(ctx context.Context, orgID, actorID, id string, approve bool, final []string, note string) error
	Departments(ctx context.Context, orgID string) ([]Department, error)
	Latest(ctx context.Context, orgID, actorID string) (*Request, error)
	PendingCount(ctx context.Context, orgID, actorID string) (int, error)
	RequesterOf(ctx context.Context, orgID, id string) (string, error)
	ToDecide(ctx context.Context, orgID, actorID string) ([]*Request, error)
}

var _ Store = (*Service)(nil)
