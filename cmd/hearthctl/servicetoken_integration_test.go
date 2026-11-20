package main

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// A service gets its own agent principal on the named team and a token that can read and cannot write.
func TestServiceTokenMakesAReadOnlyAgentOnTheTeam(t *testing.T) {
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
	db := pools.Web
	org, admin, plain, team := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Svc Token Test')`, org, "svc-"+org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Admin'),($3,$2,'user','Plain')`, admin, org, plain)
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$2)`, org, admin)
	exec(`INSERT INTO teams (id, org_id, name) VALUES ($1,$2,'Sales')`, team, org)
	t.Cleanup(func() {
		for _, q := range []string{
			`SELECT audit_purge_org($1)`, `DELETE FROM api_tokens WHERE org_id=$1`, `DELETE FROM team_members WHERE team_id IN (SELECT id FROM teams WHERE org_id=$1)`,
			`DELETE FROM teams WHERE org_id=$1`, `DELETE FROM capability_grants WHERE org_id=$1`, `DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = db.Exec(context.Background(), q, org)
		}
	})

	if _, _, err := makeServiceToken(ctx, pools, org, plain, "phone-bridge", "Sales", 0); err == nil {
		t.Fatal("somebody who is not a super admin made a service token")
	}
	secret, svc, err := makeServiceToken(ctx, pools, org, admin, "phone-bridge", "Sales", 0)
	if err != nil || secret == "" {
		t.Fatalf("make: %q %v", secret, err)
	}
	var kind string
	var onTeam bool
	var scopes []string
	if err := db.QueryRow(ctx, `SELECT kind FROM principals WHERE id=$1`, svc).Scan(&kind); err != nil || kind != "agent" {
		t.Fatalf("service principal kind = %q %v", kind, err)
	}
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM team_members WHERE team_id=$1 AND principal_id=$2)`, team, svc).Scan(&onTeam); err != nil || !onTeam {
		t.Fatalf("service is not on the team: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT scopes FROM api_tokens WHERE principal_id=$1 ORDER BY created_at DESC LIMIT 1`, svc).Scan(&scopes); err != nil ||
		len(scopes) != 1 || scopes[0] != "read" {
		t.Fatalf("token scopes = %v %v", scopes, err)
	}
	// Run again: the same service, a new token.
	_, again, err := makeServiceToken(ctx, pools, org, admin, "phone-bridge", "Sales", 0)
	if err != nil || again != svc {
		t.Fatalf("second run: %q vs %q %v", again, svc, err)
	}
	if _, _, err := makeServiceToken(ctx, pools, org, admin, "phone-bridge", "No Such Team", 0); err == nil {
		t.Fatal("a team that does not exist was accepted")
	}
}
