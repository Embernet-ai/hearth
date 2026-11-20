package groupsync_test

// Wanted and Run against a real Postgres: who belongs comes from teams and
// departments, a suspended person never does, and a dry run changes nothing.

import (
	"context"
	"log/slog"
	"os"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/groupsync"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

type fakeDir struct {
	have    []groupsync.Member
	added   []string
	removed []string
}

func (f *fakeDir) Members(context.Context, string) ([]groupsync.Member, error) { return f.have, nil }
func (f *fakeDir) Add(_ context.Context, _, e string) error                    { f.added = append(f.added, e); return nil }
func (f *fakeDir) Remove(_ context.Context, _, e string) error {
	f.removed = append(f.removed, e)
	return nil
}

func TestRunFollowsTeamsAndDepartmentsAndSkipsTheSuspended(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	if err := pg.Migrate(ctx, pools, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))); err != nil {
		t.Fatal(err)
	}
	org, tm, sp := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, q, a...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Group Sync Test')`, org, "gs-"+org[:8])
	t.Cleanup(func() {
		c := context.Background()
		for _, q := range []string{
			`DELETE FROM principal_departments WHERE org_id=$1`,
			`DELETE FROM team_members WHERE team_id IN (SELECT id FROM teams WHERE org_id=$1)`,
			`DELETE FROM teams WHERE org_id=$1`,
			`DELETE FROM spaces WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(c, q, org)
		}
	})
	person := func(email, status string) string {
		id := uuid.NewString()
		exec(`INSERT INTO principals (id, org_id, kind, display_name, status) VALUES ($1,$2,'user',$3,$4)`, id, org, email, status)
		exec(`INSERT INTO user_identities (principal_id, issuer, subject, email) VALUES ($1,'gs-test',$2,$3)`, id, id, email)
		return id
	}
	onTeam := person("OnTeam@example.com", "active")
	inDept := person("indept@example.com", "active")
	gone := person("gone@example.com", "suspended")
	person("elsewhere@example.com", "active")
	exec(`INSERT INTO teams (id, org_id, name) VALUES ($1,$2,'Gs Crew')`, tm, org)
	exec(`INSERT INTO spaces (id, org_id, name, kind, created_by) VALUES ($1,$2,'Gs Dept','area',$3)`, sp, org, onTeam)
	for _, p := range []string{onTeam, gone} {
		exec(`INSERT INTO team_members (team_id, principal_id) VALUES ($1,$2)`, tm, p)
	}
	exec(`INSERT INTO principal_departments (org_id, principal_id, space_id) VALUES ($1,$2,$3)`, org, inDept, sp)

	got, err := groupsync.Wanted(ctx, pools.Web, []string{"gs crew"}, []string{"GS DEPT"})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if len(got) != 2 || got[0] != "indept@example.com" || got[1] != "onteam@example.com" {
		t.Fatalf("wanted = %v", got)
	}

	cfg := groupsync.Config{Mappings: []groupsync.Mapping{{Group: "g@example.com", Teams: []string{"Gs Crew"}, Departments: []string{"Gs Dept"}}}}
	d := &fakeDir{have: []groupsync.Member{{Email: "gone@example.com", Role: "MEMBER"}, {Email: "indept@example.com", Role: "MEMBER"}}}
	ch, err := groupsync.Run(ctx, pools.Web, d, cfg, false)
	if err != nil || len(ch) != 2 || len(d.added)+len(d.removed) != 0 {
		t.Fatalf("dry run: %v %v %+v", err, ch, d)
	}
	// Removals are off unless the config says so: an add goes through, the
	// stray is reported as kept.
	ch, err = groupsync.Run(ctx, pools.Web, d, cfg, true)
	if err != nil || len(d.added) != 1 || len(d.removed) != 0 {
		t.Fatalf("additive: %v %+v", err, d)
	}
	skipped := 0
	for _, c := range ch {
		if c.Skipped {
			skipped++
		}
	}
	if skipped != 1 {
		t.Fatalf("one removal should be reported kept: %+v", ch)
	}
	d.added = nil
	cfg.Remove = true
	if _, err := groupsync.Run(ctx, pools.Web, d, cfg, true); err != nil {
		t.Fatal(err)
	}
	if len(d.added) != 1 || d.added[0] != "onteam@example.com" || len(d.removed) != 1 || d.removed[0] != "gone@example.com" {
		t.Fatalf("applied: %+v", d)
	}
	if _, err := groupsync.Run(ctx, pools.Web, d, groupsync.Config{Remove: true, Mappings: []groupsync.Mapping{{Group: "g@example.com", Teams: []string{"No Such Team"}}}}, true); err == nil {
		t.Fatal("a team that does not exist must be refused, not read as an empty group")
	}
	if _, err := groupsync.Run(ctx, pools.Web, d, groupsync.Config{Mappings: []groupsync.Mapping{{Group: "g@example.com"}}}, true); err == nil {
		t.Fatal("a mapping with no team or department must be refused, not empty the group")
	}
}
