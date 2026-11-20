package audit_test

// The audit log, against a real Postgres.
//
// Almost everything worth testing here is a property of the table rather than
// of the code around it. "We only ever insert" is a claim about today's code
// and an auditor is right to disbelieve it; a trigger that refuses is a claim
// about the data, and this is where it is watched refusing.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

type fx struct {
	svc   *audit.Service
	pools *pg.Pools
	org   string
	dana  string
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

	f := &fx{svc: audit.New(pools.Web), pools: pools,
		org: uuid.NewString(), dana: uuid.NewString()}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Audit Test')`,
		f.org, "au-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Dana')`,
		f.dana, f.org)

	t.Cleanup(func() {
		ctx := context.Background()
		// The audit log refuses an ordinary delete, so it takes the one
		// deliberate way out. That function removes a whole org and nothing
		// finer, and nothing outside a fixture calls it.
		_, _ = pools.Web.Exec(ctx, `SELECT audit_purge_org($1)`, f.org)
		_, _ = pools.Web.Exec(ctx, `DELETE FROM principals WHERE org_id=$1`, f.org)
		_, _ = pools.Web.Exec(ctx, `DELETE FROM orgs WHERE id=$1`, f.org)
	})
	return f
}

// The claim the whole table exists to support.
func TestTheLogRefusesToBeRewritten(t *testing.T) {
	f, ctx := setup(t), context.Background()

	if err := audit.Record(ctx, f.pools.Web, audit.Entry{
		OrgID: f.org, ActorID: f.dana, Action: audit.ActionDocumentDelete,
		EntityType: "document", EntityID: uuid.NewString(),
		Before: map[string]any{"title": "Conveyor 3"},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	var id int64
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT id FROM audit_log WHERE org_id=$1`, f.org).Scan(&id); err != nil {
		t.Fatalf("read back: %v", err)
	}

	// Changing what it says. There is no way out of this one at all, because
	// there is no legitimate reason to rewrite what a row says happened.
	_, err := f.pools.Web.Exec(ctx,
		`UPDATE audit_log SET action='nothing.happened' WHERE id=$1`, id)
	if err == nil {
		t.Error("an update was accepted, so the log is a record of what somebody " +
			"most recently decided it should say")
	} else if !strings.Contains(err.Error(), "append only") {
		t.Errorf("the update was refused with %v, and the message has to say why", err)
	}

	// Making it disappear.
	if _, err := f.pools.Web.Exec(ctx, `DELETE FROM audit_log WHERE id=$1`, id); err == nil {
		t.Error("a delete was accepted, so the one row somebody would rather " +
			"nobody saw can be removed")
	}

	// And the way round a row trigger.
	if _, err := f.pools.Web.Exec(ctx, `TRUNCATE audit_log`); err == nil {
		t.Error("a truncate was accepted, which makes the row triggers theatre")
	}

	// After all of that, it is still there and still says what it said.
	var action string
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT action FROM audit_log WHERE id=$1`, id).Scan(&action); err != nil {
		t.Fatalf("the row is gone: %v", err)
	}
	if action != audit.ActionDocumentDelete {
		t.Errorf("the row now says %q", action)
	}
}

// The one legal way out exists so that removing an organisation is possible.
// It takes an org and nothing finer, on purpose.
func TestAWholeOrganisationCanBeRemovedAndNothingSmaller(t *testing.T) {
	f, ctx := setup(t), context.Background()

	for i := 0; i < 3; i++ {
		if err := audit.Record(ctx, f.pools.Web, audit.Entry{
			OrgID: f.org, ActorID: f.dana, Action: audit.ActionGrantAdd,
			EntityType: "document", EntityID: uuid.NewString(),
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	var removed int64
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT audit_purge_org($1)`, f.org).Scan(&removed); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if removed != 3 {
		t.Errorf("purged %d rows, want 3", removed)
	}
	if n, err := f.svc.Count(ctx, f.org); err != nil || n != 0 {
		t.Errorf("%d rows left (%v)", n, err)
	}

	// And the setting it uses does not survive the call, so a later statement
	// on the same connection cannot ride on it.
	if err := audit.Record(ctx, f.pools.Web, audit.Entry{
		OrgID: f.org, ActorID: f.dana, Action: audit.ActionGrantAdd,
	}); err != nil {
		t.Fatalf("record after purge: %v", err)
	}
	if _, err := f.pools.Web.Exec(ctx,
		`DELETE FROM audit_log WHERE org_id=$1`, f.org); err == nil {
		t.Error("the purge left the escape hatch open, so an ordinary delete " +
			"now works on the same connection")
	}
}

// An entry has to survive being read back as the page reads it, including the
// two fields that are legitimately absent.
func TestWhatIsRecordedIsWhatComesBack(t *testing.T) {
	f, ctx := setup(t), context.Background()
	entity := uuid.NewString()

	if err := audit.Record(ctx, f.pools.Web, audit.Entry{
		OrgID: f.org, ActorID: f.dana, Action: audit.ActionDocumentDelete,
		EntityType: "document", EntityID: entity,
		Before: map[string]any{"title": "Conveyor 3", "kind": "asset"},
		IP:     "10.0.1.56",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// Nobody behind it, which is a fact rather than a gap.
	if err := audit.Record(ctx, f.pools.Web, audit.Entry{
		OrgID: f.org, Action: audit.ActionPersonAdd,
		EntityType: "principal", EntityID: uuid.NewString(),
		After: map[string]any{"by": "directory"},
	}); err != nil {
		t.Fatalf("record automatic: %v", err)
	}

	rows, err := f.svc.List(ctx, f.org, audit.Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	// Newest first.
	if rows[0].Action != audit.ActionPersonAdd {
		t.Errorf("the newest row is %q", rows[0].Action)
	}
	if rows[0].ActorID != nil {
		t.Error("an entry with nobody behind it came back with an actor")
	}

	deletion := rows[1]
	if deletion.ActorID == nil || *deletion.ActorID != f.dana {
		t.Error("the deletion does not name who did it")
	}
	if deletion.ActorName != "Dana" {
		t.Errorf("the actor's name is %q, and a page showing a uuid is a page "+
			"nobody reads", deletion.ActorName)
	}
	if !strings.Contains(deletion.Before, "Conveyor 3") {
		t.Errorf("before is %q, and the title is the first thing anybody asks "+
			"about a deletion", deletion.Before)
	}
	if deletion.IP != "10.0.1.56" {
		t.Errorf("the address is %q", deletion.IP)
	}

	// Filters compose, which is the whole of what the page's controls do.
	only, err := f.svc.List(ctx, f.org, audit.Filter{
		Action: audit.ActionDocumentDelete, ActorID: f.dana, EntityID: entity,
	})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if len(only) != 1 {
		t.Errorf("%d rows for one action by one person about one thing", len(only))
	}

	// And a since that excludes everything returns nothing rather than
	// everything, which is the way a date filter usually breaks.
	tomorrow := time.Now().Add(24 * time.Hour)
	none, err := f.svc.List(ctx, f.org, audit.Filter{Since: &tomorrow})
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("%d rows from tomorrow", len(none))
	}
}

// An entry with no action says nothing and is refused before it reaches the
// table, where the constraint would refuse it with a message about a check.
func TestAnEntryWithNoActionIsRefused(t *testing.T) {
	f, ctx := setup(t), context.Background()
	err := audit.Record(ctx, f.pools.Web, audit.Entry{OrgID: f.org, ActorID: f.dana})
	if err == nil {
		t.Fatal("an entry with no action was accepted")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("the wrong error entirely")
	}
}
