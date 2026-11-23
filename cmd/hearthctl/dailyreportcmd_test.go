package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
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
	// -append: the afternoon goes after the morning on the same document, as the person, with the audit row.
	ar, err := f.appendTo(ctx, reportJob{Day: "2026-10-02", Body: "and then more"}, true)
	if err != nil || ar.DocID != id || ar.NewVersion != ar.Version+1 {
		t.Fatalf("append = %+v %v", ar, err)
	}
	var body string
	var ver int64
	var updatedBy string
	if err := db.QueryRow(ctx, `SELECT body, version, updated_by::text FROM documents WHERE id = $1`, id).Scan(&body, &ver, &updatedBy); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "did a thing") || !strings.HasSuffix(body, "and then more\n") || ver != ar.NewVersion || updatedBy != pat {
		t.Fatalf("after append: body %q version %d updated_by %s", body, ver, updatedBy)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE org_id=$1 AND actor_id=$2
		AND action='daily_report.append' AND entity_id=$3`, org, pat, id).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("append audit rows = %d, %v", audits, err)
	}
	if _, _, err := f.file(ctx, reportJob{Day: "2026-11-20", Body: "tomorrow"}, true); err == nil {
		t.Fatal("a report for a day that has not happened was filed")
	}
	if _, _, err := f.file(ctx, reportJob{Day: "2026-10-05", Body: "  \n"}, true); err == nil {
		t.Fatal("an empty report was filed")
	}
}

// memReports is a docs Store holding one report, with Update's own version rule.
type memReports struct {
	docs.Store
	id, body    string
	version     int64
	has         bool
	updates     int
	lastExpects int64
	// raceOnGet bumps the version after Get returns, the way a concurrent edit would.
	raceOnGet bool
}

func (m *memReports) ReportOn(context.Context, string, string, string) (string, error) {
	if !m.has {
		return "", nil
	}
	return m.id, nil
}
func (m *memReports) Get(context.Context, string, core.Subjects, string) (*docs.Document, error) {
	d := &docs.Document{ID: m.id, Body: m.body, Version: m.version}
	if m.raceOnGet {
		m.version++
	}
	return d, nil
}
func (m *memReports) Update(_ context.Context, _ string, _ core.Subjects, _ string, p docs.UpdateParams) (*docs.Document, error) {
	if p.ExpectedVersion != 0 && p.ExpectedVersion != m.version {
		return nil, fmt.Errorf("%w: you edited version %d, current is %d", docs.ErrConflict, p.ExpectedVersion, m.version)
	}
	m.updates++
	m.lastExpects = p.ExpectedVersion
	m.body = *p.Body
	m.version++
	return &docs.Document{ID: m.id, Body: m.body, Version: m.version}, nil
}

func appendFiler(m *memReports) *dailyReportFiler {
	return &dailyReportFiler{az: personFinder{annID}, svc: m, org: "org", who: annID,
		now: func() time.Time { return time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC) }}
}

func TestAppendKeepsTheMorningAndAddsTheAfternoon(t *testing.T) {
	ctx := context.Background()
	morning := "# Morning\n\nshipped a thing.\n"
	m := &memReports{id: "d1", body: morning, version: 3, has: true}
	f := appendFiler(m)

	// A dry run says what it would do and writes nothing.
	r, err := f.appendTo(ctx, reportJob{Day: "2026-11-22", Body: "# Afternoon\n\nshipped more.\n"}, false)
	if err != nil || m.updates != 0 || m.body != morning {
		t.Fatalf("dry run = %+v %v updates=%d", r, err, m.updates)
	}
	want := morning + "\n# Afternoon\n\nshipped more.\n"
	if r.OldLen != len(morning) || r.NewLen != len(want) || r.AddedLen != len(want)-len(morning) || r.Version != 3 {
		t.Fatalf("dry run lengths = %+v, want new %d", r, len(want))
	}

	r, err = f.appendTo(ctx, reportJob{Day: "2026-11-22", Body: "# Afternoon\n\nshipped more.\n"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if m.body != want || !strings.HasPrefix(m.body, morning) {
		t.Fatalf("body = %q, want %q", m.body, want)
	}
	if m.lastExpects != 3 || r.NewVersion != 4 || m.updates != 1 {
		t.Fatalf("expected version %d, new %d, updates %d", m.lastExpects, r.NewVersion, m.updates)
	}

	// The same text again adds nothing.
	if _, err := f.appendTo(ctx, reportJob{Day: "2026-11-22", Body: "# Afternoon\n\nshipped more.\n"}, true); !errors.Is(err, errAlreadyAppended) || m.updates != 1 {
		t.Fatalf("second append: %v updates=%d", err, m.updates)
	}
}

func TestAppendSeparatorWhateverTheOldTextEndsWith(t *testing.T) {
	for old, want := range map[string]string{"": "", "a": "\n\n", "a\n": "\n", "a\n\n": "", "a\n\n\n": ""} {
		if got := appendSeparator(old); got != want {
			t.Errorf("appendSeparator(%q) = %q, want %q", old, got, want)
		}
	}
}

func TestAppendRefusesWhatItCannotDoSafely(t *testing.T) {
	ctx := context.Background()
	// No report for the day: nothing to append to, and nothing is filed.
	m := &memReports{id: "d1"}
	if _, err := appendFiler(m).appendTo(ctx, reportJob{Day: "2026-11-22", Body: "x"}, true); err == nil || m.updates != 0 {
		t.Fatalf("append with no report: %v", err)
	}
	// An empty text, and a day that has not happened.
	m = &memReports{id: "d1", body: "a\n", version: 1, has: true}
	f := appendFiler(m)
	if _, err := f.appendTo(ctx, reportJob{Day: "2026-11-22", Body: " \n"}, true); err == nil {
		t.Error("an empty append was accepted")
	}
	if _, err := f.appendTo(ctx, reportJob{Day: "2026-11-24", Body: "x"}, true); err == nil {
		t.Error("an append to a day that has not happened was accepted")
	}
	// Someone saves between the read and the write: ErrConflict, their text kept, ours not written.
	m = &memReports{id: "d1", body: "theirs\n", version: 5, has: true, raceOnGet: true}
	if _, err := appendFiler(m).appendTo(ctx, reportJob{Day: "2026-11-22", Body: "mine"}, true); !errors.Is(err, docs.ErrConflict) || m.updates != 0 || m.body != "theirs\n" {
		t.Fatalf("concurrent edit: err=%v updates=%d body=%q", err, m.updates, m.body)
	}
}

func TestAppendReportsDryRunWritesNothingAndApplyWritesOnce(t *testing.T) {
	ctx := context.Background()
	m := &memReports{id: "d1", body: "morning\n", version: 1, has: true}
	f := appendFiler(m)
	jobs := []reportJob{{Day: "2026-11-22", Body: "afternoon\n"}}
	if err := appendReports(ctx, f, jobs, "p@x", false); err != nil || m.updates != 0 {
		t.Fatalf("dry run: %v updates=%d", err, m.updates)
	}
	if err := appendReports(ctx, f, jobs, "p@x", true); err != nil || m.updates != 1 || m.body != "morning\n\nafternoon\n" {
		t.Fatalf("apply: %v updates=%d body=%q", err, m.updates, m.body)
	}
	// A rerun skips what is already in, with no error and no second write.
	if err := appendReports(ctx, f, jobs, "p@x", true); err != nil || m.updates != 1 {
		t.Fatalf("rerun: %v updates=%d", err, m.updates)
	}
}
