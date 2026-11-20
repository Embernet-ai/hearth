package calendar

import (
	"context"
	"time"
)

// Store is what the rest of Hearth calls on this domain. *Service, backed by
// Postgres, is the standalone implementation and is unchanged; with
// HEARTH_BACKEND=emberops the same calls are served from EmberOPS
// (internal/emberops/stores). Callers hold a Store, never a *Service.
type Store interface {
	Between(ctx context.Context, orgID, principalID string,
		from, to time.Time) ([]Entry, error)
	Create(ctx context.Context, orgID, actorID string, p Params) (*Entry, error)
	Delete(ctx context.Context, orgID, actorID, id string) error
	Feed(ctx context.Context, token string, now time.Time) (string, []Entry, error)
	HasFeed(ctx context.Context, principalID string) (bool, time.Time, error)
	IssueFeed(ctx context.Context, orgID, principalID string) (string, error)
	RevokeFeed(ctx context.Context, principalID string) error
}

var _ Store = (*Service)(nil)
