// Package authz answers one question: what level of access does this caller have
// to this thing.
//
// It is deliberately small, and everything about it is designed so that getting
// it wrong is hard rather than merely discouraged.
package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// ErrDenied is returned when a caller does not have the required access.
//
// It is deliberately indistinguishable between "this does not exist" and "you
// may not see this". Telling an unauthorised caller that a document id is real
// leaks the thing they were denied.
var ErrDenied = errors.New("denied")

// Service resolves subject sets and effective access.
type Service struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

// SubjectsFor computes everything a principal is, for permission purposes.
//
// One query per request. The result is bounded by the principal's own
// memberships rather than by the size of the org, which is what makes filtering
// list queries on it cheap.
func (s *Service) SubjectsFor(ctx context.Context, orgID, principalID string) (core.Subjects, error) {
	subjects := core.Subjects{
		{Kind: core.SubjectPrincipal, ID: principalID},
		{Kind: core.SubjectOrg, ID: orgID},
	}

	// A super admin carries a subject that means "everything".
	//
	// Resolved here, with the channel and team memberships, for the same reason
	// they are: on every request rather than stashed in the session, so
	// promoting or demoting somebody takes effect on their next request and not
	// at their next sign-in thirty days from now.
	//
	// It is never written to grants. It is read by LevelFor and by the filter
	// every list query carries, which is what makes "super admins see
	// everything" true of documents and not only of pages.
	var super bool
	if err := s.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM capability_grants
		                WHERE org_id = $1 AND principal_id = $2
		                  AND capability = 'super_admin')`,
		orgID, principalID).Scan(&super); err != nil {
		return nil, fmt.Errorf("authz: check super admin: %w", err)
	}
	if super {
		subjects = append(subjects, core.Subject{Kind: core.SubjectSuper, ID: principalID})
	}

	rows, err := s.db.Query(ctx, `
		SELECT cm.channel_id::text
		FROM channel_members cm
		JOIN channels c ON c.id = cm.channel_id
		WHERE cm.principal_id = $1
		  AND c.org_id = $2
		  AND c.archived_at IS NULL`,
		principalID, orgID)
	if err != nil {
		return nil, fmt.Errorf("authz: load channel memberships: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("authz: scan channel membership: %w", err)
		}
		subjects = append(subjects, core.Subject{Kind: core.SubjectChannel, ID: id})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("authz: iterate channel memberships: %w", err)
	}

	// And the teams, which is the whole of what a team is.
	//
	// Resolved on every request rather than stashed in the session, and that is
	// the property the directory sync rests on: taking somebody out of a group
	// removes the subject from the next request they make, not from the next
	// time they sign in. A session lasts thirty days, and "revoked on their
	// next login" is not revoked.
	teamRows, err := s.db.Query(ctx, `
		SELECT tm.team_id::text
		FROM team_members tm
		JOIN teams t ON t.id = tm.team_id
		WHERE tm.principal_id = $1 AND t.org_id = $2`,
		principalID, orgID)
	if err != nil {
		return nil, fmt.Errorf("authz: load team memberships: %w", err)
	}
	defer teamRows.Close()

	for teamRows.Next() {
		var id string
		if err := teamRows.Scan(&id); err != nil {
			return nil, fmt.Errorf("authz: scan team membership: %w", err)
		}
		subjects = append(subjects, core.Subject{Kind: core.SubjectTeam, ID: id})
	}
	if err := teamRows.Err(); err != nil {
		return nil, fmt.Errorf("authz: iterate team memberships: %w", err)
	}
	return subjects, nil
}

// LevelFor returns the caller's effective access to one entity: the MAXIMUM
// level across every grant whose subject they hold.
//
// Returns core.AccessNone when there is no grant, which denies.
func (s *Service) LevelFor(ctx context.Context, subjects core.Subjects, ref core.Ref) (core.AccessLevel, error) {
	if len(subjects) == 0 {
		return core.AccessNone, nil
	}
	// A super admin reaches everything. Without this they held the capability
	// and no grants, so the approvals queue listed entries whose documents
	// returned 404, and every list showed them only what happened to be shared
	// with them. Both were working exactly as written and both were wrong.
	//
	// Edit, not admin, and the difference is deliberate.
	//
	// Edit is everything the complaints were about: open any document, change
	// any status, drag any card. Admin additionally means delete and re-grant,
	// and there is one invariant that depends on nobody having it by default:
	// an agent keeps admin over what it created and grants its channels edit,
	// so an agent's work cannot be deleted by anybody who merely has access to
	// it. That is the design, it is asserted in the acceptance suite, and
	// handing every super admin admin on every row quietly removed it.
	//
	// Caught by that suite. A super admin who genuinely needs to delete
	// somebody else's row can still be granted admin on it explicitly, which
	// leaves a row in grants saying who decided that and when.
	//
	// It is a FLOOR, not a ceiling, and returning here instead of falling
	// through is what makes that true. The first version of this returned edit
	// and stopped, which meant a super admin who OWNED a document had admin on
	// it by grant and got edit anyway: they could no longer delete their own
	// work. Being promoted took access away, which is the opposite of the
	// point. Six acceptance checks said so.
	floor := core.AccessNone
	if subjects.Super() {
		floor = core.AccessEdit
	}

	// Membership of a department is a view of that department and everything
	// under it -- enough to open it and file work into it. It is never a grant
	// row, which is the point: grants on a space are copied onto every document
	// filed there, and a member sees only their own work, never a colleague's.
	if ref.Type == core.EntitySpace {
		if p := subjects.Principal(); p != "" {
			member, err := s.memberOfSpace(ctx, p, ref.ID)
			if err != nil {
				return core.AccessNone, err
			}
			if member {
				floor = core.Higher(floor, core.AccessView)
			}
		}
	}

	// ORDER BY on the enum gives the maximum without needing a CASE ladder,
	// because access_level is declared in ascending order of privilege.
	var level string
	err := s.db.QueryRow(ctx, `
		SELECT g.level::text
		FROM grants g
		WHERE g.entity_type = $1
		  AND g.entity_id   = $2
		  AND g.subject_key = ANY($3)
		  AND (g.expires_at IS NULL OR g.expires_at > now())
		ORDER BY g.level DESC
		LIMIT 1`,
		string(ref.Type), ref.ID, subjects.Keys()).Scan(&level)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No grant at all. A super admin still gets their floor, which is the
		// whole point; anybody else gets nothing, which denies.
		return floor, nil
	case err != nil:
		return core.AccessNone, fmt.Errorf("authz: resolve access to %s: %w", ref, err)
	}
	granted := core.AccessLevel(level)
	if core.Higher(floor, granted) == floor {
		return floor, nil
	}
	return granted, nil
}

// memberOfSpace reports whether a principal reaches a space by membership.
func (s *Service) memberOfSpace(ctx context.Context, principalID, spaceID string) (bool, error) {
	var member bool
	err := s.db.QueryRow(ctx, `WITH RECURSIVE `+
		core.MemberSpaces("$1")+`
		SELECT EXISTS (SELECT 1 FROM member_spaces WHERE id = $2::uuid)`,
		principalID, spaceID).Scan(&member)
	if err != nil {
		return false, fmt.Errorf("authz: department membership: %w", err)
	}
	return member, nil
}

// Require returns ErrDenied unless the caller has at least the required level.
//
// This is the function every handler calls. It returns an error rather than a
// boolean on purpose: a bool invites `if allowed { }` with no else, and a
// forgotten else is a data leak.
func (s *Service) Require(ctx context.Context, subjects core.Subjects, ref core.Ref, required core.AccessLevel) error {
	have, err := s.LevelFor(ctx, subjects, ref)
	if err != nil {
		return err
	}
	if !have.AtLeast(required) {
		return fmt.Errorf("%w: %s requires %s", ErrDenied, ref, required)
	}
	return nil
}

// Grant creates or replaces a grant.
//
// It DELETEs and INSERTs rather than UPSERTing so that an audit log built from
// this table shows a complete history. A grant that was silently upgraded in
// place leaves no trace of what it used to be.
func (s *Service) Grant(ctx context.Context, orgID string, ref core.Ref, subject core.Subject, level core.AccessLevel, grantedBy string) error {
	if !level.Valid() {
		return fmt.Errorf("authz: invalid access level %q", level)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("authz: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM grants WHERE entity_type=$1 AND entity_id=$2 AND subject_key=$3`,
		string(ref.Type), ref.ID, subject.Key()); err != nil {
		return fmt.Errorf("authz: clear existing grant: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5::access_level, $6)`,
		orgID, string(ref.Type), ref.ID, subject.Key(), string(level), grantedBy); err != nil {
		return fmt.Errorf("authz: insert grant: %w", err)
	}

	// Who may see a thing is part of what an external index has to know, because
	// a search cluster cannot join against this table and carries the answer
	// inside each document instead. Publishing here, in the same transaction,
	// is what stops "I shared the runbook and they still cannot find it".
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(ref.Type), EntityID: ref.ID,
		Event: EventGranted, ActorID: grantedBy,
		Payload: map[string]any{"subject": subject.Key(), "level": string(level)},
	}); err != nil {
		return err
	}
	// A permission change is the thing an auditor asks about first, and it is
	// the one change that leaves no other trace: nothing about the document
	// itself is different afterwards.
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: grantedBy, Action: audit.ActionGrantAdd,
		EntityType: string(ref.Type), EntityID: ref.ID,
		After: map[string]any{"subject": subject.Key(), "level": string(level)},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// EventGranted is published whenever access to something changes, in either
// direction. One event name rather than granted and revoked, because every
// subscriber reacts the same way: re-read who can see it.
const EventGranted = "granted"

// Revoke removes a grant. Removing one that does not exist is not an error.
//
// Transactional and event-publishing for the same reason Grant is: taking
// access away has to reach the external index, and an index that only hears
// about grants is an index that keeps showing a document to somebody who was
// removed from it this morning.
func (s *Service) Revoke(ctx context.Context, orgID string, ref core.Ref, subject core.Subject, actorID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("authz: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ct, err := tx.Exec(ctx,
		`DELETE FROM grants WHERE entity_type=$1 AND entity_id=$2 AND subject_key=$3`,
		string(ref.Type), ref.ID, subject.Key())
	if err != nil {
		return fmt.Errorf("authz: revoke: %w", err)
	}
	if ct.RowsAffected() == 0 {
		// Nothing changed, so nothing to tell anybody about.
		return tx.Commit(ctx)
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(ref.Type), EntityID: ref.ID,
		Event: EventGranted, ActorID: actorID,
		Payload: map[string]any{"subject": subject.Key(), "level": ""},
	}); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionGrantRemove,
		EntityType: string(ref.Type), EntityID: ref.ID,
		Before: map[string]any{"subject": subject.Key()},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GrantSpace gives a principal a department: the space itself, everything
// already under it, and every document already filed anywhere in that
// subtree.
//
// Everything created afterward reaches the same place on its own --
// plant.go copies a parent space's grants onto a child at creation, and
// docs.Create copies a space's grants onto a document filed into it -- so
// this is the one place that has to reach backward, onto whatever already
// existed before the grant was made. Without it, granting a department
// shows its space in the tree and nothing that was already filed there: the
// grant exists on the folder, never on what the folder held at the time.
//
// The actor needs the People capability, the same requirement
// SetDepartments already carries, because this lives on the same page and
// is the same kind of decision: who gets filed under what.
func (s *Service) GrantSpace(ctx context.Context, orgID, actorID, principalID, spaceID string) error {
	if err := s.mayAdminister(ctx, orgID, actorID); err != nil {
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("authz: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	subject := core.Subject{Kind: core.SubjectPrincipal, ID: principalID}

	// Depth bounded the same way the document list's own space-subtree filter
	// is: MoveSpace already refuses a cycle, and this does not depend on that
	// staying true.
	const subtreeCTE = `
		WITH RECURSIVE subtree AS (
		    SELECT id, 0 AS depth FROM spaces WHERE id = $3::uuid AND org_id = $1
		  UNION ALL
		    SELECT c.id, p.depth + 1 FROM spaces c
		    JOIN subtree p ON c.parent_id = p.id
		    WHERE p.depth < 10
		)`

	if _, err := tx.Exec(ctx, subtreeCTE+`
		INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		SELECT gen_random_uuid(), $1, 'space', subtree.id, $2, 'edit', $4
		FROM subtree
		ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
		orgID, subject.Key(), spaceID, actorID); err != nil {
		return fmt.Errorf("authz: grant space subtree: %w", err)
	}

	if _, err := tx.Exec(ctx, subtreeCTE+`
		INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		SELECT gen_random_uuid(), $1, 'document', d.id, $2, 'edit', $4
		FROM documents d
		JOIN subtree ON subtree.id = d.space_id
		WHERE d.deleted_at IS NULL
		ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
		orgID, subject.Key(), spaceID, actorID); err != nil {
		return fmt.Errorf("authz: backfill document grants: %w", err)
	}

	return tx.Commit(ctx)
}

// SpaceGrantsBy lists, for everybody in the org in one query, which
// departments (top-level spaces) they hold an explicit grant on directly --
// the People page's own display and its revoke control, same shape as
// HeldBy.
//
// Inherited visibility into a sub-space is not listed here on purpose: it
// would show every department twice, once for the grant that actually
// exists and once for each space beneath it that copied it, and the second
// kind cannot be revoked on its own anyway -- revoking the top grant takes
// the rest with it, since nothing below it has a grant of its own except the
// one this row is about.
func (s *Service) SpaceGrantsBy(ctx context.Context, orgID string) (map[string][]Department, error) {
	rows, err := s.db.Query(ctx, `
		SELECT g.subject_key, s.id::text, s.name
		  FROM grants g
		  JOIN spaces s ON s.id = g.entity_id
		 WHERE g.entity_type = 'space' AND g.org_id = $1 AND NOT g.via_manager
		   -- The top of each granted branch, once. Granting a department also
		   -- grants everything under it, and listing all of those would bury
		   -- the one that was actually given; but a part granted on its own
		   -- (Red Team without the rest of Engineering) has to show up too.
		   AND NOT EXISTS (
		       SELECT 1 FROM grants pg
		        WHERE pg.org_id = g.org_id AND pg.entity_type = 'space'
		          AND pg.entity_id = s.parent_id AND pg.subject_key = g.subject_key)
		 ORDER BY s.name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("authz: list space grants: %w", err)
	}
	defer rows.Close()

	all := map[string][]Department{}
	for rows.Next() {
		var subjectKey string
		var d Department
		if err := rows.Scan(&subjectKey, &d.ID, &d.Name); err != nil {
			return nil, err
		}
		// Only a person's own grant belongs on their row. A grant held by a
		// team or the org as a whole is real and still in effect, but it is
		// not this principal's to revoke from here, and listing it under
		// their name would suggest it was.
		subject, err := core.ParseSubject(subjectKey)
		if err != nil || subject.Kind != core.SubjectPrincipal {
			continue
		}
		all[subject.ID] = append(all[subject.ID], d)
	}
	return all, rows.Err()
}

// RevokeSpace takes a department grant back: from the space, every space
// under it, and every document filed anywhere in it -- the exact reach
// GrantSpace gave. It used to remove only the grant on the top space, which
// left every sub-space and document still open to them: "removed" on the
// page, still able to read everything.
//
// Documents they own stay theirs. Taking someone's own work away from them is
// a different, deliberate act, not a side effect of changing departments.
func (s *Service) RevokeSpace(ctx context.Context, orgID, actorID, principalID, spaceID string) error {
	if err := s.mayAdminister(ctx, orgID, actorID); err != nil {
		return err
	}
	subject := core.Subject{Kind: core.SubjectPrincipal, ID: principalID}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("authz: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const subtreeCTE = `
		WITH RECURSIVE subtree AS (
		    SELECT id, 0 AS depth FROM spaces WHERE id = $1::uuid AND org_id = $2
		  UNION ALL
		    SELECT c.id, p.depth + 1 FROM spaces c
		    JOIN subtree p ON c.parent_id = p.id
		    WHERE p.depth < 10
		)`
	if _, err := tx.Exec(ctx, subtreeCTE+`
		DELETE FROM grants g
		 USING documents d, subtree
		 WHERE g.entity_type = 'document' AND g.entity_id = d.id
		   AND d.space_id = subtree.id
		   AND d.owner_id <> $4::uuid
		   AND NOT g.via_manager
		   AND g.org_id = $2 AND g.subject_key = $3`,
		spaceID, orgID, subject.Key(), principalID); err != nil {
		return fmt.Errorf("authz: revoke documents: %w", err)
	}
	if _, err := tx.Exec(ctx, subtreeCTE+`
		DELETE FROM grants g
		 USING subtree
		 WHERE g.entity_type = 'space' AND g.entity_id = subtree.id
		   AND NOT g.via_manager
		   AND g.org_id = $2 AND g.subject_key = $3`,
		spaceID, orgID, subject.Key()); err != nil {
		return fmt.Errorf("authz: revoke space: %w", err)
	}
	return tx.Commit(ctx)
}

// SpaceGrantSet is every space one person holds a grant on directly, for the
// access editor to show which boxes are ticked.
func (s *Service) SpaceGrantSet(ctx context.Context, orgID, principalID string) (map[string]bool, error) {
	subject := core.Subject{Kind: core.SubjectPrincipal, ID: principalID}
	rows, err := s.db.Query(ctx, `
		SELECT entity_id::text FROM grants
		 WHERE entity_type = 'space' AND org_id = $1 AND subject_key = $2
		   AND NOT via_manager`,
		orgID, subject.Key())
	if err != nil {
		return nil, fmt.Errorf("authz: space grant set: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// VisibleFilter returns a SQL fragment and argument that restricts a query to
// entities the caller can see.
//
// Handing callers a fragment rather than letting each one write its own join is
// the point: there is exactly one expression of "can this caller see it" in the
// codebase, so there is exactly one place for it to be wrong.
//
// Use as:
//
//	frag, arg := authz.VisibleFilter("d.id", core.EntityDocument, subjects)
//	sql := "SELECT ... FROM documents d WHERE d.org_id = $1 AND " + frag
func VisibleFilter(idColumn string, entityType core.EntityType, subjects core.Subjects, argIndex int) (string, []any) {
	// A super admin sees every row. TRUE rather than an EXISTS nobody can fail,
	// so the planner drops the whole clause instead of joining grants for a
	// person who is going to match everything anyway.
	//
	// The placeholders still have to be consumed: callers number their
	// arguments around this fragment, and returning fewer would shift every
	// parameter after it by two.
	if subjects.Super() {
		return "TRUE", []any{string(entityType), subjects.Keys()}
	}
	frag := fmt.Sprintf(`EXISTS (
		SELECT 1 FROM grants g
		WHERE g.entity_type = $%d
		  AND g.entity_id   = %s
		  AND g.subject_key = ANY($%d)
		  AND (g.expires_at IS NULL OR g.expires_at > now()))`,
		argIndex, idColumn, argIndex+1)
	return frag, []any{string(entityType), subjects.Keys()}
}
