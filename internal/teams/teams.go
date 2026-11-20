// Package teams is a named group of people that grants accept as a subject.
//
// # Why this is small
//
// Because the permission model already had a hole shaped like it. grants has
// accepted 'team:<uuid>' subject keys since the substrate migration, and
// resolving a caller's access has always been the MAXIMUM over the subject set
// they hold. So a team is two tables and one extra query in SubjectsFor, and
// then granting a team access to a space is the same statement that grants a
// person one.
//
// The alternative, a roles system with a matrix of permissions, is the thing
// that gets built when the substrate cannot express groups. It is always bigger
// than it looks, nobody can answer "why can Dana see this" from it, and it ends
// up beside the grants rather than instead of them.
//
// # Local teams and directory teams
//
// A team made here has source local and no external id. A team the directory
// pushed has source scim and the group's own id, which is what the sync joins
// on so that renaming a group in Entra does not create a second team here.
//
// The two are deliberately not interchangeable. A sync must not take over a
// team somebody made by hand, because the first thing it would do is remove
// everybody in it who is not in a group it has never heard of.
package teams

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
)

var (
	ErrNotFound = errors.New("no such team")
	ErrInvalid  = errors.New("that is not a team")
	// ErrManaged is returned when something tries to edit a directory team by
	// hand. Allowing it produces a team that disagrees with the group it
	// mirrors until the next sync silently undoes the change.
	ErrManaged = errors.New("that team comes from the directory and is changed there")
)

const (
	SourceLocal = "local"
	SourceSCIM  = "scim"
)

// Team is a group of people.
type Team struct {
	ID          string
	OrgID       string
	Name        string
	Description string
	ExternalID  *string
	Source      string
	Members     int
	CreatedAt   time.Time
}

// Managed reports whether the directory owns this team.
func (t Team) Managed() bool { return t.Source == SourceSCIM }

// Subject is the grant subject this team is, which is the whole point of it.
func (t Team) Subject() core.Subject {
	return core.Subject{Kind: core.SubjectTeam, ID: t.ID}
}

// Member is somebody in a team.
type Member struct {
	PrincipalID string
	Name        string
	Status      string
	AddedAt     time.Time
}

type Service struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

// Create makes a team.
func (s *Service) Create(ctx context.Context, orgID, actorID, name, description string) (*Team, error) {
	return s.create(ctx, orgID, actorID, name, description, "", SourceLocal)
}

// CreateManaged makes a team the directory owns, keyed on the group's own id.
func (s *Service) CreateManaged(ctx context.Context, orgID, actorID, name, externalID string) (*Team, error) {
	if strings.TrimSpace(externalID) == "" {
		return nil, fmt.Errorf("%w: a directory team needs the group's id", ErrInvalid)
	}
	return s.create(ctx, orgID, actorID, name, "", externalID, SourceSCIM)
}

func (s *Service) create(ctx context.Context, orgID, actorID, name, description, external, source string) (*Team, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: a team needs a name", ErrInvalid)
	}
	if len(name) > 120 {
		return nil, fmt.Errorf("%w: that name is too long to fit on a page", ErrInvalid)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("teams: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id := uuid.NewString()
	var ext any
	if external != "" {
		ext = external
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO teams (id, org_id, name, description, external_id, source)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		id, orgID, name, description, ext, source); err != nil {
		if isUnique(err) {
			return nil, fmt.Errorf("%w: there is already a team called %s", ErrInvalid, name)
		}
		return nil, fmt.Errorf("teams: create: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionTeamCreate,
		EntityType: "team", EntityID: id,
		After: map[string]any{"name": name, "source": source},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("teams: commit: %w", err)
	}
	return s.Get(ctx, orgID, id)
}

// Rename changes what a team is called. Directory teams are renamed there.
func (s *Service) Rename(ctx context.Context, orgID, actorID, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: a team needs a name", ErrInvalid)
	}
	current, err := s.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	if current.Managed() {
		return ErrManaged
	}
	return s.rename(ctx, orgID, actorID, current, name)
}

// RenameManaged is the sync's own rename, which is allowed to touch a
// directory team because it is the directory that renamed the group.
func (s *Service) RenameManaged(ctx context.Context, orgID, actorID, id, name string) error {
	current, err := s.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	if current.Name == strings.TrimSpace(name) {
		return nil
	}
	return s.rename(ctx, orgID, actorID, current, strings.TrimSpace(name))
}

func (s *Service) rename(ctx context.Context, orgID, actorID string, current *Team, name string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("teams: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE teams SET name=$3, updated_at=now() WHERE id=$1 AND org_id=$2`,
		current.ID, orgID, name); err != nil {
		if isUnique(err) {
			return fmt.Errorf("%w: there is already a team called %s", ErrInvalid, name)
		}
		return fmt.Errorf("teams: rename: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionTeamRename,
		EntityType: "team", EntityID: current.ID,
		Before: map[string]any{"name": current.Name},
		After:  map[string]any{"name": name},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Delete removes a team.
//
// The grants that named it go with it, by the same reasoning the substrate uses
// everywhere else: a grant to a subject that no longer exists is a row nothing
// will ever match, and leaving it means a team recreated with the same id would
// silently inherit access nobody granted it. The ids are uuids so that cannot
// actually happen, and the row is still noise in the table an auditor reads.
func (s *Service) Delete(ctx context.Context, orgID, actorID, id string) error {
	current, err := s.Get(ctx, orgID, id)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("teams: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM grants WHERE org_id=$1 AND subject_key=$2`,
		orgID, current.Subject().Key()); err != nil {
		return fmt.Errorf("teams: clear grants: %w", err)
	}
	ct, err := tx.Exec(ctx, `DELETE FROM teams WHERE id=$1 AND org_id=$2`, id, orgID)
	if err != nil {
		return fmt.Errorf("teams: delete: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionTeamDelete,
		EntityType: "team", EntityID: id,
		Before: map[string]any{"name": current.Name, "members": current.Members},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Add puts somebody in a team. Adding them twice is not an error.
func (s *Service) Add(ctx context.Context, orgID, actorID, teamID, principalID string) error {
	team, err := s.Get(ctx, orgID, teamID)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("teams: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The principal has to belong to this org. Without the check, a uuid typed
	// into a form puts somebody from another tenant into a team here, and the
	// grants would then honour it.
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM principals WHERE id=$1 AND org_id=$2)`,
		principalID, orgID).Scan(&exists); err != nil {
		return fmt.Errorf("teams: check principal: %w", err)
	}
	if !exists {
		return fmt.Errorf("%w: there is nobody here with that id", ErrInvalid)
	}

	ct, err := tx.Exec(ctx, `
		INSERT INTO team_members (team_id, principal_id, added_by)
		VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
		teamID, principalID, nullable(actorID))
	if err != nil {
		return fmt.Errorf("teams: add member: %w", err)
	}
	if ct.RowsAffected() == 0 {
		// Already in it. Recording a second identical row would make the log
		// say something happened when nothing did.
		return tx.Commit(ctx)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionTeamMemberAdd,
		EntityType: "team", EntityID: teamID,
		After: map[string]any{"team": team.Name, "principal": principalID},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Remove takes somebody out of a team, which takes away everything the team
// granted them. Removing somebody who is not in it is not an error.
func (s *Service) Remove(ctx context.Context, orgID, actorID, teamID, principalID string) error {
	team, err := s.Get(ctx, orgID, teamID)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("teams: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ct, err := tx.Exec(ctx,
		`DELETE FROM team_members WHERE team_id=$1 AND principal_id=$2`,
		teamID, principalID)
	if err != nil {
		return fmt.Errorf("teams: remove member: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionTeamMemberDrop,
		EntityType: "team", EntityID: teamID,
		Before: map[string]any{"team": team.Name, "principal": principalID},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetMembers replaces a directory team's membership with exactly this list.
//
// One statement pair rather than a read, a diff, and a loop, because the
// directory's view is authoritative and a diff computed here would be wrong
// the moment two syncs overlap. The audit rows are written for what actually
// changed, so a sync that changes nothing writes nothing.
func (s *Service) SetMembers(ctx context.Context, orgID, actorID, teamID string, principalIDs []string) (added, removed int, err error) {
	team, err := s.Get(ctx, orgID, teamID)
	if err != nil {
		return 0, 0, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("teams: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Only ids that belong here, so a directory that sends a stranger cannot
	// put them in a team. Suspended accounts are left out for the same reason a
	// suspension takes them off every team: a directory group that still lists
	// somebody would otherwise put them back on its next sync.
	rows, err := tx.Query(ctx, `
		SELECT id::text FROM principals WHERE org_id=$1 AND status = 'active' AND id = ANY($2::uuid[])`,
		orgID, principalIDs)
	if err != nil {
		return 0, 0, fmt.Errorf("teams: check principals: %w", err)
	}
	var known []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("teams: scan principal: %w", err)
		}
		known = append(known, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	gone, err := tx.Query(ctx, `
		DELETE FROM team_members
		 WHERE team_id=$1 AND NOT (principal_id = ANY($2::uuid[]))
		RETURNING principal_id::text`, teamID, known)
	if err != nil {
		return 0, 0, fmt.Errorf("teams: prune members: %w", err)
	}
	var dropped []string
	for gone.Next() {
		var id string
		if err := gone.Scan(&id); err != nil {
			gone.Close()
			return 0, 0, fmt.Errorf("teams: scan removed: %w", err)
		}
		dropped = append(dropped, id)
	}
	gone.Close()
	if err := gone.Err(); err != nil {
		return 0, 0, err
	}

	var joined []string
	for _, id := range known {
		ct, err := tx.Exec(ctx, `
			INSERT INTO team_members (team_id, principal_id, added_by)
			VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
			teamID, id, nullable(actorID))
		if err != nil {
			return 0, 0, fmt.Errorf("teams: add member: %w", err)
		}
		if ct.RowsAffected() == 1 {
			joined = append(joined, id)
		}
	}

	for _, id := range dropped {
		if err := audit.Record(ctx, tx, audit.Entry{
			OrgID: orgID, ActorID: actorID, Action: audit.ActionTeamMemberDrop,
			EntityType: "team", EntityID: teamID,
			Before: map[string]any{"team": team.Name, "principal": id, "by": "directory"},
		}); err != nil {
			return 0, 0, err
		}
	}
	for _, id := range joined {
		if err := audit.Record(ctx, tx, audit.Entry{
			OrgID: orgID, ActorID: actorID, Action: audit.ActionTeamMemberAdd,
			EntityType: "team", EntityID: teamID,
			After: map[string]any{"team": team.Name, "principal": id, "by": "directory"},
		}); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("teams: commit: %w", err)
	}
	return len(joined), len(dropped), nil
}

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

const selectTeams = `
	SELECT t.id::text, t.org_id::text, t.name, t.description, t.external_id,
	       t.source, t.created_at,
	       (SELECT count(*) FROM team_members m WHERE m.team_id = t.id)::int
	  FROM teams t`

func (s *Service) Get(ctx context.Context, orgID, id string) (*Team, error) {
	rows, err := s.db.Query(ctx, selectTeams+` WHERE t.id=$1 AND t.org_id=$2`, id, orgID)
	if err != nil {
		return nil, fmt.Errorf("teams: get: %w", err)
	}
	defer rows.Close()
	list, err := scan(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return list[0], nil
}

// ByExternalID finds the team mirroring a directory group.
func (s *Service) ByExternalID(ctx context.Context, orgID, externalID string) (*Team, error) {
	rows, err := s.db.Query(ctx,
		selectTeams+` WHERE t.org_id=$1 AND t.external_id=$2`, orgID, externalID)
	if err != nil {
		return nil, fmt.Errorf("teams: by external id: %w", err)
	}
	defer rows.Close()
	list, err := scan(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return list[0], nil
}

func (s *Service) List(ctx context.Context, orgID string) ([]*Team, error) {
	rows, err := s.db.Query(ctx, selectTeams+` WHERE t.org_id=$1 ORDER BY lower(t.name)`, orgID)
	if err != nil {
		return nil, fmt.Errorf("teams: list: %w", err)
	}
	defer rows.Close()
	return scan(rows)
}

// Members are the people in a team, with their status, because somebody
// suspended who is still in a team is a question worth being able to see.
func (s *Service) Members(ctx context.Context, orgID, teamID string) ([]Member, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text, p.display_name, p.status, m.added_at
		  FROM team_members m
		  JOIN principals p ON p.id = m.principal_id
		 WHERE m.team_id = $1 AND p.org_id = $2
		 ORDER BY lower(p.display_name)`, teamID, orgID)
	if err != nil {
		return nil, fmt.Errorf("teams: members: %w", err)
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.PrincipalID, &m.Name, &m.Status, &m.AddedAt); err != nil {
			return nil, fmt.Errorf("teams: scan member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Of is the teams somebody belongs to.
func (s *Service) Of(ctx context.Context, orgID, principalID string) ([]*Team, error) {
	rows, err := s.db.Query(ctx, selectTeams+`
		 JOIN team_members m ON m.team_id = t.id
		 WHERE t.org_id=$1 AND m.principal_id=$2
		 ORDER BY lower(t.name)`, orgID, principalID)
	if err != nil {
		return nil, fmt.Errorf("teams: of: %w", err)
	}
	defer rows.Close()
	return scan(rows)
}

func scan(rows pgx.Rows) ([]*Team, error) {
	var out []*Team
	for rows.Next() {
		t := &Team{}
		if err := rows.Scan(&t.ID, &t.OrgID, &t.Name, &t.Description,
			&t.ExternalID, &t.Source, &t.CreatedAt, &t.Members); err != nil {
			return nil, fmt.Errorf("teams: scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func nullable(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}
