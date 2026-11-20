package stores_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Embernet-ai/hearth/internal/canvas"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
)

type pgCanvas struct {
	canvas.Store
	c     *canvas.Canvas
	nodes []*canvas.Node
	edges []*canvas.Edge
}

func (p *pgCanvas) Create(_ context.Context, _ string, _ core.Subjects, name string, sp *string) (*canvas.Canvas, error) {
	p.c = &canvas.Canvas{ID: "cccccccc-1111-1111-1111-111111111111", Name: name, SpaceID: sp}
	return p.c, nil
}
func (p *pgCanvas) Get(context.Context, string, core.Subjects, string) (*canvas.Canvas, error) {
	return p.c, nil
}
func (p *pgCanvas) Nodes(context.Context, string, core.Subjects, string) ([]*canvas.Node, error) {
	return p.nodes, nil
}
func (p *pgCanvas) Edges(context.Context, string, core.Subjects, string) ([]*canvas.Edge, error) {
	return p.edges, nil
}
func (p *pgCanvas) AddNode(_ context.Context, _ string, _ core.Subjects, _ string, n canvas.Node) (*canvas.Node, error) {
	n.ID = "n1"
	p.nodes = append(p.nodes, &n)
	return &n, nil
}
func (p *pgCanvas) AddEdge(_ context.Context, _ string, _ core.Subjects, _, from, to, label string) error {
	p.edges = append(p.edges, &canvas.Edge{ID: "e1", From: from, To: to, Label: label})
	return nil
}
func (p *pgCanvas) Delete(context.Context, string, core.Subjects, string) error { return nil }

func TestCanvasIsCopiedAsAHearthDocument(t *testing.T) {
	e := newEnv(t)
	pg := &pgCanvas{}
	s := &stores.CanvasMirror{Store: pg, Base: e.base}
	ctx := e.as("ann-tok")
	c, err := s.Create(ctx, org, actor(ann), "Line 2", nil)
	if err != nil {
		t.Fatal(err)
	}
	et, eid := "document", taskID
	if _, err := s.AddNode(ctx, org, actor(ann), c.ID, canvas.Node{Text: "Press", X: 10, Y: 20, W: 220, H: 120, EntityType: &et, EntityID: &eid}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEdge(ctx, org, actor(ann), c.ID, "n1", "n2", "feeds"); err != nil {
		t.Fatal(err)
	}
	docs := e.f.All("Hearth Document")
	if len(docs) != 1 || docs[0]["kind"] != "canvas" || docs[0]["title"] != "Line 2" || docs[0]["hearth_id"] != c.ID {
		t.Fatalf("documents = %v", docs)
	}
	var graph struct {
		Nodes []map[string]any `json:"nodes"`
		Edges []map[string]any `json:"edges"`
	}
	if err := json.Unmarshal([]byte(docs[0]["content"].(string)), &graph); err != nil {
		t.Fatal(err)
	}
	if len(graph.Nodes) != 1 || graph.Nodes[0]["entity_id"] != taskID || graph.Nodes[0]["x"] != 10.0 || len(graph.Edges) != 1 || graph.Edges[0]["label"] != "feeds" {
		t.Fatalf("graph = %+v", graph)
	}
	if err := s.Delete(ctx, org, actor(ann), c.ID); err != nil || len(e.f.All("Hearth Document")) != 0 {
		t.Fatalf("delete: %v", err)
	}
}
