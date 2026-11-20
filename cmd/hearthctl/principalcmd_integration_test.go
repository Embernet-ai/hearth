package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// channel-remove shows what it would do and changes nothing without -apply, then archives (keeping every message) with one
// audit row naming the admin and the reason, and a short id that matches more than one channel is refused.
func TestChannelRemoveDryRunThenArchive(t *testing.T) {
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
	org, admin, ch := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Chan Remove Test')`, org, "cr-"+org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Admin')`, admin, org)
	exec(`INSERT INTO channels (id, org_id, slug, name, visibility, created_by) VALUES ($1,$2,$3,'proof','private',$4)`, ch, org, "proof-"+ch[:8], admin)
	t.Cleanup(func() {
		for _, q := range []string{`SELECT audit_purge_org($1)`, `DELETE FROM channels WHERE org_id=$1`, `DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`} {
			_, _ = db.Exec(context.Background(), q, org)
		}
	})
	archived := func() bool {
		var b bool
		if err := db.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM channels WHERE id=$1`, ch).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	audits := func() int {
		var n int
		_ = db.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE org_id=$1 AND action='channel.archive'`, org).Scan(&n)
		return n
	}

	if err := removeChannel(ctx, pools, org, admin, ch, "proof cleanup", false); err != nil {
		t.Fatal(err)
	}
	if archived() || audits() != 0 {
		t.Fatal("a dry run changed something")
	}
	if err := removeChannel(ctx, pools, org, admin, "zzzz", "x", true); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("an id that matches nothing was accepted: %v", err)
	}
	if err := removeChannel(ctx, pools, org, admin, ch[:8], "proof cleanup", true); err != nil {
		t.Fatal(err)
	}
	if !archived() || audits() != 1 {
		t.Fatalf("archived=%v audits=%d, want archived with one audit row", archived(), audits())
	}
	if err := removeChannel(ctx, pools, org, admin, ch, "again", true); err == nil {
		t.Fatal("an archived channel was archived twice")
	}
}
