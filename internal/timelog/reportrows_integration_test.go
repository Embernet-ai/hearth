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
