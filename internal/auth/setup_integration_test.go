package auth_test

// First-run setup, against a real Postgres.
//
// This test exists because of a regression that only the acceptance suite
// caught, and only because it is the one thing in the repository that starts
// from an empty database and claims an installation.
//
// When the admin pages moved onto capabilities, Setup still granted the founder
// nothing. Migration 00028 grants the owner on any database that already had
// users when it ran, which is every existing deployment and no new one. So a
// fresh install produced somebody who could sign in, could not open the People
// page, and therefore could not add a single colleague. Six acceptance checks
// failed on "no second account" and every one of them was this.
//
// Acceptance runs late and takes minutes. This runs in a second.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
)

// freshInstall gives back an empty, migrated database. Empty is the whole
// point: Setup refuses to run against an installation that already has users,
// so this cannot share with anything.
func freshInstall(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := pgtest.Database(t, "hearth_test_auth_setup")
	ctx := context.Background()

	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pools.Close)

	// Between tests in this file, put it back to never-been-set-up.
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM capability_grants`,
			`DELETE FROM local_credentials`,
			`DELETE FROM principals`,
			`DELETE FROM orgs`,
		} {
			_, _ = pools.Web.Exec(ctx, q)
		}
	})
	return pools.Web
}

func TestTheFounderCanActuallyRunTheInstallationTheyJustClaimed(t *testing.T) {
	db := freshInstall(t)
	ctx := context.Background()

	svc := auth.New(db, authz.New(db), false)
	founder, err := svc.Setup(ctx, "Fireball", "Pat Morgan",
		"pat@example.com", "a-long-enough-password")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	var orgID string
	if err := db.QueryRow(ctx, `SELECT id::text FROM orgs LIMIT 1`).Scan(&orgID); err != nil {
		t.Fatalf("read org: %v", err)
	}

	held, err := authz.New(db).Capabilities(ctx, orgID, founder)
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if !held.IsSuperAdmin() {
		t.Fatal("the person who just claimed this installation is not a super admin, " +
			"so they cannot open the People page and cannot add anybody at all")
	}
	// And therefore holds everything a super admin implies, which is what makes
	// the rest of the application reachable on day one.
	for _, c := range authz.Grantable {
		if !held.Has(c) {
			t.Errorf("the founder cannot reach %s", c)
		}
	}
}

func TestSetupRefusesASecondTime(t *testing.T) {
	db := freshInstall(t)
	ctx := context.Background()
	svc := auth.New(db, authz.New(db), false)

	if _, err := svc.Setup(ctx, "Fireball", "Pat", "p@example.com", "a-long-enough-password"); err != nil {
		t.Fatalf("first setup: %v", err)
	}
	if _, err := svc.Setup(ctx, "Someone Else", "Mallory", "m@example.com", "another-long-password"); err == nil {
		t.Fatal("a second setup succeeded, which would hand a stranger their own " +
			"organisation on somebody else's installation")
	}

	// And the refusal left nothing behind.
	var orgs, people int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM orgs`).Scan(&orgs)
	_ = db.QueryRow(ctx, `SELECT count(*) FROM principals WHERE kind='user'`).Scan(&people)
	if orgs != 1 || people != 1 {
		t.Errorf("the refused setup left %d orgs and %d people behind", orgs, people)
	}
}
