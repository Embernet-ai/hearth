package plant_test

// The plant tree, against a real Postgres.
//
// Two things are being proved here and they pull in opposite directions. The
// rollup has to reach every ancestor, which means the recursive walk has to go
// all the way up. And a cycle has to be refused, because a walk that goes all
// the way up around a loop never comes back at all.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

type fx struct {
	svc   *plant.Service
	docs  *docs.Service
	time  *timelog.Service
	az    *authz.Service
	pools *pg.Pools
	org   string
	alice string
	bob   string
}

func setup(t *testing.T) *fx {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pools.Close)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := pg.Migrate(ctx, pools, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	az := authz.New(pools.Web)
	f := &fx{
		svc: plant.New(pools.Web, az), docs: docs.New(pools.Web, az),
		time: timelog.New(pools.Web, az), az: az, pools: pools,
		org: uuid.NewString(), alice: uuid.NewString(), bob: uuid.NewString(),
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Plant Test')`, f.org, "pt-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Alice')`, f.alice, f.org)
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Bob')`, f.bob, f.org)
	// Alice is the admin who builds the tree: reshaping it is admin-or-manager now.
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$2)`, f.org, f.alice)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM asset_downtime WHERE org_id=$1`,
			`DELETE FROM asset_fields WHERE org_id=$1`,
			`DELETE FROM time_entries WHERE org_id=$1`,
			`DELETE FROM capacity WHERE org_id=$1`,
			`DELETE FROM entity_links WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM property_values WHERE entity_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM property_defs WHERE org_id=$1`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM spaces WHERE org_id=$1`,
			`DELETE FROM capability_grants WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

func (f *fx) subj(t *testing.T, p string) core.Subjects {
	t.Helper()
	s, err := f.az.SubjectsFor(context.Background(), f.org, p)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	return s
}

func (f *fx) asset(t *testing.T, title string, parent *string) *docs.Document {
	t.Helper()
	ctx := context.Background()
	d, err := f.docs.Create(ctx, f.org, f.subj(t, f.alice), docs.CreateParams{
		Kind: docs.KindAsset, Title: title,
	})
	if err != nil {
		t.Fatalf("create asset %s: %v", title, err)
	}
	if err := f.svc.SetFields(ctx, f.org, f.subj(t, f.alice), d.ID,
		plant.Fields{ParentAssetID: parent}); err != nil {
		t.Fatalf("place asset %s: %v", title, err)
	}
	return d
}

// job creates a work order concerning an asset, which is how the rollup finds
// it: through entity_links, the same relation everything else uses.
func (f *fx) job(t *testing.T, title, status, assetID string, minutes int) *docs.Document {
	t.Helper()
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: title,
		Task: &docs.TaskFields{Status: status},
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := f.docs.Link(ctx, f.org, alice, d.Ref(),
		core.Ref{Type: core.EntityDocument, ID: assetID}, "concerns"); err != nil {
		t.Fatalf("link job: %v", err)
	}
	if minutes > 0 {
		if _, err := f.time.Log(ctx, f.org, alice, d.ID, minutes, time.Now(), ""); err != nil {
			t.Fatalf("log time: %v", err)
		}
	}
	return d
}

func (f *fx) find(t *testing.T, tree []*plant.Asset, id string) *plant.Asset {
	t.Helper()
	for _, a := range tree {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("asset %s is not in the tree", id)
	return nil
}

// -----------------------------------------------------------------------------

// The criterion the whole phase turns on: an open work order counts on every
// ancestor of its asset.
func TestAnOpenWorkOrderCountsOnEveryAncestor(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	site := f.asset(t, "North site", nil)
	line := f.asset(t, "Line 3", &site.ID)
	machine := f.asset(t, "Conveyor", &line.ID)
	gearbox := f.asset(t, "Gearbox", &machine.ID)
	bearing := f.asset(t, "Bearing", &gearbox.ID)

	// One open job at the very bottom, and ninety minutes against it.
	f.job(t, "Bearing is rumbling", docs.StatusTodo, bearing.ID, 90)
	// One completed job in the middle, which must count for time but not as
	// open work.
	f.job(t, "Belt changed", docs.StatusDone, machine.ID, 45)

	tree, err := f.svc.Tree(ctx, f.org, alice)
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if len(tree) != 5 {
		t.Fatalf("the tree has %d assets, want 5", len(tree))
	}

	for _, tc := range []struct {
		name       string
		id         string
		wantOpen   int
		wantOwn    int
		wantMins   int
		wantDepth  int
		wantChilds int
	}{
		{"site", site.ID, 1, 0, 135, 0, 1},
		{"line", line.ID, 1, 0, 135, 1, 1},
		{"machine", machine.ID, 1, 0, 135, 2, 1},
		{"gearbox", gearbox.ID, 1, 0, 90, 3, 1},
		{"bearing", bearing.ID, 1, 1, 90, 4, 0},
	} {
		a := f.find(t, tree, tc.id)
		if a.RolledOpen != tc.wantOpen {
			t.Errorf("%s: %d open rolled up, want %d.\n"+
				"A failure at the bottom that is not visible at the top makes "+
				"the tree decoration.", tc.name, a.RolledOpen, tc.wantOpen)
		}
		if a.OwnOpen != tc.wantOwn {
			t.Errorf("%s: %d open on the asset itself, want %d", tc.name, a.OwnOpen, tc.wantOwn)
		}
		if a.RolledMinutes != tc.wantMins {
			t.Errorf("%s: %d minutes rolled up, want %d.\n"+
				"A completed job still counts towards what the machine has cost.",
				tc.name, a.RolledMinutes, tc.wantMins)
		}
		if a.Depth != tc.wantDepth {
			t.Errorf("%s: depth %d, want %d", tc.name, a.Depth, tc.wantDepth)
		}
		if a.Children != tc.wantChilds {
			t.Errorf("%s: %d children, want %d", tc.name, a.Children, tc.wantChilds)
		}
	}

	// And the path, which is what tells two machines called Gearbox apart.
	if got := f.find(t, tree, bearing.ID).Path; got != "North site / Line 3 / Conveyor / Gearbox / Bearing" {
		t.Errorf("path is %q", got)
	}
}

// A cycle has to be refused. The failure it prevents is not a wrong number, it
// is a query that never returns and a connection that never comes back.
func TestACycleIsRejectedRatherThanHangingTheQuery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	a := f.asset(t, "A", nil)
	b := f.asset(t, "B", &a.ID)
	c := f.asset(t, "C", &b.ID)

	// The one step case: A inside itself.
	if err := f.svc.SetFields(ctx, f.org, alice, a.ID,
		plant.Fields{ParentAssetID: &a.ID}); !errors.Is(err, plant.ErrCycle) {
		t.Errorf("putting A inside itself returned %v, want ErrCycle", err)
	}
	// The multi-step case: A inside C, which is already below A.
	if err := f.svc.SetFields(ctx, f.org, alice, a.ID,
		plant.Fields{ParentAssetID: &c.ID}); !errors.Is(err, plant.ErrCycle) {
		t.Errorf("putting A inside its own grandchild returned %v, want ErrCycle", err)
	}
	// And the legal move is still allowed, or the guard is just a wall.
	if err := f.svc.SetFields(ctx, f.org, alice, c.ID,
		plant.Fields{ParentAssetID: &a.ID}); err != nil {
		t.Errorf("moving C up to A was refused: %v", err)
	}
}

// The root of a tree has no asset_fields row, because the row is written the
// first time an asset is placed and nobody places a root.
//
// That is not a corner case, it is the ordinary shape: create four assets, hang
// three of them off each other, and the top one is a bare document that other
// rows point at. A cycle check that only matches rows never sees it, and the
// loop a person is most likely to make by hand goes straight through. The
// acceptance run found this; the fixture above hid it by placing everything.
func TestACycleThroughAnUnplacedRootIsStillRefused(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	// Deliberately not through f.asset: the root is created and left alone,
	// exactly as the create form leaves it.
	site, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindAsset, Title: "North site",
	})
	if err != nil {
		t.Fatalf("create site: %v", err)
	}
	var rows int
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT count(*) FROM asset_fields WHERE document_id=$1`, site.ID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Fatalf("the root has %d asset_fields rows; this test is no longer testing "+
			"what it says it is", rows)
	}

	line := f.asset(t, "Line 3", &site.ID)
	bearing := f.asset(t, "Bearing", &line.ID)

	if err := f.svc.SetFields(ctx, f.org, alice, site.ID,
		plant.Fields{ParentAssetID: &bearing.ID}); !errors.Is(err, plant.ErrCycle) {
		t.Errorf("putting an unplaced root inside its own grandchild returned %v, "+
			"want ErrCycle.\n"+
			"A root is a bare document that other rows point at, so a check that "+
			"only matches rows never sees it.", err)
	}
	if err := f.svc.SetFields(ctx, f.org, alice, site.ID,
		plant.Fields{ParentAssetID: &line.ID}); !errors.Is(err, plant.ErrCycle) {
		t.Errorf("putting an unplaced root inside its own child returned %v, want ErrCycle", err)
	}
}

// The write guard can be bypassed, by a migration or by somebody with psql. The
// read side has to survive that, and survive means return, not return the right
// answer.
func TestACycleWrittenAroundTheServiceDoesNotHangTheTree(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	a := f.asset(t, "A", nil)
	b := f.asset(t, "B", &a.ID)
	f.job(t, "Open job on B", docs.StatusTodo, b.ID, 30)

	// Straight into the table, past every check the service makes. This is
	// what a bad migration looks like.
	if _, err := f.pools.Web.Exec(ctx,
		`UPDATE asset_fields SET parent_asset_id=$2 WHERE document_id=$1`, a.ID, b.ID); err != nil {
		t.Fatalf("write the cycle: %v", err)
	}

	done := make(chan []*plant.Asset, 1)
	fail := make(chan error, 1)
	go func() {
		tree, err := f.svc.Tree(ctx, f.org, alice)
		if err != nil {
			fail <- err
			return
		}
		done <- tree
	}()

	var tree []*plant.Asset
	select {
	case tree = <-done:
	case err := <-fail:
		t.Fatalf("the tree errored on a cycle rather than surviving it: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("the tree query did not return with a cycle in the table.\n" +
			"CYCLE on the recursive walk is what stops a bad migration from " +
			"taking every connection in the pool.")
	}

	// Returning is not enough. A cycle has no root, so a walk that only starts
	// from roots drops the loop and everything under it, and the machines
	// silently disappear from the page. That is worse than showing them in the
	// wrong place.
	if len(tree) != 2 {
		t.Fatalf("the tree has %d assets with a cycle in it, want both.\n"+
			"Machines that vanish are worse than machines drawn at the wrong "+
			"depth: nobody goes looking for what they cannot see is missing.", len(tree))
	}
	// And the work on them is still counted, exactly once.
	total := 0
	for _, x := range tree {
		total += x.OwnOpen
	}
	if total != 1 {
		t.Errorf("the one open job is counted %d times across the cycle, want once", total)
	}

	// The availability walk goes the other way, down rather than up, and needs
	// the same protection.
	other := make(chan error, 1)
	go func() {
		_, err := f.svc.AvailabilityFor(ctx, f.org, alice, a.ID,
			time.Now().AddDate(0, 0, -30), time.Now())
		other <- err
	}()
	select {
	case err := <-other:
		if err != nil {
			t.Fatalf("availability errored on a cycle: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the availability walk did not return with a cycle in the table")
	}

	// The ancestor walk, which the breadcrumb uses, is a third one.
	go func() {
		_, err := f.svc.Ancestors(ctx, f.org, alice, b.ID)
		other <- err
	}()
	select {
	case err := <-other:
		if err != nil {
			t.Fatalf("the ancestor walk errored on a cycle: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the ancestor walk did not return with a cycle in the table")
	}
}

// Scanning a label lands on the machine, and only for somebody allowed to see
// it. A code is a convenience, not a capability.
func TestScanningAnAssetCodeLandsOnThatAsset(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)

	a := f.asset(t, "Conveyor 3", nil)
	if err := f.svc.SetFields(ctx, f.org, alice, a.ID, plant.Fields{Code: "cv-003"}); err != nil {
		t.Fatalf("set code: %v", err)
	}

	// Case-insensitive, because the code is read off a label by a person as
	// often as by a scanner.
	for _, typed := range []string{"cv-003", "CV-003", "Cv-003"} {
		got, err := f.svc.ByCode(ctx, f.org, alice, typed)
		if err != nil {
			t.Fatalf("scan %q: %v", typed, err)
		}
		if got != a.ID {
			t.Errorf("scanning %q landed on %s, want %s", typed, got, a.ID)
		}
	}

	// The code is stored uppercase, so what is printed matches what is read.
	fields, err := f.svc.FieldsFor(ctx, f.org, a.ID)
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	if fields.Code != "CV-003" {
		t.Errorf("stored code is %q, want CV-003", fields.Code)
	}

	// Two machines cannot share a code. A scanner that lands on the wrong
	// machine is worse than one that lands nowhere.
	other := f.asset(t, "Conveyor 4", nil)
	if err := f.svc.SetFields(ctx, f.org, alice, other.ID,
		plant.Fields{Code: "CV-003"}); err == nil {
		t.Error("two assets were given the same code")
	}

	// And a code somebody has no grant for is indistinguishable from one that
	// does not exist.
	if _, err := f.svc.ByCode(ctx, f.org, bob, "CV-003"); !errors.Is(err, plant.ErrNotFound) {
		t.Errorf("Bob scanning a machine he cannot see got %v, want not-found.\n"+
			"Anything else turns a label into a directory of the estate.", err)
	}
	if _, err := f.svc.ByCode(ctx, f.org, alice, "NOPE"); !errors.Is(err, plant.ErrNotFound) {
		t.Errorf("scanning a code that does not exist got %v, want not-found", err)
	}
}

// A scrapped machine must not keep its tag forever.
//
// Deleting a document is a soft delete, so the old asset's row keeps its claim
// on the code. Putting the same tag on the replacement is not an edge case, it
// is what happens every time a machine is swapped: the label on the wall says
// CV-003 and the new conveyor is CV-003.
func TestAReplacementCanCarryTheScrappedMachinesCode(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	old := f.asset(t, "Conveyor 3", nil)
	if err := f.svc.SetFields(ctx, f.org, alice, old.ID, plant.Fields{Code: "CV-003"}); err != nil {
		t.Fatalf("set code: %v", err)
	}
	if err := f.docs.Delete(ctx, f.org, alice, old.ID); err != nil {
		t.Fatalf("scrap it: %v", err)
	}

	replacement := f.asset(t, "Conveyor 3 (new)", nil)
	if err := f.svc.SetFields(ctx, f.org, alice, replacement.ID,
		plant.Fields{Code: "CV-003"}); err != nil {
		t.Fatalf("the replacement could not take the scrapped machine's tag: %v", err)
	}
	got, err := f.svc.ByCode(ctx, f.org, alice, "CV-003")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got != replacement.ID {
		t.Errorf("scanning CV-003 landed on %s, want the replacement", got)
	}

	// And two LIVE assets still cannot share one, which is the guarantee the
	// unique index is there for.
	third := f.asset(t, "Conveyor 4", nil)
	if err := f.svc.SetFields(ctx, f.org, alice, third.ID,
		plant.Fields{Code: "CV-003"}); !errors.Is(err, plant.ErrInvalid) {
		t.Errorf("two live assets took the same code: %v", err)
	}
}

// A code goes in a URL that somebody types off a label. Anything needing
// escaping there will be entered wrong at least once.
func TestACodeCannotContainAnythingAURLWouldEscape(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	a := f.asset(t, "Press", nil)

	for _, bad := range []string{"CV 003", "CV/003", "CV?003", "CV#003", "CV&003",
		strings.Repeat("X", 33)} {
		if err := f.svc.SetFields(ctx, f.org, alice, a.ID,
			plant.Fields{Code: bad}); !errors.Is(err, plant.ErrInvalid) {
			t.Errorf("code %q was accepted, want ErrInvalid", bad)
		}
	}
}

// Downtime rolls up like everything else, and planned time is kept separate.
// Adding the annual shutdown to the breakdown total makes the best-maintained
// machine on site look like the worst.
func TestDowntimeRollsUpAndPlannedIsCountedSeparately(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	line := f.asset(t, "Line 3", nil)
	machine := f.asset(t, "Conveyor", &line.ID)

	from := time.Now().AddDate(0, 0, -10)
	// Two hours of breakdown on the machine, and eight hours of planned
	// shutdown on the line.
	if _, err := f.svc.LogDowntime(ctx, f.org, alice, machine.ID,
		from, from.Add(2*time.Hour), false, nil, "bearing"); err != nil {
		t.Fatalf("log breakdown: %v", err)
	}
	if _, err := f.svc.LogDowntime(ctx, f.org, alice, line.ID,
		from, from.Add(8*time.Hour), true, nil, "annual shutdown"); err != nil {
		t.Fatalf("log shutdown: %v", err)
	}

	tree, err := f.svc.Tree(ctx, f.org, alice)
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	// The rollup column counts unplanned only, which is the number a
	// reliability argument is made of.
	if got := f.find(t, tree, line.ID).RolledDowntime; got != 120 {
		t.Errorf("the line reports %d minutes down, want 120.\n"+
			"Only the breakdown counts; the planned shutdown is not a failure.", got)
	}
	if got := f.find(t, tree, machine.ID).OwnDowntime; got != 120 {
		t.Errorf("the machine reports %d minutes down, want 120", got)
	}

	av, err := f.svc.AvailabilityFor(ctx, f.org, alice, line.ID,
		time.Now().AddDate(0, 0, -30), time.Now())
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if av.Unplanned != 120 {
		t.Errorf("unplanned is %d minutes, want 120", av.Unplanned)
	}
	if av.Planned != 480 {
		t.Errorf("planned is %d minutes, want 480", av.Planned)
	}
	if av.Outages != 1 {
		t.Errorf("%d unplanned stops, want 1", av.Outages)
	}
	// Thirty days is 43200 minutes, so two hours out is a rounding error and
	// the percentage should read 99.
	if got := av.Percent(); got != 99 {
		t.Errorf("availability reads %d%%, want 99", got)
	}
}

// A machine cannot be down twice at once. Two overlapping records is one outage
// counted twice, and the availability number is what an argument is built on.
func TestAMachineCannotBeDownTwiceAtOnce(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	a := f.asset(t, "Press", nil)

	if _, err := f.svc.GoDown(ctx, f.org, alice, a.ID, time.Now().Add(-time.Hour),
		false, nil, "stopped"); err != nil {
		t.Fatalf("go down: %v", err)
	}
	if _, err := f.svc.GoDown(ctx, f.org, alice, a.ID, time.Now(),
		false, nil, "stopped again"); !errors.Is(err, plant.ErrAlreadyDown) {
		t.Errorf("a second outage was accepted: %v", err)
	}

	d, err := f.svc.ComeUp(ctx, f.org, alice, a.ID, time.Now())
	if err != nil {
		t.Fatalf("come up: %v", err)
	}
	if d.Minutes == nil || *d.Minutes < 59 || *d.Minutes > 61 {
		t.Errorf("the outage recorded %v minutes, want about 60", d.Minutes)
	}
	if _, err := f.svc.ComeUp(ctx, f.org, alice, a.ID, time.Now()); !errors.Is(err, plant.ErrNotDown) {
		t.Errorf("bringing up a machine that is already up returned %v, want ErrNotDown", err)
	}
	// And now it can go down again, because the first one is closed.
	if _, err := f.svc.GoDown(ctx, f.org, alice, a.ID, time.Now(), false, nil, ""); err != nil {
		t.Errorf("the machine could not go down again after coming up: %v", err)
	}
}

// An outage that started before the window contributes only the part inside it.
func TestAnOutageIsClippedToTheReportingWindow(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	a := f.asset(t, "Chiller", nil)

	// Ten hours, of which the last four fall inside the window.
	end := time.Now().Add(-1 * time.Hour)
	start := end.Add(-10 * time.Hour)
	if _, err := f.svc.LogDowntime(ctx, f.org, alice, a.ID, start, end, false, nil, ""); err != nil {
		t.Fatalf("log: %v", err)
	}

	av, err := f.svc.AvailabilityFor(ctx, f.org, alice, a.ID, end.Add(-4*time.Hour), time.Now())
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if av.Unplanned < 235 || av.Unplanned > 245 {
		t.Errorf("a ten hour outage with four hours inside the window counted "+
			"%d minutes, want about 240.\n"+
			"Counting the whole outage would report more downtime than the "+
			"window contains.", av.Unplanned)
	}
}

// Visibility. The tree must not become a second answer to who can see what.
func TestTheTreeShowsNothingTheCallerWasNotGranted(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)

	line := f.asset(t, "Line 3", nil)
	machine := f.asset(t, "Conveyor", &line.ID)
	f.job(t, "Open job", docs.StatusTodo, machine.ID, 60)

	if tree, err := f.svc.Tree(ctx, f.org, bob); err != nil {
		t.Fatalf("tree as bob: %v", err)
	} else if len(tree) != 0 {
		t.Fatalf("Bob was granted nothing but sees %d assets", len(tree))
	}

	// Grant Bob the machine but not the line above it. He must see the
	// machine, at the top of his tree, rather than losing it because its
	// parent is private.
	if err := f.az.Grant(ctx, f.org, core.Ref{Type: core.EntityDocument, ID: machine.ID},
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessView, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	tree, err := f.svc.Tree(ctx, f.org, bob)
	if err != nil {
		t.Fatalf("tree as bob: %v", err)
	}
	if len(tree) != 1 {
		t.Fatalf("Bob sees %d assets, want 1", len(tree))
	}
	if tree[0].ID != machine.ID {
		t.Errorf("Bob sees %s, want the machine he was granted", tree[0].Title)
	}
	if tree[0].Depth != 0 || tree[0].ParentID != nil {
		t.Errorf("the machine is at depth %d with parent %v.\n"+
			"A node whose parent is invisible has to be a root, or work "+
			"somebody was explicitly granted disappears behind a machine they "+
			"were not.", tree[0].Depth, tree[0].ParentID)
	}
	// The job on it still counts, because he can see the job's asset.
	if tree[0].RolledOpen != 1 {
		t.Errorf("Bob sees %d open jobs on his machine, want 1", tree[0].RolledOpen)
	}
	// And Alice still sees the whole thing.
	full, err := f.svc.Tree(ctx, f.org, alice)
	if err != nil {
		t.Fatalf("tree as alice: %v", err)
	}
	if len(full) != 2 {
		t.Errorf("Alice sees %d assets, want 2", len(full))
	}
	if f.find(t, full, line.ID).RolledOpen != 1 {
		t.Error("the line does not count the job on the machine below it")
	}
}

// -----------------------------------------------------------------------------
// Spaces
// -----------------------------------------------------------------------------

func TestASpaceTreeHoldsDocumentsAndRefusesACycle(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	site, err := f.svc.CreateSpace(ctx, f.org, alice, "North building", plant.KindArea, nil)
	if err != nil {
		t.Fatalf("create space: %v", err)
	}
	line, err := f.svc.CreateSpace(ctx, f.org, alice, "Line 3", plant.KindFolder, &site.ID)
	if err != nil {
		t.Fatalf("create child space: %v", err)
	}

	// A cycle, the same two ways as the asset tree.
	if err := f.svc.MoveSpace(ctx, f.org, alice, site.ID, &site.ID); !errors.Is(err, plant.ErrCycle) {
		t.Errorf("a space inside itself returned %v, want ErrCycle", err)
	}
	if err := f.svc.MoveSpace(ctx, f.org, alice, site.ID, &line.ID); !errors.Is(err, plant.ErrCycle) {
		t.Errorf("a space inside its own child returned %v, want ErrCycle", err)
	}

	// Filing a document, which is the write that spends a column the substrate
	// has carried unused since the beginning.
	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Grease the drive",
	})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	if err := f.svc.MoveDocument(ctx, f.org, alice, d.ID, &line.ID); err != nil {
		t.Fatalf("file document: %v", err)
	}

	tree, err := f.svc.SpaceTree(ctx, f.org, alice)
	if err != nil {
		t.Fatalf("space tree: %v", err)
	}
	if len(tree) != 2 {
		t.Fatalf("the space tree has %d nodes, want 2", len(tree))
	}
	if tree[0].Name != "North building" || tree[0].Depth != 0 {
		t.Errorf("the root is %q at depth %d", tree[0].Name, tree[0].Depth)
	}
	if tree[1].Depth != 1 || tree[1].Path != "North building / Line 3" {
		t.Errorf("the child is at depth %d with path %q", tree[1].Depth, tree[1].Path)
	}
	if tree[1].Documents != 1 {
		t.Errorf("the child holds %d documents, want 1", tree[1].Documents)
	}

	// A space with work in it cannot be deleted. Deleting one would either
	// orphan the documents or take them with it, and both are surprises.
	if err := f.svc.DeleteSpace(ctx, f.org, alice, line.ID); !errors.Is(err, plant.ErrInvalid) {
		t.Errorf("deleting a space with a document in it returned %v, want ErrInvalid", err)
	}
	if err := f.svc.DeleteSpace(ctx, f.org, alice, site.ID); !errors.Is(err, plant.ErrInvalid) {
		t.Errorf("deleting a space with a child returned %v, want ErrInvalid", err)
	}
	if err := f.svc.MoveDocument(ctx, f.org, alice, d.ID, nil); err != nil {
		t.Fatalf("unfile: %v", err)
	}
	if err := f.svc.DeleteSpace(ctx, f.org, alice, line.ID); err != nil {
		t.Errorf("an empty space could not be deleted: %v", err)
	}
}

func TestASpaceIsInvisibleWithoutAGrant(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)

	sp, err := f.svc.CreateSpace(ctx, f.org, alice, "Private area", plant.KindArea, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tree, err := f.svc.SpaceTree(ctx, f.org, bob); err != nil {
		t.Fatalf("tree: %v", err)
	} else if len(tree) != 0 {
		t.Errorf("Bob sees %d spaces he was not granted", len(tree))
	}
	if _, err := f.svc.GetSpace(ctx, f.org, bob, sp.ID); !errors.Is(err, plant.ErrNotFound) {
		t.Errorf("GetSpace as Bob returned %v, want not-found", err)
	}
	// And he cannot file work into it.
	d, err := f.docs.Create(ctx, f.org, bob, docs.CreateParams{
		Kind: docs.KindTask, Title: "Bob's task",
	})
	if err != nil {
		t.Fatalf("create as bob: %v", err)
	}
	if err := f.svc.MoveDocument(ctx, f.org, bob, d.ID, &sp.ID); err == nil {
		t.Error("Bob filed work into a space he cannot see")
	}
}

// A child space inherits the grants of its parent. A tree where every node has
// to be shared separately is a tree nobody uses twice.
func TestAChildSpaceInheritsWhoCanSeeItsParent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)

	parent, err := f.svc.CreateSpace(ctx, f.org, alice, "Shared", plant.KindArea, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.az.Grant(ctx, f.org, core.Ref{Type: core.EntitySpace, ID: parent.ID},
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessEdit, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	child, err := f.svc.CreateSpace(ctx, f.org, alice, "Inside", plant.KindFolder, &parent.ID)
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if _, err := f.svc.GetSpace(ctx, f.org, bob, child.ID); err != nil {
		t.Errorf("Bob can edit the parent but cannot see the child: %v", err)
	}
}

// A department member gets the department -- sees it, opens it, files work into
// it -- and sees only their own work there. Membership is never a grant row,
// because a space's grants are copied onto everything filed in it; this is the
// test that says a member cannot reach a colleague's task. Every assertion that
// a member CAN do something sits beside one that they cannot.
func TestADepartmentMemberSeesTheDepartmentButOnlyTheirOwnWork(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, _ := f.az.SubjectsFor(ctx, f.org, f.alice)
	bob, _ := f.az.SubjectsFor(ctx, f.org, f.bob)

	// Alice builds the department and holds a full grant on it, as its staff do.
	dept, err := f.svc.CreateSpace(ctx, f.org, alice, "White Team", plant.KindFolder, nil)
	if err != nil {
		t.Fatal(err)
	}
	line, err := f.svc.CreateSpace(ctx, f.org, alice, "Line 3", plant.KindFolder, &dept.ID)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindTask,
		Title: "Alice's job", SpaceID: &dept.ID, Task: &docs.TaskFields{Status: docs.StatusTodo}})
	if err != nil {
		t.Fatal(err)
	}

	visible := func(who core.Subjects, id string) bool {
		t.Helper()
		tree, err := f.svc.SpaceTree(ctx, f.org, who)
		if err != nil {
			t.Fatal(err)
		}
		for _, sp := range tree {
			if sp.ID == id {
				return true
			}
		}
		return false
	}

	// Before joining, Bob has nothing.
	if visible(bob, dept.ID) {
		t.Fatal("Bob saw White Team before he was a member")
	}
	if _, err := f.svc.GetSpace(ctx, f.org, bob, dept.ID); err == nil {
		t.Fatal("Bob opened White Team before he was a member")
	}

	// Bob joins. No grant is written.
	if _, err := f.pools.Web.Exec(ctx, `INSERT INTO principal_departments (org_id, principal_id, space_id)
		VALUES ($1,$2,$3)`, f.org, f.bob, dept.ID); err != nil {
		t.Fatal(err)
	}
	bob, _ = f.az.SubjectsFor(ctx, f.org, f.bob)

	// He sees the department and what is under it, and can open both.
	if !visible(bob, dept.ID) || !visible(bob, line.ID) {
		t.Fatal("a member should see the department and its sub-spaces")
	}
	if _, err := f.svc.GetSpace(ctx, f.org, bob, line.ID); err != nil {
		t.Fatalf("a member could not open a sub-space: %v", err)
	}
	kids, err := f.svc.SpaceChildren(ctx, f.org, bob, dept.ID)
	if err != nil || len(kids) != 1 {
		t.Fatalf("a member should see the department's sub-spaces: %v %d", err, len(kids))
	}

	// But not Alice's job filed there.
	if _, err := f.docs.Get(ctx, f.org, bob, theirs.ID); err == nil {
		t.Fatal("a member opened a colleague's task in the department")
	}
	rows, err := f.docs.Search(ctx, f.org, bob, docs.Query{SpaceID: &dept.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == theirs.ID {
			t.Fatal("a colleague's task showed in the member's department list")
		}
	}

	// He can file his own work there, sees it, and the department's staff see
	// it too -- the grants flow onto what he files, never back to him.
	mine, err := f.docs.Create(ctx, f.org, bob, docs.CreateParams{Kind: docs.KindTask,
		Title: "Bob's job", SpaceID: &line.ID, Task: &docs.TaskFields{Status: docs.StatusTodo}})
	if err != nil {
		t.Fatalf("a member could not file work into the department: %v", err)
	}
	if _, err := f.docs.Get(ctx, f.org, bob, mine.ID); err != nil {
		t.Fatalf("a member could not open their own task: %v", err)
	}
	if _, err := f.docs.Get(ctx, f.org, alice, mine.ID); err != nil {
		t.Fatalf("the department's staff could not see work a member filed: %v", err)
	}

	// Membership is view, not edit: he cannot rename the department.
	if err := f.svc.RenameSpace(ctx, f.org, bob, dept.ID, "Bob's Team"); err == nil {
		t.Fatal("a member renamed the department")
	}

	// Leaving takes it away again.
	if _, err := f.pools.Web.Exec(ctx, `DELETE FROM principal_departments WHERE principal_id=$1`, f.bob); err != nil {
		t.Fatal(err)
	}
	if visible(bob, dept.ID) {
		t.Fatal("Bob still saw White Team after leaving it")
	}
}

// Reshaping the tree is an admin's or a manager's job. A member who works in a
// space cannot rearrange it; a manager may within the department they manage; a
// top-level department and deletion are an admin's alone. A playground lets its
// own user do all of it, since building the structure is a lesson.
func TestFoldersAreForManagersAndAdmins(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice) // super admin (fixture)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	carol := uuid.NewString()
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Carol')`, carol, f.org)

	// A plain user cannot create a department; the admin can.
	if _, err := f.svc.CreateSpace(ctx, f.org, f.subj(t, f.bob), "Sales", plant.KindFolder, nil); !errors.Is(err, plant.ErrDenied) {
		t.Fatalf("a non-admin created a top-level department: %v", err)
	}
	dept, err := f.svc.CreateSpace(ctx, f.org, alice, "Operations", plant.KindFolder, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1,$2,$3)`, f.org, dept.ID, carol)

	// The manager may add and rename a folder inside their department.
	folder, err := f.svc.CreateSpace(ctx, f.org, f.subj(t, carol), "Codesys", plant.KindFolder, &dept.ID)
	if err != nil {
		t.Fatalf("the manager could not add a folder: %v", err)
	}
	if err := f.svc.RenameSpace(ctx, f.org, f.subj(t, carol), folder.ID, "CODESYS"); err != nil {
		t.Fatalf("the manager could not rename their folder: %v", err)
	}
	// A plain member of the org cannot add or rename a folder there.
	if _, err := f.svc.CreateSpace(ctx, f.org, f.subj(t, f.bob), "Sneak", plant.KindFolder, &dept.ID); !errors.Is(err, plant.ErrDenied) {
		t.Fatalf("a non-manager added a folder: %v", err)
	}
	if err := f.svc.RenameSpace(ctx, f.org, f.subj(t, f.bob), folder.ID, "Nope"); !errors.Is(err, plant.ErrDenied) {
		t.Fatalf("a non-manager renamed a folder: %v", err)
	}
	// Deletion is an admin's, not a manager's.
	if err := f.svc.DeleteSpace(ctx, f.org, f.subj(t, carol), folder.ID); !errors.Is(err, plant.ErrDenied) {
		t.Fatalf("a manager deleted a space: %v", err)
	}
	if err := f.svc.DeleteSpace(ctx, f.org, alice, folder.ID); err != nil {
		t.Fatalf("the admin could not delete: %v", err)
	}

	// A playground: its own user, who is not a super admin, may do all of it.
	pgOrg, pgUser := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO orgs (id, slug, name, playground_of) VALUES ($1,$2,'Sandbox',$3)`, pgOrg, "pg-"+pgOrg[:8], f.alice)
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','You')`, pgUser, pgOrg)
	t.Cleanup(func() {
		c := context.Background()
		for _, q := range []string{`DELETE FROM spaces WHERE org_id=$1`, `DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`} {
			_, _ = f.pools.Web.Exec(c, q, pgOrg)
		}
	})
	if _, err := f.svc.CreateSpace(ctx, pgOrg, f.subj2(t, pgOrg, pgUser), "Lesson folder", plant.KindFolder, nil); err != nil {
		t.Fatalf("the playground user could not build its tree: %v", err)
	}
}

func (f *fx) subj2(t *testing.T, orgID, p string) core.Subjects {
	t.Helper()
	s, err := f.az.SubjectsFor(context.Background(), orgID, p)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	return s
}

// Reordering sets the shared order of a parent's children, for admins at the
// top level and managers within their department. A foreign id is refused so
// one group cannot renumber another.
func TestReorderingSetsTheSharedOrder(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice) // super admin
	names := func() []string {
		t.Helper()
		tree, err := f.svc.SpaceTree(ctx, f.org, alice)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, sp := range tree {
			if sp.Depth == 0 {
				out = append(out, sp.Name)
			}
		}
		return out
	}
	mk := func(n string) string {
		t.Helper()
		sp, err := f.svc.CreateSpace(ctx, f.org, alice, n, plant.KindFolder, nil)
		if err != nil {
			t.Fatal(err)
		}
		return sp.ID
	}
	a, b, c := mk("Alpha"), mk("Bravo"), mk("Charlie")
	// Created alphabetically; reorder to Charlie, Alpha, Bravo.
	if err := f.svc.ReorderSpaces(ctx, f.org, alice, "", []string{c, a, b}); err != nil {
		t.Fatalf("admin reorder: %v", err)
	}
	if got := names(); len(got) < 3 || got[0] != "Charlie" || got[1] != "Alpha" || got[2] != "Bravo" {
		t.Fatalf("order after reorder: %v", got)
	}

	// A plain user cannot reorder the top level; an id from another parent is
	// refused even for the admin.
	if err := f.svc.ReorderSpaces(ctx, f.org, f.subj(t, f.bob), "", []string{a, b, c}); !errors.Is(err, plant.ErrDenied) {
		t.Fatalf("a non-admin reordered the top level: %v", err)
	}
	child, err := f.svc.CreateSpace(ctx, f.org, alice, "Child", plant.KindFolder, &a)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReorderSpaces(ctx, f.org, alice, "", []string{c, a, child.ID}); !errors.Is(err, plant.ErrNotFound) {
		t.Fatalf("reordering the top level with a child id was allowed: %v", err)
	}
}

// SaveSpaceOrder applies a whole arrangement at once, grouping the flat ordered
// list by each id's parent, and refuses unless the actor manages every parent
// it touches.
func TestSaveSpaceOrderAppliesAWholeArrangement(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	mk := func(n string, parent *string) string {
		t.Helper()
		sp, err := f.svc.CreateSpace(ctx, f.org, alice, n, plant.KindFolder, parent)
		if err != nil {
			t.Fatal(err)
		}
		return sp.ID
	}
	a := mk("Alpha", nil)
	b := mk("Bravo", nil)
	a1 := mk("A-one", &a)
	a2 := mk("A-two", &a)

	// Flat list in the order the page would send after dragging: Bravo first,
	// and Alpha's children swapped.
	if err := f.svc.SaveSpaceOrder(ctx, f.org, alice, []string{b, a, a2, a1}); err != nil {
		t.Fatalf("save order: %v", err)
	}
	tree, err := f.svc.SpaceTree(ctx, f.org, alice)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, sp := range tree {
		order = append(order, sp.Name)
	}
	// Bravo, then Alpha with A-two before A-one.
	want := []string{"Bravo", "Alpha", "A-two", "A-one"}
	if len(order) != 4 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] || order[3] != want[3] {
		t.Fatalf("order: %v", order)
	}
	// A non-admin touching the top level is refused and nothing moves.
	if err := f.svc.SaveSpaceOrder(ctx, f.org, f.subj(t, f.bob), []string{a, b}); !errors.Is(err, plant.ErrDenied) {
		t.Fatalf("a non-admin saved the top-level order: %v", err)
	}
}
