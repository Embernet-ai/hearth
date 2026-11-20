package playground_test

// A playground against a real Postgres: built from seed.go through the real
// services, walled off from the real organisation, and gone without a trace
// on reset -- while the real organisation keeps every row it had.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
	"github.com/Embernet-ai/hearth/internal/playground"
	"github.com/Embernet-ai/hearth/internal/timelog"
	"github.com/Embernet-ai/hearth/internal/timeoff"
)

func TestAPlaygroundIsBuiltWalledOffAndResetCleanly(t *testing.T) {
	dsn := pgtest.Database(t, "hearth_playground_test")
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
	az := authz.New(db)
	pl := plant.New(db, az)
	dc := docs.New(db, az)
	svc := playground.New(db, az, pl, dc, channels.New(db, az), timelog.New(db, az), timeoff.New(db, nil))

	// The real organisation, with a super admin who signs in by password and
	// one piece of real work. None of it may change.
	realOrg, real := uuid.NewString(), uuid.NewString()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1, 'real-co', 'Real Co')`, realOrg)
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1, $2, 'user', 'Real Person')`, real, realOrg)
	exec(`INSERT INTO local_credentials (principal_id, org_id, email, password_hash) VALUES ($1, $2, 'real@example.com', 'x')`, real, realOrg)
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1, $2, 'super_admin', $2)`, realOrg, real)
	realActor, err := az.SubjectsFor(ctx, realOrg, real)
	if err != nil {
		t.Fatal(err)
	}
	realSpace, err := pl.CreateSpace(ctx, realOrg, realActor, "Real Department", plant.KindFolder, nil)
	if err != nil {
		t.Fatal(err)
	}
	realTask, err := dc.Create(ctx, realOrg, realActor, docs.CreateParams{Kind: docs.KindTask, Title: "Real work", SpaceID: &realSpace.ID,
		Task: &docs.TaskFields{Status: docs.StatusTodo}})
	if err != nil {
		t.Fatal(err)
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	realRows := func() int {
		return count(`SELECT (SELECT count(*) FROM principals WHERE org_id = $1) + (SELECT count(*) FROM documents WHERE org_id = $1)
			+ (SELECT count(*) FROM spaces WHERE org_id = $1) + (SELECT count(*) FROM grants WHERE org_id = $1)`, realOrg)
	}
	before := realRows()

	orgID, self, err := svc.Enter(ctx, real, "Real Person")
	if err != nil {
		t.Fatalf("enter: %v", err)
	}
	if again, self2, err := svc.Enter(ctx, real, "Real Person"); err != nil || again != orgID || self2 != self {
		t.Fatalf("entering again built a second playground (%s/%s vs %s/%s, %v)", again, self2, orgID, self, err)
	}
	if !svc.IsPlayground(ctx, orgID) || svc.IsPlayground(ctx, realOrg) {
		t.Fatal("IsPlayground has the two organisations the wrong way round")
	}

	// Nothing to sign in with, and never a super admin.
	if n := count(`SELECT count(*) FROM local_credentials WHERE org_id = $1`, orgID); n != 0 {
		t.Fatalf("a playground holds %d passwords; no login path may ever arrive in one", n)
	}
	if n := count(`SELECT count(*) FROM user_identities ui JOIN principals p ON p.id = ui.principal_id WHERE p.org_id = $1`, orgID); n != 0 {
		t.Fatalf("a playground holds %d sign-in identities", n)
	}
	if n := count(`SELECT count(*) FROM capability_grants WHERE org_id = $1 AND capability = 'super_admin'`, orgID); n != 0 {
		t.Fatal("the playground self is a super admin, whose checks ignore the organisation")
	}
	if n := count(`SELECT count(*) FROM capability_grants WHERE org_id = $1 AND principal_id = $2 AND capability = 'people'`, orgID, self); n != 1 {
		t.Fatal("the playground self cannot manage people, so the admin lessons cannot be done")
	}
	var owner string
	if err := db.QueryRow(ctx, `SELECT id::text FROM principals WHERE org_id = $1 AND kind = 'user' ORDER BY created_at, id LIMIT 1`, orgID).Scan(&owner); err != nil || owner != self {
		t.Fatalf("the playground self is not its owner (owner %s, self %s, %v)", owner, self, err)
	}

	// Everything in seed.go arrived.
	if n := count(`SELECT count(*) FROM principals WHERE org_id = $1`, orgID); n < 5 {
		t.Fatalf("only %d people in the playground", n)
	}
	if n := count(`SELECT count(*) FROM documents WHERE org_id = $1 AND kind = 'task' AND title LIKE 'Lesson%'`, orgID); n < 5 {
		t.Fatalf("only %d lessons", n)
	}
	if n := count(`SELECT count(*) FROM documents d JOIN checklist_items c ON c.document_id = d.id WHERE d.org_id = $1`, orgID); n < 20 {
		t.Fatalf("only %d checklist steps", n)
	}
	if n := count(`SELECT count(*) FROM messages WHERE org_id = $1`, orgID); n == 0 {
		t.Fatal("no comments in the playground")
	}
	if n := count(`SELECT count(*) FROM time_off_requests WHERE org_id = $1 AND status = 'pending'`, orgID); n == 0 {
		t.Fatal("no time-off request waiting for the managers' lesson")
	}
	if n := count(`SELECT count(*) FROM time_entries WHERE org_id = $1`, orgID); n == 0 {
		t.Fatal("no hours in the playground")
	}

	// The wall: no grant inside the playground names anything real.
	if n := count(`SELECT count(*) FROM grants WHERE org_id = $1 AND entity_id = $2`, orgID, realTask.ID); n != 0 {
		t.Fatal("the playground was given access to real work")
	}
	// Filing playground work into a real department is refused.
	selfActor, err := az.SubjectsFor(ctx, orgID, self)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dc.Create(ctx, orgID, selfActor, docs.CreateParams{Kind: docs.KindTask, Title: "Sneaky", SpaceID: &realSpace.ID,
		Task: &docs.TaskFields{Status: docs.StatusTodo}}); !errors.Is(err, docs.ErrNotFound) {
		t.Fatalf("a playground filed work into a real department: %v", err)
	}

	// Purge refuses the real organisation, whatever happens.
	if err := svc.Purge(ctx, realOrg); !errors.Is(err, playground.ErrNotPlayground) {
		t.Fatalf("purge of the real organisation was not refused: %v", err)
	}

	// Reset: every row of the playground goes, in every table that has one.
	if err := svc.Reset(ctx, real); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if n := count(`SELECT count(*) FROM orgs WHERE id = $1`, orgID); n != 0 {
		t.Fatal("the playground organisation is still there")
	}
	rows, err := db.Query(ctx, `SELECT c.relname FROM pg_class c JOIN pg_attribute a ON a.attrelid = c.oid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind = 'r' AND a.attname = 'org_id' AND NOT a.attisdropped`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		tables = append(tables, name)
	}
	rows.Close()
	for _, tbl := range tables {
		if n := count(`SELECT count(*) FROM `+tbl+` WHERE org_id = $1`, orgID); n != 0 {
			t.Errorf("%d rows left in %s after reset", n, tbl)
		}
	}
	if after := realRows(); after != before {
		t.Fatalf("the real organisation changed: %d rows before, %d after", before, after)
	}

	// And the next visit builds a fresh one.
	fresh, _, err := svc.Enter(ctx, real, "Real Person")
	if err != nil || fresh == orgID {
		t.Fatalf("no fresh playground after reset (%s, %v)", fresh, err)
	}
}
