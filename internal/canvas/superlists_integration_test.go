package canvas_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/canvas"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// A super admin lists every canvas, including ones other people made and nobody shared; a person with no grant lists none.
func TestASuperAdminListsEveryCanvas(t *testing.T) {
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
	org, alice, outsider, admin := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Canvas Test')`, org, "cv-"+org[:8])
	for id, n := range map[string]string{alice: "Alice", outsider: "Outside", admin: "Ada"} {
		exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user',$3)`, id, org, n)
	}
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$2)`, org, admin)
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM capability_grants WHERE org_id=$1`, `DELETE FROM canvases WHERE org_id=$1`} {
			_, _ = pools.Web.Exec(context.Background(), q, org)
		}
	})
	az := authz.New(pools.Web)
	svc := canvas.New(pools.Web, az)
	as, _ := az.SubjectsFor(ctx, org, alice)
	if _, err := svc.Create(ctx, org, as, "Alice's board", nil); err != nil {
		t.Fatal(err)
	}
	adm, _ := az.SubjectsFor(ctx, org, admin)
	out, _ := az.SubjectsFor(ctx, org, outsider)
	if l, err := svc.List(ctx, org, adm); err != nil || len(l) != 1 {
		t.Errorf("a super admin's canvases = %d (%v), want 1", len(l), err)
	}
	if l, _ := svc.List(ctx, org, out); len(l) != 0 {
		t.Errorf("a person with no grant listed %d canvases", len(l))
	}
}
