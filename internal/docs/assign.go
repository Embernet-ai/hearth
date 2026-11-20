package docs

// Assigning a task to one or more people.
//
// A task has a primary assignee (task_fields.assignee_id, what the board and
// Workload count it under) and any number of others (document_assignees).
// Together they are who the task is assigned to; each one can open it and shows
// it in their "assigned to me". The two tables are kept in step here, and the
// filter reads both, so a task assigned the old single-field way still counts.
//
// Who may assign is narrower than who may edit: the task's creator, a manager
// of the department it is filed in, or an admin. Creating a task does not
// assign it to you -- the creator can see it because they own it, and adds
// themselves only if they mean to.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// MayAssign reports whether the actor may set who a task is assigned to: a
// super admin, the task's owner (its creator), or a manager of the space it is
// filed in or any department above that space.
func (s *Service) MayAssign(ctx context.Context, orgID string, actor core.Subjects, docID string) (bool, error) {
	if actor.Super() {
		return true, nil
	}
	principal := actor.Principal()
	if principal == "" {
		return false, nil
	}
	var allowed bool
	err := s.db.QueryRow(ctx, `
		WITH RECURSIVE here AS (
		    SELECT id, owner_id, space_id FROM documents
		     WHERE id = $1::uuid AND org_id = $2 AND deleted_at IS NULL
		), up AS (
		    SELECT space_id AS id FROM here WHERE space_id IS NOT NULL
		  UNION ALL
		    SELECT sp.parent_id FROM spaces sp JOIN up ON sp.id = up.id
		     WHERE sp.parent_id IS NOT NULL
		)
		SELECT EXISTS (SELECT 1 FROM here WHERE owner_id = $3::uuid)
		    OR EXISTS (SELECT 1 FROM space_managers m JOIN up ON m.space_id = up.id
		               WHERE m.org_id = $2 AND m.principal_id = $3::uuid)`,
		docID, orgID, principal).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("docs: may assign: %w", err)
	}
	return allowed, nil
}

// AssignedTo is who a task is assigned to, primary first, merging the primary
// field with the extra-assignee table so the full set reads the same however
// it was set.
func (s *Service) AssignedTo(ctx context.Context, docID string) (ids []string, primary string, err error) {
	rows, err := s.db.Query(ctx, `
		SELECT p::text, prim FROM (
		    SELECT assignee_id AS p, true AS prim, 0 AS ord
		      FROM task_fields WHERE document_id = $1 AND assignee_id IS NOT NULL
		  UNION
		    SELECT principal_id, is_primary, 1
		      FROM document_assignees WHERE document_id = $1
		) x
		WHERE p IS NOT NULL
		ORDER BY prim DESC, ord`, docID)
	if err != nil {
		return nil, "", fmt.Errorf("docs: assigned to: %w", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var id string
		var prim bool
		if err := rows.Scan(&id, &prim); err != nil {
			return nil, "", err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
		if prim && primary == "" {
			primary = id
		}
	}
	if primary == "" && len(ids) > 0 {
		primary = ids[0]
	}
	return ids, primary, rows.Err()
}

// Assign sets exactly who a task is assigned to. The primary is the one the
// board and Workload use; an empty list clears the assignment. Every assignee
// gains edit on the task, because work given to somebody who cannot open it is
// not given. Each newly added person is notified.
func (s *Service) Assign(ctx context.Context, orgID string, actor core.Subjects, docID string, assigneeIDs []string, primary string) error {
	ok, err := s.MayAssign(ctx, orgID, actor, docID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotAllowed
	}

	ids := uniqueStrings(assigneeIDs)
	if len(ids) > 0 {
		if primary == "" || !contains(ids, primary) {
			primary = ids[0]
		}
	} else {
		primary = ""
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("docs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Everyone named has to be a person in this org, or a stray id would be
	// filed as an assignee and granted the task.
	for _, id := range ids {
		var ok bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM principals WHERE id = $1::uuid AND org_id = $2 AND kind = 'user')`,
			id, orgID).Scan(&ok); err != nil {
			return fmt.Errorf("docs: check assignee: %w", err)
		}
		if !ok {
			return fmt.Errorf("%w: assignee", ErrNotFound)
		}
	}

	// Who it was assigned to before, so only the newly added are notified.
	before := map[string]bool{}
	rows, err := tx.Query(ctx, `
		SELECT principal_id::text FROM document_assignees WHERE document_id = $1
		UNION
		SELECT assignee_id::text FROM task_fields WHERE document_id = $1 AND assignee_id IS NOT NULL`, docID)
	if err != nil {
		return fmt.Errorf("docs: read assignees: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		before[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM document_assignees WHERE document_id = $1`, docID); err != nil {
		return fmt.Errorf("docs: clear assignees: %w", err)
	}
	var primaryVal any
	if primary != "" {
		primaryVal = primary
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `
			INSERT INTO document_assignees (document_id, principal_id, is_primary)
			VALUES ($1, $2::uuid, $3)`, docID, id, id == primary); err != nil {
			return fmt.Errorf("docs: add assignee: %w", err)
		}
		// Edit, like the single-assignee path: the person doing the work has to
		// record what they did.
		if _, err := tx.Exec(ctx, `
			INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
			VALUES (gen_random_uuid(), $1, 'document', $2, $3, 'edit', $4)
			ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
			orgID, docID, core.Subject{Kind: core.SubjectPrincipal, ID: id}.Key(), actor.Principal()); err != nil {
			return fmt.Errorf("docs: grant to assignee: %w", err)
		}
	}
	// The primary mirrors into task_fields, which the board, Workload and every
	// "group by assignee" already read.
	if _, err := tx.Exec(ctx,
		`UPDATE task_fields SET assignee_id = $2 WHERE document_id = $1`, docID, primaryVal); err != nil {
		return fmt.Errorf("docs: set primary assignee: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE documents SET updated_at = now() WHERE id = $1 AND org_id = $2`, docID, orgID); err != nil {
		return fmt.Errorf("docs: touch document: %w", err)
	}

	for _, id := range ids {
		if before[id] {
			continue
		}
		if err := jobs.Publish(ctx, tx, jobs.EventSpec{
			OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: docID,
			Event: jobs.EventAssigned, ActorID: actor.Principal(),
			Payload: map[string]any{"assignee_id": id},
		}); err != nil {
			return fmt.Errorf("docs: announce assignment: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func uniqueStrings(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
