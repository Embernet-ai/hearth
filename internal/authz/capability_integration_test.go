package authz_test

// Capabilities and the org chart, against a real Postgres.
//
// Same standard as the rest of this package: every denial is asserted as
// carefully as every grant, because a permission check that always says yes
// passes every test that only looks at the happy path.

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
)

// caps registers the cleanup this file needs on top of setup's.
//
// After setup's, so it runs BEFORE it: capability_grants.granted_by is
// ON DELETE RESTRICT, so rows left behind would make setup's own
// `DELETE FROM principals` fail and every test in the package would break on
// teardown rather than on anything real.
func caps(t *testing.T, f *fixture) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM capability_grants WHERE org_id=$1`,
			`DELETE FROM space_managers WHERE org_id=$1`,
			`UPDATE principals SET department_id=NULL, approver_id=NULL WHERE org_id=$1`,
			`DELETE FROM spaces WHERE org_id=$1`,
		} {
			_, _ = f.pools.Web.Exec(ctx, q, f.org)
		}
	})
}

func (f *fixture) makeSuper(t *testing.T, id string) {
	t.Helper()
	if _, err := f.pools.Web.Exec(context.Background(),
		`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
		 VALUES ($1,$2,'super_admin',$2) ON CONFLICT DO NOTHING`, f.org, id); err != nil {
		t.Fatalf("make super admin: %v", err)
	}
}

func (f *fixture) space(t *testing.T, name string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := f.pools.Web.Exec(context.Background(),
		`INSERT INTO spaces (id, org_id, name, kind, created_by) VALUES ($1,$2,$3,'area',$4)`,
		id, f.org, name, f.alice); err != nil {
		t.Fatalf("space: %v", err)
	}
	return id
}

func TestSuperAdminImpliesEverythingElse(t *testing.T) {
	f := setup(t)
	caps(t, f)
	ctx := context.Background()

	f.makeSuper(t, f.alice)
	held, err := f.svc.Capabilities(ctx, f.org, f.alice)
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	for _, c := range authz.Grantable {
		if !held.Has(c) {
			t.Errorf("a super admin does not hold %s, so promoting somebody "+
				"would still leave sections closed to them", c)
		}
	}
	if !held.IsSuperAdmin() {
		t.Error("IsSuperAdmin is false for a super admin")
	}

	// Somebody with one grant holds that one and nothing else. IsSuperAdmin
	// stays false, which is what stops a People administrator promoting
	// themselves.
	if err := f.svc.GrantCapability(ctx, f.org, f.alice, f.bob, authz.People); err != nil {
		t.Fatalf("grant: %v", err)
	}
	bob, err := f.svc.Capabilities(ctx, f.org, f.bob)
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if !bob.Has(authz.People) {
		t.Error("bob does not hold what he was granted")
	}
	if bob.Has(authz.Finance) || bob.IsSuperAdmin() {
		t.Error("one grant handed over more than one capability")
	}
}

func TestOnlyASuperAdminChangesWhatSomebodyMayReach(t *testing.T) {
	f := setup(t)
	caps(t, f)
	ctx := context.Background()

	// Carol administers People. That is not the same as being able to hand out
	// capabilities, and this is the case that matters: otherwise the person who
	// manages accounts can quietly promote themselves.
	f.makeSuper(t, f.alice)
	if err := f.svc.GrantCapability(ctx, f.org, f.alice, f.carol, authz.People); err != nil {
		t.Fatalf("grant: %v", err)
	}

	if err := f.svc.GrantCapability(ctx, f.org, f.carol, f.carol, authz.SuperAdmin); !errors.Is(err, authz.ErrNotSuperAdmin) {
		t.Fatalf("somebody with People promoted themselves: %v", err)
	}
	if err := f.svc.RevokeCapability(ctx, f.org, f.dave, f.alice, authz.SuperAdmin); !errors.Is(err, authz.ErrNotSuperAdmin) {
		t.Fatalf("a nobody revoked a super admin: %v", err)
	}
	held, _ := f.svc.Capabilities(ctx, f.org, f.alice)
	if !held.IsSuperAdmin() {
		t.Error("alice lost super admin to a call that should have been refused")
	}
}

func TestTheLastSuperAdminCannotBeRemoved(t *testing.T) {
	f := setup(t)
	caps(t, f)
	ctx := context.Background()

	f.makeSuper(t, f.alice)
	// An installation with no super admin has nobody who can appoint one, and
	// leaving that state needs a database client.
	if err := f.svc.RevokeCapability(ctx, f.org, f.alice, f.alice, authz.SuperAdmin); err == nil {
		t.Fatal("the last super admin removed themselves")
	}

	// With a second one, the first may stand down.
	f.makeSuper(t, f.bob)
	if err := f.svc.RevokeCapability(ctx, f.org, f.alice, f.alice, authz.SuperAdmin); err != nil {
		t.Fatalf("standing down with a colleague in place: %v", err)
	}
	held, _ := f.svc.Capabilities(ctx, f.org, f.alice)
	if held.IsSuperAdmin() {
		t.Error("she is still a super admin after standing down")
	}
}

func TestParseCapabilityRefusesAnythingElse(t *testing.T) {
	if _, err := authz.ParseCapability("wizard"); err == nil {
		t.Error("a capability nobody defined was accepted, so a POST naming it " +
			"would look like it granted something")
	}
	got, err := authz.ParseCapability("  finance  ")
	if err != nil || got != authz.Finance {
		t.Errorf("finance did not parse: %v %v", got, err)
	}
}

// The department SQL is unusual enough to be worth its own test: it has to set
// a department, clear one, and refuse a space from another organisation.
func TestTheOrgChartIsSetAndCleared(t *testing.T) {
	f := setup(t)
	caps(t, f)
	ctx := context.Background()

	f.makeSuper(t, f.alice)
	ops := f.space(t, "Operations "+uuid.NewString()[:8])

	// Several at once, which is the whole reason this became a table: Dana
	// is in Marketing, Engineering, Red Team and White Team.
	eng := f.space(t, "Engineering "+uuid.NewString()[:8])
	if err := f.svc.SetDepartments(ctx, f.org, f.alice, f.bob, []string{ops, eng}); err != nil {
		t.Fatalf("set departments: %v", err)
	}
	chart, err := f.svc.StructureFor(ctx, f.org)
	if err != nil {
		t.Fatalf("structure: %v", err)
	}
	if chart[f.bob] == nil || len(chart[f.bob].Departments) != 2 {
		t.Fatalf("bob is in %v, want two departments", chart[f.bob])
	}
	if !chart[f.bob].In(ops) || !chart[f.bob].In(eng) {
		t.Error("one of the departments did not stick")
	}
	for _, d := range chart[f.bob].Departments {
		if d.Name == "" {
			t.Error("the chart does not carry a department name, so the page has to ask again")
		}
	}

	// Replace, not add. A form that can only add has no way to express a
	// removal, and somebody taken off a team stays on it forever.
	if err := f.svc.SetDepartments(ctx, f.org, f.alice, f.bob, []string{eng}); err != nil {
		t.Fatalf("narrow departments: %v", err)
	}
	chart, _ = f.svc.StructureFor(ctx, f.org)
	if chart[f.bob].In(ops) || !chart[f.bob].In(eng) {
		t.Errorf("replacing the set left %v", chart[f.bob].Departments)
	}

	// Clearing is a real state, not an error.
	if err := f.svc.SetDepartments(ctx, f.org, f.alice, f.bob, nil); err != nil {
		t.Fatalf("clear departments: %v", err)
	}
	chart, _ = f.svc.StructureFor(ctx, f.org)
	if len(chart[f.bob].Departments) != 0 {
		t.Error("the departments did not clear")
	}

	// A department belonging to somebody else must not be usable.
	other := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Other')`, other, "o-"+other[:8]); err != nil {
		t.Fatalf("other org: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM spaces WHERE org_id=$1`, other)
		_, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM orgs WHERE id=$1`, other)
	})
	theirs := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO spaces (id, org_id, name, kind, created_by) VALUES ($1,$2,'Theirs','area',$3)`,
		theirs, other, f.alice); err != nil {
		t.Fatalf("their space: %v", err)
	}
	if err := f.svc.SetDepartments(ctx, f.org, f.alice, f.bob, []string{theirs}); err == nil {
		t.Error("somebody was filed under another organisation's department, " +
			"which would route their hours to that tenant's manager")
	}
}

func TestManagersAreAddedAndRemoved(t *testing.T) {
	f := setup(t)
	caps(t, f)
	ctx := context.Background()

	f.makeSuper(t, f.alice)
	ops := f.space(t, "Operations "+uuid.NewString()[:8])

	if err := f.svc.AddManager(ctx, f.org, f.alice, ops, f.carol); err != nil {
		t.Fatalf("add manager: %v", err)
	}
	// Twice is not an error. Somebody pressing a button again is not a fault.
	if err := f.svc.AddManager(ctx, f.org, f.alice, ops, f.carol); err != nil {
		t.Fatalf("adding the same manager twice: %v", err)
	}
	chart, _ := f.svc.StructureFor(ctx, f.org)
	if chart[f.carol] == nil || len(chart[f.carol].Manages) != 1 {
		t.Fatalf("carol manages %v, want one department", chart[f.carol])
	}

	if err := f.svc.RemoveManager(ctx, f.org, f.alice, ops, f.carol); err != nil {
		t.Fatalf("remove manager: %v", err)
	}
	chart, _ = f.svc.StructureFor(ctx, f.org)
	if len(chart[f.carol].Manages) != 0 {
		t.Error("carol still manages it after being removed")
	}

	// A person who does not administer people cannot appoint a manager.
	if err := f.svc.AddManager(ctx, f.org, f.dave, ops, f.dave); !errors.Is(err, authz.ErrDenied) {
		t.Errorf("somebody with no grant appointed themselves a manager: %v", err)
	}
}

func TestNobodyIsTheirOwnApprover(t *testing.T) {
	f := setup(t)
	caps(t, f)
	ctx := context.Background()

	f.makeSuper(t, f.alice)
	if err := f.svc.SetApprover(ctx, f.org, f.alice, f.bob, f.bob); err == nil {
		t.Error("bob was made his own approver, which is not an approval")
	}
	if err := f.svc.SetApprover(ctx, f.org, f.alice, f.bob, f.carol); err != nil {
		t.Fatalf("set approver: %v", err)
	}
	chart, _ := f.svc.StructureFor(ctx, f.org)
	if chart[f.bob].ApproverID != f.carol || chart[f.bob].ApproverName == "" {
		t.Errorf("the approver did not stick: %+v", chart[f.bob])
	}
}

// A super admin sees every document, not only the ones shared with them.
//
// This is the bug Yara hit. "Super admin" was a capability and document
// access is a grant, so the approvals queue listed entries he could not open
// and every list showed him only his own work. The queue said yes, the document
// said no, and both were working exactly as written.
func TestASuperAdminReachesDocumentsNobodySharedWithThem(t *testing.T) {
	f := setup(t)
	caps(t, f)
	ctx := context.Background()

	// dave has no grant on the fixture's document. That is what dave is for.
	before, err := f.svc.SubjectsFor(ctx, f.org, f.dave)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	if before.Super() {
		t.Fatal("somebody with no capability is carrying the super subject")
	}
	lvl, err := f.svc.LevelFor(ctx, before, core.Ref{Type: core.EntityDocument, ID: f.docID})
	if err != nil {
		t.Fatalf("level: %v", err)
	}
	if lvl != core.AccessNone {
		t.Fatalf("dave already had %q on a document nobody shared with him", lvl)
	}

	f.makeSuper(t, f.dave)

	after, err := f.svc.SubjectsFor(ctx, f.org, f.dave)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	if !after.Super() {
		t.Fatal("a super admin is not carrying the super subject, so nothing else here can work")
	}
	lvl, err = f.svc.LevelFor(ctx, after, core.Ref{Type: core.EntityDocument, ID: f.docID})
	if err != nil {
		t.Fatalf("level: %v", err)
	}
	if lvl != core.AccessEdit {
		t.Fatalf("a super admin has %q on a document, want edit. This is the 404: "+
			"the approvals queue lists it and the document page refuses it", lvl)
	}
	// Edit and not admin, on purpose: an agent keeps admin over what it created
	// so its work cannot be deleted by everybody who can reach it.
	if err := f.svc.Require(ctx, after,
		core.Ref{Type: core.EntityDocument, ID: f.docID}, core.AccessAdmin); err == nil {
		t.Error("a super admin has admin on every row, which removes the protection " +
			"on an agent's own work")
	}
	if err := f.svc.Require(ctx, after,
		core.Ref{Type: core.EntityDocument, ID: f.docID}, core.AccessEdit); err != nil {
		t.Errorf("a super admin cannot edit: %v", err)
	}
}

// The super subject is never a grant, and must never be storable as one.
func TestTheSuperSubjectIsNotAGrantAndCannotBeParsedAsOne(t *testing.T) {
	// It is left out of the keys sent to the database, because nothing in
	// grants.subject_key will ever match it.
	subs := core.Subjects{
		{Kind: core.SubjectPrincipal, ID: "p1"},
		{Kind: core.SubjectSuper, ID: "p1"},
	}
	for _, k := range subs.Keys() {
		if k == "super:p1" {
			t.Error("the super subject is being sent to the database as a grant key")
		}
	}
	if len(subs.Keys()) != 1 {
		t.Errorf("keys are %v, want only the principal", subs.Keys())
	}
	// And a row claiming it would be a permanent skeleton key, so reading one
	// back has to fail.
	if _, err := core.ParseSubject("super:p1"); err == nil {
		t.Error("a stored grant to super: parsed successfully, which would be a " +
			"skeleton key that nothing revokes")
	}
}

// A whole department shows as one entry, not once per team inside it; a team
// granted on its own shows too, or granting "only some parts" of a department
// would look like it had granted nothing.
func TestSpaceGrantsListTheTopOfEachGrantedBranch(t *testing.T) {
	f := setup(t)
	caps(t, f)
	ctx := context.Background()
	f.makeSuper(t, f.alice)
	eng := f.space(t, "Engineering")
	child := func(name string) string {
		id := uuid.NewString()
		if _, err := f.pools.Web.Exec(ctx,
			`INSERT INTO spaces (id, org_id, parent_id, name, kind, created_by) VALUES ($1,$2,$3,$4,'folder',$5)`,
			id, f.org, eng, name, f.alice); err != nil {
			t.Fatalf("child space: %v", err)
		}
		return id
	}
	red, _ := child("Red Team"), child("White Team")

	if err := f.svc.GrantSpace(ctx, f.org, f.alice, f.bob, eng); err != nil {
		t.Fatalf("grant department: %v", err)
	}
	if err := f.svc.GrantSpace(ctx, f.org, f.alice, f.carol, red); err != nil {
		t.Fatalf("grant one team: %v", err)
	}

	all, err := f.svc.SpaceGrantsBy(ctx, f.org)
	if err != nil {
		t.Fatal(err)
	}
	if got := all[f.bob]; len(got) != 1 || got[0].Name != "Engineering" {
		t.Errorf("whole department: want just Engineering, got %+v", got)
	}
	if got := all[f.carol]; len(got) != 1 || got[0].Name != "Red Team" {
		t.Errorf("one team on its own: want Red Team, got %+v", got)
	}
}

// Taking a department away takes everything it gave: the folders under it and
// the documents already filed there. It used to remove only the top folder,
// so someone "removed" from a department could still open all of its work.
// Their own documents stay theirs.
func TestRevokingADepartmentTakesBackEverythingInIt(t *testing.T) {
	f := setup(t)
	caps(t, f)
	ctx := context.Background()
	f.makeSuper(t, f.alice)
	eng := f.space(t, "Engineering")
	red := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO spaces (id, org_id, parent_id, name, kind, created_by) VALUES ($1,$2,$3,'Red Team','folder',$4)`,
		red, f.org, eng, f.alice); err != nil {
		t.Fatal(err)
	}
	doc := func(owner string) string {
		id := uuid.NewString()
		if _, err := f.pools.Web.Exec(ctx,
			`INSERT INTO documents (id, org_id, space_id, kind, title, owner_id, created_by, updated_by)
			 VALUES ($1,$2,$3,'doc','x',$4,$4,$4)`, id, f.org, red, owner); err != nil {
			t.Fatal(err)
		}
		return id
	}
	theirs, bobsOwn := doc(f.alice), doc(f.bob)
	t.Cleanup(func() {
		_, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM documents WHERE id IN ($1,$2)`, theirs, bobsOwn)
	})

	if err := f.svc.GrantSpace(ctx, f.org, f.alice, f.bob, eng); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RevokeSpace(ctx, f.org, f.alice, f.bob, eng); err != nil {
		t.Fatal(err)
	}

	count := func(entityType, id string) int {
		var n int
		_ = f.pools.Web.QueryRow(ctx, `SELECT count(*) FROM grants WHERE entity_type=$1 AND entity_id=$2 AND subject_key=$3`,
			entityType, id, "principal:"+f.bob).Scan(&n)
		return n
	}
	if count("space", eng)+count("space", red) != 0 {
		t.Error("a folder under the department is still granted")
	}
	if count("document", theirs) != 0 {
		t.Error("a document in the department is still open to them")
	}
	if count("document", bobsOwn) != 1 {
		t.Error("their own document lost its grant")
	}
	if all, _ := f.svc.SpaceGrantsBy(ctx, f.org); len(all[f.bob]) != 0 {
		t.Errorf("the page still lists access: %+v", all[f.bob])
	}
}
