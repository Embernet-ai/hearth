package stores_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
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
func (p *pgPlant) SpaceAccessOne(_ context.Context, _, id string) (*plant.SpaceAccess, error) {
	for _, s := range p.spaces {
		if s.ID == id {
			return s, nil
		}
	}
	return nil, nil
}
func (p *pgPlant) RenameSpace(_ context.Context, _ string, _ core.Subjects, id, name string) error {
	for _, s := range p.spaces {
		if s.ID == id {
			s.Name = name
		}
	}
	return nil
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

// A playground is somebody's sandbox. Its sync used to copy the sandbox's spaces to EmberOPS and, when none collided by name,
// delete every other space there, which is the real org's. A playground's edit now changes nothing in EmberOPS.
func TestPlaygroundSpaceSyncLeavesEmberOPSAlone(t *testing.T) {
	e, _, pg := newPlant(t)
	pg.spaces = []*plant.SpaceAccess{{ID: "dddddddd-0000-0000-0000-000000000001", Name: "Sandbox", Kind: "folder"}}
	e.f.Put("Hearth Space", map[string]any{"name": "Engineering", "hearth_id": "eeeeeeee-0000-0000-0000-000000000001", "space_name": "Engineering"})
	sync := &stores.SpaceSync{C: e.c, Source: pg, Dir: e.dir,
		Real: func(_ context.Context, orgID string) (bool, error) { return orgID != "playground", nil }}
	if err := sync.Sync(e.as("svc-tok"), "playground"); err != nil {
		t.Fatal(err)
	}
	spaces := e.f.All("Hearth Space")
	if len(spaces) != 1 || spaces[0]["space_name"] != "Engineering" {
		t.Fatalf("a playground's sync changed EmberOPS: %v", spaces)
	}
	// The real org still syncs: the sandbox space is not in it, the real one is kept as the source says.
	pg.spaces = []*plant.SpaceAccess{{ID: "eeeeeeee-0000-0000-0000-000000000001", Name: "Engineering", Kind: "folder"}}
	if err := sync.Sync(e.as("svc-tok"), org); err != nil {
		t.Fatal(err)
	}
	if n := len(e.f.All("Hearth Space")); n != 1 {
		t.Fatalf("real org sync left %d spaces", n)
	}
}

// A person's rate is an Activity Cost, and /plant prices the minutes booked on an asset's work by it, as the Postgres
// query prices them by capacity.rate_cents. Without it the cost column stayed zero in this mode for anyone with a rate.
func TestAssetTreeRollupsPriceLabourByActivityCost(t *testing.T) {
	e, s, _ := newPlant(t)
	e.f.Put("Task", map[string]any{"name": "TASK-1", "hearth_id": "11111111-1111-1111-1111-111111111111", "status": "Working"})
	e.f.Put("Hearth Entity Link", map[string]any{"from_doctype": "Task", "from_name": "TASK-1", "to_doctype": "Asset", "to_name": "AST-1", "link_kind": "concerns"})
	e.f.Put("Timesheet", map[string]any{"name": "TS-1", "employee": "HR-EMP-1"})
	e.f.Put("Timesheet", map[string]any{"name": "TS-2", "employee": "HR-EMP-2"}) // no rate
	e.f.Put("Timesheet Detail", map[string]any{"task": "TASK-1", "parent": "TS-1", "hours": 1.5})
	e.f.Put("Timesheet Detail", map[string]any{"task": "TASK-1", "parent": "TS-2", "hours": 2})
	e.f.Put("Activity Cost", map[string]any{"name": "AC-1", "employee": "HR-EMP-1", "activity_type": "Hearth", "costing_rate": 40.0})
	tree, err := s.Tree(e.as("ann-tok"), org, actor(ann))
	if err != nil {
		t.Fatal(err)
	}
	line, press := tree[0], tree[1]
	// 90 minutes at 40.00 an hour is 60.00, so 6000 cents; the unrated two hours add nothing.
	if press.OwnCostCents != 6000 || line.RolledCost != 6000 || line.OwnCostCents != 0 {
		t.Fatalf("press own cost = %d, line rolled = %d, line own = %d", press.OwnCostCents, line.RolledCost, line.OwnCostCents)
	}
}

// A space deleted in Hearth goes from EmberOPS even when work Hearth deleted is
// still marked deleted there and links it: EmberOPS will not delete a space
// something links, and the sync retried that forever. The deleted work goes
// first; work that is still live is never touched and keeps the space.
func TestADeletedSpaceGoesEvenWhenDeletedWorkStillLinksIt(t *testing.T) {
	e, s, _ := newPlant(t)
	ctx := e.as("svc-tok")
	gone, _ := s.CreateSpace(ctx, org, actor(ann), "Gone", "folder", nil)
	kept, _ := s.CreateSpace(ctx, org, actor(ann), "Kept", "folder", nil)
	if err := s.Spaces.Sync(ctx, org); err != nil {
		t.Fatal(err)
	}
	name := map[string]string{}
	for _, sp := range e.f.All("Hearth Space") {
		name[sp["space_name"].(string)] = sp["name"].(string)
	}
	e.f.Put("Task", map[string]any{"name": "TASK-D", "hearth_space": name["Gone"], "hearth_deleted_at": "2026-11-21 03:00:00"})
	e.f.Put("Hearth Document", map[string]any{"name": "HDOC-D", "hearth_space": name["Gone"], "deleted_at": "2026-11-21 03:00:00"})
	e.f.Put("Task", map[string]any{"name": "TASK-L", "hearth_space": name["Kept"]})

	if err := s.DeleteSpace(ctx, org, actor(ann), gone.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSpace(ctx, org, actor(ann), kept.ID); err != nil {
		t.Fatal(err)
	}
	left := map[string]bool{}
	for _, sp := range e.f.All("Hearth Space") {
		left[sp["space_name"].(string)] = true
	}
	if left["Gone"] {
		t.Error("a deleted space stayed in EmberOPS because deleted work still linked it")
	}
	if !left["Kept"] {
		t.Error("a space with live work linked was deleted out from under it")
	}
	if len(e.f.All("Task")) != 1 || e.f.All("Task")[0]["name"] != "TASK-L" || len(e.f.All("Hearth Document")) != 0 {
		t.Errorf("tasks = %v, documents = %v: want only the live task left", e.f.All("Task"), e.f.All("Hearth Document"))
	}
}

type moveOK struct{ *pgPlant }

func (moveOK) MoveDocument(context.Context, string, core.Subjects, string, *string) error { return nil }

type kindOf map[string]string

func (k kindOf) GetAsSystem(_ context.Context, _, id string) (*docs.Document, error) {
	return &docs.Document{ID: id, Kind: k[id]}, nil
}

// Taking a document out of a space, or filing it in another, moves its EmberOPS record's link too. Left behind, the record
// kept the old space in EmberOPS for good, and a space with a record linked can never be deleted there.
func TestMovingADocumentMovesItsEmberOPSLink(t *testing.T) {
	e, s, pg := newPlant(t)
	ctx := e.as("svc-tok")
	s.Store = moveOK{pg}
	const task, note = "dddddddd-0000-0000-0000-000000000001", "dddddddd-0000-0000-0000-000000000002"
	s.Docs = kindOf{task: docs.KindTask, note: docs.KindDoc}
	a, _ := s.CreateSpace(ctx, org, actor(ann), "A", "folder", nil)
	b, _ := s.CreateSpace(ctx, org, actor(ann), "B", "folder", nil)
	if err := s.Spaces.Sync(ctx, org); err != nil {
		t.Fatal(err)
	}
	name := map[string]string{}
	for _, sp := range e.f.All("Hearth Space") {
		name[sp["space_name"].(string)] = sp["name"].(string)
	}
	e.f.Put("Task", map[string]any{"name": "TASK-1", "hearth_id": task, "hearth_space": name["A"]})
	e.f.Put("Hearth Document", map[string]any{"name": "HDOC-1", "hearth_id": note, "hearth_space": name["A"]})

	if err := s.MoveDocument(ctx, org, actor(ann), task, &b.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.f.All("Task")[0]["hearth_space"]; got != name["B"] {
		t.Errorf("task's space in EmberOPS = %v, want %s", got, name["B"])
	}
	if err := s.MoveDocument(ctx, org, actor(ann), note, nil); err != nil {
		t.Fatal(err)
	}
	if got := e.f.All("Hearth Document")[0]["hearth_space"]; got != nil {
		t.Errorf("document taken out of its space still links %v in EmberOPS", got)
	}
	_ = a
}

// A space that would not go (live work still linked it) is tried again on the next copy, though nothing else changed. The copy
// used to stop at "nothing changed" and never came back for it, so a deleted space stayed in EmberOPS for good.
func TestADeletedSpaceThatWouldNotGoIsTriedAgain(t *testing.T) {
	e, s, _ := newPlant(t)
	ctx := e.as("ann-tok")
	sp, _ := s.CreateSpace(ctx, org, actor(ann), "Stuck", "folder", nil)
	if err := s.Spaces.Sync(ctx, org); err != nil {
		t.Fatal(err)
	}
	name := e.f.All("Hearth Space")[0]["name"].(string)
	e.f.Put("Task", map[string]any{"name": "TASK-LIVE", "hearth_space": name})
	if err := s.DeleteSpace(ctx, org, actor(ann), sp.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.f.All("Hearth Space")) != 1 {
		t.Fatalf("a space with live work linked was deleted: %d left", len(e.f.All("Hearth Space")))
	}
	// The work goes (the person moved it, or finished with it), and nothing about the spaces has changed.
	e.f.Put("Task", map[string]any{"name": "TASK-LIVE", "hearth_space": nil})
	if err := s.Spaces.Sync(ctx, org); err != nil {
		t.Fatal(err)
	}
	if n := len(e.f.All("Hearth Space")); n != 0 {
		t.Errorf("%d spaces left: the deleted space was never tried again", n)
	}
}

// Filing a task in a folder made a moment ago works. The folder's copy into EmberOPS was run as the person who made the folder,
// which EmberOPS refuses for staff, so the folder was not there when the task linked it and the create failed ("could not
// create") until the background copy caught up a few seconds later.
func TestATaskFiledRightAfterItsFolderWasMadeLands(t *testing.T) {
	e, s, _ := newPlant(t)
	ctx := e.as("ann-tok") // a person: EmberOPS lets only the service account write a Hearth Space
	folder, err := s.CreateSpace(ctx, org, actor(ann), "Fresh folder", "folder", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.f.All("Hearth Space")) != 1 {
		t.Fatalf("the folder was not copied into EmberOPS by the time CreateSpace returned: %d spaces", len(e.f.All("Hearth Space")))
	}
	ds := &stores.Docs{Store: newSpine(), Base: e.base, Dir: e.dir}
	d, err := ds.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindTask, Title: "first task", SpaceID: &folder.ID})
	if err != nil {
		t.Fatalf("a task filed right after its folder was made failed: %v", err)
	}
	doc, err := ds.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindDoc, Title: "first note", SpaceID: &folder.ID})
	if err != nil {
		t.Fatalf("a document filed right after its folder was made failed: %v", err)
	}
	if len(e.f.All("Task")) != 1 || len(e.f.All("Hearth Document")) != 1 || d.ID == "" || doc.ID == "" {
		t.Errorf("tasks %d, documents %d in EmberOPS", len(e.f.All("Task")), len(e.f.All("Hearth Document")))
	}
}

// A folder made, renamed or removed sends only that folder to EmberOPS. Each used to send every folder again (171 of them on
// live, about ten seconds in the request); the whole copy belongs to the scheduled job.
func TestACreateRenameOrDeleteSendsOnlyThatFolder(t *testing.T) {
	e, s, _ := newPlant(t)
	ctx := e.as("ann-tok")
	for _, n := range []string{"One", "Two", "Three", "Four", "Five"} {
		if _, err := s.CreateSpace(ctx, org, actor(ann), n, "folder", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Spaces.Sync(ctx, org); err != nil {
		t.Fatal(err)
	}
	have := func() map[string]bool {
		out := map[string]bool{}
		for _, sp := range e.f.All("Hearth Space") {
			out[sp["space_name"].(string)] = true
		}
		return out
	}
	if len(have()) != 5 {
		t.Fatalf("baseline: %v", have())
	}

	before := e.f.BulkItems()
	six, err := s.CreateSpace(ctx, org, actor(ann), "Six", "folder", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := e.f.BulkItems() - before; n != 1 {
		t.Errorf("a create sent %d spaces, want only its own", n)
	}
	if !have()["Six"] || len(have()) != 6 {
		t.Errorf("after the create: %v", have())
	}

	before = e.f.BulkItems()
	if err := s.RenameSpace(ctx, org, actor(ann), six.ID, "Sixth"); err != nil {
		t.Fatal(err)
	}
	if n := e.f.BulkItems() - before; n != 1 {
		t.Errorf("a rename sent %d spaces, want only its own", n)
	}
	if !have()["Sixth"] || have()["Six"] {
		t.Errorf("after the rename: %v", have())
	}

	before = e.f.BulkItems()
	if err := s.DeleteSpace(ctx, org, actor(ann), six.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.f.BulkItems() - before; n != 0 {
		t.Errorf("a delete sent %d spaces, want none", n)
	}
	if have()["Sixth"] || len(have()) != 5 {
		t.Errorf("after the delete: %v", have())
	}
}

// Once a folder's own copy has been accepted, the whole copy has nothing left to send for it: it sends only what changed since
// EmberOPS last accepted each space, and writes nothing when nothing did. Every write by a signed in person asks for a whole
// copy shortly after, so this is what keeps those from resending every folder each time.
func TestTheWholeCopyAfterAFolderCopySendsNothing(t *testing.T) {
	e, s, pg := newPlant(t)
	ctx := e.as("ann-tok")
	for _, n := range []string{"A", "B", "C"} {
		if _, err := s.CreateSpace(ctx, org, actor(ann), n, "folder", nil); err != nil {
			t.Fatal(err)
		}
	}
	before := e.f.BulkItems()
	if err := s.Spaces.Sync(ctx, org); err != nil {
		t.Fatal(err)
	}
	if n := e.f.BulkItems() - before; n != 0 {
		t.Errorf("a whole copy right after each folder's own copy sent %d spaces, want none", n)
	}
	// A real change to one space sends that space and no other.
	pg.spaces[1].Name = "B renamed elsewhere"
	before = e.f.BulkItems()
	if err := s.Spaces.Sync(ctx, org); err != nil {
		t.Fatal(err)
	}
	if n := e.f.BulkItems() - before; n != 1 {
		t.Errorf("one changed space sent %d, want 1", n)
	}
	// A space Hearth no longer has is removed, though no request said so.
	pg.spaces = pg.spaces[:2]
	if err := s.Spaces.Sync(ctx, org); err != nil {
		t.Fatal(err)
	}
	if n := len(e.f.All("Hearth Space")); n != 2 {
		t.Errorf("%d spaces in EmberOPS, want 2 after one was removed from Hearth", n)
	}
}

// Copies of one space and the whole copy never overlap: EmberOPS refuses both when they do ("Document has been modified after you
// have opened it"), which is what made a rename fail and a task filed right after a folder return 500 on live 2.22.9. Run
// together under -race they leave EmberOPS holding exactly Hearth's spaces.
func TestCopiesOfOneSpaceAndTheWholeCopyDoNotOverlap(t *testing.T) {
	e, s, _ := newPlant(t)
	ctx := e.as("ann-tok")
	var made []*plant.Space
	for _, n := range []string{"P", "Q", "R", "S"} {
		sp, err := s.CreateSpace(ctx, org, actor(ann), n, "folder", nil)
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, sp)
	}
	done := make(chan error, 40)
	for i := 0; i < 10; i++ {
		go func() { done <- s.Spaces.Sync(ctx, org) }()
		for _, sp := range made {
			sp := sp
			go func() { done <- s.Spaces.SyncOne(ctx, org, sp.ID) }()
		}
	}
	for i := 0; i < 50; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.f.All("Hearth Space")); n != 4 {
		t.Errorf("%d spaces in EmberOPS, want 4", n)
	}
}
