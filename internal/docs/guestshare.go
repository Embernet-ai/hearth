package docs

// Sharing one task with one person at a chosen level: a guest.
//
// The grant engine already carries view / comment / edit, enforced everywhere;
// this is the way to hand one of them to a named person for a single task
// without putting them on it as an assignee or opening the whole department.
// Who may share is the same narrow set who may assign -- the task's creator, a
// manager of its department, or an admin.
//
// A viewer reads it, a commenter also comments and ticks its checklist and
// attaches files, an editor changes it. Admin is never handed out this way: a
// guest does not get to delete the task or re-share it.

import (
	"context"
	"fmt"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Guest is one person a task is shared with, and at what level.
type Guest struct {
	PersonID string
	Name     string
	Level    string
}

// guestLevels are the levels a guest may be given. Admin is deliberately absent.
func guestLevel(l core.AccessLevel) bool {
	return l == core.AccessView || l == core.AccessComment || l == core.AccessEdit
}

// ShareWith gives a person a level on a task, or changes the level they have.
// Assigning someone already grants them edit, so sharing is for people who are
// not on the task.
func (s *Service) ShareWith(ctx context.Context, orgID string, actor core.Subjects, docID, personID string, level core.AccessLevel) error {
	if !guestLevel(level) {
		return fmt.Errorf("%w: a guest can be a viewer, a commenter or an editor", ErrField)
	}
	ok, err := s.MayAssign(ctx, orgID, actor, docID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotAllowed
	}
	var exists bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM principals WHERE id = $1::uuid AND org_id = $2 AND kind = 'user')`,
		personID, orgID).Scan(&exists); err != nil {
		return fmt.Errorf("docs: check guest: %w", err)
	}
	if !exists {
		return fmt.Errorf("%w: person", ErrNotFound)
	}
	return s.authz.Grant(ctx, orgID, core.Ref{Type: core.EntityDocument, ID: docID},
		core.Subject{Kind: core.SubjectPrincipal, ID: personID}, level, actor.Principal())
}

// Unshare removes a guest. It refuses to touch the owner or an assignee, whose
// access is theirs by a different right and must not be stripped as a side
// effect of tidying the guest list.
func (s *Service) UnshareGuest(ctx context.Context, orgID string, actor core.Subjects, docID, personID string) error {
	ok, err := s.MayAssign(ctx, orgID, actor, docID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotAllowed
	}
	var protected bool
	if err := s.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM documents WHERE id = $1::uuid AND owner_id = $2::uuid)
		    OR EXISTS (SELECT 1 FROM task_fields WHERE document_id = $1::uuid AND assignee_id = $2::uuid)
		    OR EXISTS (SELECT 1 FROM document_assignees WHERE document_id = $1::uuid AND principal_id = $2::uuid)`,
		docID, personID).Scan(&protected); err != nil {
		return fmt.Errorf("docs: check guest: %w", err)
	}
	if protected {
		return fmt.Errorf("%w: that person is the owner or an assignee, not a guest", ErrField)
	}
	return s.authz.Revoke(ctx, orgID, core.Ref{Type: core.EntityDocument, ID: docID},
		core.Subject{Kind: core.SubjectPrincipal, ID: personID}, actor.Principal())
}

// Guests lists the people a task is shared with -- those with a direct grant
// who are neither its owner nor an assignee, so the list is purely the guests.
func (s *Service) Guests(ctx context.Context, orgID, docID string) ([]Guest, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text, COALESCE(NULLIF(p.display_name, ''), p.id::text), g.level::text
		  FROM grants g
		  JOIN principals p ON g.subject_key = 'principal:' || p.id::text
		 WHERE g.entity_type = 'document' AND g.entity_id = $1::uuid AND g.org_id = $2
		   AND (g.expires_at IS NULL OR g.expires_at > now())
		   AND p.id <> (SELECT owner_id FROM documents WHERE id = $1::uuid)
		   AND p.id NOT IN (
		       SELECT assignee_id FROM task_fields WHERE document_id = $1::uuid AND assignee_id IS NOT NULL
		       UNION
		       SELECT principal_id FROM document_assignees WHERE document_id = $1::uuid)
		 ORDER BY lower(p.display_name)`, docID, orgID)
	if err != nil {
		return nil, fmt.Errorf("docs: guests: %w", err)
	}
	defer rows.Close()
	var out []Guest
	for rows.Next() {
		var g Guest
		if err := rows.Scan(&g.PersonID, &g.Name, &g.Level); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
