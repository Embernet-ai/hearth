package stores

import (
	"context"
	"log/slog"
	"time"

	"github.com/Embernet-ai/hearth/internal/deptreq"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
	"github.com/Embernet-ai/hearth/internal/teams"
)

// Teams and department requests are the two domains where Postgres stays the
// store of record and EmberOPS holds a copy kept current on every write.
//
// A team is a subject Hearth's access control grants to, and a department
// request, once approved, changes the departments a person belongs to; both
// feed queries that run on every page (authz.SubjectsFor, core.MemberSpaces),
// which cannot be asked of EmberOPS per row. So the write happens in Hearth
// first, with all its rules, and the resulting team or request is then written
// to EmberOPS (Employee Group, Hearth Department Request) so its reports,
// permission hooks and users see the same thing. An edit made to an Employee
// Group or a request inside EmberOPS does not flow back.

// TeamsMirror writes teams to Postgres and copies each into EmberOPS.
type TeamsMirror struct {
	teams.Store
	Base
	Dir Directory
	Log *slog.Logger
}

var _ teams.Store = (*TeamsMirror)(nil)

func (s *TeamsMirror) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *TeamsMirror) push(ctx context.Context, orgID, id string) {
	t, err := s.Store.Get(ctx, orgID, id)
	if err != nil {
		s.log().Warn("emberops: could not read a team to copy it", "team", id, "error", err)
		return
	}
	members, err := s.Store.Members(ctx, orgID, id)
	if err != nil {
		s.log().Warn("emberops: could not read team members to copy them", "team", id, "error", err)
		return
	}
	emps, err := s.Dir.EmployeeIDs(ctx)
	if err != nil {
		s.log().Warn("emberops: could not read employees to copy a team", "team", id, "error", err)
		return
	}
	has := map[string]bool{}
	for _, id := range emps {
		has[id] = true
	}
	var ids []string
	for _, m := range members {
		if has[m.PrincipalID] { // a bot or agent with no Employee is not in the group
			ids = append(ids, m.PrincipalID)
		}
	}
	rec := mapping.Rec{"id": t.ID, "name": t.Name, "source": t.Source}
	if t.ExternalID != nil {
		rec["external_id"] = *t.ExternalID
	}
	doc := mapping.TeamDoc(rec, ids)
	delete(doc, "doctype")
	delete(doc, "hearth_id")
	if _, err := s.C.Upsert(ctx, "Employee Group", t.ID, doc); err != nil {
		s.log().Warn("emberops: could not copy a team", "team", id, "error", err)
	}
}

func (s *TeamsMirror) Create(ctx context.Context, orgID, actorID, name, description string) (*teams.Team, error) {
	t, err := s.Store.Create(ctx, orgID, actorID, name, description)
	if err == nil {
		s.push(ctx, orgID, t.ID)
	}
	return t, err
}

func (s *TeamsMirror) CreateManaged(ctx context.Context, orgID, actorID, name, externalID string) (*teams.Team, error) {
	t, err := s.Store.CreateManaged(ctx, orgID, actorID, name, externalID)
	if err == nil {
		s.push(ctx, orgID, t.ID)
	}
	return t, err
}

func (s *TeamsMirror) Rename(ctx context.Context, orgID, actorID, id, name string) error {
	err := s.Store.Rename(ctx, orgID, actorID, id, name)
	if err == nil {
		s.push(ctx, orgID, id)
	}
	return err
}

func (s *TeamsMirror) RenameManaged(ctx context.Context, orgID, actorID, id, name string) error {
	err := s.Store.RenameManaged(ctx, orgID, actorID, id, name)
	if err == nil {
		s.push(ctx, orgID, id)
	}
	return err
}

func (s *TeamsMirror) Delete(ctx context.Context, orgID, actorID, id string) error {
	err := s.Store.Delete(ctx, orgID, actorID, id)
	if err == nil {
		if derr := s.C.DeleteByHearthID(ctx, "Employee Group", id); derr != nil {
			s.log().Warn("emberops: could not remove a deleted team", "team", id, "error", derr)
		}
	}
	return err
}

func (s *TeamsMirror) Add(ctx context.Context, orgID, actorID, teamID, principalID string) error {
	err := s.Store.Add(ctx, orgID, actorID, teamID, principalID)
	if err == nil {
		s.push(ctx, orgID, teamID)
	}
	return err
}

func (s *TeamsMirror) Remove(ctx context.Context, orgID, actorID, teamID, principalID string) error {
	err := s.Store.Remove(ctx, orgID, actorID, teamID, principalID)
	if err == nil {
		s.push(ctx, orgID, teamID)
	}
	return err
}

func (s *TeamsMirror) SetMembers(ctx context.Context, orgID, actorID, teamID string, ids []string) (int, int, error) {
	added, removed, err := s.Store.SetMembers(ctx, orgID, actorID, teamID, ids)
	if err == nil && (added > 0 || removed > 0) {
		s.push(ctx, orgID, teamID)
	}
	return added, removed, err
}

// DeptReqMirror files and decides department requests in Hearth, where
// approval changes the person's departments, and copies each request into
// EmberOPS as a Hearth Department Request.
type DeptReqMirror struct {
	deptreq.Store
	Base
	Dir Directory
	Log *slog.Logger
}

var _ deptreq.Store = (*DeptReqMirror)(nil)

func (s *DeptReqMirror) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *DeptReqMirror) push(ctx context.Context, r *deptreq.Request) {
	if r == nil {
		return
	}
	emails, err := s.Dir.Emails(ctx)
	if err != nil {
		s.log().Warn("emberops: could not copy a department request", "request", r.ID, "error", err)
		return
	}
	rec := mapping.Rec{"id": r.ID, "requester_id": r.RequesterID, "status": r.Status, "note": r.Note,
		"decision_note": r.DecisionNote, "decided_by_email": emails[r.DecidedBy]}
	if r.DecidedAt != nil {
		rec["decided_at"] = r.DecidedAt.UTC().Format(time.RFC3339)
	}
	var spaces []mapping.Rec
	for _, d := range r.Asked {
		spaces = append(spaces, mapping.Rec{"space_id": d.ID, "kind": "asked"})
	}
	for _, d := range r.Granted {
		spaces = append(spaces, mapping.Rec{"space_id": d.ID, "kind": "granted"})
	}
	doc := mapping.TZ{Loc: s.loc()}.DepartmentRequestDoc(rec, spaces)
	delete(doc, "doctype")
	delete(doc, "hearth_id")
	if r.DecidedBy == "" {
		doc["decided_by"], doc["decided_at"] = nil, nil
	}
	if _, err := s.C.Upsert(ctx, "Hearth Department Request", r.ID, doc); err != nil {
		s.log().Warn("emberops: could not copy a department request", "request", r.ID, "error", err)
	}
}

// Ask files the request. Asking again replaces a waiting one (Postgres marks it
// cancelled), and EmberOPS allows one pending request per person, so the
// replaced request is cancelled there first.
func (s *DeptReqMirror) Ask(ctx context.Context, orgID, actorID string, spaceIDs []string, note string) (*deptreq.Request, error) {
	prev, _ := s.Store.Latest(ctx, orgID, actorID)
	r, err := s.Store.Ask(ctx, orgID, actorID, spaceIDs, note)
	if err != nil {
		return nil, err
	}
	if prev != nil && prev.Status == "pending" && prev.ID != r.ID {
		prev.Status = "cancelled"
		s.push(ctx, prev)
	}
	if full, err := s.Store.Latest(ctx, orgID, actorID); err == nil && full != nil {
		s.push(ctx, full)
	}
	return r, nil
}

func (s *DeptReqMirror) Cancel(ctx context.Context, orgID, actorID string) error {
	if err := s.Store.Cancel(ctx, orgID, actorID); err != nil {
		return err
	}
	if r, err := s.Store.Latest(ctx, orgID, actorID); err == nil {
		s.push(ctx, r)
	}
	return nil
}

func (s *DeptReqMirror) Decide(ctx context.Context, orgID, actorID, id string, approve bool, final []string, note string) error {
	if err := s.Store.Decide(ctx, orgID, actorID, id, approve, final, note); err != nil {
		return err
	}
	if who, err := s.Store.RequesterOf(ctx, orgID, id); err == nil && who != "" {
		if r, err := s.Store.Latest(ctx, orgID, who); err == nil && r != nil && r.ID == id {
			s.push(ctx, r)
		}
	}
	return nil
}
