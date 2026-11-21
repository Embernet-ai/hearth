package stores_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/emberops/stores"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
)

// Who a person's approver is when the Employee is made: the named approver, else the one manager of their department,
// and nobody when there are two (a link to one of them would shut the other out of deciding).
func TestThePersonRecordNamesTheRightApprover(t *testing.T) {
	dsn := pgtest.Database(t, "hearth_ensure_person_test")
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	if err := pg.Migrate(ctx, pools, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))); err != nil {
		t.Fatal(err)
	}
	db := pools.Web
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	org := uuid.NewString()
	nia, named, solo, duo1, duo2, bot := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	d1, d2 := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1, $2, 'Ensure Co')`, org, "en-"+org[:8])
	for id, n := range map[string]string{nia: "Nia", named: "Named", solo: "Solo", duo1: "Duo1", duo2: "Duo2"} {
		exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1, $2, 'user', $3)`, id, org, n)
		exec(`INSERT INTO user_identities (principal_id, issuer, subject, email) VALUES ($1, 'i', $2, $3)`, id, id, n+"@fireballz.ai")
	}
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1, $2, 'agent', 'Bot')`, bot, org)
	for _, d := range []string{d1, d2} {
		exec(`INSERT INTO spaces (id, org_id, kind, name, created_by) VALUES ($1, $2, 'folder', $3, $4)`, d, org, "Dept "+d[:4], solo)
	}
	src := stores.PGPerson{DB: db}
	approverOf := func(id string) string {
		r, err := src.Person(ctx, org, id)
		if err != nil {
			t.Fatal(err)
		}
		return r.S("approver_id")
	}

	// A department with one manager: that manager.
	exec(`INSERT INTO principal_departments (org_id, principal_id, space_id) VALUES ($1, $2, $3)`, org, nia, d1)
	exec(`INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1, $2, $3)`, org, d1, solo)
	if got := approverOf(nia); got != solo {
		t.Fatalf("one manager: approver %q, want that manager", got)
	}
	// A named approver wins over the department.
	exec(`UPDATE principals SET approver_id = $2 WHERE id = $1`, nia, named)
	if got := approverOf(nia); got != named {
		t.Fatalf("named approver: %q", got)
	}
	// Two managers and no named approver: nobody.
	exec(`UPDATE principals SET approver_id = NULL WHERE id = $1`, nia)
	exec(`INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1, $2, $3)`, org, d1, duo1)
	if got := approverOf(nia); got != "" {
		t.Fatalf("two managers: approver %q, want none", got)
	}
	// Somebody who manages their own department is not their own approver.
	exec(`INSERT INTO principal_departments (org_id, principal_id, space_id) VALUES ($1, $2, $3)`, org, solo, d1)
	if got := approverOf(solo); got == solo {
		t.Fatal("a manager is their own approver")
	}
	// A person with no email is an agent and is read as such.
	if r, err := src.Person(ctx, org, bot); err != nil || r.Has("email") {
		t.Fatalf("bot: %v %v", r, err)
	}
	_ = duo2
}
