package timelog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/importer/clickup"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

func (f *fx) grant(t *testing.T, principal, capability string) {
	t.Helper()
	if _, err := f.pools.Web.Exec(context.Background(),
		`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
		 VALUES ($1,$2,$3,$2)`, f.org, principal, capability); err != nil {
		t.Fatalf("grant %s: %v", capability, err)
	}
}

func (f *fx) holder(t *testing.T, entryID string) string {
	t.Helper()
	var p string
	if err := f.pools.Web.QueryRow(context.Background(),
		`SELECT principal_id::text FROM time_entries WHERE id=$1`, entryID).Scan(&p); err != nil {
		t.Fatalf("holder: %v", err)
	}
	return p
}

// One entry the approver may not decide sinks the whole batch, so a bulk
// approval is never quietly half done.
func TestApproveManyIsAllOrNothing(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()
	carol := f.person(t, "Carol")
	if _, err := f.pools.Web.Exec(ctx,
		`UPDATE principals SET approver_id=$2 WHERE id=$1`, f.alice, f.bob); err != nil {
		t.Fatal(err)
	}
	mine, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 60, time.Now(), "")
	if err != nil {
		t.Fatal(err)
	}
	notMine, err := f.svc.LogOwn(ctx, f.org, f.subj(t, carol), "", 30, time.Now(), "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.ApproveMany(ctx, f.org, f.bob, []string{mine.ID, notMine.ID}); !errors.Is(err, timelog.ErrBatchRefused) {
		t.Fatalf("mixed batch: got %v, want ErrBatchRefused", err)
	}
	pending, err := f.svc.Pending(ctx, f.org, f.bob)
	if err != nil || len(pending) != 1 {
		t.Fatalf("after a refused batch Alice's entry must still be pending: %v %v", pending, err)
	}

	n, err := f.svc.ApproveMany(ctx, f.org, f.bob, []string{mine.ID, mine.ID})
	if err != nil || n != 1 {
		t.Fatalf("approve: n=%d err=%v", n, err)
	}
	if pending, _ := f.svc.Pending(ctx, f.org, f.bob); len(pending) != 0 {
		t.Fatalf("still pending after approval: %v", pending)
	}
}

// A super admin may approve anybody's time, but never their own, and that
// holds in bulk exactly as it does one at a time.
func TestApproveManyNeverSignsOffYourOwnTime(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()
	f.superAdmin(t, f.bob)
	theirs, _ := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 60, time.Now(), "")
	own, _ := f.svc.LogOwn(ctx, f.org, f.subj(t, f.bob), "", 60, time.Now(), "")

	if _, err := f.svc.ApproveMany(ctx, f.org, f.bob, []string{theirs.ID, own.ID}); !errors.Is(err, timelog.ErrBatchRefused) {
		t.Fatalf("got %v, want ErrBatchRefused", err)
	}
}

// Only entries the importer wrote and nobody has decided move. Hours somebody
// logged by hand stay theirs, and a signed-off entry stays where it was signed.
func TestReassignMovesOnlyUndecidedImportedHours(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()
	f.grant(t, f.alice, "finance")
	task := f.workOrder(t, "Imported job")
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO document_assignees (document_id, principal_id, is_primary) VALUES ($1,$2,true)`,
		task.ID, f.bob); err != nil {
		t.Fatal(err)
	}

	log := func(note string) string {
		t.Helper()
		e, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), task.ID, 120, time.Now(), note)
		if err != nil {
			t.Fatal(err)
		}
		return e.ID
	}
	imported := log(clickup.ImportedTimeNote)
	byHand := log("worked it myself")
	signed := log(clickup.ImportedTimeNote)
	if _, err := f.pools.Web.Exec(ctx,
		`UPDATE time_entries SET approved=true, decided_at=now(), decided_by=$2 WHERE id=$1`,
		signed, f.bob); err != nil {
		t.Fatal(err)
	}

	list, err := f.svc.Imported(ctx, f.org, f.alice, clickup.ImportedTimeNote)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != imported || list[0].SuggestedID != f.bob {
		t.Fatalf("want only the undecided imported entry, suggested to Bob; got %+v", list)
	}

	moves := []timelog.Move{{imported, f.bob}, {byHand, f.bob}, {signed, f.bob}}
	n, err := f.svc.Reassign(ctx, f.org, f.alice, clickup.ImportedTimeNote, moves)
	if err != nil || n != 1 {
		t.Fatalf("reassign: n=%d err=%v", n, err)
	}
	if f.holder(t, imported) != f.bob {
		t.Error("the imported entry did not move")
	}
	if f.holder(t, byHand) != f.alice || f.holder(t, signed) != f.alice {
		t.Error("a hand-logged or signed-off entry moved")
	}

	// Moved hours stay pending, so the new person's approver still sees them.
	var decided bool
	_ = f.pools.Web.QueryRow(ctx,
		`SELECT decided_at IS NOT NULL FROM time_entries WHERE id=$1`, imported).Scan(&decided)
	if decided {
		t.Error("reassigning must not decide the entry")
	}
}

// Whose name hours sit under is what an invoice reads, so it is billing's call.
func TestImportedHoursAreBillingOnly(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()
	if _, err := f.svc.Imported(ctx, f.org, f.bob, clickup.ImportedTimeNote); !errors.Is(err, timelog.ErrNotAllowed) {
		t.Fatalf("list: got %v, want ErrNotAllowed", err)
	}
	if _, err := f.svc.Reassign(ctx, f.org, f.bob, clickup.ImportedTimeNote, nil); !errors.Is(err, timelog.ErrNotAllowed) {
		t.Fatalf("reassign: got %v, want ErrNotAllowed", err)
	}
}

// A removed entry is gone as far as billing is concerned, so neither the
// imported-hours page nor a bulk approval may reach it.
func TestRemovedEntriesAreOutOfReachOfBulkActions(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()
	f.grant(t, f.alice, "finance")
	f.superAdmin(t, f.bob)
	task := f.workOrder(t, "Imported then removed")
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO document_assignees (document_id, principal_id, is_primary) VALUES ($1,$2,true)`,
		task.ID, f.bob); err != nil {
		t.Fatal(err)
	}
	e, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), task.ID, 60, time.Now(), clickup.ImportedTimeNote)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Remove(ctx, f.org, f.subj(t, f.alice), e.ID); err != nil {
		t.Fatal(err)
	}

	if list, _ := f.svc.Imported(ctx, f.org, f.alice, clickup.ImportedTimeNote); len(list) != 0 {
		t.Fatalf("a removed entry is listed: %+v", list)
	}
	if n, _ := f.svc.Reassign(ctx, f.org, f.alice, clickup.ImportedTimeNote,
		[]timelog.Move{{e.ID, f.bob}}); n != 0 || f.holder(t, e.ID) != f.alice {
		t.Fatal("a removed entry was moved")
	}
	if _, err := f.svc.ApproveMany(ctx, f.org, f.bob, []string{e.ID}); !errors.Is(err, timelog.ErrBatchRefused) {
		t.Fatalf("approving a removed entry: got %v, want ErrBatchRefused", err)
	}
}

// The "already decided" record for somebody who sees everyone's time. This
// query used to fail outright for exactly those people -- $2 appeared only in
// "AND $2 IS NOT NULL", which Postgres cannot type -- and the page swallowed
// the error, so the list was simply always empty for them.
func TestDecidedShowsEveryonesRecordToSomebodyWhoSeesAll(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()
	f.superAdmin(t, f.bob)
	e, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 45, time.Now(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Decide(ctx, f.org, f.bob, e.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	carol := f.person(t, "Carol")
	from, to := time.Now().AddDate(0, -1, 0), time.Now().AddDate(0, 0, 1)
	all, err := f.svc.Decided(ctx, f.org, carol, from, to, true)
	if err != nil {
		t.Fatalf("decided, seeing all: %v", err)
	}
	if len(all) != 1 || all[0].ID != e.ID {
		t.Fatalf("want Bob's decision visible to everybody-time; got %+v", all)
	}
}
