// Package views stores the filtered lists people name and come back to.
//
// A saved view holds a QUESTION, never answers. Two people opening the same
// view run the same query against their own subject set, so each of them sees
// the rows they are allowed to see and neither learns anything about the other's
// access. That is why this package has no grants of its own: there is no data
// here to protect, and pretending otherwise would imply sharing a view could
// disclose something.
//
// The stored query is the same structure the address bar encodes. A saved view
// and a link to a filtered list are one object, so they cannot drift.
package views

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

	"github.com/Embernet-ai/hearth/internal/docs"
)

var (
	ErrNotFound = errors.New("view not found")
	// ErrDuplicate means this person already has a view by that name. Names are
	// unique per person, not per org: two people are each allowed a "Mine".
	ErrDuplicate = errors.New("you already have a view with that name")
	ErrName      = errors.New("a view needs a name")
	ErrKind      = errors.New("unknown view kind")
)

// View is one saved list.
type View struct {
	ID      string
	OrgID   string
	OwnerID string
	SpaceID *string
	Name    string
	// Kind is how it renders: table, board, calendar, or timeline.
	Kind      string
	Query     docs.Query
	Shared    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Path is where this view lives, which is also how it is shared. The query
// string comes along so the address is a complete description of the list even
// if the row is later deleted.
func (v *View) Path() string {
	q := v.Query.Encode()
	q.Set("view", v.Kind)
	return "/views/" + v.ID + "?" + q.Encode()
}

type Service struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

// Save creates or replaces a view by name for one person.
//
// Upsert on (org, owner, name) rather than erroring, because "save this view"
// pressed twice means the second one wins, and making somebody delete their own
// view before they can correct it is friction with no purpose.
func (s *Service) Save(ctx context.Context, orgID, ownerID string, v View) (*View, error) {
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" {
		return nil, ErrName
	}
	if v.Kind == "" {
		v.Kind = docs.ViewTable
	}
	if !docs.ValidViewKind(v.Kind) {
		return nil, fmt.Errorf("%w: %q", ErrKind, v.Kind)
	}

	payload, err := json.Marshal(v.Query)
	if err != nil {
		return nil, fmt.Errorf("views: encode query: %w", err)
	}

	out := &View{OrgID: orgID, OwnerID: ownerID, Name: v.Name, Kind: v.Kind,
		Query: v.Query, Shared: v.Shared, SpaceID: v.SpaceID}
	err = s.db.QueryRow(ctx, `
		INSERT INTO saved_views (id, org_id, owner_id, space_id, name, kind, query, shared)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (org_id, owner_id, name) DO UPDATE
		    SET space_id = EXCLUDED.space_id,
		        kind     = EXCLUDED.kind,
		        query    = EXCLUDED.query,
		        shared   = EXCLUDED.shared,
		        updated_at = now()
		RETURNING id::text, created_at, updated_at`,
		uuid.NewString(), orgID, ownerID, v.SpaceID, v.Name, v.Kind, payload, v.Shared).
		Scan(&out.ID, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("views: save: %w", err)
	}
	return out, nil
}

// Get returns a view the caller may open: their own, or one somebody shared.
//
// A view that exists but is neither is reported as not found, for the same
// reason a document is: telling somebody an id is real is a disclosure, even
// when what is behind it is only a query.
func (s *Service) Get(ctx context.Context, orgID, principalID, id string) (*View, error) {
	v := &View{}
	var payload []byte
	err := s.db.QueryRow(ctx, `
		SELECT id::text, org_id::text, owner_id::text, space_id::text,
		       name, kind, query, shared, created_at, updated_at
		FROM saved_views
		WHERE id = $1 AND org_id = $2 AND (owner_id = $3 OR shared)`,
		id, orgID, principalID).
		Scan(&v.ID, &v.OrgID, &v.OwnerID, &v.SpaceID,
			&v.Name, &v.Kind, &payload, &v.Shared, &v.CreatedAt, &v.UpdatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("views: get %s: %w", id, err)
	}
	if err := json.Unmarshal(payload, &v.Query); err != nil {
		return nil, fmt.Errorf("views: decode query for %s: %w", id, err)
	}
	return v, nil
}

// List returns the caller's own views and everything shared with the org, with
// their own first so a long shared list never buries them.
func (s *Service) List(ctx context.Context, orgID, principalID string) ([]*View, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, org_id::text, owner_id::text, space_id::text,
		       name, kind, query, shared, created_at, updated_at
		FROM saved_views
		WHERE org_id = $1 AND (owner_id = $2 OR shared)
		ORDER BY (owner_id = $2) DESC, name`, orgID, principalID)
	if err != nil {
		return nil, fmt.Errorf("views: list: %w", err)
	}
	defer rows.Close()

	var out []*View
	for rows.Next() {
		v := &View{}
		var payload []byte
		if err := rows.Scan(&v.ID, &v.OrgID, &v.OwnerID, &v.SpaceID,
			&v.Name, &v.Kind, &payload, &v.Shared, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, fmt.Errorf("views: scan: %w", err)
		}
		if err := json.Unmarshal(payload, &v.Query); err != nil {
			return nil, fmt.Errorf("views: decode query for %s: %w", v.ID, err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Delete removes a view. Only its owner can, including when it is shared:
// unsharing somebody else's list is not the same authority as ignoring it.
func (s *Service) Delete(ctx context.Context, orgID, principalID, id string) error {
	ct, err := s.db.Exec(ctx,
		`DELETE FROM saved_views WHERE id=$1 AND org_id=$2 AND owner_id=$3`,
		id, orgID, principalID)
	if err != nil {
		return fmt.Errorf("views: delete: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
