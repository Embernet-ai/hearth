package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

func TestParseReportDay(t *testing.T) {
	now := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC) // 22:00 on 19 Nov in New York
	if _, err := parseReportDay("2026-11-19", now); err != nil {
		t.Errorf("today in the office: %v", err)
	}
	if _, err := parseReportDay("2026-11-20", now); err == nil {
		t.Error("tomorrow in the office was accepted")
	}
	if _, err := parseReportDay("2026-10-02", now); err != nil {
		t.Errorf("a past day: %v", err)
	}
	for _, bad := range []string{"", "2026-13-01", "10/02/2026", "2026-10-2x"} {
		if _, err := parseReportDay(bad, now); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	at, _ := parseReportDay("2026-10-02", now)
	if got := docs.ReportDay(at); got != "2026-10-02" {
		t.Errorf("the instant lands on %s in the office, want 2026-10-02", got)
	}
}

func TestReadReportDirRejectsStrayNamesAndSortsByDay(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"2026-10-05.md": "b", "2026-10-02.md": "a"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := readReportDir(dir)
	if err != nil || len(jobs) != 2 || jobs[0].Day != "2026-10-02" || jobs[1].Body != "b" {
		t.Fatalf("jobs = %v, %v", jobs, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readReportDir(dir); err == nil {
		t.Error("a file that is not a day was ignored")
	}
}

// The filer against a real Postgres: the report is the person's, on the day
// asked for, in the open department, once, with an audit row.
func TestDailyReportFilerAgainstPostgres(t *testing.T) {
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
	org, pat := uuid.NewString(), uuid.NewString()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Report Test')`, org, "rep-"+org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Pat Test')`, pat, org)
	t.Cleanup(func() {
		for _, q := range []string{
			`SELECT audit_purge_org($1)`,
			`DELETE FROM time_entries WHERE org_id=$1`, `DELETE FROM external_refs WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`, `DELETE FROM document_assignees WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM outbox WHERE org_id=$1`, `DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM spaces WHERE org_id=$1`, `DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = db.Exec(context.Background(), q, org)
		}
	})

	az := authz.New(db)
	f := &dailyReportFiler{db: db, az: az, svc: docs.New(db, az), org: org, who: pat,
		now: func() time.Time { return time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC) }}

	// A dry run writes nothing.
	if id, space, err := f.file(ctx, reportJob{Day: "2026-10-02", Body: "did a thing"}, false); err != nil || id != "" || space == "" {
		t.Fatalf("dry run = %q %q %v", id, space, err)
	}
	if got, _ := f.svc.ReportOn(ctx, org, pat, "2026-10-02"); got != "" {
		t.Fatal("a dry run filed a report")
	}

	id, space, err := f.file(ctx, reportJob{Day: "2026-10-02", Body: "did a thing"}, true)
	if err != nil || id == "" {
		t.Fatalf("file = %q %v", id, err)
	}
	var author, owner, kind, day, title string
	var spaceID string
	if err := db.QueryRow(ctx, `SELECT created_by::text, owner_id::text, kind, report_date::text, title, space_id::text
		FROM documents WHERE id = $1`, id).Scan(&author, &owner, &kind, &day, &title, &spaceID); err != nil {
		t.Fatal(err)
	}
	if author != pat || owner != pat || kind != docs.KindDailyReport || day != "2026-10-02" || spaceID != space {
		t.Fatalf("report = author %s owner %s kind %s day %s space %s", author, owner, kind, day, spaceID)
	}
	if got, _ := f.svc.ReportOn(ctx, org, pat, "2026-10-02"); got != id {
		t.Fatalf("ReportOn = %q, want %q", got, id)
	}
	var audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE org_id=$1 AND actor_id=$2
		AND action='daily_report.file' AND entity_id=$3`, org, pat, id).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit rows = %d, %v", audits, err)
	}

	// The same day again is the database's own refusal, before and after -apply.
	for _, apply := range []bool{false, true} {
		if _, _, err := f.file(ctx, reportJob{Day: "2026-10-02", Body: "again"}, apply); !errors.Is(err, docs.ErrReportExists) {
			t.Fatalf("second report (apply=%v): %v, want ErrReportExists", apply, err)
		}
	}
	if _, _, err := f.file(ctx, reportJob{Day: "2026-11-20", Body: "tomorrow"}, true); err == nil {
		t.Fatal("a report for a day that has not happened was filed")
	}
	if _, _, err := f.file(ctx, reportJob{Day: "2026-10-05", Body: "  \n"}, true); err == nil {
		t.Fatal("an empty report was filed")
	}
}
