package plant

// Who may shape the space tree.
//
// Making and moving folders is structural, so it is held to the same line as
// moving people: a super admin anywhere, a manager within the department they
// manage, and -- for the top level and for deletion -- an admin only. A plain
// member with edit on a space can work in it but not rearrange the tree.
//
// The one exception is a playground, a made-up company run by its own user who
// is deliberately never a super admin; building its structure is part of the
// lessons, so everything is permitted there and nowhere else.

import (
	"context"
	"errors"
	"fmt"

	"github.com/Embernet-ai/hearth/internal/core"
)

// ErrDenied means the actor may see the tree but not reshape this part of it.
var ErrDenied = errors.New("plant: not allowed to change the space tree")

func (s *Service) isPlayground(ctx context.Context, orgID string) (bool, error) {
	var pg bool
	if err := s.db.QueryRow(ctx,
		`SELECT playground_of IS NOT NULL FROM orgs WHERE id = $1`, orgID).Scan(&pg); err != nil {
		return false, fmt.Errorf("plant: is playground: %w", err)
	}
	return pg, nil
}

// maySpaceAdmin reports whether the actor may do an admin-level act on the tree:
// create a top-level department, or delete a space. A super admin, or a
// playground.
func (s *Service) maySpaceAdmin(ctx context.Context, orgID string, actor core.Subjects) (bool, error) {
	if actor.Super() {
		return true, nil
	}
	return s.isPlayground(ctx, orgID)
}

// mayManageSpace reports whether the actor may reshape a particular space --
// add a folder inside it, rename it, move it. A super admin, a manager of the
// space or of a department above it, or a playground.
func (s *Service) mayManageSpace(ctx context.Context, orgID string, actor core.Subjects, spaceID string) (bool, error) {
	if actor.Super() {
		return true, nil
	}
	if pg, err := s.isPlayground(ctx, orgID); err != nil || pg {
		return pg, err
	}
	principal := actor.Principal()
	if principal == "" {
		return false, nil
	}
	var ok bool
	err := s.db.QueryRow(ctx, `
		WITH RECURSIVE up AS (
		    SELECT id, parent_id FROM spaces WHERE id = $1::uuid AND org_id = $2
		  UNION ALL
		    SELECT sp.id, sp.parent_id FROM spaces sp JOIN up ON sp.id = up.parent_id
		)
		SELECT EXISTS (
		    SELECT 1 FROM space_managers m JOIN up ON m.space_id = up.id
		     WHERE m.org_id = $2 AND m.principal_id = $3::uuid)`,
		spaceID, orgID, principal).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("plant: may manage space: %w", err)
	}
	return ok, nil
}

// MayManageAnySpace reports whether the actor can reshape any part of the tree
// at all -- a super admin, a playground user, or a manager of at least one
// space. For the rail, to show the "+ folder" affordance only to those who
// could use it.
func (s *Service) MayManageAnySpace(ctx context.Context, orgID string, actor core.Subjects) (bool, error) {
	if ok, err := s.maySpaceAdmin(ctx, orgID, actor); err != nil || ok {
		return ok, err
	}
	principal := actor.Principal()
	if principal == "" {
		return false, nil
	}
	var ok bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM space_managers WHERE org_id = $1 AND principal_id = $2::uuid)`,
		orgID, principal).Scan(&ok); err != nil {
		return false, fmt.Errorf("plant: manages any: %w", err)
	}
	return ok, nil
}

// ManageableSpaces marks, for a tree the actor can see, which spaces they may
// reshape: all of them for a super admin or in a playground; for a manager,
// the departments they manage and everything beneath. Computed from the tree
// already in hand rather than a query per node.
func (s *Service) ManageableSpaces(ctx context.Context, orgID string, actor core.Subjects, tree []*Space) (map[string]bool, error) {
	out := make(map[string]bool, len(tree))
	admin, err := s.maySpaceAdmin(ctx, orgID, actor)
	if err != nil {
		return nil, err
	}
	if admin {
		for _, sp := range tree {
			out[sp.ID] = true
		}
		return out, nil
	}
	principal := actor.Principal()
	if principal == "" {
		return out, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT space_id::text FROM space_managers WHERE org_id = $1 AND principal_id = $2::uuid`,
		orgID, principal)
	if err != nil {
		return nil, fmt.Errorf("plant: managed roots: %w", err)
	}
	roots := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		roots[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// A space is manageable if it is a managed root or sits under one. Walk up
	// each node's parent chain, bounded against a cycle.
	parent := make(map[string]*string, len(tree))
	for _, sp := range tree {
		parent[sp.ID] = sp.ParentID
	}
	for _, sp := range tree {
		for id, depth := sp.ID, 0; id != "" && depth < 50; depth++ {
			if roots[id] {
				out[sp.ID] = true
				break
			}
			p := parent[id]
			if p == nil {
				break
			}
			id = *p
		}
	}
	return out, nil
}

// ReorderSpaces sets the order of a parent's children to exactly orderedIDs.
// The caller must manage that parent (or be an admin, for the top level). Every
// id has to be a current child of parentID, so one group cannot be used to
// renumber another.
func (s *Service) ReorderSpaces(ctx context.Context, orgID string, actor core.Subjects, parentID string, orderedIDs []string) error {
	var allowed bool
	var err error
	if parentID == "" {
		allowed, err = s.maySpaceAdmin(ctx, orgID, actor)
	} else {
		allowed, err = s.mayManageSpace(ctx, orgID, actor, parentID)
	}
	if err != nil {
		return err
	}
	if !allowed {
		return ErrDenied
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("plant: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for i, id := range orderedIDs {
		tag, err := tx.Exec(ctx, `
			UPDATE spaces SET position = $4, updated_at = now()
			 WHERE id = $1::uuid AND org_id = $2
			   AND COALESCE(parent_id::text, '') = $3`,
			id, orgID, parentID, i)
		if err != nil {
			return fmt.Errorf("plant: reorder: %w", err)
		}
		if tag.RowsAffected() != 1 {
			// Not a child of this parent (or not this org's): refuse the whole
			// batch rather than renumber a stranger.
			return ErrNotFound
		}
	}
	return tx.Commit(ctx)
}

// MayAdminSpaces reports whether the actor may do admin-level tree acts --
// create a top-level department, delete a space: a super admin, or a playground
// user. For the "+ dept" affordance.
func (s *Service) MayAdminSpaces(ctx context.Context, orgID string, actor core.Subjects) (bool, error) {
	return s.maySpaceAdmin(ctx, orgID, actor)
}

// SaveSpaceOrder applies a whole arrangement at once: the ids in the order they
// should appear, from which each parent's children are renumbered by where they
// fall. The actor must manage every parent that is touched, or nothing is saved.
func (s *Service) SaveSpaceOrder(ctx context.Context, orgID string, actor core.Subjects, orderedIDs []string) error {
	// Group ids by their current parent, keeping the given order.
	groups := map[string][]string{}
	order := []string{} // parent keys in first-seen order, just for determinism
	for _, id := range orderedIDs {
		var parent *string
		err := s.db.QueryRow(ctx,
			`SELECT parent_id::text FROM spaces WHERE id = $1::uuid AND org_id = $2`, id, orgID).Scan(&parent)
		if err != nil {
			return fmt.Errorf("plant: read parent: %w", err)
		}
		key := ""
		if parent != nil {
			key = *parent
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], id)
	}

	// Permission for every touched parent, up front, so a refusal saves nothing.
	for _, parent := range order {
		var ok bool
		var err error
		if parent == "" {
			ok, err = s.maySpaceAdmin(ctx, orgID, actor)
		} else {
			ok, err = s.mayManageSpace(ctx, orgID, actor, parent)
		}
		if err != nil {
			return err
		}
		if !ok {
			return ErrDenied
		}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("plant: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, parent := range order {
		for i, id := range groups[parent] {
			if _, err := tx.Exec(ctx,
				`UPDATE spaces SET position = $3, updated_at = now() WHERE id = $1::uuid AND org_id = $2`,
				id, orgID, i); err != nil {
				return fmt.Errorf("plant: save order: %w", err)
			}
		}
	}
	return tx.Commit(ctx)
}
