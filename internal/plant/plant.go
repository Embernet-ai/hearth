// Package plant is the tree the work hangs off: spaces that hold documents,
// assets that hold each other, and the rollups that make a failure at the
// bottom visible at the top.
//
// # Why a tree at all
//
// A bearing lives in a gearbox, which drives a conveyor, which is part of line
// three, which is in the north building. Somebody deciding whether to run the
// line tonight is asking about line three, and the answer is made of things
// four levels below it. A flat list of assets cannot answer that question, and
// a maintenance system that cannot answer it is a list of jobs.
//
// # Cycles
//
// A tree that can contain a cycle is not a tree, and the failure is not a wrong
// answer, it is a query that never returns and a connection that never comes
// back. Two guards, because one is not enough:
//
//   - On write, the parent is walked to the root inside the same transaction,
//     under an advisory lock on the org, and a reparent that would close a loop
//     is refused. The lock is what stops two concurrent reparents from each
//     seeing a legal tree and together making an illegal one.
//   - On read, every recursive walk carries a CYCLE clause, so a loop written
//     around the service by a migration or by somebody with psql still
//     terminates and still gives the right numbers. The depth bound beside it
//     is a product decision rather than a safety one: seven levels is a deep
//     plant and drawing more than sixteen helps nobody.
//
// A cycle also has no root, so anything the placement walk cannot reach is
// added to the tree at the top rather than dropped. A machine that silently
// disappears from the page is worse than one drawn in the wrong place, because
// nobody goes looking for what they cannot see is missing.
package plant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("that is not a shape the tree can hold")
	// ErrCycle is its own error because it is the one a person needs
	// explaining. "Not found" and "you cannot put line three inside the
	// gearbox that hangs off it" are different problems.
	ErrCycle = errors.New("that would put something inside itself")
)

// maxDepth bounds every recursive walk.
//
// Sixteen is far past any real plant: site, building, line, cell, machine,
// assembly, component is seven. It exists so that a cycle written around this
// package produces a bounded wrong answer instead of a request that never
// returns and a pool that never frees.
const maxDepth = 16

// Space kinds, which the substrate has allowed since the beginning.
const (
	KindFolder  = "folder"
	KindProject = "project"
	KindArea    = "area"
)

var validSpaceKinds = map[string]bool{KindFolder: true, KindProject: true, KindArea: true}

// Space is one node of the organisational tree.
type Space struct {
	ID       string
	OrgID    string
	ParentID *string
	Name     string
	Kind     string
	// Depth is how far from the root, for rendering an indented list without
	// the page having to rebuild the tree itself.
	Depth int
	// Path is the names from the root down, which is what somebody needs to
	// tell two "Line 3" apart.
	Path string
	// Documents is how many live documents sit directly in this space.
	Documents int
}

// Authorizer is the slice of access control this package needs.
type Authorizer interface {
	Require(ctx context.Context, subjects core.Subjects, ref core.Ref, required core.AccessLevel) error
	Grant(ctx context.Context, orgID string, ref core.Ref, subject core.Subject, level core.AccessLevel, grantedBy string) error
}

type Service struct {
	db    *pgxpool.Pool
	authz Authorizer
}

func New(db *pgxpool.Pool, az Authorizer) *Service { return &Service{db: db, authz: az} }

// -----------------------------------------------------------------------------
// Spaces
// -----------------------------------------------------------------------------

// CreateSpace adds a node and grants its creator admin, in one transaction.
//
// Same rule as a document: a space that exists with no grant is invisible to
// everybody including the person who just made it.
func (s *Service) CreateSpace(ctx context.Context, orgID string, actor core.Subjects,
	name, kind string, parentID *string) (*Space, error) {

	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: a space needs a name", ErrInvalid)
	}
	if kind == "" {
		kind = KindFolder
	}
	if !validSpaceKinds[kind] {
		return nil, fmt.Errorf("%w: %q is not a space kind", ErrInvalid, kind)
	}
	principal := actor.Principal()
	if principal == "" {
		return nil, errors.New("plant: no acting principal")
	}
	// A folder inside a department is a manager's to make (or an admin's); a
	// top-level department is an admin's alone. A member who merely works in a
	// space does not get to rearrange it.
	if parentID != nil && *parentID != "" {
		ok, err := s.mayManageSpace(ctx, orgID, actor, *parentID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrDenied
		}
	} else {
		parentID = nil
		ok, err := s.maySpaceAdmin(ctx, orgID, actor)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrDenied
		}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("plant: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A space that already exists is returned rather than refused.
	//
	// This is what makes an import safe to re-run. The importer built a node
	// per ClickUp space, folder and list, and with a plain INSERT a second run
	// built the whole tree again -- nineteen departments where the source has
	// nine, with documents split arbitrarily between the two copies. The
	// in-memory dedupe it carried only ever covered one run.
	//
	// Matched the way the unique indexes match: same org, same parent, same
	// name ignoring case and surrounding space. "Engineering" and " engineering"
	// are one department to everybody except a byte comparison.
	sp := &Space{ID: uuid.NewString(), OrgID: orgID, ParentID: parentID, Name: name, Kind: kind}
	var existing string
	err = tx.QueryRow(ctx, `
		SELECT id::text FROM spaces
		WHERE org_id = $1
		  AND parent_id IS NOT DISTINCT FROM $2
		  AND lower(btrim(name)) = lower(btrim($3))`,
		orgID, parentID, name).Scan(&existing)
	switch {
	case err == nil:
		// Found. Committing rather than returning early so the grant
		// inheritance below still runs: a parent shared since the space was
		// made should reach it.
		sp.ID = existing
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx, `
			INSERT INTO spaces (id, org_id, parent_id, name, kind, created_by)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			sp.ID, orgID, sp.ParentID, name, kind, principal); err != nil {
			return nil, fmt.Errorf("plant: create space: %w", err)
		}
	default:
		return nil, fmt.Errorf("plant: look for an existing space: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		VALUES (gen_random_uuid(), $1, 'space', $2, $3, 'admin', $4)
		ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
		orgID, sp.ID, core.Subject{Kind: core.SubjectPrincipal, ID: principal}.Key(), principal,
	); err != nil {
		return nil, fmt.Errorf("plant: grant space: %w", err)
	}
	// Whoever can edit the parent can see the child. A tree where each node
	// has to be shared separately is a tree nobody uses twice.
	if sp.ParentID != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, expires_at, via_space_id)
			SELECT gen_random_uuid(), $1, 'space', $2, g.subject_key, g.level, $4, g.expires_at, g.via_space_id
			FROM grants g
			WHERE g.entity_type = 'space' AND g.entity_id = $3
			  AND (g.expires_at IS NULL OR g.expires_at > now())
			ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
			orgID, sp.ID, *sp.ParentID, principal); err != nil {
			return nil, fmt.Errorf("plant: inherit space grants: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("plant: commit: %w", err)
	}
	return sp, nil
}

// RenameSpace changes the label. Moving one is MoveSpace, deliberately
// separate: a rename cannot break the tree and a move can.
func (s *Service) RenameSpace(ctx context.Context, orgID string, actor core.Subjects,
	id, name string) error {

	if ok, err := s.mayManageSpace(ctx, orgID, actor, id); err != nil {
		return err
	} else if !ok {
		return ErrDenied
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: a space needs a name", ErrInvalid)
	}
	ct, err := s.db.Exec(ctx,
		`UPDATE spaces SET name=$3, updated_at=now() WHERE id=$1 AND org_id=$2`, id, orgID, name)
	if err != nil {
		return fmt.Errorf("plant: rename space: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveSpace reparents a node, refusing anything that would close a loop.
func (s *Service) MoveSpace(ctx context.Context, orgID string, actor core.Subjects,
	id string, parentID *string) error {

	ok, err := s.mayManageSpace(ctx, orgID, actor, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrDenied
	}
	if parentID != nil && *parentID != "" {
		ok, err := s.mayManageSpace(ctx, orgID, actor, *parentID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrDenied
		}
	} else {
		parentID = nil
		// Moving to the top level makes it a department, an admin's act.
		ok, err := s.maySpaceAdmin(ctx, orgID, actor)
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

	if err := lockTree(ctx, tx, orgID); err != nil {
		return err
	}
	if parentID != nil {
		if err := checkNoCycle(ctx, tx, "spaces", "parent_id", id, *parentID); err != nil {
			return err
		}
	}
	ct, err := tx.Exec(ctx,
		`UPDATE spaces SET parent_id=$3, updated_at=now() WHERE id=$1 AND org_id=$2`,
		id, orgID, parentID)
	if err != nil {
		return fmt.Errorf("plant: move space: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// DeleteSpace removes an empty node.
//
// Empty, deliberately. Deleting a space with work in it would either orphan the
// documents or delete them, and both are surprises. Emptying it first is one
// more step and no surprises at all.
func (s *Service) DeleteSpace(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	if ok, err := s.maySpaceAdmin(ctx, orgID, actor); err != nil {
		return err
	} else if !ok {
		return ErrDenied
	}
	var children, documents int
	err := s.db.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM spaces WHERE parent_id = $1),
		       (SELECT count(*) FROM documents WHERE space_id = $1 AND deleted_at IS NULL)`,
		id).Scan(&children, &documents)
	if err != nil {
		return fmt.Errorf("plant: check space is empty: %w", err)
	}
	if children > 0 || documents > 0 {
		return fmt.Errorf("%w: it still holds %d space(s) and %d document(s)",
			ErrInvalid, children, documents)
	}
	// In one transaction with the record of who removed it: a space that is
	// gone with nobody attached is the question an audit log exists to answer.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("plant: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var name, kind string
	if err := tx.QueryRow(ctx, `SELECT name, kind FROM spaces WHERE id=$1 AND org_id=$2`, id, orgID).Scan(&name, &kind); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("plant: read space: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM spaces WHERE id=$1 AND org_id=$2`, id, orgID); err != nil {
		return fmt.Errorf("plant: delete space: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actor.Principal(), Action: audit.ActionSpaceDelete,
		EntityType: string(core.EntitySpace), EntityID: id,
		Before: map[string]any{"name": name, "kind": kind},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SpaceTree returns every space the caller can see, in tree order.
//
// Depth-bounded, so a cycle written around this package produces a truncated
// list rather than a request that never returns.
func (s *Service) SpaceTree(ctx context.Context, orgID string, actor core.Subjects) ([]*Space, error) {
	// A super admin sees the whole tree. Without this they saw only the spaces
	// somebody had granted them, which meant the departments in the rail and
	// the projects page were both filtered down to whatever they happened to
	// have been shared. "Still not able to see projects" was partly this.
	//
	// Written as a prefix rather than replacing the EXISTS, so the parameters
	// stay exactly where they are and nothing after this has to be renumbered.
	superOr := ""
	if actor.Super() {
		superOr = "TRUE OR "
	}
	// Department members see their departments too, without a grant: see
	// authz.MemberSpacesCTE for why membership is never a grant row.
	rows, err := s.db.Query(ctx, `
		WITH RECURSIVE `+core.MemberSpaces("$4")+`,
		visible AS (
		    SELECT sp.id, sp.parent_id, sp.name, sp.kind, sp.position
		    FROM spaces sp
		    WHERE sp.org_id = $1
		      AND (`+superOr+`EXISTS (
		          SELECT 1 FROM grants g
		           WHERE g.entity_type = 'space' AND g.entity_id = sp.id
		             AND g.subject_key = ANY($2)
		             AND (g.expires_at IS NULL OR g.expires_at > now()))
		        OR sp.id IN (SELECT id FROM member_spaces))
		),
		tree AS (
		    -- Roots are nodes with no parent, and nodes whose parent the caller
		    -- cannot see. Hiding a child because its parent is private would
		    -- lose work somebody was explicitly granted.
		    SELECT v.id, v.parent_id, v.name, v.kind, 0 AS depth,
		           v.name AS path, ARRAY[lpad(v.position::text, 6, '0'), lower(v.name), v.id::text] AS sort
		    FROM visible v
		    WHERE v.parent_id IS NULL
		       OR NOT EXISTS (SELECT 1 FROM visible p WHERE p.id = v.parent_id)
		  UNION ALL
		    SELECT c.id, c.parent_id, c.name, c.kind, t.depth + 1,
		           t.path || ' / ' || c.name, t.sort || lpad(c.position::text, 6, '0') || lower(c.name) || c.id::text
		    FROM visible c
		    JOIN tree t ON c.parent_id = t.id
		    WHERE t.depth < $3
		) CYCLE id SET looped USING tree_path
		SELECT t.id::text, t.parent_id::text, t.name, t.kind, t.depth, t.path,
		       (SELECT count(*) FROM documents d
		         WHERE d.space_id = t.id AND d.deleted_at IS NULL)::int
		FROM tree t
		WHERE NOT t.looped
		ORDER BY t.sort`, orgID, actor.Keys(), maxDepth, actor.Principal())
	if err != nil {
		return nil, fmt.Errorf("plant: space tree: %w", err)
	}
	defer rows.Close()

	var out []*Space
	for rows.Next() {
		sp := &Space{OrgID: orgID}
		if err := rows.Scan(&sp.ID, &sp.ParentID, &sp.Name, &sp.Kind,
			&sp.Depth, &sp.Path, &sp.Documents); err != nil {
			return nil, fmt.Errorf("plant: scan space: %w", err)
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// GetSpace returns one node.
func (s *Service) GetSpace(ctx context.Context, orgID string, actor core.Subjects, id string) (*Space, error) {
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntitySpace, ID: id}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	sp := &Space{OrgID: orgID, ID: id}
	err := s.db.QueryRow(ctx, `
		SELECT parent_id::text, name, kind,
		       (SELECT count(*) FROM documents d WHERE d.space_id = $1 AND d.deleted_at IS NULL)::int
		FROM spaces WHERE id=$1 AND org_id=$2`, id, orgID).
		Scan(&sp.ParentID, &sp.Name, &sp.Kind, &sp.Documents)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("plant: get space: %w", err)
	}
	sp.Path, sp.Depth, err = s.spacePath(ctx, id)
	if err != nil {
		return nil, err
	}
	// The path starts at the highest folder the caller may open. Naming a
	// parent they were never given is how a person delegated one subfolder
	// would learn what the rest of the space is called.
	if !actor.Super() {
		if sp.Path, sp.Depth, err = s.visiblePath(ctx, actor, id); err != nil {
			return nil, err
		}
	}
	return sp, nil
}

// visiblePath is the names from the highest folder the actor may view down to
// id, and how many folders that is below the first of them.
func (s *Service) visiblePath(ctx context.Context, actor core.Subjects, id string) (string, int, error) {
	rows, err := s.db.Query(ctx, `
		WITH RECURSIVE up AS (
		    SELECT id, parent_id, name, 0 AS depth FROM spaces WHERE id = $1
		  UNION ALL
		    SELECT p.id, p.parent_id, p.name, u.depth + 1
		    FROM spaces p JOIN up u ON p.id = u.parent_id
		    WHERE u.depth < $2
		) CYCLE id SET looped USING up_path
		SELECT id::text, name FROM up WHERE NOT looped ORDER BY depth`, id, maxDepth)
	if err != nil {
		return "", 0, fmt.Errorf("plant: visible path: %w", err)
	}
	var names []string
	var ids []string
	for rows.Next() {
		var sid, name string
		if err := rows.Scan(&sid, &name); err != nil {
			rows.Close()
			return "", 0, err
		}
		ids = append(ids, sid)
		names = append(names, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", 0, err
	}
	// Nearest first; stop at the first one the actor cannot open.
	keep := 0
	for i, sid := range ids {
		if i > 0 {
			if err := s.authz.Require(ctx, actor,
				core.Ref{Type: core.EntitySpace, ID: sid}, core.AccessView); err != nil {
				break
			}
		}
		keep = i + 1
	}
	parts := make([]string, keep)
	for i := 0; i < keep; i++ {
		parts[keep-1-i] = names[i]
	}
	return strings.Join(parts, " / "), keep - 1, nil
}

// spacePath walks to the root for the breadcrumb.
func (s *Service) spacePath(ctx context.Context, id string) (string, int, error) {
	var path string
	var depth int
	err := s.db.QueryRow(ctx, `
		WITH RECURSIVE up AS (
		    SELECT id, parent_id, name, 0 AS depth FROM spaces WHERE id = $1
		  UNION ALL
		    SELECT p.id, p.parent_id, p.name, u.depth + 1
		    FROM spaces p JOIN up u ON p.id = u.parent_id
		    WHERE u.depth < $2
		) CYCLE id SET looped USING up_path
		SELECT string_agg(name, ' / ' ORDER BY depth DESC), max(depth)
		FROM up WHERE NOT looped`,
		id, maxDepth).Scan(&path, &depth)
	if err != nil {
		return "", 0, fmt.Errorf("plant: space path: %w", err)
	}
	return path, depth, nil
}

// MoveDocument puts a document in a space, or takes it out.
//
// This is the write that spends a column the substrate has carried unused since
// the beginning.
func (s *Service) MoveDocument(ctx context.Context, orgID string, actor core.Subjects,
	docID string, spaceID *string) error {

	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessEdit); err != nil {
		return err
	}
	if spaceID != nil && *spaceID != "" {
		// Edit on the destination, because putting work into somebody's space
		// is a change to that space.
		if err := s.authz.Require(ctx, actor,
			core.Ref{Type: core.EntitySpace, ID: *spaceID}, core.AccessEdit); err != nil {
			return err
		}
	} else {
		spaceID = nil
	}

	ct, err := s.db.Exec(ctx, `
		UPDATE documents SET space_id=$3, updated_at=now()
		 WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`, docID, orgID, spaceID)
	if err != nil {
		return fmt.Errorf("plant: move document: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// -----------------------------------------------------------------------------
// Cycle guards
// -----------------------------------------------------------------------------

// lockTree serialises reparenting inside one org.
//
// Without it, two concurrent moves each read a legal tree, each write a legal
// change, and together produce a loop. The window is small and the failure is a
// query that never returns, which is the worst trade there is. An advisory lock
// held to the end of the transaction costs nothing on a tree that is reparented
// by hand a few times a week.
func lockTree(ctx context.Context, tx pgx.Tx, orgID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "plant:"+orgID); err != nil {
		return fmt.Errorf("plant: lock tree: %w", err)
	}
	return nil
}

// checkNoCycle walks from the proposed parent to the root and refuses if it
// arrives back at the node being moved.
//
// The match is on the parent reference as well as the row, and that is not
// belt and braces: an asset with no asset_fields row is still somebody's
// parent, because the row is created the first time an asset is placed and a
// root is never placed. Matching only on rows means the topmost ancestor is
// never seen, and the one cycle a person is most likely to create by hand,
// putting the site inside something on it, goes straight through.
//
// The table and column are interpolated rather than bound, which is safe only
// because both come from constants in this file. Nothing derived from a request
// reaches them, and there is a test that says so.
func checkNoCycle(ctx context.Context, tx pgx.Tx, table, parentCol, id, newParent string) error {
	if id == newParent {
		return fmt.Errorf("%w: it cannot be its own parent", ErrCycle)
	}
	if table != "spaces" && table != "asset_fields" {
		return fmt.Errorf("plant: refusing to walk %q", table)
	}
	idCol := "id"
	if table == "asset_fields" {
		idCol = "document_id"
	}

	var hit bool
	err := tx.QueryRow(ctx, `
		WITH RECURSIVE up AS (
		    SELECT `+idCol+` AS id, `+parentCol+` AS parent, 0 AS depth
		    FROM `+table+` WHERE `+idCol+` = $1
		  UNION ALL
		    SELECT t.`+idCol+`, t.`+parentCol+`, u.depth + 1
		    FROM `+table+` t JOIN up u ON t.`+idCol+` = u.parent
		    WHERE u.depth < $3
		) CYCLE id SET looped USING up_path
		SELECT EXISTS (
		    SELECT 1 FROM up
		     WHERE NOT looped AND (id = $2 OR parent = $2))`,
		newParent, id, maxDepth).Scan(&hit)
	if err != nil {
		return fmt.Errorf("plant: cycle check: %w", err)
	}
	if hit {
		return fmt.Errorf("%w: %s is already below it", ErrCycle, newParent)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Shared helpers
// -----------------------------------------------------------------------------

// visibleDocument restricts a query to documents the caller can see. Written
// out here rather than shared from authz for the same reason the other feature
// packages write it out: the fragment carries its own argument positions.
const visibleDocument = `EXISTS (
	SELECT 1 FROM grants g
	 WHERE g.entity_type = 'document' AND g.entity_id = %s
	   AND g.subject_key = ANY($%d)
	   AND (g.expires_at IS NULL OR g.expires_at > now()))`

func (s *Service) canView(ctx context.Context, actor core.Subjects, docID string) error {
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessView); err != nil {
		return ErrNotFound
	}
	return nil
}

func (s *Service) canEdit(ctx context.Context, actor core.Subjects, docID string) error {
	return s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessEdit)
}

// Documents is the slice of the spine this package needs.
type Documents interface {
	Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*docs.Document, error)
}

var _ Documents = (*docs.Service)(nil)

// nowOrNil is used by the forms: an empty date field clears the value rather
// than setting it to the zero time, which would render as the year one.
func nowOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// SpaceChildren returns what sits directly inside a space.
//
// Named for spaces because Children already belongs to assets, and one method
// answering "what is inside this" for two different trees is how somebody ends
// up passing an asset id to the space query and getting an empty list they
// cannot explain.
//
// One level, not the subtree. A department page is somewhere to look around
// from -- these are the sub-departments and projects to walk into next -- and
// listing every descendant would reproduce the flat wall of links the
// navigation was rebuilt to stop being.
//
// Permission filtered like the tree: a child somebody cannot see does not
// appear, rather than appearing as a name they cannot open.
func (s *Service) SpaceChildren(ctx context.Context, orgID string, actor core.Subjects, parentID string) ([]*Space, error) {
	rows, err := s.db.Query(ctx, `
		WITH RECURSIVE `+core.MemberSpaces("$4")+`
		SELECT sp.id::text, sp.name, sp.kind,
		       (SELECT count(*) FROM documents d
		         WHERE d.space_id = sp.id AND d.deleted_at IS NULL) AS docs
		FROM spaces sp
		WHERE sp.org_id = $1 AND sp.parent_id = $2
		  AND ($3::text[] @> ARRAY['*super'] OR EXISTS (
		      SELECT 1 FROM grants g
		       WHERE g.entity_type = 'space' AND g.entity_id = sp.id
		         AND g.subject_key = ANY($3)
		         AND (g.expires_at IS NULL OR g.expires_at > now()))
		    OR sp.id IN (SELECT id FROM member_spaces))
		ORDER BY sp.position, lower(sp.name)`,
		orgID, parentID, actor.GrantKeys(), actor.Principal())
	if err != nil {
		return nil, fmt.Errorf("plant: children: %w", err)
	}
	defer rows.Close()

	var out []*Space
	for rows.Next() {
		sp := &Space{OrgID: orgID}
		if err := rows.Scan(&sp.ID, &sp.Name, &sp.Kind, &sp.Documents); err != nil {
			return nil, fmt.Errorf("plant: scan child: %w", err)
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// FindSpaceByName looks for a space by name anywhere in the tree.
//
// For an importer reconciling against a structure a person has since
// rearranged. Matching only where the source expects a space to be means an
// import recreates it in the old place and files everything there, which leaves
// the reorganised tree intact and empty -- the worst of both, because it looks
// right.
//
// Shallowest first, so a department beats a project of the same name: if both
// "Embernet" the department and "Embernet" the project exist, the source's
// top-level space is the department.
func (s *Service) FindSpaceByName(ctx context.Context, orgID, name string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `
		WITH RECURSIVE depth AS (
		    SELECT id, parent_id, name, 0 AS d FROM spaces
		     WHERE org_id = $1 AND parent_id IS NULL
		  UNION ALL
		    SELECT c.id, c.parent_id, c.name, p.d + 1 FROM spaces c
		    JOIN depth p ON c.parent_id = p.id WHERE p.d < 10
		)
		SELECT id::text FROM depth
		 WHERE lower(btrim(name)) = lower(btrim($2))
		 ORDER BY d LIMIT 1`, orgID, name).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("plant: find space by name: %w", err)
	}
	return id, nil
}
