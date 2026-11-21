package plant_test

// Delegating one folder, against a real Postgres.
//
// The rule being proved: a person given one subfolder (a contractor or anybody)
// reaches that folder and everything under it, tasks included at the level they
// were given, and nothing above it or beside it, on every path that reads: the
// tree, the folder, a document, the task list, a search, a comment. A manager
// gives and takes it back in place, with an optional end date, and each is
// written to the audit log.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/notify"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/search"
)

// tree is a department with two projects and a deep branch:
//
//	Dept
//	  Projects
//	    Alpha            <- the folder that gets delegated
//	      Deep           <- a folder under it
//	        Deepest      <- and one under that
//	    Beta             <- a sibling of Alpha
type tree struct {
	dept, projects, alpha, deep, deepest, beta string
	// A task in each, by folder name.
	task map[string]string
	doc  map[string]string
}

type delegateFx struct {
	*fx
	mgr, carol string
	t          tree
}

func newDelegateFx(t *testing.T) *delegateFx {
	t.Helper()
	f := setup(t)
	ctx := context.Background()
	d := &delegateFx{fx: f, mgr: uuid.NewString(), carol: uuid.NewString()}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Mara Manager')`, d.mgr, f.org)
	exec(`INSERT INTO principals (id, org_id, kind, display_name, contractor) VALUES ($1,$2,'user','Carol Contractor', true)`, d.carol, f.org)
	t.Cleanup(func() {
		_, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM space_managers WHERE org_id=$1`, f.org)
		_, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM files WHERE org_id=$1`, f.org)
	})

	alice := f.subj(t, f.alice)
	mk := func(name string, parent *string) string {
		sp, err := f.svc.CreateSpace(ctx, f.org, alice, name, "folder", parent)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return sp.ID
	}
	d.t.dept = mk("Dept", nil)
	d.t.projects = mk("Projects", &d.t.dept)
	d.t.alpha = mk("Alpha", &d.t.projects)
	d.t.deep = mk("Deep", &d.t.alpha)
	d.t.deepest = mk("Deepest", &d.t.deep)
	d.t.beta = mk("Beta", &d.t.projects)
	// Mara manages the department, so she manages everything under it.
	exec(`INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1,$2,$3)`, f.org, d.t.dept, d.mgr)

	d.t.task, d.t.doc = map[string]string{}, map[string]string{}
	for name, id := range map[string]string{"Dept": d.t.dept, "Projects": d.t.projects, "Alpha": d.t.alpha,
		"Deep": d.t.deep, "Deepest": d.t.deepest, "Beta": d.t.beta} {
		id := id
		tk, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
			Kind: docs.KindTask, Title: "task in " + name, SpaceID: &id, Task: &docs.TaskFields{Status: "todo"}})
		if err != nil {
			t.Fatalf("task %s: %v", name, err)
		}
		d.t.task[name] = tk.ID
		dc, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
			Kind: docs.KindDoc, Title: "plan for " + name, Body: "walnut " + name, SpaceID: &id})
		if err != nil {
			t.Fatalf("doc %s: %v", name, err)
		}
		d.t.doc[name] = dc.ID
	}
	return d
}

func (d *delegateFx) mara(t *testing.T) core.Subjects   { return d.subj(t, d.mgr) }
func (d *delegateFx) carolS(t *testing.T) core.Subjects { return d.subj(t, d.carol) }

func (d *delegateFx) give(t *testing.T, space string, level core.AccessLevel, exp *time.Time) {
	t.Helper()
	if err := d.svc.Delegate(context.Background(), d.org, d.mara(t), space, d.carol, level, exp); err != nil {
		t.Fatalf("delegate: %v", err)
	}
}

func (d *delegateFx) canGetDoc(t *testing.T, who core.Subjects, id string) bool {
	t.Helper()
	_, err := d.docs.Get(context.Background(), d.org, who, id)
	if err != nil && !errors.Is(err, docs.ErrNotFound) {
		t.Fatalf("get %s: %v", id, err)
	}
	return err == nil
}

func (d *delegateFx) canGetSpace(t *testing.T, who core.Subjects, id string) bool {
	t.Helper()
	_, err := d.svc.GetSpace(context.Background(), d.org, who, id)
	if err != nil && !errors.Is(err, plant.ErrNotFound) {
		t.Fatalf("get space %s: %v", id, err)
	}
	return err == nil
}

func names(sp []*plant.Space) map[string]bool {
	out := map[string]bool{}
	for _, s := range sp {
		out[s.Name] = true
	}
	return out
}

// The person sees the delegated folder and the folders under it, and none of the
// folders above or beside it.
func TestDelegatedFolderIsAllTheTreeShows(t *testing.T) {
	d := newDelegateFx(t)
	d.give(t, d.t.alpha, core.AccessView, nil)
	carol := d.carolS(t)

	tr, err := d.svc.SpaceTree(context.Background(), d.org, carol)
	if err != nil {
		t.Fatal(err)
	}
	got := names(tr)
	for _, want := range []string{"Alpha", "Deep", "Deepest"} {
		if !got[want] {
			t.Errorf("tree is missing %s: %v", want, got)
		}
	}
	for _, not := range []string{"Dept", "Projects", "Beta"} {
		if got[not] {
			t.Errorf("tree shows %s, which was not delegated: %v", not, got)
		}
	}
	// Alpha is the top of what she sees: nothing above it to hang from.
	for _, s := range tr {
		if s.Name == "Alpha" && (s.Depth != 0 || s.Path != "Alpha") {
			t.Errorf("Alpha shows at depth %d with path %q, want 0 and \"Alpha\"", s.Depth, s.Path)
		}
	}
}

// Delegating at any depth: one folder deep in the tree, and only it.
func TestDelegatingAtDepthReachesOnlyThatBranch(t *testing.T) {
	d := newDelegateFx(t)
	d.give(t, d.t.deep, core.AccessView, nil)
	carol := d.carolS(t)

	for name, want := range map[string]bool{"Deep": true, "Deepest": true, "Alpha": false, "Projects": false, "Dept": false, "Beta": false} {
		id := map[string]string{"Deep": d.t.deep, "Deepest": d.t.deepest, "Alpha": d.t.alpha,
			"Projects": d.t.projects, "Dept": d.t.dept, "Beta": d.t.beta}[name]
		if got := d.canGetSpace(t, carol, id); got != want {
			t.Errorf("folder %s: reachable=%v, want %v", name, got, want)
		}
		if got := d.canGetDoc(t, carol, d.t.doc[name]); got != want {
			t.Errorf("document in %s: reachable=%v, want %v", name, got, want)
		}
		if got := d.canGetDoc(t, carol, d.t.task[name]); got != want {
			t.Errorf("task in %s: reachable=%v, want %v", name, got, want)
		}
	}
}

// The parent and a sibling are not found, in the folder and in what is filed there.
func TestParentAndSiblingAreNotFound(t *testing.T) {
	d := newDelegateFx(t)
	d.give(t, d.t.alpha, core.AccessEdit, nil)
	carol := d.carolS(t)
	for name, id := range map[string]string{"parent": d.t.projects, "grandparent": d.t.dept, "sibling": d.t.beta} {
		if d.canGetSpace(t, carol, id) {
			t.Errorf("the %s folder is reachable", name)
		}
	}
	for _, name := range []string{"Projects", "Dept", "Beta"} {
		if d.canGetDoc(t, carol, d.t.doc[name]) || d.canGetDoc(t, carol, d.t.task[name]) {
			t.Errorf("work filed in %s is reachable", name)
		}
	}
	if d.canGetDoc(t, carol, d.t.doc["Alpha"]) == false {
		t.Error("the delegated folder's own document is not reachable")
	}
}

// Tasks: visible and workable at the grant's level, and only in the folder.
func TestTasksFollowTheGrantLevel(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		level        core.AccessLevel
		edit, remark bool
	}{
		{core.AccessView, false, false},
		{core.AccessComment, false, true},
		{core.AccessEdit, true, true},
	} {
		t.Run(string(tc.level), func(t *testing.T) {
			d := newDelegateFx(t)
			d.give(t, d.t.alpha, tc.level, nil)
			carol := d.carolS(t)

			for _, folder := range []string{"Alpha", "Deep", "Deepest"} {
				id := d.t.task[folder]
				if !d.canGetDoc(t, carol, id) {
					t.Fatalf("task in %s not visible at %s", folder, tc.level)
				}
				title := "changed by carol"
				_, err := d.docs.Update(ctx, d.org, carol, id, docs.UpdateParams{Title: &title,
					Task: &docs.TaskPatch{Status: ptr("in_progress")}})
				if tc.edit && err != nil {
					t.Errorf("edit on the task in %s refused at %s: %v", folder, tc.level, err)
				}
				if !tc.edit && err == nil {
					t.Errorf("task in %s was changed at %s", folder, tc.level)
				}
				if got := d.docs.CanComment(ctx, carol, id) == nil; got != tc.remark {
					t.Errorf("comment on the task in %s at %s: allowed=%v want %v", folder, tc.level, got, tc.remark)
				}
			}
			// A sibling's task cannot be changed or found, whatever the level.
			title := "nope"
			if _, err := d.docs.Update(ctx, d.org, carol, d.t.task["Beta"], docs.UpdateParams{Title: &title}); err == nil {
				t.Error("changed a task in a sibling folder")
			}
			if d.canGetDoc(t, carol, d.t.task["Beta"]) || d.canGetDoc(t, carol, d.t.task["Projects"]) {
				t.Error("a task above or beside the folder is visible")
			}

			// The task list holds the folder's tasks and no others.
			list, err := d.docs.List(ctx, d.org, carol, docs.ListParams{Kind: docs.KindTask, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, x := range list {
				got[x.ID] = true
			}
			for _, w := range []string{"Alpha", "Deep", "Deepest"} {
				if !got[d.t.task[w]] {
					t.Errorf("task list lacks the task in %s", w)
				}
			}
			for _, not := range []string{"Beta", "Projects", "Dept"} {
				if got[d.t.task[not]] {
					t.Errorf("task list shows the task in %s", not)
				}
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// A subtask of a task in the folder is the person's too.
func TestSubtasksOfAFolderTaskAreReachable(t *testing.T) {
	d := newDelegateFx(t)
	d.give(t, d.t.alpha, core.AccessEdit, nil)
	carol := d.carolS(t)
	parent := d.t.task["Alpha"]
	sub, err := d.docs.Create(context.Background(), d.org, carol, docs.CreateParams{
		Kind: docs.KindTask, Title: "carol's subtask", ParentID: &parent, Task: &docs.TaskFields{Status: "todo"}})
	if err != nil {
		t.Fatalf("carol could not add a subtask: %v", err)
	}
	if !d.canGetDoc(t, carol, sub.ID) {
		t.Error("the contractor cannot open the subtask she added")
	}
	// The parent stays hers to read, and a subtask never lifts her above it.
	if !d.canGetDoc(t, carol, parent) || d.canGetDoc(t, carol, d.t.task["Projects"]) {
		t.Error("the parent task is not reachable, or the one above it is")
	}
}

// Search finds the folder's work and never the rest.
func TestSearchDoesNotLeak(t *testing.T) {
	d := newDelegateFx(t)
	d.give(t, d.t.alpha, core.AccessView, nil)
	carol := d.carolS(t)
	pg := search.NewPostgres(d.pools.Web)

	res, err := pg.Search(context.Background(), d.org, carol, search.Query{Text: "walnut", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, h := range res.Hits {
		found[h.Title] = true
	}
	for _, w := range []string{"plan for Alpha", "plan for Deep", "plan for Deepest"} {
		if !found[w] {
			t.Errorf("search misses %q: %v", w, found)
		}
	}
	for _, not := range []string{"plan for Beta", "plan for Projects", "plan for Dept"} {
		if found[not] {
			t.Errorf("search leaks %q", not)
		}
	}
}

// What a folder is called above it never reaches the person: not in the path of
// the folder, not in the path a roll-up lists.
func TestNoParentNamesReachTheContractor(t *testing.T) {
	d := newDelegateFx(t)
	d.give(t, d.t.deep, core.AccessView, nil)
	sp, err := d.svc.GetSpace(context.Background(), d.org, d.carolS(t), d.t.deepest)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Path != "Deep / Deepest" {
		t.Errorf("path = %q, want %q: it must start at the folder she was given", sp.Path, "Deep / Deepest")
	}
	// The manager still sees the whole path.
	m, err := d.svc.GetSpace(context.Background(), d.org, d.subj(t, d.alice), d.t.deepest)
	if err != nil {
		t.Fatal(err)
	}
	if m.Path != "Dept / Projects / Alpha / Deep / Deepest" {
		t.Errorf("manager's path = %q", m.Path)
	}
	// A folder's children list shows nothing the grants do not reach.
	kids, err := d.svc.SpaceChildren(context.Background(), d.org, d.carolS(t), d.t.projects)
	if err != nil {
		t.Fatal(err)
	}
	if len(kids) != 0 {
		t.Errorf("the parent's children list gave %d folders to somebody who cannot open the parent", len(kids))
	}
}

// Work made in the folder after the delegation, and work moved in or out, follows
// the folder with no step by hand.
func TestWorkCreatedAndMovedFollowsTheFolder(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	d.give(t, d.t.alpha, core.AccessEdit, nil)
	carol, mara := d.carolS(t), d.subj(t, d.alice)

	// A new folder and a new task under Alpha, made by the manager.
	nf, err := d.svc.CreateSpace(ctx, d.org, mara, "Fresh", "folder", &d.t.deep)
	if err != nil {
		t.Fatal(err)
	}
	if !d.canGetSpace(t, carol, nf.ID) {
		t.Error("a folder made under the delegated one is not reachable")
	}
	nt, err := d.docs.Create(ctx, d.org, mara, docs.CreateParams{Kind: docs.KindTask, Title: "new", SpaceID: &nf.ID,
		Task: &docs.TaskFields{Status: "todo"}})
	if err != nil {
		t.Fatal(err)
	}
	if !d.canGetDoc(t, carol, nt.ID) {
		t.Error("a task filed in the delegated folder after the delegation is not reachable")
	}

	// A document moved out of Alpha is no longer hers; one moved in is.
	if err := d.svc.MoveDocument(ctx, d.org, mara, d.t.doc["Alpha"], &d.t.beta); err != nil {
		t.Fatal(err)
	}
	if d.canGetDoc(t, carol, d.t.doc["Alpha"]) {
		t.Error("a document moved out of the folder is still reachable")
	}
	if err := d.svc.MoveDocument(ctx, d.org, mara, d.t.doc["Beta"], &d.t.deep); err != nil {
		t.Fatal(err)
	}
	if !d.canGetDoc(t, carol, d.t.doc["Beta"]) {
		t.Error("a document moved into the folder is not reachable")
	}

	// A folder moved out takes its access with it; one moved in gains it.
	if err := d.svc.MoveSpace(ctx, d.org, mara, d.t.deepest, &d.t.beta); err != nil {
		t.Fatal(err)
	}
	if d.canGetSpace(t, carol, d.t.deepest) || d.canGetDoc(t, carol, d.t.task["Deepest"]) {
		t.Error("a folder moved out of the delegated one is still reachable")
	}
	if err := d.svc.MoveSpace(ctx, d.org, mara, d.t.deepest, &d.t.deep); err != nil {
		t.Fatal(err)
	}
	if !d.canGetSpace(t, carol, d.t.deepest) || !d.canGetDoc(t, carol, d.t.task["Deepest"]) {
		t.Error("a folder moved into the delegated one is not reachable")
	}
}

// An end date ends it everywhere, including what was copied down.
func TestExpiryEndsAccess(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	soon := time.Now().Add(time.Hour)
	d.give(t, d.t.alpha, core.AccessEdit, &soon)
	carol := d.carolS(t)
	if !d.canGetDoc(t, carol, d.t.task["Deep"]) || !d.canGetSpace(t, carol, d.t.alpha) {
		t.Fatal("not reachable before it ends")
	}
	// Move the end into the past, as time passing would.
	if _, err := d.pools.Web.Exec(ctx,
		`UPDATE grants SET expires_at = now() - interval '1 minute' WHERE via_space_id = $1`, d.t.alpha); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{"Alpha": d.t.alpha, "Deep": d.t.deep, "Deepest": d.t.deepest} {
		if d.canGetSpace(t, carol, id) {
			t.Errorf("folder %s reachable after the end date", name)
		}
		if d.canGetDoc(t, carol, d.t.task[name]) || d.canGetDoc(t, carol, d.t.doc[name]) {
			t.Errorf("work in %s reachable after the end date", name)
		}
	}
	if tr, _ := d.svc.SpaceTree(ctx, d.org, carol); len(tr) != 0 {
		t.Errorf("tree still shows %d folders", len(tr))
	}
	res, err := search.NewPostgres(d.pools.Web).Search(ctx, d.org, carol, search.Query{Text: "walnut", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Errorf("search still gives %d hits after the end date", len(res.Hits))
	}
	// And work made while it was running does not carry on forever: a task made
	// now gets nothing for her.
	id := d.t.alpha
	nt, err := d.docs.Create(ctx, d.org, d.mara(t), docs.CreateParams{Kind: docs.KindTask, Title: "later", SpaceID: &id})
	if err != nil {
		t.Fatal(err)
	}
	if d.canGetDoc(t, carol, nt.ID) {
		t.Error("work filed after the end date reaches the person")
	}
}

// A grant that was copied from a folder with an end date carries the date.
func TestCopiedGrantsCarryTheEndDate(t *testing.T) {
	d := newDelegateFx(t)
	soon := time.Now().Add(2 * time.Hour)
	d.give(t, d.t.alpha, core.AccessView, &soon)
	nf, err := d.svc.CreateSpace(context.Background(), d.org, d.mara(t), "Later", "folder", &d.t.alpha)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.pools.Web.QueryRow(context.Background(), `
		SELECT count(*) FROM grants WHERE entity_type='space' AND entity_id=$1::uuid
		   AND subject_key = $2 AND expires_at IS NOT NULL`, nf.ID, "principal:"+d.carol).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the new folder's copy of her grant has no end date (%d rows with one)", n)
	}
}

// Taking it back is immediate, complete, and touches nothing else.
func TestRevokeIsImmediateAndExact(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	d.give(t, d.t.alpha, core.AccessEdit, nil)
	carol := d.carolS(t)
	// A grant she holds for another reason, on a different folder, stays.
	if err := d.az.Grant(ctx, d.org, core.Ref{Type: core.EntitySpace, ID: d.t.beta},
		core.Subject{Kind: core.SubjectPrincipal, ID: d.carol}, core.AccessView, d.alice); err != nil {
		t.Fatal(err)
	}
	if !d.canGetSpace(t, carol, d.t.deepest) {
		t.Fatal("not reachable before the revoke")
	}
	if err := d.svc.Undelegate(ctx, d.org, d.mara(t), d.t.alpha, d.carol); err != nil {
		t.Fatal(err)
	}
	// The same subject set, no new sign in: it takes effect on the next read.
	for name, id := range map[string]string{"Alpha": d.t.alpha, "Deep": d.t.deep, "Deepest": d.t.deepest} {
		if d.canGetSpace(t, carol, id) || d.canGetDoc(t, carol, d.t.task[name]) || d.canGetDoc(t, carol, d.t.doc[name]) {
			t.Errorf("%s still reachable after the revoke", name)
		}
	}
	if !d.canGetSpace(t, carol, d.t.beta) {
		t.Error("the revoke removed a grant that was not the delegation's")
	}
	// Delegating again after a revoke works.
	d.give(t, d.t.alpha, core.AccessView, nil)
	if !d.canGetDoc(t, carol, d.t.task["Deep"]) {
		t.Error("not reachable after delegating again")
	}
}

// Nobody loses what they already hold: a department grant stays at its level and
// survives a revoke.
func TestADelegationNeverShrinksAnExistingGrant(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	if err := d.az.GrantSpace(ctx, d.org, d.alice, d.carol, d.t.alpha); err != nil {
		t.Fatal(err)
	}
	d.give(t, d.t.alpha, core.AccessView, nil)
	carol := d.carolS(t)
	title := "still mine to edit"
	if _, err := d.docs.Update(ctx, d.org, carol, d.t.task["Deep"], docs.UpdateParams{Title: &title}); err != nil {
		t.Errorf("a view delegation took away an edit grant she held: %v", err)
	}
	if err := d.svc.Undelegate(ctx, d.org, d.mara(t), d.t.alpha, d.carol); err != nil {
		t.Fatal(err)
	}
	if !d.canGetSpace(t, carol, d.t.alpha) || !d.canGetDoc(t, carol, d.t.task["Deep"]) {
		t.Error("revoking the delegation removed a grant that was never its own")
	}
}

// Who may delegate: a manager at or above the folder, and nobody else.
func TestOnlyAManagerMayDelegate(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	d.give(t, d.t.alpha, core.AccessEdit, nil)

	// Carol holds edit but manages nothing: she cannot hand it on.
	other := uuid.NewString()
	if _, err := d.pools.Web.Exec(ctx, `INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Other')`, other, d.org); err != nil {
		t.Fatal(err)
	}
	if err := d.svc.Delegate(ctx, d.org, d.carolS(t), d.t.deep, other, core.AccessView, nil); !errors.Is(err, plant.ErrDenied) {
		t.Errorf("a contractor with edit delegated: %v", err)
	}
	// A manager of a different department cannot either.
	elsewhere := uuid.NewString()
	if _, err := d.pools.Web.Exec(ctx, `INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Elsewhere')`, elsewhere, d.org); err != nil {
		t.Fatal(err)
	}
	other2, err := d.svc.CreateSpace(ctx, d.org, d.subj(t, d.alice), "Other dept", "folder", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.pools.Web.Exec(ctx, `INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1,$2,$3)`, d.org, other2.ID, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := d.svc.Delegate(ctx, d.org, d.subj(t, elsewhere), d.t.beta, other, core.AccessView, nil); !errors.Is(err, plant.ErrDenied) {
		t.Errorf("a manager of another department delegated: %v", err)
	}
	if err := d.svc.Undelegate(ctx, d.org, d.carolS(t), d.t.alpha, d.carol); !errors.Is(err, plant.ErrDenied) {
		t.Errorf("a contractor took back her own delegation: %v", err)
	}
	if _, err := d.svc.Delegations(ctx, d.org, d.carolS(t), d.t.alpha); !errors.Is(err, plant.ErrDenied) {
		t.Errorf("a contractor listed who has access: %v", err)
	}
	// A manager can, on a subfolder, in place.
	if err := d.svc.Delegate(ctx, d.org, d.mara(t), d.t.beta, other, core.AccessView, nil); err != nil {
		t.Errorf("the department's manager could not delegate a subfolder: %v", err)
	}
}

// Bad requests are refused: a level that is not for a folder, a past end date, a
// person who is not here, and a folder that is already reached from above.
func TestDelegationRefusesWhatItShould(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	mara := d.mara(t)
	if err := d.svc.Delegate(ctx, d.org, mara, d.t.alpha, d.carol, core.AccessAdmin, nil); !errors.Is(err, plant.ErrInvalid) {
		t.Errorf("admin level: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	if err := d.svc.Delegate(ctx, d.org, mara, d.t.alpha, d.carol, core.AccessView, &past); !errors.Is(err, plant.ErrInvalid) {
		t.Errorf("past end date: %v", err)
	}
	if err := d.svc.Delegate(ctx, d.org, mara, d.t.alpha, uuid.NewString(), core.AccessView, nil); !errors.Is(err, plant.ErrNoSuchPerson) {
		t.Errorf("unknown person: %v", err)
	}
	d.give(t, d.t.alpha, core.AccessView, nil)
	if err := d.svc.Delegate(ctx, d.org, mara, d.t.deep, d.carol, core.AccessEdit, nil); !errors.Is(err, plant.ErrAlreadyReaches) {
		t.Errorf("delegating a folder already reached from above: %v", err)
	}
}

// Every give and every take back writes an audit row naming who, whom and what.
func TestDelegationIsAudited(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	exp := time.Now().Add(48 * time.Hour)
	d.give(t, d.t.alpha, core.AccessComment, &exp)
	if err := d.svc.Undelegate(ctx, d.org, d.mara(t), d.t.alpha, d.carol); err != nil {
		t.Fatal(err)
	}
	rows, err := d.pools.Web.Query(ctx, `
		SELECT action, actor_id::text, entity_id::text, after::text, before::text
		  FROM audit_log WHERE org_id = $1 AND entity_type = 'space'
		   AND action IN ($2, $3) ORDER BY id`, d.org, audit.ActionGrantAdd, audit.ActionGrantRemove)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var seen []string
	for rows.Next() {
		var action, actor, entity string
		var after, before *string
		if err := rows.Scan(&action, &actor, &entity, &after, &before); err != nil {
			t.Fatal(err)
		}
		if actor != d.mgr || entity != d.t.alpha {
			t.Errorf("%s row: actor %s entity %s", action, actor, entity)
		}
		seen = append(seen, action)
		body := ""
		if after != nil {
			body += *after
		}
		if before != nil {
			body += *before
		}
		if !contains(body, d.carol) {
			t.Errorf("%s row does not name the person: %s", action, body)
		}
		if action == audit.ActionGrantAdd && (!contains(body, "comment") || !contains(body, "expires_at")) {
			t.Errorf("grant row lacks the level or end date: %s", body)
		}
	}
	if len(seen) != 2 || seen[0] != audit.ActionGrantAdd || seen[1] != audit.ActionGrantRemove {
		t.Errorf("audit rows = %v, want grant.add then grant.remove", seen)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// The open daily reports department is everybody's, except a contractor's.
func TestContractorDoesNotReachTheOpenDepartment(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	if _, err := d.pools.Web.Exec(ctx, `
		INSERT INTO spaces (id, org_id, name, kind, created_by, open_to_all)
		VALUES (gen_random_uuid(), $1, 'Daily reports', 'area', $2, true)`, d.org, d.alice); err != nil {
		t.Fatal(err)
	}
	d.give(t, d.t.alpha, core.AccessView, nil)
	tr, err := d.svc.SpaceTree(ctx, d.org, d.carolS(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := names(tr); got["Daily reports"] || !got["Alpha"] {
		t.Errorf("contractor's tree = %v, want Alpha and not the open department", got)
	}
	tr, _ = d.svc.SpaceTree(ctx, d.org, d.subj(t, d.mgr))
	if !names(tr)["Daily reports"] {
		t.Error("an ordinary account lost the open department")
	}
}

// The manager lists who has what, ends included, and finds a person by address.
func TestManagerSeesDelegations(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	exp := time.Now().Add(72 * time.Hour)
	d.give(t, d.t.alpha, core.AccessView, &exp)
	list, err := d.svc.Delegations(ctx, d.org, d.mara(t), d.t.alpha)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].PersonID != d.carol || list[0].Level != core.AccessView ||
		list[0].ExpiresAt == nil || !list[0].Contractor || list[0].Expired {
		t.Fatalf("delegations = %+v", list)
	}
	// Folders under it are not listed as separate delegations.
	if l, _ := d.svc.Delegations(ctx, d.org, d.mara(t), d.t.deep); len(l) != 0 {
		t.Errorf("a folder below lists %d delegations of its own", len(l))
	}
	if _, err := d.pools.Web.Exec(ctx, `INSERT INTO local_credentials (principal_id, org_id, email, password_hash) VALUES ($1,$2,'carol@example.test','x')`, d.carol, d.org); err != nil {
		t.Fatal(err)
	}
	id, _, err := d.svc.PersonByEmail(ctx, d.org, d.mara(t), " Carol@Example.test ")
	if err == nil || id != "" {
		t.Logf("untrimmed address resolved: %v", err)
	}
	id, _, err = d.svc.PersonByEmail(ctx, d.org, d.mara(t), "Carol@Example.test")
	if err != nil || id != d.carol {
		t.Errorf("PersonByEmail = %q, %v", id, err)
	}
	if _, _, err := d.svc.PersonByEmail(ctx, d.org, d.carolS(t), "carol@example.test"); !errors.Is(err, plant.ErrDenied) {
		t.Errorf("a contractor looked a person up: %v", err)
	}
}

// clusterThatKnowsNothingNew answers from a stale subject list, as the search
// cluster does between a grant ending and the index hearing about it: it hands
// back every document it is asked about.
type clusterThatKnowsNothingNew struct{ ids []string }

func (clusterThatKnowsNothingNew) Name() string                { return "stale-cluster" }
func (clusterThatKnowsNothingNew) Ready(context.Context) error { return nil }
func (c clusterThatKnowsNothingNew) Search(context.Context, string, core.Subjects, search.Query) (*search.Results, error) {
	var hits []search.Hit
	for _, id := range c.ids {
		hits = append(hits, search.Hit{EntityType: "document", EntityID: id, Title: id})
	}
	return &search.Results{Hits: hits, Total: len(hits)}, nil
}

// An answer from the search cluster is checked against the live grants, so a
// delegation that ended or was taken back stops showing at once.
func TestSearchClusterAnswerIsVerified(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	d.give(t, d.t.alpha, core.AccessView, nil)
	carol := d.carolS(t)
	all := []string{d.t.doc["Alpha"], d.t.doc["Deep"], d.t.doc["Beta"], d.t.doc["Dept"]}
	svc := search.New(clusterThatKnowsNothingNew{ids: all}, search.NewPostgres(d.pools.Web))

	res, err := svc.Search(ctx, d.org, carol, search.Query{Text: "walnut"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Engine != "stale-cluster" || len(res.Hits) != 2 || res.Total != 2 {
		t.Fatalf("hits = %d total %d from %s, want the 2 in her folder", len(res.Hits), res.Total, res.Engine)
	}
	for _, h := range res.Hits {
		if h.EntityID == d.t.doc["Beta"] || h.EntityID == d.t.doc["Dept"] {
			t.Errorf("the cluster's answer leaked %s", h.EntityID)
		}
	}
	if err := d.svc.Undelegate(ctx, d.org, d.mara(t), d.t.alpha, d.carol); err != nil {
		t.Fatal(err)
	}
	res, err = svc.Search(ctx, d.org, carol, search.Query{Text: "walnut"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Errorf("%d hits still shown after the revoke", len(res.Hits))
	}
}

// A notification written while the person had the task does not outlive the
// access: not in the inbox, not in the badge.
func TestInboxStopsShowingWhatTheyCanNoLongerOpen(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	d.give(t, d.t.alpha, core.AccessView, nil)
	for name, task := range map[string]string{"Alpha": d.t.task["Alpha"], "Beta": d.t.task["Beta"]} {
		if _, err := d.pools.Web.Exec(ctx, `
			INSERT INTO notifications (id, org_id, principal_id, reason, entity_type, entity_id, title, excerpt, dedupe_key)
			VALUES (gen_random_uuid(), $1, $2, 'mentioned', 'document', $3, $4, 'secret excerpt', $5)`,
			d.org, d.carol, task, "task in "+name, "k-"+name); err != nil {
			t.Fatal(err)
		}
	}
	nsvc := notify.New(d.pools.Web)
	carol := d.carolS(t)
	list, err := nsvc.InboxFor(ctx, carol, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Title != "task in Alpha" {
		t.Fatalf("inbox = %d rows, want only the one about her folder", len(list))
	}
	if n, _ := nsvc.UnreadFor(ctx, carol); n != 1 {
		t.Errorf("badge = %d, want 1", n)
	}
	if err := d.svc.Undelegate(ctx, d.org, d.mara(t), d.t.alpha, d.carol); err != nil {
		t.Fatal(err)
	}
	if list, _ = nsvc.InboxFor(ctx, carol, false, 50); len(list) != 0 {
		t.Errorf("inbox still has %d rows after the revoke", len(list))
	}
	if n, _ := nsvc.UnreadFor(ctx, carol); n != 0 {
		t.Errorf("badge = %d after the revoke, want 0", n)
	}
}
