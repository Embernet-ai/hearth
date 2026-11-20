package teams_test

// Teams, against a real Postgres.
//
// The claim is that a team is a grant subject and nothing more, so the test
// that matters is not about this package at all: it is that somebody in a team
// can see what the team was granted, and cannot the moment they are taken out.
// That runs through the authz service, which is the point.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/teams"
)

type fx struct {
	svc   *teams.Service
	az    *authz.Service
	docs  *docs.Service
	pools *pg.Pools
	org   string
	dana  string
	sam   string
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
		svc: teams.New(pools.Web), az: az, docs: docs.New(pools.Web, az),
		pools: pools, org: uuid.NewString(),
		dana: uuid.NewString(), sam: uuid.NewString(),
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Teams Test')`,
		f.org, "tm-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Dana')`,
		f.dana, f.org)
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Sam')`,
		f.sam, f.org)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM team_members WHERE team_id IN (SELECT id FROM teams WHERE org_id=$1)`,
			`DELETE FROM teams WHERE org_id=$1`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

func (f *fx) subjects(t *testing.T, principal string) core.Subjects {
	t.Helper()
	s, err := f.az.SubjectsFor(context.Background(), f.org, principal)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	return s
}

// The whole feature, in one test.
//
// A team is granted a machine, somebody in the team can see it, and the moment
// they are taken out of the team they cannot. Nothing here re-implements
// permissions: the grant is the same statement a person's grant is, and the
// answer comes from the same MAX over the subject set.
func TestATeamIsAGrantSubjectAndRemovalTakesItAway(t *testing.T) {
	f, ctx := setup(t), context.Background()
	dana := f.subjects(t, f.dana)

	machine, err := f.docs.Create(ctx, f.org, dana, docs.CreateParams{
		Kind: docs.KindAsset, Title: "Conveyor 3",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Sam cannot see it. Nothing has been shared with anybody.
	if _, err := f.docs.Get(ctx, f.org, f.subjects(t, f.sam), machine.ID); !errors.Is(err, docs.ErrNotFound) {
		t.Fatalf("somebody with no grant could read it: %v", err)
	}

	team, err := f.svc.Create(ctx, f.org, f.dana, "Line 3 maintenance", "the night shift")
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	if err := f.svc.Add(ctx, f.org, f.dana, team.ID, f.sam); err != nil {
		t.Fatalf("add member: %v", err)
	}
	// The team subject is in Sam's set now, without Sam signing in again. That
	// is what makes the directory sync work: subjects are resolved per request.
	if !hasTeam(f.subjects(t, f.sam), team.ID) {
		t.Fatal("the team is not in the member's subject set")
	}
	// And still nothing: being in a team grants nothing until the team is
	// granted something.
	if _, err := f.docs.Get(ctx, f.org, f.subjects(t, f.sam), machine.ID); !errors.Is(err, docs.ErrNotFound) {
		t.Fatal("joining a team granted access to something nobody shared with it")
	}

	if err := f.docs.Share(ctx, f.org, dana, machine.ID,
		team.Subject(), core.AccessEdit, f.dana); err != nil {
		t.Fatalf("share: %v", err)
	}
	got, err := f.docs.Get(ctx, f.org, f.subjects(t, f.sam), machine.ID)
	if err != nil {
		t.Fatalf("somebody in the team could not read what the team was granted: %v", err)
	}
	if got.Title != "Conveyor 3" {
		t.Errorf("read back %q", got.Title)
	}

	// The half that matters on the day somebody leaves.
	if err := f.svc.Remove(ctx, f.org, f.dana, team.ID, f.sam); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	if hasTeam(f.subjects(t, f.sam), team.ID) {
		t.Fatal("the team is still in their subject set after they were removed")
	}
	if _, err := f.docs.Get(ctx, f.org, f.subjects(t, f.sam), machine.ID); !errors.Is(err, docs.ErrNotFound) {
		t.Fatalf("removing somebody from a team left their access behind: %v", err)
	}
}

// Deleting a team takes its grants with it. A grant naming a subject that no
// longer exists is a row nothing will ever match and noise in the table an
// auditor reads.
func TestDeletingATeamTakesItsGrants(t *testing.T) {
	f, ctx := setup(t), context.Background()
	dana := f.subjects(t, f.dana)

	machine, err := f.docs.Create(ctx, f.org, dana, docs.CreateParams{
		Kind: docs.KindAsset, Title: "Press 2",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	team, err := f.svc.Create(ctx, f.org, f.dana, "Presses", "")
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	if err := f.docs.Share(ctx, f.org, dana, machine.ID,
		team.Subject(), core.AccessView, f.dana); err != nil {
		t.Fatalf("share: %v", err)
	}

	if err := f.svc.Delete(ctx, f.org, f.dana, team.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var left int
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT count(*) FROM grants WHERE org_id=$1 AND subject_key=$2`,
		f.org, team.Subject().Key()).Scan(&left); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if left != 0 {
		t.Errorf("%d grants outlived the team that held them", left)
	}
}

// A team the directory owns is changed in the directory. Letting it be edited
// here produces a team that disagrees with the group it mirrors until the next
// sync silently undoes the change, which is worse than being told no.
func TestADirectoryTeamIsNotEditedByHand(t *testing.T) {
	f, ctx := setup(t), context.Background()

	team, err := f.svc.CreateManaged(ctx, f.org, "", "Everyone", "group-42")
	if err != nil {
		t.Fatalf("create managed: %v", err)
	}
	if !team.Managed() {
		t.Fatal("a directory team does not know it is one")
	}
	if err := f.svc.Rename(ctx, f.org, f.dana, team.ID, "Everybody"); !errors.Is(err, teams.ErrManaged) {
		t.Errorf("renaming a directory team by hand gave %v", err)
	}
	// The sync's own rename is allowed, because it is the directory that
	// renamed the group.
	if err := f.svc.RenameManaged(ctx, f.org, "", team.ID, "Everybody"); err != nil {
		t.Errorf("the directory could not rename its own group: %v", err)
	}
	// And it is found by the group's id rather than by its name, which is what
	// makes a rename there not create a second team here.
	found, err := f.svc.ByExternalID(ctx, f.org, "group-42")
	if err != nil || found.ID != team.ID {
		t.Errorf("the team was not found by the directory's id: %v", err)
	}
	if found.Name != "Everybody" {
		t.Errorf("the rename did not land: %q", found.Name)
	}
}

// SetMembers is what a directory sync uses, and it has to end with exactly the
// list it was given rather than with the union of that list and what was there.
func TestSettingMembershipReplacesItRatherThanAddingToIt(t *testing.T) {
	f, ctx := setup(t), context.Background()

	team, err := f.svc.CreateManaged(ctx, f.org, "", "Maintenance", "group-7")
	if err != nil {
		t.Fatalf("create managed: %v", err)
	}
	if err := f.svc.Add(ctx, f.org, "", team.ID, f.dana); err != nil {
		t.Fatalf("add: %v", err)
	}

	// The directory now says the group is Sam and only Sam.
	added, removed, err := f.svc.SetMembers(ctx, f.org, "", team.ID, []string{f.sam})
	if err != nil {
		t.Fatalf("set members: %v", err)
	}
	if added != 1 || removed != 1 {
		t.Errorf("added %d and removed %d, want one of each", added, removed)
	}
	members, err := f.svc.Members(ctx, f.org, team.ID)
	if err != nil {
		t.Fatalf("members: %v", err)
	}
	if len(members) != 1 || members[0].PrincipalID != f.sam {
		t.Fatalf("the team is %+v, want exactly Sam", members)
	}

	// A second identical sync changes nothing and says so, so a page of audit
	// entries does not appear every five minutes for a group nobody touched.
	added, removed, err = f.svc.SetMembers(ctx, f.org, "", team.ID, []string{f.sam})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if added != 0 || removed != 0 {
		t.Errorf("an unchanged sync reported %d added and %d removed", added, removed)
	}

	// And a stranger from another tenant is not put in a team here, however
	// confidently the directory names them.
	if _, _, err := f.svc.SetMembers(ctx, f.org, "", team.ID,
		[]string{f.sam, uuid.NewString()}); err != nil {
		t.Fatalf("set members with a stranger: %v", err)
	}
	members, _ = f.svc.Members(ctx, f.org, team.ID)
	if len(members) != 1 {
		t.Errorf("%d members after a sync naming somebody who is not here", len(members))
	}
}

func TestATeamNeedsANameAndCannotShareOne(t *testing.T) {
	f, ctx := setup(t), context.Background()

	if _, err := f.svc.Create(ctx, f.org, f.dana, "   ", ""); !errors.Is(err, teams.ErrInvalid) {
		t.Errorf("a team with no name was accepted: %v", err)
	}
	if _, err := f.svc.Create(ctx, f.org, f.dana, "Fitters", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Case insensitive, because two teams called Fitters and fitters is two
	// teams nobody can tell apart on a page.
	if _, err := f.svc.Create(ctx, f.org, f.dana, "fitters", ""); !errors.Is(err, teams.ErrInvalid) {
		t.Errorf("a second team with the same name was accepted: %v", err)
	}
}

func hasTeam(subjects core.Subjects, id string) bool {
	for _, s := range subjects {
		if s.Kind == core.SubjectTeam && s.ID == id {
			return true
		}
	}
	return false
}

// A directory sync that still lists somebody who has been suspended must not
// put them back. The suspension took them off every team; the directory's
// group lagging behind is exactly how that would be undone by the next sync.
func TestADirectorySyncDoesNotBringASuspendedAccountBack(t *testing.T) {
	f, ctx := setup(t), context.Background()
	team, err := f.svc.CreateManaged(ctx, f.org, "", "Sales", "group-sales")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pools.Web.Exec(ctx, `UPDATE principals SET status='suspended' WHERE id=$1`, f.sam); err != nil {
		t.Fatal(err)
	}
	added, _, err := f.svc.SetMembers(ctx, f.org, "", team.ID, []string{f.dana, f.sam})
	if err != nil {
		t.Fatal(err)
	}
	members, _ := f.svc.Members(ctx, f.org, team.ID)
	if added != 1 || len(members) != 1 || members[0].PrincipalID != f.dana {
		t.Errorf("sync added %d, team is %+v, want Dana only", added, members)
	}
}
