package docs

import (
	"context"
	"fmt"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Link kinds. "concerns" is the one that matters operationally: a work order
// concerns an asset, which is the question every maintenance history answers.
const (
	LinkMention  = "mention"
	LinkConcerns = "concerns"
	LinkBlocks   = "blocks"
	// LinkRelates is a plain association: these two belong together and
	// neither orders nor owns the other. Distinct from a mention, which means
	// one referred to the other in its text, and from blocks, which is a
	// sequence somebody has to respect.
	LinkRelates = "relates"
)

// Linked returns every document related to this one, in either direction.
//
// Both directions in one query, because the relationship is symmetric to a
// reader even though it is stored with a from and a to. Someone looking at an
// asset wants the work orders raised against it, and nobody thinks of that as
// "the reverse index".
//
// Permission filtered like every other list. A link to something you cannot see
// does not appear at all, rather than appearing as an unclickable id, which
// would leak that the thing exists.
func (s *Service) Linked(ctx context.Context, orgID string, actor core.Subjects, docID string) ([]*Document, error) {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}

	rows, err := s.db.Query(ctx, `
		WITH related AS (
		    SELECT to_id   AS id FROM entity_links
		     WHERE org_id = $1 AND from_type = 'document' AND from_id = $2 AND to_type = 'document'
		    UNION
		    SELECT from_id AS id FROM entity_links
		     WHERE org_id = $1 AND to_type = 'document' AND to_id = $2 AND from_type = 'document'
		)
		SELECT d.id::text, d.kind, d.title, d.updated_at, t.status
		FROM documents d
		JOIN related r ON r.id = d.id
		LEFT JOIN task_fields t ON t.document_id = d.id
		WHERE d.org_id = $1
		  AND d.deleted_at IS NULL
		  AND EXISTS (
		      SELECT 1 FROM grants g
		      WHERE g.entity_type = 'document'
		        AND g.entity_id = d.id
		        AND g.subject_key = ANY($3)
		        AND (g.expires_at IS NULL OR g.expires_at > now()))
		ORDER BY d.updated_at DESC
		LIMIT 100`, orgID, docID, actor.Keys())
	if err != nil {
		return nil, fmt.Errorf("docs: linked: %w", err)
	}
	defer rows.Close()

	var out []*Document
	for rows.Next() {
		d := &Document{}
		var status *string
		if err := rows.Scan(&d.ID, &d.Kind, &d.Title, &d.UpdatedAt, &status); err != nil {
			return nil, fmt.Errorf("docs: scan linked: %w", err)
		}
		if status != nil {
			d.Task = &TaskFields{Status: *status}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// LinkedTo returns every document related to something that is not a document:
// a contact, a company, a file.
//
// Separate from Linked rather than a parameter on it, because the permission
// question is different. Linked checks that you can see the document you are
// standing on; here the caller is standing on a contact, and what has to be
// checked is that they can see it. The rows themselves are filtered the same
// way either side.
func (s *Service) LinkedTo(ctx context.Context, orgID string, actor core.Subjects,
	ref core.Ref) ([]*Document, error) {

	if err := s.authz.Require(ctx, actor, ref, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx, `
		WITH related AS (
		    SELECT from_id AS id FROM entity_links
		     WHERE org_id = $1 AND to_type = $2 AND to_id = $3 AND from_type = 'document'
		    UNION
		    SELECT to_id   AS id FROM entity_links
		     WHERE org_id = $1 AND from_type = $2 AND from_id = $3 AND to_type = 'document'
		)
		SELECT d.id::text, d.kind, d.title, d.updated_at, t.status
		FROM documents d
		JOIN related r ON r.id = d.id
		LEFT JOIN task_fields t ON t.document_id = d.id
		WHERE d.org_id = $1 AND d.deleted_at IS NULL
		  AND EXISTS (
		      SELECT 1 FROM grants g
		       WHERE g.entity_type = 'document' AND g.entity_id = d.id
		         AND g.subject_key = ANY($4)
		         AND (g.expires_at IS NULL OR g.expires_at > now()))
		ORDER BY d.updated_at DESC
		LIMIT 100`, orgID, string(ref.Type), ref.ID, actor.Keys())
	if err != nil {
		return nil, fmt.Errorf("docs: linked to %s: %w", ref.Type, err)
	}
	defer rows.Close()

	var out []*Document
	for rows.Next() {
		d := &Document{}
		var status *string
		if err := rows.Scan(&d.ID, &d.Kind, &d.Title, &d.UpdatedAt, &status); err != nil {
			return nil, fmt.Errorf("docs: scan linked: %w", err)
		}
		if status != nil {
			d.Task = &TaskFields{Status: *status}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UnlinkRefs removes a relationship between any two entities.
//
// Edit on the from side, view on the other, matching Link: taking a vendor off
// a compressor is a change to the compressor.
func (s *Service) UnlinkRefs(ctx context.Context, orgID string, actor core.Subjects,
	from, to core.Ref, kind string) error {

	if err := s.authz.Require(ctx, actor, from, core.AccessEdit); err != nil {
		return err
	}
	if kind == "" {
		kind = LinkMention
	}
	_, err := s.db.Exec(ctx, `
		DELETE FROM entity_links
		 WHERE org_id = $1 AND kind = $6
		   AND ((from_type=$2 AND from_id=$3 AND to_type=$4 AND to_id=$5)
		     OR (from_type=$4 AND from_id=$5 AND to_type=$2 AND to_id=$3))`,
		orgID, string(from.Type), from.ID, string(to.Type), to.ID, kind)
	if err != nil {
		return fmt.Errorf("docs: unlink refs: %w", err)
	}
	return nil
}

// Unlink removes a relationship in whichever direction it was stored.
func (s *Service) Unlink(ctx context.Context, orgID string, actor core.Subjects, a, b string) error {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: a}, core.AccessEdit); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `
		DELETE FROM entity_links
		WHERE org_id = $1 AND from_type='document' AND to_type='document'
		  AND ((from_id=$2 AND to_id=$3) OR (from_id=$3 AND to_id=$2))`, orgID, a, b)
	if err != nil {
		return fmt.Errorf("docs: unlink: %w", err)
	}
	return nil
}

// People is one person who can be assigned work.
type People struct {
	ID   string
	Name string
}

// Assignable lists the principals in an org, for an assignee picker.
//
// Not permission filtered by design: who works here is not a secret to someone
// who already has a session, and an assignee dropdown that hides people makes
// work unassignable for reasons nobody can see.
func (s *Service) Assignable(ctx context.Context, orgID string) ([]People, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, COALESCE(NULLIF(display_name,''), handle::text, id::text)
		FROM principals
		WHERE org_id = $1 AND kind = 'user' AND status = 'active'
		ORDER BY 2`, orgID)
	if err != nil {
		return nil, fmt.Errorf("docs: assignable: %w", err)
	}
	defer rows.Close()

	var out []People
	for rows.Next() {
		var p People
		if err := rows.Scan(&p.ID, &p.Name); err != nil {
			return nil, fmt.Errorf("docs: scan assignable: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
