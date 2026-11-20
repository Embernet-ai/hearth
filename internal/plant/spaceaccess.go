package plant

import (
	"context"
	"fmt"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
)

// SpaceGrant is one row of who may reach a space.
type SpaceGrant struct {
	Kind      core.SubjectKind
	SubjectID string
	Level     core.AccessLevel
	ExpiresAt *time.Time
}

// SpaceAccess is a space with everything that decides who may reach it. The
// EmberOPS back end copies it into EmberOPS, so permission hooks there see the
// access Hearth shows.
type SpaceAccess struct {
	ID       string
	ParentID *string
	Name     string
	Kind     string
	Position int
	// Managers and Members are principal ids: the people who manage the
	// department, and the people who belong to it.
	Managers []string
	Members  []string
	Grants   []SpaceGrant
}

// SpaceAccessAll reads every space of an org, parents before children, in four
// queries. It checks no permission: it is for the system copying the structure
// elsewhere.
func (s *Service) SpaceAccessAll(ctx context.Context, orgID string) ([]*SpaceAccess, error) {
	rows, err := s.db.Query(ctx, `
		WITH RECURSIVE tree AS (
		    SELECT id, parent_id, name, kind, position, 0 AS depth FROM spaces WHERE org_id = $1 AND parent_id IS NULL
		  UNION ALL
		    SELECT c.id, c.parent_id, c.name, c.kind, c.position, t.depth + 1
		      FROM spaces c JOIN tree t ON c.parent_id = t.id WHERE t.depth < $2
		) CYCLE id SET looped USING tree_path
		SELECT id::text, parent_id::text, name, kind, position FROM tree WHERE NOT looped ORDER BY depth, position, lower(name), id`,
		orgID, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("plant: space access: %w", err)
	}
	var out []*SpaceAccess
	by := map[string]*SpaceAccess{}
	for rows.Next() {
		sp := &SpaceAccess{}
		if err := rows.Scan(&sp.ID, &sp.ParentID, &sp.Name, &sp.Kind, &sp.Position); err != nil {
			rows.Close()
			return nil, fmt.Errorf("plant: scan space access: %w", err)
		}
		out = append(out, sp)
		by[sp.ID] = sp
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, q := range []struct {
		sql string
		add func(sp *SpaceAccess, id string)
	}{
		{`SELECT m.space_id::text, m.principal_id::text FROM space_managers m JOIN spaces s ON s.id = m.space_id WHERE s.org_id = $1`,
			func(sp *SpaceAccess, id string) { sp.Managers = append(sp.Managers, id) }},
		{`SELECT d.space_id::text, d.principal_id::text FROM principal_departments d JOIN spaces s ON s.id = d.space_id WHERE s.org_id = $1`,
			func(sp *SpaceAccess, id string) { sp.Members = append(sp.Members, id) }},
	} {
		rs, err := s.db.Query(ctx, q.sql, orgID)
		if err != nil {
			return nil, fmt.Errorf("plant: space people: %w", err)
		}
		for rs.Next() {
			var space, who string
			if err := rs.Scan(&space, &who); err != nil {
				rs.Close()
				return nil, err
			}
			if sp := by[space]; sp != nil {
				q.add(sp, who)
			}
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			return nil, err
		}
	}

	gr, err := s.db.Query(ctx, `
		SELECT entity_id::text, subject_key, level, expires_at FROM grants
		 WHERE org_id = $1 AND entity_type = 'space' AND (expires_at IS NULL OR expires_at > now())`, orgID)
	if err != nil {
		return nil, fmt.Errorf("plant: space grants: %w", err)
	}
	defer gr.Close()
	for gr.Next() {
		var space, key, level string
		var exp *time.Time
		if err := gr.Scan(&space, &key, &level, &exp); err != nil {
			return nil, err
		}
		sub, err := core.ParseSubject(key)
		if err != nil {
			continue
		}
		if sp := by[space]; sp != nil {
			sp.Grants = append(sp.Grants, SpaceGrant{Kind: sub.Kind, SubjectID: sub.ID, Level: core.AccessLevel(level), ExpiresAt: exp})
		}
	}
	return out, gr.Err()
}
