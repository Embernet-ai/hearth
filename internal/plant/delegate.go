package plant

// Delegating one folder.
//
// A manager of a department, or of any folder above one, can give a person view,
// comment or edit on that one folder: the folder, every folder under it and
// every document and task filed in any of them. The person reaches nothing above
// the folder and nothing beside it, and a contractor account is no different from
// anybody else here, because what a person may open is only ever the grant rows
// on the thing.
//
// The rows are the ordinary grants, marked with the folder they were delegated
// from (grants.via_space_id, migration 00063). That mark is what lets a revoke
// take back exactly what the delegation gave and never a grant somebody holds for
// another reason, and what lets the database keep the rows true when work is
// created in the folder or moved in or out of it.
//
// A delegation never changes a grant that is not its own. Somebody who already
// holds a department grant keeps it, at the level it has.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

var (
	// ErrAlreadyReaches means the person already reaches this folder through a
	// delegation made higher up. Delegating it again would make two rows that
	// disagree about when access ends; end or change the higher one instead.
	ErrAlreadyReaches = errors.New("that person already reaches this folder through a folder above it")
	// ErrNoSuchPerson is a person who is not an active account in this organisation.
	ErrNoSuchPerson = errors.New("no such person here")
)

// Delegation is one person's reach into one folder, as the manager sees it.
type Delegation struct {
	PersonID   string
	Name       string
	Email      string
	Contractor bool
	Level      core.AccessLevel
	ExpiresAt  *time.Time
	Expired    bool
	GrantedAt  time.Time
	GrantedBy  string
}

func validDelegationLevel(l core.AccessLevel) bool {
	return l == core.AccessView || l == core.AccessComment || l == core.AccessEdit
}

// Delegations lists who reaches this folder through a delegation made on it,
// expired ones included so a manager can see what ended. The caller has to
// manage the folder.
func (s *Service) Delegations(ctx context.Context, orgID string, actor core.Subjects, spaceID string) ([]Delegation, error) {
	if ok, err := s.mayManageSpace(ctx, orgID, actor, spaceID); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrDenied
	}
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text, p.display_name, COALESCE(c.email, ''), p.contractor,
		       g.level::text, g.expires_at, g.expires_at IS NOT NULL AND g.expires_at <= now(),
		       g.granted_at, COALESCE(gb.display_name, '')
		  FROM grants g
		  JOIN principals p ON g.subject_key = 'principal:' || p.id::text
		  LEFT JOIN local_credentials c ON c.principal_id = p.id
		  LEFT JOIN principals gb ON gb.id = g.granted_by
		 WHERE g.entity_type = 'space' AND g.entity_id = $1::uuid AND g.org_id = $2
		   AND g.via_space_id = $1::uuid AND NOT g.via_manager
		 ORDER BY lower(p.display_name), p.id`, spaceID, orgID)
	if err != nil {
		return nil, fmt.Errorf("plant: delegations: %w", err)
	}
	defer rows.Close()
	var out []Delegation
	for rows.Next() {
		var d Delegation
		var level string
		if err := rows.Scan(&d.PersonID, &d.Name, &d.Email, &d.Contractor, &level, &d.ExpiresAt,
			&d.Expired, &d.GrantedAt, &d.GrantedBy); err != nil {
			return nil, fmt.Errorf("plant: scan delegation: %w", err)
		}
		d.Level = core.AccessLevel(level)
		out = append(out, d)
	}
	return out, rows.Err()
}

// subtreeCTE walks down from the folder, bounded so a loop written around this
// package cannot hold the request.
const subtreeCTE = `
	WITH RECURSIVE subtree AS (
	    SELECT id, 0 AS depth FROM spaces WHERE id = $3::uuid AND org_id = $1
	  UNION ALL
	    SELECT c.id, p.depth + 1 FROM spaces c
	    JOIN subtree p ON c.parent_id = p.id
	    WHERE p.depth < 32
	)`

// Delegate gives a person a level on a folder and everything under it, ending at
// expires when it is set. Giving the same folder again changes the level and the
// end date. The actor must manage the folder; the person must be an active
// account in the organisation, a contractor account or not.
func (s *Service) Delegate(ctx context.Context, orgID string, actor core.Subjects,
	spaceID, personID string, level core.AccessLevel, expires *time.Time) error {

	if !validDelegationLevel(level) {
		return fmt.Errorf("%w: a folder can be given as view, comment or edit", ErrInvalid)
	}
	if expires != nil && !expires.After(time.Now()) {
		return fmt.Errorf("%w: the end date has to be in the future", ErrInvalid)
	}
	if ok, err := s.mayManageSpace(ctx, orgID, actor, spaceID); err != nil {
		return err
	} else if !ok {
		return ErrDenied
	}
	actorID := actor.Principal()
	if actorID == "" {
		return errors.New("plant: no acting principal")
	}
	subject := core.Subject{Kind: core.SubjectPrincipal, ID: personID}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("plant: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var name string
	err = tx.QueryRow(ctx, `
		SELECT display_name FROM principals
		 WHERE id = $1::uuid AND org_id = $2 AND status = 'active' AND kind = 'user'`,
		personID, orgID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoSuchPerson
	}
	if err != nil {
		return fmt.Errorf("plant: look up person: %w", err)
	}
	var folderName string
	err = tx.QueryRow(ctx, `SELECT name FROM spaces WHERE id = $1::uuid AND org_id = $2`, spaceID, orgID).Scan(&folderName)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("plant: look up folder: %w", err)
	}

	var above bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM grants
		                WHERE entity_type = 'space' AND entity_id = $1::uuid AND subject_key = $2
		                  AND via_space_id IS NOT NULL AND via_space_id <> $1::uuid AND NOT via_manager
		                  AND (expires_at IS NULL OR expires_at > now()))`,
		spaceID, subject.Key()).Scan(&above); err != nil {
		return fmt.Errorf("plant: check inherited delegation: %w", err)
	}
	if above {
		return ErrAlreadyReaches
	}

	const upsert = `
		ON CONFLICT (entity_type, entity_id, subject_key) DO UPDATE
		   SET level = EXCLUDED.level, expires_at = EXCLUDED.expires_at,
		       granted_by = EXCLUDED.granted_by, granted_at = now(),
		       via_space_id = EXCLUDED.via_space_id, via_manager = false
		 WHERE grants.via_space_id = EXCLUDED.via_space_id OR grants.via_manager`

	tag, err := tx.Exec(ctx, subtreeCTE+`
		INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, expires_at, via_space_id)
		SELECT gen_random_uuid(), $1, 'space', subtree.id, $2, $4::access_level, $5::uuid, $6, $3::uuid
		  FROM subtree`+upsert,
		orgID, subject.Key(), spaceID, string(level), actorID, expires)
	if err != nil {
		return fmt.Errorf("plant: delegate folders: %w", err)
	}
	folders := tag.RowsAffected()
	tag, err = tx.Exec(ctx, subtreeCTE+`
		INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, expires_at, via_space_id)
		SELECT gen_random_uuid(), $1, 'document', d.id, $2, $4::access_level, $5::uuid, $6, $3::uuid
		  FROM documents d JOIN subtree ON subtree.id = d.space_id
		 WHERE d.org_id = $1 AND d.deleted_at IS NULL`+upsert,
		orgID, subject.Key(), spaceID, string(level), actorID, expires)
	if err != nil {
		return fmt.Errorf("plant: delegate documents: %w", err)
	}
	documents := tag.RowsAffected()

	if err := publishReindex(ctx, tx, orgID, spaceID, actorID, subject.Key(), string(level)); err != nil {
		return err
	}
	after := map[string]any{
		"subject": subject.Key(), "person": name, "level": string(level),
		"scope": "folder", "folder": folderName, "folders": folders, "documents": documents,
	}
	if expires != nil {
		after["expires_at"] = expires.UTC().Format(time.RFC3339)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionGrantAdd,
		EntityType: string(core.EntitySpace), EntityID: spaceID, After: after,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Undelegate takes back a delegation made on this folder: the folder, the
// folders under it and the documents in them, and only what the delegation
// gave. It takes effect on the person's next request, because every read asks
// the grants table.
func (s *Service) Undelegate(ctx context.Context, orgID string, actor core.Subjects, spaceID, personID string) error {
	if ok, err := s.mayManageSpace(ctx, orgID, actor, spaceID); err != nil {
		return err
	} else if !ok {
		return ErrDenied
	}
	actorID := actor.Principal()
	if actorID == "" {
		return errors.New("plant: no acting principal")
	}
	subject := core.Subject{Kind: core.SubjectPrincipal, ID: personID}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("plant: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var name, folderName string
	err = tx.QueryRow(ctx, `SELECT name FROM spaces WHERE id = $1::uuid AND org_id = $2`, spaceID, orgID).Scan(&folderName)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("plant: look up folder: %w", err)
	}
	_ = tx.QueryRow(ctx, `SELECT display_name FROM principals WHERE id = $1::uuid AND org_id = $2`,
		personID, orgID).Scan(&name)

	tag, err := tx.Exec(ctx, `
		DELETE FROM grants
		 WHERE org_id = $1 AND subject_key = $2 AND via_space_id = $3::uuid AND NOT via_manager`,
		orgID, subject.Key(), spaceID)
	if err != nil {
		return fmt.Errorf("plant: undelegate: %w", err)
	}
	// Somebody who manages this folder, or one above it, still opens it: what
	// the delegation had taken over goes back to that.
	if _, err := tx.Exec(ctx, `
		SELECT hearth_apply_manager(m.space_id, m.principal_id)
		  FROM space_managers m
		 WHERE m.principal_id = $1::uuid`,
		personID); err != nil {
		return fmt.Errorf("plant: restore manager access: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Nothing was delegated, so nothing changed and nothing to record.
		return tx.Commit(ctx)
	}
	if err := publishReindex(ctx, tx, orgID, spaceID, actorID, subject.Key(), ""); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionGrantRemove,
		EntityType: string(core.EntitySpace), EntityID: spaceID,
		Before: map[string]any{
			"subject": subject.Key(), "person": name, "scope": "folder",
			"folder": folderName, "rows": tag.RowsAffected(),
		},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// publishReindex tells the search index that who may see the documents under a
// folder changed, so an external index stops (or starts) showing them. One
// event for the folder and one for each document under it, in the transaction
// that changed the grants.
func publishReindex(ctx context.Context, tx pgx.Tx, orgID, spaceID, actorID, subjectKey, level string) error {
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntitySpace), EntityID: spaceID,
		Event: authz.EventGranted, ActorID: actorID,
		Payload: map[string]any{"subject": subjectKey, "level": level},
	}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, subtreeCTE+`
		INSERT INTO outbox (org_id, entity_type, entity_id, event, payload, actor_id, depth)
		SELECT $1, 'document', d.id, $4, jsonb_build_object('subject', $2::text, 'level', $5::text), $6::uuid, 0
		  FROM documents d JOIN subtree ON subtree.id = d.space_id
		 WHERE d.org_id = $1 AND d.deleted_at IS NULL`,
		orgID, subjectKey, spaceID, authz.EventGranted, level, actorID); err != nil {
		return fmt.Errorf("plant: publish reindex: %w", err)
	}
	return nil
}

// PersonByEmail finds the active account an address signs in as, for a manager
// naming the person to give a folder to. Typing an address rather than choosing
// from a list means a manager is never handed the directory of everybody in the
// organisation, which a contractor account is exactly the case for. Asking is
// held to the same line as delegating: only somebody who manages a folder.
func (s *Service) PersonByEmail(ctx context.Context, orgID string, actor core.Subjects, email string) (string, string, error) {
	if ok, err := s.MayManageAnySpace(ctx, orgID, actor); err != nil {
		return "", "", err
	} else if !ok {
		return "", "", ErrDenied
	}
	var id, name string
	err := s.db.QueryRow(ctx, `
		SELECT p.id::text, p.display_name FROM principals p
		 WHERE p.org_id = $1 AND p.status = 'active' AND p.kind = 'user'
		   AND (EXISTS (SELECT 1 FROM local_credentials c WHERE c.principal_id = p.id AND lower(c.email::text) = lower($2))
		     OR EXISTS (SELECT 1 FROM user_identities u WHERE u.principal_id = p.id AND lower(u.email::text) = lower($2)))
		 ORDER BY p.created_at LIMIT 1`, orgID, email).Scan(&id, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNoSuchPerson
	}
	if err != nil {
		return "", "", fmt.Errorf("plant: find person: %w", err)
	}
	return id, name, nil
}

// ManagedFolder is a folder a manager is about to share, with its whole path,
// which they may read because they manage it. It refuses anybody who does not.
func (s *Service) ManagedFolder(ctx context.Context, orgID string, actor core.Subjects, id string) (*Space, error) {
	if ok, err := s.mayManageSpace(ctx, orgID, actor, id); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrDenied
	}
	sp := &Space{OrgID: orgID, ID: id}
	err := s.db.QueryRow(ctx, `SELECT parent_id::text, name, kind FROM spaces WHERE id = $1::uuid AND org_id = $2`,
		id, orgID).Scan(&sp.ParentID, &sp.Name, &sp.Kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("plant: managed folder: %w", err)
	}
	if sp.Path, sp.Depth, err = s.spacePath(ctx, id); err != nil {
		return nil, err
	}
	return sp, nil
}

// ReachablePeople is who a list of colleagues may name for this actor. For an
// ordinary account restricted is false and nothing is cut down. For a
// contractor account it is the person themselves and everybody who holds a
// grant of their own on a space the account can open, and nobody else.
func (s *Service) ReachablePeople(ctx context.Context, orgID string, actor core.Subjects) (people map[string]bool, restricted bool, err error) {
	principal := actor.Principal()
	// A super admin is never narrowed, whatever their account says.
	if principal == "" || actor.Super() {
		return nil, false, nil
	}
	if err := s.db.QueryRow(ctx,
		`SELECT contractor FROM principals WHERE id = $1::uuid AND org_id = $2`,
		principal, orgID).Scan(&restricted); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("plant: contractor check: %w", err)
	}
	if !restricted {
		return nil, false, nil
	}
	rows, err := s.db.Query(ctx, `WITH RECURSIVE `+core.ReachablePeople("$1", "$2")+`
		SELECT id::text FROM reachable_people`, principal, actor.Keys())
	if err != nil {
		return nil, true, fmt.Errorf("plant: reachable people: %w", err)
	}
	defer rows.Close()
	people = map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, true, err
		}
		people[id] = true
	}
	return people, true, rows.Err()
}
