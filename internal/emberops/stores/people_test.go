package stores_test

import (
	"context"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/deptreq"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
	"github.com/Embernet-ai/hearth/internal/teams"
)

type pgTeams struct {
	teams.Store
	byID    map[string]*teams.Team
	members map[string][]string
}

func (p *pgTeams) Create(_ context.Context, _, _, name, _ string) (*teams.Team, error) {
	t := &teams.Team{ID: "eeeeeeee-0000-0000-0000-00000000000" + string(rune('1'+len(p.byID))), Name: name, Source: teams.SourceLocal}
	p.byID[t.ID] = t
	return t, nil
}
func (p *pgTeams) Get(_ context.Context, _, id string) (*teams.Team, error) { return p.byID[id], nil }
func (p *pgTeams) Members(_ context.Context, _, id string) ([]teams.Member, error) {
	var out []teams.Member
	for _, m := range p.members[id] {
		out = append(out, teams.Member{PrincipalID: m})
	}
	return out, nil
}
func (p *pgTeams) Add(_ context.Context, _, _, team, who string) error {
	p.members[team] = append(p.members[team], who)
	return nil
}
func (p *pgTeams) Rename(_ context.Context, _, _, id, name string) error {
	p.byID[id].Name = name
	return nil
}
func (p *pgTeams) Delete(_ context.Context, _, _, id string) error {
	delete(p.byID, id)
	return nil
}

func TestTeamsAreWrittenToPostgresThenCopiedToEmployeeGroups(t *testing.T) {
	e := newEnv(t)
	pg := &pgTeams{byID: map[string]*teams.Team{}, members: map[string][]string{}}
	s := &stores.TeamsMirror{Store: pg, Base: e.base, Dir: e.dir}
	ctx := e.as("ann-tok")
	tm, err := s.Create(ctx, org, ann, "Night shift", "")
	if err != nil {
		t.Fatal(err)
	}
	// An agent with no Employee is in the team in Hearth and not in the group.
	pg.members[tm.ID] = []string{"99999999-0000-0000-0000-000000000000"}
	if err := s.Add(ctx, org, ann, tm.ID, bob); err != nil {
		t.Fatal(err)
	}
	groups := e.f.All("Employee Group")
	if len(groups) != 1 || groups[0]["employee_group_name"] != "Night shift" || groups[0]["hearth_id"] != tm.ID {
		t.Fatalf("groups = %v", groups)
	}
	list, _ := groups[0]["employee_list"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["employee"] != "EMP-bob" {
		t.Fatalf("members = %v", list)
	}
	if err := s.Rename(ctx, org, ann, tm.ID, "Nights"); err != nil || e.f.All("Employee Group")[0]["employee_group_name"] != "Nights" {
		t.Fatalf("rename: %v", err)
	}
	if err := s.Delete(ctx, org, ann, tm.ID); err != nil || len(e.f.All("Employee Group")) != 0 {
		t.Fatalf("delete: %v", err)
	}
}

type pgReqs struct {
	deptreq.Store
	latest map[string]*deptreq.Request
	n      int
}

func (p *pgReqs) Latest(_ context.Context, _, who string) (*deptreq.Request, error) {
	return p.latest[who], nil
}
func (p *pgReqs) Ask(_ context.Context, _, who string, ids []string, note string) (*deptreq.Request, error) {
	p.n++
	r := &deptreq.Request{ID: "ffffffff-0000-0000-0000-00000000000" + string(rune('0'+p.n)), RequesterID: who, Status: "pending", Note: note}
	for _, id := range ids {
		r.Asked = append(r.Asked, deptreq.Department{ID: id})
	}
	p.latest[who] = r
	return r, nil
}
func (p *pgReqs) RequesterOf(_ context.Context, _, id string) (string, error) {
	for w, r := range p.latest {
		if r.ID == id {
			return w, nil
		}
	}
	return "", nil
}
func (p *pgReqs) Decide(_ context.Context, _, by, id string, approve bool, final []string, note string) error {
	who, _ := p.RequesterOf(nil, "", id)
	r := p.latest[who]
	r.Status, r.DecidedBy, r.DecisionNote = "approved", by, note
	now := time.Now()
	r.DecidedAt = &now
	for _, f := range final {
		r.Granted = append(r.Granted, deptreq.Department{ID: f})
	}
	return nil
}

func TestDepartmentRequestsAreCopiedAndTheReplacedOneIsCancelled(t *testing.T) {
	e := newEnv(t)
	e.f.Put("Hearth Space", map[string]any{"name": "HSP-1", "hearth_id": "dddddddd-0000-0000-0000-000000000001"})
	e.f.Put("Hearth Space", map[string]any{"name": "HSP-2", "hearth_id": "dddddddd-0000-0000-0000-000000000002"})
	pg := &pgReqs{latest: map[string]*deptreq.Request{}}
	s := &stores.DeptReqMirror{Store: pg, Base: e.base, Dir: e.dir}
	ctx := e.as("bob-tok")
	r1, err := s.Ask(ctx, org, bob, []string{"dddddddd-0000-0000-0000-000000000001"}, "please")
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := s.Ask(ctx, org, bob, []string{"dddddddd-0000-0000-0000-000000000002"}, "actually this")
	by := map[string]map[string]any{}
	for _, d := range e.f.All("Hearth Department Request") {
		by[d["hearth_id"].(string)] = d
	}
	if len(by) != 2 || by[r1.ID]["status"] != "cancelled" || by[r2.ID]["status"] != "pending" || by[r2.ID]["employee"] != "EMP-bob" {
		t.Fatalf("requests = %v", by)
	}
	sp, _ := by[r2.ID]["spaces"].([]any)
	if len(sp) != 1 || sp[0].(map[string]any)["request_kind"] != "asked" || sp[0].(map[string]any)["hearth_space"] != "HSP-2" {
		t.Fatalf("spaces = %v", sp)
	}
	if err := s.Decide(e.as("ann-tok"), org, ann, r2.ID, true, []string{"dddddddd-0000-0000-0000-000000000001"}, "ok"); err != nil {
		t.Fatal(err)
	}
	got := e.f.All("Hearth Department Request")
	for _, d := range got {
		if d["hearth_id"] == r2.ID {
			if d["status"] != "approved" || d["decided_by"] != "ann@example.com" || d["decision_note"] != "ok" {
				t.Fatalf("decided = %v", d)
			}
			sp, _ := d["spaces"].([]any)
			if len(sp) != 2 {
				t.Fatalf("asked and granted = %v", sp)
			}
		}
	}
}
