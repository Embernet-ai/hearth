package timelog_test

// Removing time leaves a record, on every path, and the admin removal is one
// transaction a super admin makes with a reason.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/timelog"
)

func (f *fx) removalRows(t *testing.T, entryID string) (n int, actor, via string) {
	t.Helper()
	err := f.pools.Web.QueryRow(context.Background(), `
		SELECT count(*), COALESCE(max(actor_id::text),''), COALESCE(max(after->>'via'),'')
		  FROM audit_log WHERE entity_id=$1 AND action='time.remove'`, entryID).Scan(&n, &actor, &via)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func (f *fx) logFor(t *testing.T, who, note string) string {
	t.Helper()
	e, err := f.svc.LogOwn(context.Background(), f.org, f.subj(t, who), "", 30, time.Now().Add(-time.Hour), note)
	if err != nil {
		t.Fatal(err)
	}
	return e.ID
}

func TestRemovingAnEntryWritesAnAuditRowOnceNamingWhoDidIt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.logFor(t, f.alice, "to be removed")
	if err := f.svc.Remove(ctx, f.org, f.subj(t, f.alice), id); err != nil {
		t.Fatal(err)
	}
	if n, actor, via := f.removalRows(t, id); n != 1 || actor != f.alice || via != "remove" {
		t.Fatalf("want one time.remove row by Alice via remove, got n=%d actor=%s via=%s", n, actor, via)
	}
	// A second click on a stale page agrees the entry is gone and adds nothing.
	if err := f.svc.Remove(ctx, f.org, f.subj(t, f.alice), id); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := f.removalRows(t, id); n != 1 {
		t.Fatalf("a repeat removal wrote %d rows, want 1", n)
	}
	// Somebody refused the removal leaves no row either.
	other := f.logFor(t, f.alice, "bob may not")
	if err := f.svc.Remove(ctx, f.org, f.subj(t, f.bob), other); err == nil {
		t.Fatal("bob removed alice's entry")
	}
	if n, _, _ := f.removalRows(t, other); n != 0 {
		t.Fatalf("a refused removal wrote %d rows", n)
	}
}

func TestClearingImportedEntriesIsAudited(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	task := f.workOrder(t, "Re-import target")
	for i := 0; i < 2; i++ {
		if _, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), task.ID, 15, time.Now().Add(-time.Duration(i+1)*time.Hour), "imported marker"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.svc.DeleteImported(ctx, f.org, task.ID, "imported marker"); err != nil {
		t.Fatal(err)
	}
	var n int
	var entries, minutes float64
	if err := f.pools.Web.QueryRow(ctx, `
		SELECT count(*), COALESCE(max((after->>'entries')::int),0), COALESCE(max((after->>'minutes')::int),0)
		  FROM audit_log WHERE entity_id=$1 AND action='time.remove_imported'`, task.ID).Scan(&n, &entries, &minutes); err != nil {
		t.Fatal(err)
	}
	if n != 1 || entries != 2 || minutes != 30 {
		t.Fatalf("want one row for 2 entries and 30 minutes, got n=%d entries=%v minutes=%v", n, entries, minutes)
	}
	// Clearing nothing records nothing.
	if err := f.svc.DeleteImported(ctx, f.org, task.ID, "imported marker"); err != nil {
		t.Fatal(err)
	}
	_ = f.pools.Web.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE entity_id=$1 AND action='time.remove_imported'`, task.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("an empty clear wrote a row: %d", n)
	}
}

func TestAdminRemovalIsSuperAdminOnlyDryByDefaultAndOneAuditRowEach(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.superAdmin(t, f.bob)
	a, b := f.logFor(t, f.alice, "one"), f.logFor(t, f.alice, "two")
	ids := []string{a, b}

	// Alice is neither a super admin nor allowed to use this path on her own entries.
	if _, err := f.svc.RemoveMany(ctx, f.org, f.subj(t, f.alice), ids, "why", true); !errors.Is(err, timelog.ErrNotAllowed) {
		t.Fatalf("a person who is not a super admin: got %v, want ErrNotAllowed", err)
	}
	admin := f.subj(t, f.bob)
	// A dry run reports and changes nothing.
	lines, err := f.svc.RemoveMany(ctx, f.org, admin, ids, "", false)
	if err != nil || len(lines) != 2 || lines[0].Removed || lines[0].AlreadyRemoved {
		t.Fatalf("dry run: %v %+v", err, lines)
	}
	if n, _, _ := f.removalRows(t, a); n != 0 {
		t.Fatal("a dry run wrote an audit row")
	}
	if got, _ := f.svc.Mine(ctx, f.org, f.alice, time.Now().Add(-24*time.Hour), time.Now().Add(time.Hour), false); len(got) != 2 {
		t.Fatalf("a dry run removed entries: %d left", len(got))
	}
	// Applying needs a reason.
	if _, err := f.svc.RemoveMany(ctx, f.org, admin, ids, "  ", true); !errors.Is(err, timelog.ErrNeedsReason) {
		t.Fatalf("no reason: got %v", err)
	}
	// One bad id takes the whole list down with it: nothing is removed.
	if _, err := f.svc.RemoveMany(ctx, f.org, admin, []string{a, "11111111-1111-1111-1111-111111111111"}, "why", true); !errors.Is(err, timelog.ErrNotFound) {
		t.Fatalf("unknown entry: got %v", err)
	}
	if n, _, _ := f.removalRows(t, a); n != 0 {
		t.Fatal("a failed list left a removal behind")
	}
	// Apply.
	lines, err = f.svc.RemoveMany(ctx, f.org, admin, ids, "ClickUp leftovers", true)
	if err != nil || len(lines) != 2 || !lines[0].Removed || !lines[1].Removed {
		t.Fatalf("apply: %v %+v", err, lines)
	}
	for _, id := range ids {
		if n, actor, via := f.removalRows(t, id); n != 1 || actor != f.bob || via != "admin remove" {
			t.Fatalf("entry %s: n=%d actor=%s via=%s", id, n, actor, via)
		}
	}
	var reason string
	_ = f.pools.Web.QueryRow(ctx, `SELECT after->>'reason' FROM audit_log WHERE entity_id=$1 AND action='time.remove'`, a).Scan(&reason)
	if reason != "ClickUp leftovers" {
		t.Fatalf("the audit row lost the reason: %q", reason)
	}
	// They are soft-removed, so Restore still works, and a second apply skips them.
	lines, err = f.svc.RemoveMany(ctx, f.org, admin, ids, "again", true)
	if err != nil || !lines[0].AlreadyRemoved || lines[0].Removed {
		t.Fatalf("second apply: %v %+v", err, lines)
	}
	if n, _, _ := f.removalRows(t, a); n != 1 {
		t.Fatalf("a repeat wrote another row: %d", n)
	}
	if err := f.svc.Restore(ctx, f.org, f.subj(t, f.alice), a); err != nil {
		t.Fatal(err)
	}
}
