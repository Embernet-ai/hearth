package plant_test

// Managers open what they manage, super admins are never narrowed, and a
// contractor account's lists of colleagues are cut to what it may be shown.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/plant"
)

// A manager who is not a member of the department and holds no grant opens the
// folder they can delegate, and everything under it: folders, documents, tasks.
func TestAManagerOpensWhatTheyManage(t *testing.T) {
	d := newDelegateFx(t)
	mara := d.mara(t)
	for name, id := range map[string]string{"Dept": d.t.dept, "Projects": d.t.projects, "Alpha": d.t.alpha,
		"Deep": d.t.deep, "Deepest": d.t.deepest, "Beta": d.t.beta} {
		if !d.canGetSpace(t, mara, id) {
			t.Errorf("the manager cannot open folder %s", name)
		}
		if !d.canGetDoc(t, mara, d.t.doc[name]) || !d.canGetDoc(t, mara, d.t.task[name]) {
			t.Errorf("the manager cannot open the work in %s", name)
		}
	}
	tr, err := d.svc.SpaceTree(context.Background(), d.org, mara)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(tr); len(got) != 6 {
		t.Errorf("the manager's tree = %v, want all six folders", got)
	}
	list, err := d.docs.List(context.Background(), d.org, mara, docs.ListParams{Kind: docs.KindTask, Limit: 100})
	if err != nil || len(list) != 6 {
		t.Errorf("the manager's task list has %d tasks (%v), want 6", len(list), err)
	}
	// They can delegate what they can now open.
	if err := d.svc.Delegate(context.Background(), d.org, mara, d.t.alpha, d.carol, core.AccessView, nil); err != nil {
		t.Errorf("delegate: %v", err)
	}
}

// Somebody who does not manage the folder still cannot open it, and nor does a
// manager of a different department.
func TestANonManagerStillCannotOpenIt(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	rand := uuid.NewString()
	if _, err := d.pools.Web.Exec(ctx, `INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Nobody')`, rand, d.org); err != nil {
		t.Fatal(err)
	}
	other, err := d.svc.CreateSpace(ctx, d.org, d.subj(t, d.alice), "Other dept", "folder", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.pools.Web.Exec(ctx, `INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1,$2,$3)`, d.org, other.ID, rand); err != nil {
		t.Fatal(err)
	}
	s := d.subj(t, rand)
	for _, id := range []string{d.t.dept, d.t.alpha, d.t.beta} {
		if d.canGetSpace(t, s, id) {
			t.Errorf("a manager of another department opened folder %s", id)
		}
	}
	if d.canGetDoc(t, s, d.t.task["Alpha"]) || d.canGetDoc(t, s, d.t.doc["Dept"]) {
		t.Error("a manager of another department opened work in this one")
	}
	if err := d.svc.Delegate(ctx, d.org, s, d.t.alpha, d.carol, core.AccessView, nil); !errors.Is(err, plant.ErrDenied) {
		t.Errorf("a manager of another department delegated: %v", err)
	}
	if d.canGetSpace(t, d.carolS(t), d.t.alpha) {
		t.Error("a contractor with nothing opened a folder")
	}
}

// What a manager opens follows the folder: new work and moves reach them, they
// lose it when taken off, they keep what a manager above still gives, and a
// level they already hold is never lowered.
func TestManagerAccessFollowsTheFolderAndTheRole(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	alice, mara := d.subj(t, d.alice), d.mara(t)

	nt, err := d.docs.Create(ctx, d.org, alice, docs.CreateParams{Kind: docs.KindTask, Title: "new", SpaceID: &d.t.deep,
		Task: &docs.TaskFields{Status: "todo"}})
	if err != nil {
		t.Fatal(err)
	}
	nf, err := d.svc.CreateSpace(ctx, d.org, alice, "Fresh", "folder", &d.t.alpha)
	if err != nil {
		t.Fatal(err)
	}
	if !d.canGetDoc(t, mara, nt.ID) || !d.canGetSpace(t, mara, nf.ID) {
		t.Error("work and folders made after they became manager do not reach them")
	}
	// Moved out of the department: gone for the manager.
	elsewhere, err := d.svc.CreateSpace(ctx, d.org, alice, "Elsewhere", "folder", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.svc.MoveDocument(ctx, d.org, alice, nt.ID, &elsewhere.ID); err != nil {
		t.Fatal(err)
	}
	if d.canGetDoc(t, mara, nt.ID) {
		t.Error("a document moved out of the department still opens for its manager")
	}

	// A higher level already held is left alone: view is the floor, not a cap.
	if err := d.az.Grant(ctx, d.org, core.Ref{Type: core.EntityDocument, ID: d.t.task["Alpha"]},
		core.Subject{Kind: core.SubjectPrincipal, ID: d.mgr}, core.AccessEdit, d.alice); err != nil {
		t.Fatal(err)
	}
	title := "manager edit"
	if _, err := d.docs.Update(ctx, d.org, d.mara(t), d.t.task["Alpha"], docs.UpdateParams{Title: &title}); err != nil {
		t.Errorf("a manager's own edit grant was lowered: %v", err)
	}

	// Delegating to the manager and taking it back leaves them where they were.
	if err := d.svc.Delegate(ctx, d.org, alice, d.t.beta, d.mgr, core.AccessEdit, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.svc.Undelegate(ctx, d.org, alice, d.t.beta, d.mgr); err != nil {
		t.Fatal(err)
	}
	if !d.canGetSpace(t, d.mara(t), d.t.beta) || !d.canGetDoc(t, d.mara(t), d.t.task["Beta"]) {
		t.Error("taking back a delegation took away what the manager opens by managing")
	}
	// The delegation list shows only delegations, never the role.
	if l, _ := d.svc.Delegations(ctx, d.org, alice, d.t.dept); len(l) != 0 {
		t.Errorf("the role is listed as %d delegations", len(l))
	}

	// Taken off as manager: it ends, and the People page never showed it.
	if _, err := d.pools.Web.Exec(ctx, `DELETE FROM space_managers WHERE space_id=$1 AND principal_id=$2`, d.t.dept, d.mgr); err != nil {
		t.Fatal(err)
	}
	if d.canGetSpace(t, d.mara(t), d.t.alpha) || d.canGetDoc(t, d.mara(t), d.t.doc["Deep"]) {
		t.Error("a manager taken off still opens the folder")
	}
	grants, err := d.az.SpaceGrantsBy(ctx, d.org)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants[d.mgr]) != 0 {
		t.Errorf("the role shows as a department grant on the People page: %v", grants[d.mgr])
	}
}

// A manager of a folder above and of one below keeps the lower one when only
// the higher is taken away, and the other way round.
func TestTwoManagerRolesDoNotTakeEachOtherAway(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	if _, err := d.pools.Web.Exec(ctx, `INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1,$2,$3)`, d.org, d.t.alpha, d.mgr); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pools.Web.Exec(ctx, `DELETE FROM space_managers WHERE space_id=$1 AND principal_id=$2`, d.t.alpha, d.mgr); err != nil {
		t.Fatal(err)
	}
	if !d.canGetSpace(t, d.mara(t), d.t.deepest) {
		t.Error("taking off the lower role took away what the higher still gives")
	}
	if _, err := d.pools.Web.Exec(ctx, `INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1,$2,$3)`, d.org, d.t.alpha, d.mgr); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pools.Web.Exec(ctx, `DELETE FROM space_managers WHERE space_id=$1 AND principal_id=$2`, d.t.dept, d.mgr); err != nil {
		t.Fatal(err)
	}
	if !d.canGetSpace(t, d.mara(t), d.t.deepest) || d.canGetSpace(t, d.mara(t), d.t.beta) {
		t.Error("after the higher role went, the manager does not hold exactly the lower folder")
	}
}

// A super admin with no grant is never narrowed: they open a deep delegated
// folder, read every parent's name in its path, see everybody, and can delegate
// and take back anywhere.
func TestASuperAdminIsNeverNarrowed(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	d.give(t, d.t.deepest, core.AccessView, nil)
	root := uuid.NewString()
	if _, err := d.pools.Web.Exec(ctx, `INSERT INTO principals (id, org_id, kind, display_name, contractor) VALUES ($1,$2,'user','Sam Super', true)`, root, d.org); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pools.Web.Exec(ctx, `INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$3)`, d.org, root, d.alice); err != nil {
		t.Fatal(err)
	}
	sam := d.subj(t, root) // marked a contractor, and still a super admin.
	if !d.canGetSpace(t, sam, d.t.deepest) || !d.canGetDoc(t, sam, d.t.task["Deepest"]) {
		t.Error("a super admin cannot open the deep delegated folder")
	}
	sp, err := d.svc.GetSpace(ctx, d.org, sam, d.t.deepest)
	if err != nil || sp.Path != "Dept / Projects / Alpha / Deep / Deepest" {
		t.Errorf("super admin's path = %q, %v, want every parent named", sp.Path, err)
	}
	tr, _ := d.svc.SpaceTree(ctx, d.org, sam)
	if len(names(tr)) != 6 {
		t.Errorf("super admin's tree has %d folders, want 6", len(names(tr)))
	}
	if _, restricted, err := d.svc.ReachablePeople(ctx, d.org, sam); err != nil || restricted {
		t.Errorf("a super admin's people lists are restricted (%v, %v)", restricted, err)
	}
	// Delegate and take back, anywhere.
	exp := time.Now().Add(time.Hour)
	if err := d.svc.Delegate(ctx, d.org, sam, d.t.beta, d.carol, core.AccessEdit, &exp); err != nil {
		t.Fatalf("a super admin could not delegate: %v", err)
	}
	if !d.canGetDoc(t, d.carolS(t), d.t.task["Beta"]) {
		t.Error("the super admin's delegation did not take")
	}
	if l, err := d.svc.Delegations(ctx, d.org, sam, d.t.beta); err != nil || len(l) != 1 {
		t.Errorf("a super admin lists %d delegations (%v)", len(l), err)
	}
	if err := d.svc.Undelegate(ctx, d.org, sam, d.t.beta, d.carol); err != nil {
		t.Fatalf("a super admin could not take it back: %v", err)
	}
	if d.canGetDoc(t, d.carolS(t), d.t.task["Beta"]) {
		t.Error("the super admin's revoke did not take")
	}
	if _, _, err := d.svc.PersonByEmail(ctx, d.org, sam, "nobody@example.test"); !errors.Is(err, plant.ErrNoSuchPerson) {
		t.Errorf("a super admin could not look a person up: %v", err)
	}
}

// A contractor account's lists of colleagues are cut to themselves and the
// people who hold a grant of their own on a folder the account can open. An
// ordinary account is not restricted at all.
func TestContractorPeopleAreCutToWhatItMayBeShown(t *testing.T) {
	d := newDelegateFx(t)
	ctx := context.Background()
	d.give(t, d.t.alpha, core.AccessView, nil)
	friend, stranger := uuid.NewString(), uuid.NewString()
	for id, n := range map[string]string{friend: "Fran Friend", stranger: "Sid Stranger"} {
		if _, err := d.pools.Web.Exec(ctx, `INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user',$3)`, id, d.org, n); err != nil {
			t.Fatal(err)
		}
	}
	// Fran holds Deep, under Alpha, in her own right; Sid holds Beta, beside it.
	if err := d.svc.Delegate(ctx, d.org, d.mara(t), d.t.deep, friend, core.AccessEdit, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.svc.Delegate(ctx, d.org, d.mara(t), d.t.beta, stranger, core.AccessEdit, nil); err != nil {
		t.Fatal(err)
	}

	got, restricted, err := d.svc.ReachablePeople(ctx, d.org, d.carolS(t))
	if err != nil || !restricted {
		t.Fatalf("contractor not restricted: %v %v", restricted, err)
	}
	// Herself, Fran, and the people who hold the folder by managing it or by
	// having made it (Mara and Alice); not Sid, who holds a folder beside it.
	if !got[d.carol] || !got[friend] || !got[d.mgr] || !got[d.alice] || got[stranger] || len(got) != 4 {
		t.Errorf("reachable = %v, want herself, Fran, Mara and Alice only", got)
	}
	if _, restricted, _ := d.svc.ReachablePeople(ctx, d.org, d.subj(t, d.alice)); restricted {
		t.Error("a super admin is restricted")
	}
	if all, restricted, err := d.svc.ReachablePeople(ctx, d.org, d.subj(t, friend)); err != nil || restricted || all != nil {
		t.Errorf("an ordinary account is restricted: %v %v %v", all, restricted, err)
	}
}
