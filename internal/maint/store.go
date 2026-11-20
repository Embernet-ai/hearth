package maint

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
	Delete(ctx context.Context, orgID string, actor core.Subjects, id string) error
	ForAsset(ctx context.Context, orgID string, actor core.Subjects, assetID string) ([]*Schedule, error)
	Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*Schedule, error)
	HistoryForAsset(ctx context.Context, orgID string, actor core.Subjects,
		assetID string, limit int) ([]Occurrence, error)
	LatestReading(ctx context.Context, assetID, key string) (*Reading, error)
	List(ctx context.Context, orgID string, actor core.Subjects) ([]*Schedule, error)
	MeterKeys(ctx context.Context, assetID string) ([]string, error)
	Readings(ctx context.Context, orgID string, actor core.Subjects,
		assetID string, limit int) ([]Reading, error)
	RecordReading(ctx context.Context, orgID string, actor core.Subjects,
		assetID, key string, value float64, note string) (*Reading, error)
	Report(ctx context.Context, orgID string, actor core.Subjects,
		from, to time.Time) (*Compliance, error)
	Save(ctx context.Context, orgID string, actor core.Subjects, sch Schedule) (*Schedule, error)
	SetEnabled(ctx context.Context, orgID string, actor core.Subjects, id string, on bool) error
}

var _ Store = (*Service)(nil)
