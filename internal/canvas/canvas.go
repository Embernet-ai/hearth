// Package canvas is a spatial surface for the things a list cannot hold.
//
// A line diagram, a floor plan, a decision somebody sketched in a meeting.
//
// # Why this is not a drawing tool
//
// Because a picture of the plant goes out of date the day it is drawn, and
// then it is worse than nothing: somebody reads it, believes it, and walks to
// a machine that was decommissioned last year. What makes this worth having is
// that a card on it is a live entity. The box labelled Conveyor 3 opens the
// asset, shows whether it is down right now, and disappears when the asset is
// deleted, because it is a reference rather than a rectangle with a word in it.
//
// So there is no freehand drawing here, no shapes, and no colours beyond what
// the entity itself says. Every one of those would be a thing to keep in step
// with reality, and none of them answers a question.
package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/core"
)

var (
	ErrNotFound = errors.New("canvas not found")
	ErrInvalid  = errors.New("that is not something a canvas can hold")
)

// Canvas is one surface.
type Canvas struct {
	ID        string
	OrgID     string
	SpaceID   *string
	Name      string
	Nodes     int
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (c *Canvas) Ref() core.Ref { return core.Ref{Type: core.EntitySpace, ID: c.ID} }

// Node is one card.
type Node struct {
	ID         string
	CanvasID   string
	EntityType *string
	EntityID   *string
	Text       string
	X, Y, W, H int
	Style      map[string]any

	// The live part. Filled from whatever the card points at, every time the
	// canvas is opened, which is what stops the diagram becoming a picture.
	Title  string
	Kind   string
	Status string
	// Visible is false when the card points at something this caller cannot
	// see. The card still occupies its place, so the diagram does not silently
	// rearrange, but it says nothing about what is there.
	Visible bool
	// Missing is true when the entity has been deleted. Shown rather than
	// hidden, because a hole in a line diagram is information.
	Missing bool
}

// Label is what to draw on the card.
func (n Node) Label() string {
	switch {
	case n.EntityID == nil:
		return n.Text
	case n.Missing:
		return "(deleted)"
	case !n.Visible:
		return "(not shared with you)"
	case n.Title != "":
		return n.Title
	default:
		return "Untitled"
	}
}

// Href is where clicking the card goes, empty when there is nowhere.
func (n Node) Href() string {
	if n.EntityID == nil || !n.Visible || n.Missing {
		return ""
	}
	switch core.EntityType(*n.EntityType) {
	case core.EntityContact:
		return "/contacts/" + *n.EntityID
	case core.EntityCompany:
		return "/companies/" + *n.EntityID
	default:
		return "/documents/" + *n.EntityID
	}
}

// Edge joins two cards.
type Edge struct {
	ID       string
	CanvasID string
	From     string
	To       string
	Label    string
}

// Authorizer is the slice of access control this package needs.
type Authorizer interface {
	Require(ctx context.Context, subjects core.Subjects, ref core.Ref, required core.AccessLevel) error
}

type Service struct {
	db    *pgxpool.Pool
	authz Authorizer
}

func New(db *pgxpool.Pool, az Authorizer) *Service { return &Service{db: db, authz: az} }

// Create adds a canvas and grants its creator admin, in one transaction.
func (s *Service) Create(ctx context.Context, orgID string, actor core.Subjects,
	name string, spaceID *string) (*Canvas, error) {

	principal := actor.Principal()
	if principal == "" {
		return nil, errors.New("canvas: no acting principal")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: a canvas needs a name", ErrInvalid)
	}
	if spaceID != nil && *spaceID == "" {
		spaceID = nil
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("canvas: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	c := &Canvas{ID: uuid.NewString(), OrgID: orgID, Name: name, SpaceID: spaceID}
	err = tx.QueryRow(ctx, `
		INSERT INTO canvases (id, org_id, space_id, name, created_by)
		VALUES ($1,$2,$3,$4,$5) RETURNING created_at, updated_at`,
		c.ID, orgID, spaceID, name, principal).Scan(&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("canvas: create: %w", err)
	}
	// A canvas is granted as a space, because it is a container of references
	// rather than a document, and reusing the entity type means the grants
	// table needs nothing new.
	if _, err := tx.Exec(ctx, `
		INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		VALUES (gen_random_uuid(), $1, 'space', $2, $3, 'admin', $4)`,
		orgID, c.ID, core.Subject{Kind: core.SubjectPrincipal, ID: principal}.Key(), principal,
	); err != nil {
		return nil, fmt.Errorf("canvas: grant: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("canvas: commit: %w", err)
	}
	return c, nil
}

// List returns the canvases the caller can see.
func (s *Service) List(ctx context.Context, orgID string, actor core.Subjects) ([]*Canvas, error) {
	rows, err := s.db.Query(ctx, `
		SELECT c.id::text, c.space_id::text, c.name, c.created_at, c.updated_at,
		       (SELECT count(*) FROM canvas_nodes n WHERE n.canvas_id = c.id)::int
		FROM canvases c
		WHERE c.org_id = $1 AND c.deleted_at IS NULL
		  AND EXISTS (
		      SELECT 1 FROM grants g
		       WHERE g.entity_type = 'space' AND g.entity_id = c.id
		         AND g.subject_key = ANY($2)
		         AND (g.expires_at IS NULL OR g.expires_at > now()))
		ORDER BY c.name`, orgID, actor.Keys())
	if err != nil {
		return nil, fmt.Errorf("canvas: list: %w", err)
	}
	defer rows.Close()

	var out []*Canvas
	for rows.Next() {
		c := &Canvas{OrgID: orgID}
		if err := rows.Scan(&c.ID, &c.SpaceID, &c.Name, &c.CreatedAt, &c.UpdatedAt,
			&c.Nodes); err != nil {
			return nil, fmt.Errorf("canvas: scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get returns one canvas.
func (s *Service) Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*Canvas, error) {
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntitySpace, ID: id}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	c := &Canvas{ID: id, OrgID: orgID}
	err := s.db.QueryRow(ctx, `
		SELECT space_id::text, name, created_at, updated_at
		FROM canvases WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`, id, orgID).
		Scan(&c.SpaceID, &c.Name, &c.CreatedAt, &c.UpdatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("canvas: get: %w", err)
	}
	return c, nil
}

// Delete removes a canvas. Its cards go with it; the entities they pointed at
// are untouched, because a card was only ever a reference.
func (s *Service) Delete(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntitySpace, ID: id}, core.AccessAdmin); err != nil {
		return err
	}
	ct, err := s.db.Exec(ctx,
		`UPDATE canvases SET deleted_at=now() WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`,
		id, orgID)
	if err != nil {
		return fmt.Errorf("canvas: delete: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Nodes returns the cards, resolved against what is actually there now.
//
// This is the whole feature. Every card is joined to its entity on every read,
// so a diagram opened today reflects today: a machine that is down says so, a
// work order that was completed shows completed, and a card pointing at
// something deleted says that rather than lying quietly.
func (s *Service) Nodes(ctx context.Context, orgID string, actor core.Subjects,
	canvasID string) ([]*Node, error) {

	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntitySpace, ID: canvasID}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx, `
		SELECT n.id::text, n.entity_type, n.entity_id::text, n.text,
		       n.x, n.y, n.w, n.h, n.style,
		       COALESCE(d.title, ct.name, co.name, ''),
		       COALESCE(d.kind, ''),
		       COALESCE(t.status, ''),
		       -- Deleted rather than merely invisible. The two look the same
		       -- from here and mean different things to a reader.
		       (n.entity_id IS NOT NULL AND d.id IS NULL AND ct.id IS NULL AND co.id IS NULL),
		       (n.entity_id IS NULL OR EXISTS (
		           SELECT 1 FROM grants g
		            WHERE g.entity_type = n.entity_type AND g.entity_id = n.entity_id
		              AND g.subject_key = ANY($3)
		              AND (g.expires_at IS NULL OR g.expires_at > now())))
		FROM canvas_nodes n
		LEFT JOIN documents d  ON n.entity_type = 'document' AND d.id  = n.entity_id AND d.deleted_at IS NULL
		LEFT JOIN task_fields t ON t.document_id = d.id
		LEFT JOIN contacts ct  ON n.entity_type = 'contact'  AND ct.id = n.entity_id AND ct.deleted_at IS NULL
		LEFT JOIN companies co ON n.entity_type = 'company'  AND co.id = n.entity_id AND co.deleted_at IS NULL
		WHERE n.canvas_id = $1 AND n.org_id = $2
		ORDER BY n.y, n.x`, canvasID, orgID, actor.Keys())
	if err != nil {
		return nil, fmt.Errorf("canvas: nodes: %w", err)
	}
	defer rows.Close()

	var out []*Node
	for rows.Next() {
		n := &Node{CanvasID: canvasID}
		var style []byte
		if err := rows.Scan(&n.ID, &n.EntityType, &n.EntityID, &n.Text,
			&n.X, &n.Y, &n.W, &n.H, &style,
			&n.Title, &n.Kind, &n.Status, &n.Missing, &n.Visible); err != nil {
			return nil, fmt.Errorf("canvas: scan node: %w", err)
		}
		if len(style) > 0 {
			_ = json.Unmarshal(style, &n.Style)
		}
		// A card the caller cannot see keeps its place and loses its content.
		// Blanking the title here rather than in SQL keeps the permission
		// answer in one expression above.
		if !n.Visible {
			n.Title, n.Kind, n.Status = "", "", ""
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// AddNode puts a card on a canvas.
func (s *Service) AddNode(ctx context.Context, orgID string, actor core.Subjects,
	canvasID string, n Node) (*Node, error) {

	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntitySpace, ID: canvasID}, core.AccessEdit); err != nil {
		return nil, err
	}
	if n.EntityID != nil && *n.EntityID == "" {
		n.EntityID, n.EntityType = nil, nil
	}
	if n.EntityID != nil {
		if n.EntityType == nil || *n.EntityType == "" {
			t := string(core.EntityDocument)
			n.EntityType = &t
		}
		// View on the thing being added. Dropping a card for something you
		// cannot see would let somebody discover an id by watching what the
		// canvas refuses.
		if err := s.authz.Require(ctx, actor,
			core.Ref{Type: core.EntityType(*n.EntityType), ID: *n.EntityID},
			core.AccessView); err != nil {
			return nil, ErrNotFound
		}
	}
	n.Text = strings.TrimSpace(n.Text)
	if n.EntityID == nil && n.Text == "" {
		return nil, fmt.Errorf("%w: a card needs something on it", ErrInvalid)
	}
	if n.W <= 0 {
		n.W = 220
	}
	if n.H <= 0 {
		n.H = 120
	}

	n.ID, n.CanvasID = uuid.NewString(), canvasID
	_, err := s.db.Exec(ctx, `
		INSERT INTO canvas_nodes (id, canvas_id, org_id, entity_type, entity_id,
		                          text, x, y, w, h)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		n.ID, canvasID, orgID, n.EntityType, n.EntityID, n.Text, n.X, n.Y, n.W, n.H)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: that is already on this canvas", ErrInvalid)
		}
		return nil, fmt.Errorf("canvas: add node: %w", err)
	}
	s.touch(ctx, canvasID)
	return &n, nil
}

// MoveNode changes a card's position or size.
func (s *Service) MoveNode(ctx context.Context, orgID string, actor core.Subjects,
	canvasID, nodeID string, x, y, w, h int) error {

	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntitySpace, ID: canvasID}, core.AccessEdit); err != nil {
		return err
	}
	// Clamped rather than refused. A drag that goes wrong should put the card
	// at the edge, not fail with an error nobody can act on while holding a
	// mouse button.
	x, y = clamp(x, -100000, 100000), clamp(y, -100000, 100000)
	w, h = clamp(w, 40, 4000), clamp(h, 30, 4000)

	ct, err := s.db.Exec(ctx, `
		UPDATE canvas_nodes SET x=$4, y=$5, w=$6, h=$7, updated_at=now()
		 WHERE id=$1 AND canvas_id=$2 AND org_id=$3`, nodeID, canvasID, orgID, x, y, w, h)
	if err != nil {
		return fmt.Errorf("canvas: move node: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.touch(ctx, canvasID)
	return nil
}

// RemoveNode takes a card off. The entity is untouched.
func (s *Service) RemoveNode(ctx context.Context, orgID string, actor core.Subjects,
	canvasID, nodeID string) error {

	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntitySpace, ID: canvasID}, core.AccessEdit); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx,
		`DELETE FROM canvas_nodes WHERE id=$1 AND canvas_id=$2 AND org_id=$3`,
		nodeID, canvasID, orgID); err != nil {
		return fmt.Errorf("canvas: remove node: %w", err)
	}
	s.touch(ctx, canvasID)
	return nil
}

// Edges returns the lines between cards.
func (s *Service) Edges(ctx context.Context, orgID string, actor core.Subjects,
	canvasID string) ([]*Edge, error) {

	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntitySpace, ID: canvasID}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx, `
		SELECT id::text, from_node::text, to_node::text, label
		FROM canvas_edges WHERE canvas_id=$1 AND org_id=$2`, canvasID, orgID)
	if err != nil {
		return nil, fmt.Errorf("canvas: edges: %w", err)
	}
	defer rows.Close()

	var out []*Edge
	for rows.Next() {
		e := &Edge{CanvasID: canvasID}
		if err := rows.Scan(&e.ID, &e.From, &e.To, &e.Label); err != nil {
			return nil, fmt.Errorf("canvas: scan edge: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddEdge joins two cards.
func (s *Service) AddEdge(ctx context.Context, orgID string, actor core.Subjects,
	canvasID, from, to, label string) error {

	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntitySpace, ID: canvasID}, core.AccessEdit); err != nil {
		return err
	}
	if from == to {
		return fmt.Errorf("%w: a line from a card to itself", ErrInvalid)
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO canvas_edges (id, canvas_id, org_id, from_node, to_node, label)
		SELECT gen_random_uuid(), $1, $2, $3, $4, $5
		 WHERE EXISTS (SELECT 1 FROM canvas_nodes WHERE id=$3 AND canvas_id=$1)
		   AND EXISTS (SELECT 1 FROM canvas_nodes WHERE id=$4 AND canvas_id=$1)
		ON CONFLICT (from_node, to_node) DO UPDATE SET label = EXCLUDED.label`,
		canvasID, orgID, from, to, strings.TrimSpace(label))
	if err != nil {
		return fmt.Errorf("canvas: add edge: %w", err)
	}
	s.touch(ctx, canvasID)
	return nil
}

// RemoveEdge takes a line off.
func (s *Service) RemoveEdge(ctx context.Context, orgID string, actor core.Subjects,
	canvasID, edgeID string) error {

	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntitySpace, ID: canvasID}, core.AccessEdit); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx,
		`DELETE FROM canvas_edges WHERE id=$1 AND canvas_id=$2 AND org_id=$3`,
		edgeID, canvasID, orgID); err != nil {
		return fmt.Errorf("canvas: remove edge: %w", err)
	}
	s.touch(ctx, canvasID)
	return nil
}

// touch records that the canvas changed. Best effort: failing a move because
// the timestamp did not update would be the wrong trade.
func (s *Service) touch(ctx context.Context, id string) {
	_, _ = s.db.Exec(ctx, `UPDATE canvases SET updated_at=now() WHERE id=$1`, id)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}
