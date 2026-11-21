package main

import (
	"bytes"
	"context"
	"io"
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

// person-suspend finds an account by principal id when its sign-in identity is
// gone (the case after a throwaway's identity was deleted), so suspending it needs
// no SQL. Nothing changes without apply, one unknown id stops the whole run, and a
// principal of another organisation is not found.
func TestPersonSuspendFindsAnAccountByPrincipalID(t *testing.T) {
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
	org, other := uuid.NewString(), uuid.NewString()
	admin, one, two, stranger := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Suspend A')`, org, "sa-"+org[:8])
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Suspend B')`, other, "sb-"+other[:8])
	for id, n := range map[string]string{admin: "Admin", one: "One", two: "Two"} {
		exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user',$3)`, id, org, n)
	}
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Stranger')`, stranger, other)
	exec(`INSERT INTO local_credentials (principal_id, org_id, email, password_hash) VALUES ($1,$2,$3,'x')`, admin, org, "admin-"+org[:8]+"@example.test")
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$2)`, org, admin)
	t.Cleanup(func() {
		for _, q := range []string{`SELECT audit_purge_org($1)`, `DELETE FROM sessions WHERE org_id=$1`, `DELETE FROM capability_grants WHERE org_id=$1`,
			`DELETE FROM local_credentials WHERE org_id=$1`, `DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`} {
			_, _ = db.Exec(context.Background(), q, org)
		}
		for _, q := range []string{`DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`} {
			_, _ = db.Exec(context.Background(), q, other)
		}
	})
	status := func(id string) string {
		var s string
		if err := db.QueryRow(ctx, `SELECT status FROM principals WHERE id=$1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// Dry run: named, not changed.
	var out bytes.Buffer
	if n, err := suspendPeople(ctx, pools, org, admin, nil, []string{one}, false, &out); err != nil || n != 1 || status(one) != "active" {
		t.Fatalf("dry run: n=%d err=%v status=%s", n, err, status(one))
	}
	if !strings.Contains(out.String(), "would suspend") || !strings.Contains(out.String(), one) {
		t.Errorf("dry run output = %q", out.String())
	}
	// An unknown id, and another organisation's, stop the run before anything changes.
	for _, bad := range []string{uuid.NewString(), stranger, "not-a-uuid"} {
		if _, err := suspendPeople(ctx, pools, org, admin, nil, []string{one, bad}, true, io.Discard); err == nil {
			t.Errorf("%s: no error", bad)
		}
		if status(one) != "active" {
			t.Fatalf("a refused run changed %s", one)
		}
	}
	if status(stranger) != "active" {
		t.Error("another organisation's principal was touched")
	}
	// Apply by id, both accounts, with no sign-in identity anywhere.
	if n, err := suspendPeople(ctx, pools, org, admin, nil, []string{one, two}, true, io.Discard); err != nil || n != 2 {
		t.Fatalf("apply: n=%d err=%v", n, err)
	}
	if status(one) != "suspended" || status(two) != "suspended" {
		t.Errorf("statuses = %s, %s", status(one), status(two))
	}
	var audits int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE org_id=$1 AND action='person.status'`, org).Scan(&audits)
	if audits != 2 {
		t.Errorf("%d audit rows, want one each", audits)
	}
	// An account already suspended is not an active person any more.
	if _, err := suspendPeople(ctx, pools, org, admin, nil, []string{one}, true, io.Discard); err == nil {
		t.Error("suspended an account twice")
	}
}
