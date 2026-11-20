package stores_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
	"github.com/Embernet-ai/hearth/internal/plant"
)

type pgPlant struct {
	plant.Store
	tree   []*plant.Asset
	fields map[string]plant.Fields
	spaces []*plant.SpaceAccess
}

func (p *pgPlant) Tree(context.Context, string, core.Subjects) ([]*plant.Asset, error) {
	return p.tree, nil
}
func (p *pgPlant) SetFields(_ context.Context, _ string, _ core.Subjects, id string, f plant.Fields) error {
	p.fields[id] = f
	return nil
}
func (p *pgPlant) CreateSpace(_ context.Context, _ string, _ core.Subjects, name, kind string, parent *string) (*plant.Space, error) {
	id := fmt.Sprintf("cccccccc-0000-0000-0000-%012d", len(p.spaces)+1)
	p.spaces = append(p.spaces, &plant.SpaceAccess{ID: id, ParentID: parent, Name: name, Kind: kind})
	return &plant.Space{ID: id, Name: name, Kind: kind}, nil
}
func (p *pgPlant) DeleteSpace(_ context.Context, _ string, _ core.Subjects, id string) error {
	for i, s := range p.spaces {
		if s.ID == id {
			p.spaces = append(p.spaces[:i], p.spaces[i+1:]...)
			break
		}
	}
	return nil
}
func (p *pgPlant) SpaceAccessAll(context.Context, string) ([]*plant.SpaceAccess, error) {
	return p.spaces, nil
}

const pressID, lineID = assetID, "99999999-9999-9999-9999-999999999999"

func newPlant(t *testing.T) (*env, *stores.Plant, *pgPlant) {
	e := newEnv(t)
	e.f.Put("Asset", map[string]any{"name": "AST-1", "hearth_id": pressID, "asset_name": "Press 4"})
	e.f.Put("Asset", map[string]any{"name": "AST-L", "hearth_id": lineID, "asset_name": "Line 2"})
	pg := &pgPlant{fields: map[string]plant.Fields{}, tree: []*plant.Asset{
		{ID: lineID}, {ID: pressID, ParentID: strp(lineID)},
	}}
	sync := &stores.SpaceSync{C: e.c, Source: pg, Dir: e.dir}
	return e, &stores.Plant{Store: pg, Base: e.base, Dir: e.dir, Authz: denyAssets{deny: map[string]bool{}}, Spaces: sync}, pg
}

func strp(s string) *string { return &s }

func TestPlantDowntimeLivesInAssetFailureLog(t *testing.T) {
	e, s, _ := newPlant(t)
	ctx := e.as("ann-tok")
	start := time.Now().Add(-3 * time.Hour).Truncate(time.Minute)
	d, err := s.GoDown(ctx, org, actor(ann), pressID, start, false, nil, " jam ")
	if err != nil || !d.Open() {
		t.Fatalf("go down: %+v %v", d, err)
	}
	logs := e.f.All("Asset Failure Log")
	if len(logs) != 1 || logs[0]["asset"] != "AST-1" || logs[0]["hearth_note"] != "jam" || logs[0]["downtime_end"] != nil {
		t.Fatalf("log = %v", logs)
	}
	if _, err := s.GoDown(ctx, org, actor(ann), pressID, start, false, nil, ""); !errors.Is(err, plant.ErrAlreadyDown) {
		t.Fatalf("second outage: %v", err)
	}
	if _, err := s.GoDown(ctx, org, actor(ann), pressID, time.Now().Add(time.Hour), false, nil, ""); !errors.Is(err, plant.ErrInvalid) {
		t.Fatalf("future outage: %v", err)
	}
	up, err := s.ComeUp(ctx, org, actor(ann), pressID, start.Add(2*time.Hour))
	if err != nil || up.Minutes == nil || *up.Minutes != 120 {
		t.Fatalf("come up: %+v %v", up, err)
	}
	if _, err := s.ComeUp(ctx, org, actor(ann), pressID, time.Time{}); !errors.Is(err, plant.ErrNotDown) {
		t.Fatalf("come up twice: %v", err)
	}
	// An outage already over, planned this time.
	if _, err := s.LogDowntime(ctx, org, actor(ann), pressID, start.Add(-48*time.Hour), start.Add(-47*time.Hour), true, nil, "shutdown"); err != nil {
		t.Fatal(err)
	}
	list, err := s.DowntimeFor(ctx, org, actor(ann), pressID, 10)
	if err != nil || len(list) != 2 || list[0].Open() || *list[0].Minutes != 120 || !list[1].Planned || list[1].ByName != "Ann" {
		t.Fatalf("list = %+v %v", list, err)
	}

	// Availability rolls up the tree: an outage on the press counts for the line.
	from, to := start.Add(-72*time.Hour), start.Add(24*time.Hour)
	av, err := s.AvailabilityFor(ctx, org, actor(ann), lineID, from, to)
	if err != nil || av.Unplanned != 120 || av.Planned != 60 || av.Outages != 1 {
		t.Fatalf("availability = %+v %v", av, err)
	}
	// Clamped to the window.
	av, _ = s.AvailabilityFor(ctx, org, actor(ann), pressID, start.Add(time.Hour), to)
	if av.Unplanned != 60 {
		t.Fatalf("clamped = %+v", av)
	}
	if err := s.RemoveDowntime(ctx, org, actor(ann), list[0].ID); err != nil || len(e.f.All("Asset Failure Log")) != 1 {
		t.Fatalf("remove: %v", err)
	}
}

func TestPlantAssetIdentityIsWrittenToBothAndFoundByCode(t *testing.T) {
	e, s, pg := newPlant(t)
	ctx := e.as("ann-tok")
	inst := time.Date(2020, 5, 1, 0, 0, 0, 0, time.UTC)
	if err := s.SetFields(ctx, org, actor(ann), pressID, plant.Fields{Code: " P-004 ", ParentAssetID: strp(lineID), InstalledAt: &inst}); err != nil {
		t.Fatal(err)
	}
	if pg.fields[pressID].Code != " P-004 " {
		t.Fatal("the spine did not get the fields")
	}
	var a map[string]any
	for _, x := range e.f.All("Asset") {
		if x["hearth_id"] == pressID {
			a = x
		}
	}
	if a["hearth_code"] != "P-004" || a["hearth_parent_asset"] != "AST-L" || a["hearth_installed_at"] != "2020-05-01 00:00:00" {
		t.Fatalf("asset = %v", a)
	}
	f, err := s.FieldsFor(ctx, org, pressID)
	if err != nil || f.Code != "P-004" || f.ParentAssetID == nil || *f.ParentAssetID != lineID || f.InstalledAt == nil {
		t.Fatalf("fields = %+v %v", f, err)
	}
	id, err := s.ByCode(ctx, org, actor(ann), "p-004")
	if err != nil || id != pressID {
		t.Fatalf("by code = %q %v", id, err)
	}
	if _, err := s.ByCode(ctx, org, actor(ann), "nope"); !errors.Is(err, plant.ErrNotFound) {
		t.Fatalf("unknown code: %v", err)
	}
}

func TestSpaceStructureAndAccessAreCopiedIntoEmberOPS(t *testing.T) {
	e, s, pg := newPlant(t)
	ctx := e.as("svc-tok")
	e.f.Put("Employee Group", map[string]any{"name": "EG-1", "hearth_id": "aaaaaaaa-1111-1111-1111-111111111111"})
	dept, err := s.CreateSpace(ctx, org, actor(ann), "Maintenance", "folder", nil)
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := s.CreateSpace(ctx, org, actor(ann), "Line 2", "area", &dept.ID)
	// Access arrives from Hearth's own tables: ann manages, bob belongs, the team
	// and the whole org have grants, and a channel grant has no EmberOPS subject.
	exp := time.Now().Add(24 * time.Hour)
	pg.spaces[0].Managers, pg.spaces[0].Members = []string{ann}, []string{ann, bob}
	pg.spaces[0].Grants = []plant.SpaceGrant{
		{Kind: core.SubjectPrincipal, SubjectID: bob, Level: core.AccessEdit, ExpiresAt: &exp},
		{Kind: core.SubjectTeam, SubjectID: "aaaaaaaa-1111-1111-1111-111111111111", Level: core.AccessView},
		{Kind: core.SubjectOrg, SubjectID: org, Level: core.AccessComment},
		{Kind: core.SubjectChannel, SubjectID: "x", Level: core.AccessView},
	}
	if err := s.Spaces.Sync(ctx, org); err != nil {
		t.Fatal(err)
	}
	by := map[string]map[string]any{}
	for _, sp := range e.f.All("Hearth Space") {
		by[sp["space_name"].(string)] = sp
	}
	if len(by) != 2 || by["Line 2"]["parent_hearth_space"] != by["Maintenance"]["name"] {
		t.Fatalf("spaces = %v", by)
	}
	if d := by["Maintenance"]["is_department"]; d != 1.0 && d != 1 {
		t.Fatalf("a top-level space is a department: %v", d)
	}
	members, _ := by["Maintenance"]["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("members = %v", members)
	}
	roles := map[string]string{}
	for _, m := range members {
		mm := m.(map[string]any)
		roles[mm["user"].(string)] = mm["member_role"].(string)
	}
	if roles["ann@example.com"] != "Manager" || roles["bob@example.com"] != "Member" {
		t.Fatalf("roles = %v", roles)
	}
	grants, _ := by["Maintenance"]["grants"].([]any)
	if len(grants) != 3 {
		t.Fatalf("grants (the channel one must be left out) = %v", grants)
	}
	kinds := map[string]map[string]any{}
	for _, g := range grants {
		gm := g.(map[string]any)
		kinds[gm["subject_type"].(string)] = gm
	}
	if kinds["User"]["user"] != "bob@example.com" || kinds["User"]["level"] != "edit" || kinds["User"]["expires_on"] == nil ||
		kinds["Employee Group"]["employee_group"] != "EG-1" || kinds["Role"]["role"] != "Employee" {
		t.Fatalf("grant rows = %v", kinds)
	}

	// Unchanged structure costs no write.
	before := e.f.CallCount("bulk_upsert")
	if err := s.Spaces.Sync(ctx, org); err != nil {
		t.Fatal(err)
	}
	if e.f.CallCount("bulk_upsert") != before {
		t.Fatal("a no-op sync wrote")
	}

	// A space deleted in Hearth is deleted in EmberOPS.
	if err := s.DeleteSpace(ctx, org, actor(ann), sub.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(e.f.All("Hearth Space")); n != 1 {
		t.Fatalf("%d spaces left in EmberOPS", n)
	}
}

// In the EmberOps back end the tree's numbers (open work, minutes, downtime, per asset and per subtree) were read from Postgres
// tables that stop receiving time, status changes and outages, so every column read zero for new activity.
func TestAssetTreeRollupsComeFromEmberOps(t *testing.T) {
	e, s, pg := newPlant(t)
	pg.tree[0].OwnOpen, pg.tree[1].OwnMinutes = 99, 99 // Postgres's stale answers must not survive
	e.f.Put("Task", map[string]any{"name": "TASK-1", "hearth_id": "11111111-1111-1111-1111-111111111111", "status": "Working"})
	e.f.Put("Task", map[string]any{"name": "TASK-2", "hearth_id": "22222222-2222-2222-2222-222222222222", "status": "Completed"})
	for _, n := range []string{"TASK-1", "TASK-2"} {
		e.f.Put("Hearth Entity Link", map[string]any{"from_doctype": "Task", "from_name": n, "to_doctype": "Asset", "to_name": "AST-1", "link_kind": "concerns"})
	}
	e.f.Put("Timesheet Detail", map[string]any{"task": "TASK-1", "hours": 1.5})
	e.f.Put("Timesheet Detail", map[string]any{"task": "TASK-2", "hours": 0.5})
	e.f.Put("Asset Failure Log", map[string]any{"asset": "AST-1", "downtime_minutes": 120, "downtime_end": "2026-11-19 10:00:00", "hearth_planned": 0})
	e.f.Put("Asset Failure Log", map[string]any{"asset": "AST-1", "downtime_minutes": 60, "downtime_end": "2026-11-19 12:00:00", "hearth_planned": 1})
	tree, err := s.Tree(e.as("ann-tok"), org, actor(ann))
	if err != nil {
		t.Fatal(err)
	}
	line, press := tree[0], tree[1]
	if press.OwnOpen != 1 || press.OwnMinutes != 120 || press.OwnDowntime != 120 {
		t.Fatalf("press own = %+v", press)
	}
	if line.OwnOpen != 0 || line.OwnMinutes != 0 || line.RolledOpen != 1 || line.RolledMinutes != 120 || line.RolledDowntime != 120 {
		t.Fatalf("line = %+v", line)
	}
}
