package timelog_test

// The details export's read, against a real Postgres: only the people asked
// for, everybody when none are named, no removed entries, the billing gate
// ahead of any row, and the rows exactly the ones TimeReport gives.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/timelog"
)

func TestTimeReportRowsChosenPeopleAndEverybody(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	grant(t, f, f.alice, "finance")

	a1, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 60, time.Now(), "alice one")
	if err != nil {
		t.Fatalf("log alice: %v", err)
	}
	if _, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.bob), "", 30, time.Now(), "bob one"); err != nil {
		t.Fatalf("log bob: %v", err)
	}
	gone, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.bob), "", 45, time.Now(), "bob removed")
	if err != nil {
		t.Fatalf("log bob 2: %v", err)
	}
	if err := f.svc.Remove(ctx, f.org, f.subj(t, f.bob), gone.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	collect := func(ids []string) []timelog.ReportRow {
		t.Helper()
		var got []timelog.ReportRow
		err := f.svc.TimeReportRows(ctx, f.org, f.alice, ids, from, to, func(r timelog.ReportRow) error {
			got = append(got, r)
			return nil
		})
		if err != nil {
			t.Fatalf("rows: %v", err)
		}
		return got
	}

	if all := collect(nil); len(all) != 2 || all[0].PersonName != "Alice" || all[1].PersonName != "Bob" {
		t.Fatalf("everybody: want Alice then Bob, one entry each, got %+v", all)
	}
	only := collect([]string{f.alice})
	if len(only) != 1 || only[0].ID != a1.ID || only[0].PrincipalID != f.alice {
		t.Fatalf("only alice: got %+v", only)
	}
	both := collect([]string{f.bob, f.alice})
	if len(both) != 2 {
		t.Fatalf("both: got %+v", both)
	}

	// Same rows as the per-person read, so the two exports cannot drift apart.
	old, err := f.svc.TimeReport(ctx, f.org, f.alice, "", from, to)
	if err != nil || len(old) != len(both) {
		t.Fatalf("TimeReport: %v %+v", err, old)
	}
	for i := range old {
		if old[i].ID != both[i].ID || old[i].Minutes != both[i].Minutes || old[i].Note != both[i].Note {
			t.Errorf("row %d differs: %+v vs %+v", i, old[i], both[i])
		}
	}

	// Stopping early from fn stops the read and hands back the error.
	stop := errors.New("stop")
	if err := f.svc.TimeReportRows(ctx, f.org, f.alice, nil, from, to, func(timelog.ReportRow) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("fn's error should come back, got %v", err)
	}
}

func TestTimeReportRowsNeedsBilling(t *testing.T) {
	f := setup(t)
	called := false
	err := f.svc.TimeReportRows(context.Background(), f.org, f.bob, nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour),
		func(timelog.ReportRow) error { called = true; return nil })
	if !errors.Is(err, timelog.ErrNotAllowed) || called {
		t.Fatalf("without Finance: err %v, called %v", err, called)
	}
}

// Every row says where its hours stand: waiting, approved with who and when, or sent back. The two reads agree, and the
// space the entry sits in comes back as an id so the report can be narrowed to it.
func TestTimeReportRowsCarryApproval(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()
	f.superAdmin(t, f.bob)
	grant(t, f, f.bob, "finance")
	okE, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 60, time.Now(), "approved one")
	if err != nil {
		t.Fatal(err)
	}
	badE, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 30, time.Now(), "sent back")
	if err != nil {
		t.Fatal(err)
	}
	waitE, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 15, time.Now(), "waiting")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Decide(ctx, f.org, f.bob, okE.ID, true, "fine"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Decide(ctx, f.org, f.bob, badE.ID, false, "wrong job"); err != nil {
		t.Fatal(err)
	}
	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	want := map[string]string{okE.ID: timelog.ApprovalApproved, badE.ID: timelog.ApprovalRejected, waitE.ID: timelog.ApprovalPending}
	check := func(name string, rows []timelog.ReportRow) {
		t.Helper()
		if len(rows) != 3 {
			t.Fatalf("%s: %d rows", name, len(rows))
		}
		for _, r := range rows {
			if r.Approval != want[r.ID] {
				t.Errorf("%s: %s reads %q, want %q", name, r.Note, r.Approval, want[r.ID])
			}
			if r.Approval == timelog.ApprovalPending && (r.DecidedAt != nil || r.DeciderName != "") {
				t.Errorf("%s: a waiting entry names a decision: %+v", name, r)
			}
			if r.Approval != timelog.ApprovalPending && (r.DecidedAt == nil || r.DeciderName != "Bob") {
				t.Errorf("%s: a decided entry does not say who and when: %+v", name, r)
			}
		}
	}
	rows, err := f.svc.TimeReport(ctx, f.org, f.bob, "", from, to)
	if err != nil {
		t.Fatal(err)
	}
	check("TimeReport", rows)
	var streamed []timelog.ReportRow
	if err := f.svc.TimeReportRows(ctx, f.org, f.bob, nil, from, to, func(r timelog.ReportRow) error { streamed = append(streamed, r); return nil }); err != nil {
		t.Fatal(err)
	}
	check("TimeReportRows", streamed)
}
