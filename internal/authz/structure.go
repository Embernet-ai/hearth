package authz

// The org chart, to the extent this application has one: which department
// somebody is in, who manages that department, and who signs off their hours
// when the department is not the answer.
//
// It lives here rather than in auth because every operation is a permission
// question. "Who may approve Yara's time" is answered by these three tables
// and by nothing else, and putting them beside the capability grants keeps one
// package able to answer the whole question.
//
// A department is a top-level space. Nothing here creates one: departments are
// the space tree the rail already renders, and a second table of departments
// beside it would be the mistake this repository already has a commit about.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Embernet-ai/hearth/internal/audit"
)

// Department is one membership.
type Department struct {
	ID   string
	Name string
}

// In reports membership, for a template deciding whether an option is
// selected. A method rather than a template helper, because "is this id in that
// slice" written in a template is a loop with a flag in it.
func (s *Structure) In(spaceID string) bool {
	if s == nil {
		return false
	}
	for _, d := range s.Departments {
		if d.ID == spaceID {
			return true
		}
	}
	return false
}

// Structure is one person's place in the chart.
type Structure struct {
	// Departments, plural. Somebody sits in Marketing and Engineering and two
	// teams under it, and a single column could not say so.
	Departments  []Department
	ApproverID   string
	ApproverName string
	// TimeAutoApprove is true while the person's time is approved as it is
	// logged, and TimeAutoApproveSince is when that began. See
	// SetTimeAutoApprove.
	TimeAutoApprove      bool
	TimeAutoApproveSince *time.Time
	// Manages are the departments this person manages, by name, for the page
	// to show without a second query per row.
	Manages []string
}

// mayAdminister reports whether the actor can change the chart: move somebody
// between departments, name their approver, make somebody a manager, or open a
// department's work to them.
//
// A super admin only (the owner is one). Not People: HR is management, and
// management decides time off and reads the directory, but moving people
// around is an admin's act and nobody below admin holds it. A manager's say
// over their own department goes through department requests (deptreq), which
// keeps them inside it. Checked in every mutator below rather than once in a
// handler, because a permission enforced only at the edge is one the second
// caller does not enforce at all.
// MayAdminister reports whether the actor may place people in departments
// without asking anybody: a super admin, or People in a playground.
func (s *Service) MayAdminister(ctx context.Context, orgID, actorID string) bool {
	return s.mayAdminister(ctx, orgID, actorID) == nil
}

func (s *Service) mayAdminister(ctx context.Context, orgID, actorID string) error {
	held, err := s.Capabilities(ctx, orgID, actorID)
	if err != nil {
		return err
	}
	if held.IsSuperAdmin() {
		return nil
	}
	// The one exception is a playground: a made-up company whose only real
	// user runs it. Its self holds People and never super_admin (see package
	// playground for why), moving its made-up colleagues is one of the
	// lessons, and nothing in there is anybody else's.
	ok, err := s.MayMovePeopleIn(ctx, orgID, held)
	if err != nil {
		return err
	}
	if !ok {
		return ErrDenied
	}
	return nil
}

// MayMovePeopleIn reports whether somebody holding these capabilities may move
// people around in this org: a super admin anywhere, or People inside a
// playground. For a page deciding whether to show the controls, so the page
// and the check cannot disagree.
func (s *Service) MayMovePeopleIn(ctx context.Context, orgID string, held Held) (bool, error) {
	if held.IsSuperAdmin() {
		return true, nil
	}
	if !held.Has(People) {
		return false, nil
	}
	var sandbox bool
	if err := s.db.QueryRow(ctx,
		`SELECT playground_of IS NOT NULL FROM orgs WHERE id = $1`, orgID).Scan(&sandbox); err != nil {
		return false, fmt.Errorf("authz: is playground: %w", err)
	}
	return sandbox, nil
}

// SetDepartments replaces somebody's departments with exactly this set.
//
// Replace rather than add, because the page sends the whole set: a form that
// posts additions only has no way to express a removal, and somebody taken off
// a team stays on it forever.
//
// An empty set is a real state rather than an error. A contractor who reports
// to one named person belongs to no department, and inventing one for them
// would put their hours in front of a manager with nothing to do with them.
//
// Nothing is inherited. Being in Engineering does not put somebody in Red Team
// even though Red Team sits under it: membership is exact, because the
// alternative is a rule about the tree that has to be re-reasoned every time
// anybody asks who approves what.
func (s *Service) SetDepartments(ctx context.Context, orgID, actorID, principalID string, spaceIDs []string) error {
	if err := s.mayAdminister(ctx, orgID, actorID); err != nil {
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("authz: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The person has to exist in this org before anything is written, or a
	// wrong id would silently clear nothing and report success.
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM principals WHERE id = $1 AND org_id = $2)`,
		principalID, orgID).Scan(&exists); err != nil {
		return fmt.Errorf("authz: look up person: %w", err)
	}
	if !exists {
		return errors.New("authz: no such person")
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM principal_departments WHERE principal_id = $1`, principalID); err != nil {
		return fmt.Errorf("authz: clear departments: %w", err)
	}

	for _, space := range spaceIDs {
		if space == "" {
			continue
		}
		// The space is taken through a lookup scoped to the org rather than on
		// trust. Filing somebody under another tenant's department would route
		// their hours to that tenant's manager.
		tag, err := tx.Exec(ctx, `
			INSERT INTO principal_departments (org_id, principal_id, space_id)
			SELECT $1, $2, sp.id FROM spaces sp
			 WHERE sp.org_id = $1 AND sp.id = $3::uuid
			ON CONFLICT (principal_id, space_id) DO NOTHING`, orgID, principalID, space)
		if err != nil {
			return fmt.Errorf("authz: set department: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Either it was already there, which cannot happen after the delete
			// above, or the space is not this org's. Refused rather than
			// skipped, so a bad id is not silently dropped from the set.
			return errors.New("authz: no such department")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("authz: commit: %w", err)
	}
	return nil
}

// SetApprover names the individual who signs off this person's time,
// overriding their department. An empty approverID clears it.
func (s *Service) SetApprover(ctx context.Context, orgID, actorID, principalID, approverID string) error {
	if err := s.mayAdminister(ctx, orgID, actorID); err != nil {
		return err
	}
	if approverID != "" && approverID == principalID {
		// The schema refuses this too. Saying so here means the page gets a
		// sentence rather than a constraint violation.
		return errors.New("authz: nobody approves their own time")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("authz: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var was string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(approver_id::text,'') FROM principals
		 WHERE id = $1 AND org_id = $2 FOR UPDATE`, principalID, orgID).Scan(&was); err != nil {
		return errors.New("authz: no such person")
	}
	var now string
	if err := tx.QueryRow(ctx, `
		UPDATE principals
		   SET approver_id = (SELECT id FROM principals
		                       WHERE org_id = $1 AND id = NULLIF($3,'')::uuid),
		       updated_at = now()
		 WHERE id = $2 AND org_id = $1
		RETURNING COALESCE(approver_id::text,'')`, orgID, principalID, approverID).Scan(&now); err != nil {
		return fmt.Errorf("authz: set approver: %w", err)
	}
	if was != now {
		if err := audit.Record(ctx, tx, audit.Entry{
			OrgID: orgID, ActorID: actorID, Action: audit.ActionApproverSet,
			EntityType: "principal", EntityID: principalID,
			Before: map[string]any{"approver_id": was}, After: map[string]any{"approver_id": now},
		}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// AddManager puts somebody in charge of a department.
func (s *Service) AddManager(ctx context.Context, orgID, actorID, spaceID, principalID string) error {
	if err := s.mayAdminister(ctx, orgID, actorID); err != nil {
		return err
	}
	// The SELECTs are the org check. An INSERT with the ids taken on trust
	// would let one tenant appoint a manager over another tenant's department.
	tag, err := s.db.Exec(ctx, `
		INSERT INTO space_managers (org_id, space_id, principal_id)
		SELECT $1, s.id, p.id
		  FROM spaces s, principals p
		 WHERE s.org_id = $1 AND s.id = $2::uuid
		   AND p.org_id = $1 AND p.id = $3::uuid
		ON CONFLICT (space_id, principal_id) DO NOTHING`, orgID, spaceID, principalID)
	if err != nil {
		return fmt.Errorf("authz: add manager: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either it was already there, which is fine, or one of the ids does
		// not belong to this org, which is not. Distinguished so a silent
		// no-op cannot look like success.
		var exists bool
		if err := s.db.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM space_managers
			                 WHERE space_id = $1::uuid AND principal_id = $2::uuid)`,
			spaceID, principalID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errors.New("authz: no such person, or no such department")
		}
	}
	return nil
}

// RemoveManager takes somebody off a department.
func (s *Service) RemoveManager(ctx context.Context, orgID, actorID, spaceID, principalID string) error {
	if err := s.mayAdminister(ctx, orgID, actorID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `
		DELETE FROM space_managers
		 WHERE org_id = $1 AND space_id = $2::uuid AND principal_id = $3::uuid`,
		orgID, spaceID, principalID)
	if err != nil {
		return fmt.Errorf("authz: remove manager: %w", err)
	}
	return nil
}

// StructureFor reads the whole chart in two queries.
//
// Two rather than one per person, because the People page lists everybody and a
// query per row is the same page rendered twenty-three times.
func (s *Service) StructureFor(ctx context.Context, orgID string) (map[string]*Structure, error) {
	out := map[string]*Structure{}

	rows, err := s.db.Query(ctx, `
		SELECT p.id::text,
		       COALESCE(a.id::text,''), COALESCE(NULLIF(a.display_name,''),''), p.time_auto_approve_since
		  FROM principals p
		  LEFT JOIN principals a ON a.id = p.approver_id
		 WHERE p.org_id = $1 AND p.kind = 'user'`, orgID)
	if err != nil {
		return nil, fmt.Errorf("authz: read structure: %w", err)
	}
	for rows.Next() {
		var id string
		st := &Structure{}
		if err := rows.Scan(&id, &st.ApproverID, &st.ApproverName, &st.TimeAutoApproveSince); err != nil {
			rows.Close()
			return nil, err
		}
		st.TimeAutoApprove = st.TimeAutoApproveSince != nil
		out[id] = st
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Memberships, one query for everybody rather than one per person, ordered
	// by name so the page lists them the same way twice running.
	depts, err := s.db.Query(ctx, `
		SELECT pd.principal_id::text, s.id::text, s.name
		  FROM principal_departments pd
		  JOIN spaces s ON s.id = pd.space_id
		 WHERE pd.org_id = $1
		 ORDER BY s.name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("authz: read departments: %w", err)
	}
	for depts.Next() {
		var pid string
		var d Department
		if err := depts.Scan(&pid, &d.ID, &d.Name); err != nil {
			depts.Close()
			return nil, err
		}
		if out[pid] == nil {
			out[pid] = &Structure{}
		}
		out[pid].Departments = append(out[pid].Departments, d)
	}
	depts.Close()
	if err := depts.Err(); err != nil {
		return nil, err
	}

	managers, err := s.db.Query(ctx, `
		SELECT m.principal_id::text, s.name
		  FROM space_managers m JOIN spaces s ON s.id = m.space_id
		 WHERE m.org_id = $1 ORDER BY s.name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("authz: read managers: %w", err)
	}
	defer managers.Close()
	for managers.Next() {
		var id, name string
		if err := managers.Scan(&id, &name); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = &Structure{}
		}
		out[id].Manages = append(out[id].Manages, name)
	}
	return out, managers.Err()
}

// SetTimeAutoApprove turns on or off the approval of a person's time as they log
// it, for salaried staff whose hours nobody signs off one by one. Only a super
// admin may set it, in a playground too: it removes the approval step, so it is
// not something People or a department manager can give or take.
//
// Turning it on records the moment, and only work that starts at or after that
// moment is approved by it, so a backlog filed for the person stays with their
// approver. Entries already logged keep the state they have. A change is
// audited with the person, what it was, and what it is. Setting it to what it
// already is writes nothing and keeps the original moment.
func (s *Service) SetTimeAutoApprove(ctx context.Context, orgID, actorID, principalID string, on bool) error {
	return s.SetTimeAutoApproveFrom(ctx, orgID, actorID, principalID, on, nil)
}

// SetTimeAutoApproveFrom is SetTimeAutoApprove with the moment chosen: from is the day the setting is counted from, so
// salaried people whose time has always been approved by standing order cover every entry they file, including one
// dated before the day the switch was thrown. Nil from keeps the old behaviour (now when it turns on, the original
// moment when it is already on). With from set the setting is on, and moving the moment is audited like any change.
func (s *Service) SetTimeAutoApproveFrom(ctx context.Context, orgID, actorID, principalID string, on bool, from *time.Time) error {
	held, err := s.Capabilities(ctx, orgID, actorID)
	if err != nil {
		return err
	}
	if !held.IsSuperAdmin() {
		return ErrNotSuperAdmin
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("authz: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var since *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT time_auto_approve_since FROM principals
		 WHERE id = $1 AND org_id = $2 AND kind = 'user' FOR UPDATE`, principalID, orgID).Scan(&since); err != nil {
		return errors.New("authz: no such person")
	}
	if from != nil && !on {
		return errors.New("authz: a start day goes with turning it on")
	}
	moved := from != nil && (since == nil || !since.Equal(*from))
	if (since != nil) == on && !moved {
		return nil
	}
	var after *time.Time
	if err := tx.QueryRow(ctx, `
		UPDATE principals
		   SET time_auto_approve_since = CASE WHEN NOT $3 THEN NULL WHEN $4::timestamptz IS NOT NULL THEN $4::timestamptz ELSE now() END, updated_at = now()
		 WHERE id = $1 AND org_id = $2
		RETURNING time_auto_approve_since`, principalID, orgID, on, from).Scan(&after); err != nil {
		return fmt.Errorf("authz: set time auto approve: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionTimeAutoApprove,
		EntityType: "principal", EntityID: principalID,
		Before: map[string]any{"time_auto_approve": since != nil, "since": since},
		After:  map[string]any{"time_auto_approve": on, "since": after},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
