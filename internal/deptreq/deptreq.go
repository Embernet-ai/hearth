// Package deptreq is somebody asking which departments they belong in, and the
// answer.
//
// The person ticks departments. The managers of those departments, or a super
// admin, see the question in Approvals and say yes, no with a reason, or yes to
// a corrected set. A yes replaces the person's departments with the approved
// set, which routes their hours and time off to the right approver and makes
// them a member: they can open the department and file work into it, and see
// only their own work there (see core.MemberSpaces).
//
// # Who may decide
//
// Super admins (the owner is one) decide anything. A manager decides only
// within the departments they manage: every department a decision adds or
// removes has to be one of theirs. Otherwise approving "White Team" could quietly take the
// person out of Engineering, which the White Team manager has no say over.
//
// HR (People) is management, not admin: HR does not move people between
// departments, here or anywhere else (see authz.mayAdminister).
package deptreq

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrInvalid is a request that cannot be filed or decided as given.
	ErrInvalid = errors.New("deptreq: invalid")
	// ErrDenied is a decision the actor may not make, or a request that is no
	// longer waiting. The two read the same on purpose.
	ErrDenied = errors.New("deptreq: not yours to decide, or already decided")
)

// Department is one top-level space.
type Department struct {
	ID   string
	Name string
}

// Request is one person's question and, once decided, its answer.
type Request struct {
	ID            string
	RequesterID   string
	RequesterName string
	Note          string
	Status        string
	Asked         []Department
	Granted       []Department
	// Current is the person's departments right now, for a decider to see
	// what a yes would change.
	Current      []Department
	DecidedBy    string
	DeciderName  string
	DecidedAt    *time.Time
	DecisionNote string
	CreatedAt    time.Time
}

// AskedHas reports whether a department was asked for, for a template ticking
// boxes.
func (r *Request) AskedHas(id string) bool {
	return slices.ContainsFunc(r.Asked, func(d Department) bool { return d.ID == id })
}

// Notifier tells deciders about a request and the person about the answer.
// Optional; nil sends nothing.
type Notifier interface {
	DepartmentsRequested(ctx context.Context, orgID string, r *Request, deciderIDs []string)
	DepartmentsDecided(ctx context.Context, orgID, deciderID string, r *Request)
}

type Service struct {
	db     *pgxpool.Pool
	notify Notifier
}

func New(db *pgxpool.Pool, n Notifier) *Service { return &Service{db: db, notify: n} }

// isAdmin is the capability clause: super admin. $1 org, $2 actor.
const isAdmin = `EXISTS (SELECT 1 FROM capability_grants g
	          WHERE g.org_id = $1 AND g.principal_id = $2
	            AND g.capability = 'super_admin')`

// Departments is every department in the org, by name: what a person may ask
// for. Department names are not private; their contents are.
func (s *Service) Departments(ctx context.Context, orgID string) ([]Department, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, name FROM spaces
		 WHERE org_id = $1 AND parent_id IS NULL
		 ORDER BY lower(name)`, orgID)
	if err != nil {
		return nil, fmt.Errorf("deptreq: departments: %w", err)
	}
	defer rows.Close()
	var out []Department
	for rows.Next() {
		var d Department
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Current is the departments a person is in now.
func (s *Service) Current(ctx context.Context, orgID, principalID string) ([]Department, error) {
	rows, err := s.db.Query(ctx, `
		SELECT sp.id::text, sp.name FROM principal_departments pd
		  JOIN spaces sp ON sp.id = pd.space_id
		 WHERE pd.org_id = $1 AND pd.principal_id = $2
		 ORDER BY lower(sp.name)`, orgID, principalID)
	if err != nil {
		return nil, fmt.Errorf("deptreq: current: %w", err)
	}
	defer rows.Close()
	var out []Department
	for rows.Next() {
		var d Department
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Ask files the actor's question, replacing any they already have waiting.
func (s *Service) Ask(ctx context.Context, orgID, actorID string, spaceIDs []string, note string) (*Request, error) {
	spaceIDs = unique(spaceIDs)
	if len(spaceIDs) == 0 {
		return nil, fmt.Errorf("%w: choose at least one department", ErrInvalid)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("deptreq: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE department_requests SET status = 'cancelled', updated_at = now()
		 WHERE org_id = $1 AND requester_id = $2 AND status = 'pending'`, orgID, actorID); err != nil {
		return nil, fmt.Errorf("deptreq: replace: %w", err)
	}
	r := &Request{ID: uuid.NewString(), RequesterID: actorID, Note: strings.TrimSpace(note), Status: "pending"}
	err = tx.QueryRow(ctx, `
		INSERT INTO department_requests (id, org_id, requester_id, note)
		SELECT $1, $2, p.id, $4 FROM principals p WHERE p.id = $3 AND p.org_id = $2
		RETURNING created_at, (SELECT display_name FROM principals WHERE id = $3)`,
		r.ID, orgID, actorID, r.Note).Scan(&r.CreatedAt, &r.RequesterName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, fmt.Errorf("deptreq: ask: %w", err)
	}
	if err := insertSpaces(ctx, tx, orgID, r.ID, "asked", spaceIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("deptreq: commit: %w", err)
	}

	if s.notify != nil {
		if ids, err := s.deciders(ctx, orgID, r.ID, actorID); err == nil {
			s.notify.DepartmentsRequested(ctx, orgID, r, ids)
		}
	}
	return r, nil
}

// insertSpaces records departments against a request. Each has to be a
// top-level space of this org: filing somebody under another tenant's
// department, or under a project, is refused rather than skipped.
func insertSpaces(ctx context.Context, tx pgx.Tx, orgID, requestID, kind string, spaceIDs []string) error {
	for _, id := range spaceIDs {
		tag, err := tx.Exec(ctx, `
			INSERT INTO department_request_spaces (request_id, space_id, kind)
			SELECT $2, sp.id, $3 FROM spaces sp
			 WHERE sp.org_id = $1 AND sp.id::text = $4 AND sp.parent_id IS NULL`,
			orgID, requestID, kind, id)
		if err != nil {
			return fmt.Errorf("deptreq: record department: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: no such department", ErrInvalid)
		}
	}
	return nil
}

// deciders is who should hear about a request: the managers of the
// departments asked for, or the super admins when none of them has a manager,
// so a request never waits on nobody.
func (s *Service) deciders(ctx context.Context, orgID, requestID, requesterID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		WITH managers AS (
		    SELECT DISTINCT m.principal_id
		      FROM department_request_spaces rs
		      JOIN space_managers m ON m.space_id = rs.space_id
		     WHERE rs.request_id = $2 AND rs.kind = 'asked'
		)
		SELECT principal_id::text FROM managers
		UNION
		SELECT g.principal_id::text FROM capability_grants g
		 WHERE g.org_id = $1 AND g.capability = 'super_admin'
		   AND NOT EXISTS (SELECT 1 FROM managers)`, orgID, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != requesterID {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// mayDecide is who sees a pending request: a super admin, or a manager of a
// department it asks for. Deciding is checked again, more strictly, in Decide.
const mayDecide = `(` + isAdmin + `
	OR EXISTS (SELECT 1 FROM department_request_spaces rs
	             JOIN space_managers m ON m.space_id = rs.space_id
	            WHERE rs.request_id = r.id AND rs.kind = 'asked' AND m.principal_id = $2))`

// ToDecide is every pending request the actor may answer, oldest first.
func (s *Service) ToDecide(ctx context.Context, orgID, actorID string) ([]*Request, error) {
	rows, err := s.db.Query(ctx, `
		SELECT r.id::text FROM department_requests r
		 WHERE r.org_id = $1 AND r.status = 'pending' AND r.requester_id <> $2
		   AND `+mayDecide+`
		 ORDER BY r.created_at`, orgID, actorID)
	if err != nil {
		return nil, fmt.Errorf("deptreq: to decide: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*Request, 0, len(ids))
	for _, id := range ids {
		r, err := s.get(ctx, orgID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// PendingCount is ToDecide counted, for the badge on the rail.
func (s *Service) PendingCount(ctx context.Context, orgID, actorID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM department_requests r
		 WHERE r.org_id = $1 AND r.status = 'pending' AND r.requester_id <> $2
		   AND `+mayDecide, orgID, actorID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("deptreq: pending count: %w", err)
	}
	return n, nil
}

// Latest is the actor's most recent request, or nil if they never asked.
func (s *Service) Latest(ctx context.Context, orgID, actorID string) (*Request, error) {
	var id string
	err := s.db.QueryRow(ctx, `
		SELECT id::text FROM department_requests
		 WHERE org_id = $1 AND requester_id = $2
		 ORDER BY created_at DESC LIMIT 1`, orgID, actorID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("deptreq: latest: %w", err)
	}
	return s.get(ctx, orgID, id)
}

// RequesterOf is who filed a request, for routing a notification link to the
// right page. Empty when there is no such request in the org.
func (s *Service) RequesterOf(ctx context.Context, orgID, id string) (string, error) {
	var who string
	err := s.db.QueryRow(ctx, `SELECT requester_id::text FROM department_requests
		WHERE id::text = $2 AND org_id = $1`, orgID, id).Scan(&who)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return who, err
}

func (s *Service) get(ctx context.Context, orgID, id string) (*Request, error) {
	r := &Request{ID: id}
	var decidedBy, deciderName *string
	err := s.db.QueryRow(ctx, `
		SELECT r.requester_id::text, COALESCE(NULLIF(p.display_name, ''), p.id::text), r.note, r.status,
		       r.decided_by::text, d.display_name, r.decided_at, r.decision_note, r.created_at
		  FROM department_requests r
		  JOIN principals p ON p.id = r.requester_id
		  LEFT JOIN principals d ON d.id = r.decided_by
		 WHERE r.id = $2 AND r.org_id = $1`, orgID, id).
		Scan(&r.RequesterID, &r.RequesterName, &r.Note, &r.Status,
			&decidedBy, &deciderName, &r.DecidedAt, &r.DecisionNote, &r.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("deptreq: get: %w", err)
	}
	if decidedBy != nil {
		r.DecidedBy = *decidedBy
	}
	if deciderName != nil {
		r.DeciderName = *deciderName
	}
	rows, err := s.db.Query(ctx, `
		SELECT rs.kind, sp.id::text, sp.name FROM department_request_spaces rs
		  JOIN spaces sp ON sp.id = rs.space_id
		 WHERE rs.request_id = $1 ORDER BY lower(sp.name)`, id)
	if err != nil {
		return nil, fmt.Errorf("deptreq: spaces: %w", err)
	}
	for rows.Next() {
		var kind string
		var d Department
		if err := rows.Scan(&kind, &d.ID, &d.Name); err != nil {
			rows.Close()
			return nil, err
		}
		if kind == "granted" {
			r.Granted = append(r.Granted, d)
		} else {
			r.Asked = append(r.Asked, d)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	r.Current, err = s.Current(ctx, orgID, r.RequesterID)
	return r, err
}

// Decide answers a pending request.
//
// A yes takes the decider's final set of departments -- the ticked boxes,
// which may differ from what was asked -- and makes it the person's whole set.
// A no needs a reason, because "no" with nothing else leaves the person with
// no idea what to ask for instead.
func (s *Service) Decide(ctx context.Context, orgID, actorID, id string, approve bool, final []string, note string) error {
	note = strings.TrimSpace(note)
	final = unique(final)
	if !approve && note == "" {
		return fmt.Errorf("%w: say why, so they know what to ask for instead", ErrInvalid)
	}
	if approve && len(final) == 0 {
		return fmt.Errorf("%w: tick at least one department, or say no", ErrInvalid)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("deptreq: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Locked, so two deciders answering at once cannot both win.
	var requester string
	err = tx.QueryRow(ctx, `
		SELECT r.requester_id::text FROM department_requests r
		 WHERE r.id::text = $3 AND r.org_id = $1 AND r.status = 'pending' AND r.requester_id <> $2
		   AND `+mayDecide+`
		 FOR UPDATE`, orgID, actorID, id).Scan(&requester)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return fmt.Errorf("deptreq: find request: %w", err)
	}

	if approve {
		if err := s.mayChange(ctx, tx, orgID, actorID, requester, final); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM principal_departments WHERE org_id = $1 AND principal_id = $2`,
			orgID, requester); err != nil {
			return fmt.Errorf("deptreq: clear departments: %w", err)
		}
		if err := insertSpaces(ctx, tx, orgID, id, "granted", final); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO principal_departments (org_id, principal_id, space_id)
			SELECT $1, $2, rs.space_id FROM department_request_spaces rs
			 WHERE rs.request_id = $3 AND rs.kind = 'granted'`, orgID, requester, id); err != nil {
			return fmt.Errorf("deptreq: set departments: %w", err)
		}
	}

	status := "denied"
	if approve {
		status = "approved"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE department_requests
		   SET status = $3, decided_by = $2, decided_at = now(), decision_note = $4, updated_at = now()
		 WHERE id::text = $5 AND org_id = $1`, orgID, actorID, status, note, id); err != nil {
		return fmt.Errorf("deptreq: decide: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("deptreq: commit: %w", err)
	}

	if s.notify != nil {
		if r, err := s.get(ctx, orgID, id); err == nil {
			s.notify.DepartmentsDecided(ctx, orgID, actorID, r)
		}
	}
	return nil
}

// mayChange enforces the manager rule: a super admin may set anything; a
// manager only departments they manage, counting both what is added and what
// is removed.
func (s *Service) mayChange(ctx context.Context, tx pgx.Tx, orgID, actorID, requester string, final []string) error {
	var admin bool
	if err := tx.QueryRow(ctx, `SELECT `+isAdmin, orgID, actorID).Scan(&admin); err != nil {
		return fmt.Errorf("deptreq: check capability: %w", err)
	}
	if admin {
		return nil
	}
	var outside []string
	err := tx.QueryRow(ctx, `
		WITH current AS (
		    SELECT space_id::text AS id FROM principal_departments WHERE org_id = $1 AND principal_id = $3
		), final AS (
		    SELECT unnest($4::text[]) AS id
		), changed AS (
		    (SELECT id FROM final EXCEPT SELECT id FROM current)
		    UNION
		    (SELECT id FROM current EXCEPT SELECT id FROM final)
		)
		SELECT COALESCE(array_agg(c.id), '{}') FROM changed c
		 WHERE NOT EXISTS (SELECT 1 FROM space_managers m
		                    WHERE m.space_id::text = c.id AND m.principal_id = $2)`,
		orgID, actorID, requester, final).Scan(&outside)
	if err != nil {
		return fmt.Errorf("deptreq: check managed: %w", err)
	}
	if len(outside) > 0 {
		return ErrDenied
	}
	return nil
}

// Cancel withdraws the actor's own pending request.
func (s *Service) Cancel(ctx context.Context, orgID, actorID string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE department_requests SET status = 'cancelled', updated_at = now()
		 WHERE org_id = $1 AND requester_id = $2 AND status = 'pending'`, orgID, actorID)
	if err != nil {
		return fmt.Errorf("deptreq: cancel: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDenied
	}
	return nil
}

func unique(ids []string) []string {
	var out []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
