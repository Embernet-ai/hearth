package stores

import (
	"context"
	"log/slog"

	"github.com/Embernet-ai/hearth/internal/canvas"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
)

// CanvasMirror keeps canvases in Postgres, where their access is decided and
// where a card is resolved live against the entity it points at (visibility,
// status, "deleted"), and copies each canvas, with its nodes and edges, into
// EmberOPS as a Hearth Document of kind canvas after every change. The graph
// travels as the document's content JSON.
//
// A canvas is Postgres-owned for the same reason a space is: Hearth's grants
// govern it and its cards are resolved against Hearth's own entities on every
// open. An edit made to the content inside EmberOPS does not flow back.
type CanvasMirror struct {
	canvas.Store
	Base
	Log *slog.Logger
}

var _ canvas.Store = (*CanvasMirror)(nil)

func (s *CanvasMirror) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *CanvasMirror) push(ctx context.Context, orgID string, actor core.Subjects, id string) {
	c, err := s.Store.Get(ctx, orgID, actor, id)
	if err != nil {
		s.log().Warn("emberops: could not read a canvas to copy it", "canvas", id, "error", err)
		return
	}
	nodes, err := s.Store.Nodes(ctx, orgID, actor, id)
	if err != nil {
		s.log().Warn("emberops: could not read canvas nodes to copy them", "canvas", id, "error", err)
		return
	}
	edges, err := s.Store.Edges(ctx, orgID, actor, id)
	if err != nil {
		s.log().Warn("emberops: could not read canvas edges to copy them", "canvas", id, "error", err)
		return
	}
	rec := mapping.Rec{"id": c.ID, "name": c.Name}
	if c.SpaceID != nil {
		rec["space_id"] = *c.SpaceID
	}
	var ns, es []mapping.Rec
	for _, n := range nodes {
		r := mapping.Rec{"id": n.ID, "text": n.Text, "x": float64(n.X), "y": float64(n.Y), "w": float64(n.W), "h": float64(n.H)}
		if n.EntityType != nil {
			r["entity_type"] = *n.EntityType
		}
		if n.EntityID != nil {
			r["entity_id"] = *n.EntityID
		}
		if n.Style != nil {
			r["style"] = n.Style
		}
		ns = append(ns, r)
	}
	for _, e := range edges {
		es = append(es, mapping.Rec{"id": e.ID, "from_node": e.From, "to_node": e.To, "label": e.Label})
	}
	doc := mapping.TZ{Loc: s.loc()}.CanvasDoc(rec, ns, es)
	delete(doc, "doctype")
	delete(doc, "hearth_id")
	if c.SpaceID == nil {
		doc["hearth_space"] = nil
	}
	if _, err := s.C.Upsert(ctx, "Hearth Document", c.ID, doc); err != nil {
		s.log().Warn("emberops: could not copy a canvas", "canvas", id, "error", err)
	}
}

func (s *CanvasMirror) Create(ctx context.Context, orgID string, actor core.Subjects, name string, spaceID *string) (*canvas.Canvas, error) {
	c, err := s.Store.Create(ctx, orgID, actor, name, spaceID)
	if err == nil {
		s.push(ctx, orgID, actor, c.ID)
	}
	return c, err
}

func (s *CanvasMirror) AddNode(ctx context.Context, orgID string, actor core.Subjects, canvasID string, n canvas.Node) (*canvas.Node, error) {
	out, err := s.Store.AddNode(ctx, orgID, actor, canvasID, n)
	if err == nil {
		s.push(ctx, orgID, actor, canvasID)
	}
	return out, err
}

func (s *CanvasMirror) AddEdge(ctx context.Context, orgID string, actor core.Subjects, canvasID, from, to, label string) error {
	err := s.Store.AddEdge(ctx, orgID, actor, canvasID, from, to, label)
	if err == nil {
		s.push(ctx, orgID, actor, canvasID)
	}
	return err
}

func (s *CanvasMirror) MoveNode(ctx context.Context, orgID string, actor core.Subjects, canvasID, nodeID string, x, y, w, h int) error {
	err := s.Store.MoveNode(ctx, orgID, actor, canvasID, nodeID, x, y, w, h)
	if err == nil {
		s.push(ctx, orgID, actor, canvasID)
	}
	return err
}

func (s *CanvasMirror) RemoveNode(ctx context.Context, orgID string, actor core.Subjects, canvasID, nodeID string) error {
	err := s.Store.RemoveNode(ctx, orgID, actor, canvasID, nodeID)
	if err == nil {
		s.push(ctx, orgID, actor, canvasID)
	}
	return err
}

func (s *CanvasMirror) RemoveEdge(ctx context.Context, orgID string, actor core.Subjects, canvasID, edgeID string) error {
	err := s.Store.RemoveEdge(ctx, orgID, actor, canvasID, edgeID)
	if err == nil {
		s.push(ctx, orgID, actor, canvasID)
	}
	return err
}

func (s *CanvasMirror) Delete(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	if err := s.Store.Delete(ctx, orgID, actor, id); err != nil {
		return err
	}
	if err := s.C.DeleteByHearthID(emberops.WithUser(ctx, nil), "Hearth Document", id); err != nil {
		s.log().Warn("emberops: could not remove a deleted canvas", "canvas", id, "error", err)
	}
	return nil
}
