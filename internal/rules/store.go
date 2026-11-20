package rules

import (
	"context"

	"github.com/Embernet-ai/hearth/internal/jobs"
)

// Store is what the rest of Hearth calls on this domain. *Service, backed by
// Postgres, is the standalone implementation and is unchanged; with
// HEARTH_BACKEND=emberops the same calls are served from EmberOPS
// (internal/emberops/stores). Callers hold a Store, never a *Service.
type Store interface {
	Delete(ctx context.Context, orgID, id string) error
	Get(ctx context.Context, orgID, id string) (*Rule, error)
	List(ctx context.Context, orgID string) ([]*Rule, error)
	Matching(ctx context.Context, orgID, event string) ([]*Rule, error)
	Runs(ctx context.Context, orgID, ruleID string, limit int) ([]*Run, error)
	Save(ctx context.Context, orgID, actorID string, r Rule) (*Rule, error)
	SetEnabled(ctx context.Context, orgID, id string, on bool) error
}

// RunStore is where the engine writes what rules decided and read the history
// to rehearse against. *Service keeps them in Postgres.
type RunStore interface {
	RecordRun(ctx context.Context, r *Run) (bool, error)
	FinishRun(ctx context.Context, ruleID string, eventID int64, taken []string, errText string) error
	ClearDryRuns(ctx context.Context, ruleID string) error
	RecentEvents(ctx context.Context, orgID, event string, limit int) ([]jobs.Event, error)
}

var (
	_ Store    = (*Service)(nil)
	_ RunStore = (*Service)(nil)
)
