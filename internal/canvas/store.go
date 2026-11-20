package canvas

import (
	"context"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Store is what the rest of Hearth calls on this domain. *Service, backed by
// Postgres, is the standalone implementation and is unchanged; with
// HEARTH_BACKEND=emberops the same calls are served from EmberOPS
// (internal/emberops/stores). Callers hold a Store, never a *Service.
type Store interface {
	AddEdge(ctx context.Context, orgID string, actor core.Subjects,
		canvasID, from, to, label string) error
	AddNode(ctx context.Context, orgID string, actor core.Subjects,
		canvasID string, n Node) (*Node, error)
	Create(ctx context.Context, orgID string, actor core.Subjects,
		name string, spaceID *string) (*Canvas, error)
	Delete(ctx context.Context, orgID string, actor core.Subjects, id string) error
	Edges(ctx context.Context, orgID string, actor core.Subjects,
		canvasID string) ([]*Edge, error)
	Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*Canvas, error)
	List(ctx context.Context, orgID string, actor core.Subjects) ([]*Canvas, error)
	MoveNode(ctx context.Context, orgID string, actor core.Subjects,
		canvasID, nodeID string, x, y, w, h int) error
	Nodes(ctx context.Context, orgID string, actor core.Subjects,
		canvasID string) ([]*Node, error)
	RemoveEdge(ctx context.Context, orgID string, actor core.Subjects,
		canvasID, edgeID string) error
	RemoveNode(ctx context.Context, orgID string, actor core.Subjects,
		canvasID, nodeID string) error
}

var _ Store = (*Service)(nil)
