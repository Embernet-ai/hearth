// Package crmviews stores the filtered, sorted and grouped CRM lists people name and come back to.
//
// A view holds the list's own query string, never rows. Opening one is following a link to the list with that query, so it
// runs against the opener's access like any other visit.
package crmviews

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("view not found")
	ErrName     = errors.New("a view needs a name")
	ErrList     = errors.New("that is not a CRM list")
)

// Lists are the pages a view can be saved on, by their path.
var Lists = map[string]string{"/companies": "companies", "/contacts": "contacts", "/deals": "deals"}

// View is one saved list.
type View struct {
	ID        string
	OrgID     string
	OwnerID   string
	OwnerName string
	List      string
	Name      string
	Query     string
	Shared    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Path is the list this view opens, with its query.
func (v *View) Path() string {
	p := "/" + v.List
	if v.Query != "" {
		p += "?" + v.Query
	}
	return p
}

// Own reports whether the view belongs to that person.
func (v *View) Own(principal string) bool { return v.OwnerID == principal }

type Service struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

// Clean reads the address of a list somebody is looking at and returns the list and the query worth keeping. The page
// number goes, because a saved view opens at the first page, and so do parameters that are not part of a list's question.
func Clean(from string) (list, query string, err error) {
	u, err := url.Parse(from)
	if err != nil {
		return "", "", ErrList
	}
	list, ok := Lists[u.Path]
	if !ok {
		return "", "", ErrList
	}
	q := u.Query()
	q.Del("page")
	return list, q.Encode(), nil
}

// Save creates a view or replaces the person's view of that name on that list.
func (s *Service) Save(ctx context.Context, orgID, ownerID string, v View) (*View, error) {
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" {
		return nil, ErrName
	}
	if _, ok := map[string]bool{"companies": true, "contacts": true, "deals": true}[v.List]; !ok {
		return nil, ErrList
	}
	v.OrgID, v.OwnerID = orgID, ownerID
	err := s.db.QueryRow(ctx, `
		INSERT INTO crm_saved_views (id, org_id, owner_id, list, name, query, shared)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (org_id, owner_id, list, name)
		DO UPDATE SET query = EXCLUDED.query, shared = EXCLUDED.shared, updated_at = now()
		RETURNING id::text, created_at, updated_at`,
		uuid.NewString(), orgID, ownerID, v.List, v.Name, v.Query, v.Shared).
		Scan(&v.ID, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("crmviews: save: %w", err)
	}
	return &v, nil
}

// List returns the person's own views of one list and every shared view of it, own first.
func (s *Service) List(ctx context.Context, orgID, principal, list string) ([]*View, error) {
	rows, err := s.db.Query(ctx, `
		SELECT v.id::text, v.owner_id::text, coalesce(p.display_name,''), v.list, v.name, v.query, v.shared,
		       v.created_at, v.updated_at
		FROM crm_saved_views v
		JOIN principals p ON p.id = v.owner_id
		WHERE v.org_id = $1 AND v.list = $2 AND (v.owner_id = $3 OR v.shared)
		ORDER BY (v.owner_id = $3) DESC, lower(v.name), v.id`, orgID, list, principal)
	if err != nil {
		return nil, fmt.Errorf("crmviews: list: %w", err)
	}
	defer rows.Close()
	var out []*View
	for rows.Next() {
		v := &View{OrgID: orgID}
		if err := rows.Scan(&v.ID, &v.OwnerID, &v.OwnerName, &v.List, &v.Name, &v.Query, &v.Shared, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, fmt.Errorf("crmviews: scan: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Delete removes the person's own view. Somebody else's shared view is not theirs to delete.
func (s *Service) Delete(ctx context.Context, orgID, principal, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM crm_saved_views WHERE id = $1 AND org_id = $2 AND owner_id = $3`, id, orgID, principal)
	if err != nil {
		return fmt.Errorf("crmviews: delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
