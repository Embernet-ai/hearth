package docs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
)

func mkReport(t *testing.T, f *fx, who string, space *string, day *time.Time, body string) (*docs.Document, error) {
	t.Helper()
	return f.svc.Create(context.Background(), f.org, f.subj(t, who), docs.CreateParams{
		Kind: docs.KindDailyReport, Title: "Daily report", Body: body, SpaceID: space, ReportDate: day,
	})
}

// One report per person per day, enforced by the database, and a deleted
// report gives the day back.
func TestOneDailyReportPerPersonPerDay(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	first, err := mkReport(t, f, f.alice, nil, nil, "first")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := mkReport(t, f, f.alice, nil, nil, "second"); !errors.Is(err, docs.ErrReportExists) {
		t.Fatalf("second report the same day: got %v, want ErrReportExists", err)
	}
	// Somebody else, same day, is fine.
	if _, err := mkReport(t, f, f.bob, nil, nil, "bob's"); err != nil {
		t.Fatalf("bob: %v", err)
	}
	// Another day, same person, is fine.
	other := time.Now().AddDate(0, 0, -3)
	if _, err := mkReport(t, f, f.alice, nil, &other, "older"); err != nil {
		t.Fatalf("other day: %v", err)
	}
	got, err := f.svc.ReportOn(ctx, f.org, f.alice, docs.ReportDay(time.Now()))
	if err != nil || got != first.ID {
		t.Fatalf("ReportOn = %q, %v; want %q", got, err, first.ID)
	}
	if _, err := f.pools.Web.Exec(ctx, `UPDATE documents SET deleted_at = now() WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.ReportOn(ctx, f.org, f.alice, docs.ReportDay(time.Now())); got != "" {
		t.Fatalf("a deleted report still answers for the day: %q", got)
	}
	if _, err := mkReport(t, f, f.alice, nil, nil, "again"); err != nil {
		t.Fatalf("after delete: %v", err)
	}
}

// The open department: everybody in the org can see it and file under it, with
// no grant and no department row, and sees only their own reports there.
func TestTheOpenDailyReportsDepartmentLetsEveryoneFile(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	space, err := f.svc.EnsureReportSpace(ctx, f.org, f.alice)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.svc.EnsureReportSpace(ctx, f.org, f.bob)
	if err != nil || again != space {
		t.Fatalf("EnsureReportSpace is not idempotent: %q then %q (%v)", space, again, err)
	}

	// Dave is in nothing: no grants, no department, no channel.
	dave := f.subj(t, f.dave)
	lvl, err := f.az.LevelFor(ctx, dave, core.Ref{Type: core.EntitySpace, ID: space})
	if err != nil || !lvl.AtLeast(core.AccessView) {
		t.Fatalf("dave on the open department: %q, %v; want at least view", lvl, err)
	}

	mine, err := mkReport(t, f, f.dave, &space, nil, "dave's day")
	if err != nil {
		t.Fatalf("dave could not file: %v", err)
	}
	if _, err := mkReport(t, f, f.bob, &space, nil, "bob's day"); err != nil {
		t.Fatal(err)
	}

	// Dave sees his own and not Bob's.
	seen, err := f.svc.Reports(ctx, f.org, dave, docs.ReportFilter{SpaceID: space})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].ID != mine.ID || seen[0].Author != "Dave" {
		t.Fatalf("dave sees %d reports, want only his own: %+v", len(seen), seen)
	}
	if seen[0].SpacePath != docs.ReportSpaceName || seen[0].Day != docs.ReportDay(time.Now()) {
		t.Fatalf("entry path/day = %q / %q", seen[0].SpacePath, seen[0].Day)
	}
}

// The roll-up honours space (with descendants), date range and person, and
// shows a reader only what they may read.
func TestTheRollUpFiltersAndRespectsAccess(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	var parent, child string
	for name, dst := range map[string]*string{"Engineering": &parent} {
		if err := f.pools.Web.QueryRow(ctx, `
			INSERT INTO spaces (id, org_id, name, kind, created_by)
			VALUES (gen_random_uuid(), $1, $2, 'area', $3) RETURNING id::text`, f.org, name, f.alice).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.pools.Web.QueryRow(ctx, `
		INSERT INTO spaces (id, org_id, parent_id, name, kind, created_by)
		VALUES (gen_random_uuid(), $1, $2, 'Line 3', 'folder', $3) RETURNING id::text`, f.org, parent, f.alice).Scan(&child); err != nil {
		t.Fatal(err)
	}
	// Alice (the space's admin) is who reads.
	for _, sp := range []string{parent, child} {
		if err := f.az.Grant(ctx, f.org, core.Ref{Type: core.EntitySpace, ID: sp},
			core.Subject{Kind: core.SubjectPrincipal, ID: f.alice}, core.AccessEdit, f.alice); err != nil {
			t.Fatal(err)
		}
	}
	d1 := time.Now().AddDate(0, 0, -1)
	d2 := time.Now().AddDate(0, 0, -5)
	if _, err := mkReport(t, f, f.alice, &child, &d1, "child yesterday"); err != nil {
		t.Fatal(err)
	}
	if _, err := mkReport(t, f, f.alice, &parent, &d2, "parent old"); err != nil {
		t.Fatal(err)
	}
	// Bob files under the parent AFTER alice's grant exists, so alice (a space
	// editor) can read it; Dave's stays private to Dave.
	if _, err := mkReport(t, f, f.bob, &parent, &d1, "bob yesterday"); err != nil {
		t.Fatal(err)
	}
	if _, err := mkReport(t, f, f.dave, nil, &d1, "dave private"); err != nil {
		t.Fatal(err)
	}

	all, err := f.svc.Reports(ctx, f.org, alice, docs.ReportFilter{SpaceID: parent})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("space with descendants: %d reports, want 3: %+v", len(all), all)
	}
	// Newest day first.
	if all[0].Day < all[len(all)-1].Day {
		t.Fatalf("not newest first: %+v", all)
	}
	from := docs.ReportDay(d1)
	recent, err := f.svc.Reports(ctx, f.org, alice, docs.ReportFilter{SpaceID: parent, From: from})
	if err != nil || len(recent) != 2 {
		t.Fatalf("from %s: %d reports (%v), want 2", from, len(recent), err)
	}
	only, err := f.svc.Reports(ctx, f.org, alice, docs.ReportFilter{SpaceID: parent, PersonID: f.bob})
	if err != nil || len(only) != 1 || only[0].Author != "Bob" {
		t.Fatalf("person filter: %+v (%v)", only, err)
	}
	// Alice does not see Dave's unfiled report.
	everything, err := f.svc.Reports(ctx, f.org, alice, docs.ReportFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range everything {
		if r.Author == "Dave" {
			t.Fatalf("alice can read dave's private report: %+v", r)
		}
	}
	if child == "" || !contains(all, "Engineering / Line 3") {
		t.Fatalf("space path missing from %+v", all)
	}
}

func contains(rs []*docs.ReportEntry, path string) bool {
	for _, r := range rs {
		if r.SpacePath == path {
			return true
		}
	}
	return false
}

// A super admin reads every report, and the query has to build for them: the
// shared access fragment is a different shape for a super admin, and the
// roll-up once failed outright for exactly that caller.
func TestASuperAdminCanReadTheRollUp(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := mkReport(t, f, f.bob, nil, nil, "bob private"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$2)`,
		f.org, f.alice); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Reports(ctx, f.org, f.subj(t, f.alice), docs.ReportFilter{})
	if err != nil {
		t.Fatalf("super admin roll-up: %v", err)
	}
	if len(got) != 1 || got[0].Author != "Bob" {
		t.Fatalf("super admin sees %+v, want bob's report", got)
	}
}

// A report's day is the office's day, not the server's. At 00:30 UTC it is
// still the evening before in New York, so two instants half an hour apart
// across UTC midnight are one day, and the second report is refused. This
// failed on CI, which runs in UTC, while passing on a laptop in the office.
func TestAReportsDayIsTheOfficeDayWhateverTheServerClockSays(t *testing.T) {
	f := setup(t)
	evening := time.Date(2026, 10, 25, 23, 30, 0, 0, time.UTC) // 7:30pm Oct 25 in New York
	after := time.Date(2026, 10, 26, 0, 30, 0, 0, time.UTC)    // 8:30pm Oct 25 in New York
	if _, err := mkReport(t, f, f.alice, nil, &evening, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := mkReport(t, f, f.alice, nil, &after, "b"); !errors.Is(err, docs.ErrReportExists) {
		t.Fatalf("same office day across UTC midnight: got %v, want ErrReportExists", err)
	}
	if got := docs.ReportDay(after); got != "2026-10-25" {
		t.Fatalf("ReportDay = %s, want 2026-10-25", got)
	}
}
