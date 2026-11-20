package timelog_test

// Soft delete, against a real Postgres. The interesting checks are the ones
// that protect the whole point of the feature: a removed entry actually
// disappears from the ordinary views, Restore actually brings it back intact,
// and neither Correct nor a second Remove can be used on something already
// removed without saying so plainly.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/timelog"
)

// Removing an entry takes it out of the ordinary list, and Restore puts it
// back -- intact, not as a new row with a new id.
func TestRemoveHidesAndRestoreBringsBackTheSameEntry(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	entry, err := f.svc.LogOwn(ctx, f.org, alice, "", 90, time.Now(), "an hour and a half")
	if err != nil {
		t.Fatalf("log: %v", err)
	}

	mine, err := f.svc.Mine(ctx, f.org, f.alice, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false)
	if err != nil {
		t.Fatalf("mine before remove: %v", err)
	}
	if len(mine) != 1 {
		t.Fatalf("expected 1 entry before removing anything, got %d", len(mine))
	}

	if err := f.svc.Remove(ctx, f.org, alice, entry.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	mine, err = f.svc.Mine(ctx, f.org, f.alice, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false)
	if err != nil {
		t.Fatalf("mine after remove: %v", err)
	}
	if len(mine) != 0 {
		t.Fatalf("a removed entry still showed up in the ordinary view: %+v", mine)
	}

	// showRemoved=true is the recovery path -- it has to actually be
	// findable there, with the note and minutes intact, not just a bare row.
	withRemoved, err := f.svc.Mine(ctx, f.org, f.alice, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), true)
	if err != nil {
		t.Fatalf("mine with removed: %v", err)
	}
	if len(withRemoved) != 1 {
		t.Fatalf("expected the removed entry to show up with showRemoved=true, got %d", len(withRemoved))
	}
	if !withRemoved[0].Deleted() {
		t.Error("the entry returned with showRemoved=true does not report itself as deleted")
	}
	if withRemoved[0].Note != "an hour and a half" {
		t.Errorf("the removed entry's note changed: %q", withRemoved[0].Note)
	}

	if err := f.svc.Restore(ctx, f.org, alice, entry.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}

	mine, err = f.svc.Mine(ctx, f.org, f.alice, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false)
	if err != nil {
		t.Fatalf("mine after restore: %v", err)
	}
	if len(mine) != 1 {
		t.Fatalf("expected the entry back in the ordinary view after restore, got %d", len(mine))
	}
	if mine[0].ID != entry.ID {
		t.Errorf("restore produced a different entry: got %s, want %s", mine[0].ID, entry.ID)
	}
	if mine[0].Deleted() {
		t.Error("a restored entry still reports itself as deleted")
	}
}

// Neither Correct nor a second Remove works on something already removed --
// the reason has to be "it was removed", not a bare permission refusal,
// because the actual next step (restore it first) is different from either.
// Correct is the one action that stays a real refusal on a removed entry,
// unlike Remove/Restore -- editing the content of something currently marked
// gone is a genuine conflict, not a stale double-click, so it must not
// silently revive the entry as a side effect of a correction.
func TestCorrectRefusesAnAlreadyRemovedEntry(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	entry, err := f.svc.LogOwn(ctx, f.org, alice, "", 60, time.Now(), "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if err := f.svc.Remove(ctx, f.org, alice, entry.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if err := f.svc.Correct(ctx, f.org, alice, entry.ID, 30, nil, ""); err != timelog.ErrDeleted {
		t.Errorf("correcting a removed entry: got %v, want ErrDeleted", err)
	}
}

// Restoring something that is not removed -- never was, or already got
// restored by something else -- is a no-op, not an error. The real case this
// protects is a stale "Show removed" list still offering Restore on an entry
// somebody already brought back via the page's own Undo banner.
func TestRestoreIsIdempotent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	entry, err := f.svc.LogOwn(ctx, f.org, alice, "", 45, time.Now(), "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if err := f.svc.Restore(ctx, f.org, alice, entry.ID); err != nil {
		t.Errorf("restoring a never-removed entry should be a no-op, got %v", err)
	}

	if err := f.svc.Remove(ctx, f.org, alice, entry.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := f.svc.Restore(ctx, f.org, alice, entry.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	// Restoring again, as a stale page's Restore button would.
	if err := f.svc.Restore(ctx, f.org, alice, entry.ID); err != nil {
		t.Errorf("restoring an already-restored entry should be a no-op, got %v", err)
	}
}

// Removing something already removed is the same no-op, for the same
// reason -- two tabs, or a click that landed after somebody else's Remove.
func TestRemoveIsIdempotent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	entry, err := f.svc.LogOwn(ctx, f.org, alice, "", 45, time.Now(), "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if err := f.svc.Remove(ctx, f.org, alice, entry.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := f.svc.Remove(ctx, f.org, alice, entry.ID); err != nil {
		t.Errorf("removing an already-removed entry should be a no-op, got %v", err)
	}
}

// Restore has the same authority as Remove: the entry's own author, or an
// admin of the document it is filed against. A stranger gets neither.
func TestRestoreRespectsTheSamePermissionAsRemove(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	d := f.workOrder(t, "Somebody's job")

	entry, err := f.svc.Log(ctx, f.org, alice, d.ID, 60, time.Now(), "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if err := f.svc.Remove(ctx, f.org, alice, entry.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	carol := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Carol')`,
		carol, f.org); err != nil {
		t.Fatalf("carol: %v", err)
	}
	if err := f.svc.Restore(ctx, f.org, f.subj(t, carol), entry.ID); err == nil {
		t.Error("a stranger restored somebody else's removed entry")
	}
	if err := f.svc.Restore(ctx, f.org, alice, entry.ID); err != nil {
		t.Errorf("its own author could not restore it: %v", err)
	}
}

// The billing report must never bill a removed entry -- this is the reason
// deleted_at is filtered everywhere it is, not just in My Time.
func TestBillingReportExcludesRemovedEntries(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	grant(t, f, f.alice, "finance")

	kept, err := f.svc.LogOwn(ctx, f.org, alice, "", 60, time.Now(), "kept")
	if err != nil {
		t.Fatalf("log kept: %v", err)
	}
	removed, err := f.svc.LogOwn(ctx, f.org, alice, "", 120, time.Now(), "removed")
	if err != nil {
		t.Fatalf("log removed: %v", err)
	}
	if err := f.svc.Remove(ctx, f.org, alice, removed.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	totals, err := f.svc.TimeReportSummary(ctx, f.org, f.alice, from, to)
	if err != nil {
		t.Fatalf("report summary: %v", err)
	}
	if len(totals) != 1 || totals[0].Minutes != 60 {
		t.Fatalf("expected only the kept 60 minutes in the summary, got %+v", totals)
	}

	rows, err := f.svc.TimeReport(ctx, f.org, f.alice, "", from, to)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != kept.ID {
		t.Fatalf("expected only the kept entry in the detail report, got %+v", rows)
	}
}
