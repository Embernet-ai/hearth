package crm

import (
	"context"
	"fmt"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Favorite is a record a person has starred.
type Favorite struct {
	Ref       core.Ref
	CreatedAt time.Time
}

// SetFavorite stars or unstars a record for the acting person. Only a record the person can see can be starred.
func (s *Service) SetFavorite(ctx context.Context, orgID string, actor core.Subjects, ref core.Ref, on bool) error {
	principal := actor.Principal()
	if principal == "" {
		return fmt.Errorf("crm: no acting principal")
	}
	switch ref.Type {
	case core.EntityCompany, core.EntityContact, core.EntityDeal:
	default:
		return fmt.Errorf("%w: only companies, contacts and deals can be starred", ErrInvalid)
	}
	if !on {
		_, err := s.db.Exec(ctx, `DELETE FROM crm_favorites WHERE org_id=$1 AND principal_id=$2 AND entity_type=$3 AND entity_id=$4`,
			orgID, principal, string(ref.Type), ref.ID)
		if err != nil {
			return fmt.Errorf("crm: unstar: %w", err)
		}
		return nil
	}
	if err := s.authz.Require(ctx, actor, ref, core.AccessView); err != nil {
		return ErrNotFound
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO crm_favorites (org_id, principal_id, entity_type, entity_id) VALUES ($1,$2,$3,$4)
		ON CONFLICT DO NOTHING`, orgID, principal, string(ref.Type), ref.ID)
	if err != nil {
		return fmt.Errorf("crm: star: %w", err)
	}
	return nil
}

// IsFavorite reports whether the acting person has starred the record.
func (s *Service) IsFavorite(ctx context.Context, orgID string, actor core.Subjects, ref core.Ref) (bool, error) {
	var on bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_favorites
		WHERE org_id=$1 AND principal_id=$2 AND entity_type=$3 AND entity_id=$4)`,
		orgID, actor.Principal(), string(ref.Type), ref.ID).Scan(&on)
	if err != nil {
		return false, fmt.Errorf("crm: is favorite: %w", err)
	}
	return on, nil
}

// Favorites lists the acting person's starred records, newest first. A record they can no longer see, or that has been
// deleted, is left out rather than named.
func (s *Service) Favorites(ctx context.Context, orgID string, actor core.Subjects) ([]Favorite, error) {
	rows, err := s.db.Query(ctx, `
		SELECT f.entity_type, f.entity_id::text, f.created_at
		FROM crm_favorites f
		WHERE f.org_id = $1 AND f.principal_id = $2
		  AND CASE f.entity_type
		        WHEN 'company' THEN EXISTS (SELECT 1 FROM companies c WHERE c.id = f.entity_id AND c.deleted_at IS NULL AND `+visible("company", "c", 3)+`)
		        WHEN 'contact' THEN EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = f.entity_id AND ct.deleted_at IS NULL AND `+visible("contact", "ct", 3)+`)
		        ELSE EXISTS (SELECT 1 FROM deals d WHERE d.id = f.entity_id AND d.deleted_at IS NULL AND `+visible("deal", "d", 3)+`)
		      END
		ORDER BY f.created_at DESC, f.entity_id`, orgID, actor.Principal(), actor.Keys())
	if err != nil {
		return nil, fmt.Errorf("crm: favorites: %w", err)
	}
	defer rows.Close()
	var out []Favorite
	for rows.Next() {
		var typ string
		var f Favorite
		if err := rows.Scan(&typ, &f.Ref.ID, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("crm: scan favorite: %w", err)
		}
		f.Ref.Type = core.EntityType(typ)
		out = append(out, f)
	}
	return out, rows.Err()
}
