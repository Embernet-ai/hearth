package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

var importNow = time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)

func TestReadTimeImportChecksEveryRow(t *testing.T) {
	good := "start,minutes,note\n2026-10-02T09:00:00-04:00,90,\"Reconstructed from GitHub activity: HotLoop, 3 commits\"\n2026-10-02T14:00:00-04:00,30,Reconstructed from GitHub activity: Hearth\n2026-10-03T14:00:00-04:00,60,Added on Pat's own account: work not in commits\n"
	rows, sum, err := readTimeImport(strings.NewReader(good), importNow)
	if err != nil || len(rows) != 3 || rows[0].Minutes != 90 || len(sum) != 64 {
		t.Fatalf("good file = %v %q %v", rows, sum, err)
	}
	for name, bad := range map[string]string{
		"wrong header":   "when,minutes,note\n",
		"bad time":       "start,minutes,note\nyesterday,30,Reconstructed from x\n",
		"zero minutes":   "start,minutes,note\n2026-10-02T09:00:00Z,0,Reconstructed from x\n",
		"too long":       "start,minutes,note\n2026-10-02T09:00:00Z,100001,Reconstructed from x\n",
		"not labelled":   "start,minutes,note\n2026-10-02T09:00:00Z,30,worked on hearth\n",
		"future":         "start,minutes,note\n2026-11-21T09:00:00Z,30,Reconstructed from x\n",
		"same start":     "start,minutes,note\n2026-10-02T09:00:00Z,30,Reconstructed from x\n2026-10-02T05:00:00-04:00,30,Reconstructed from y\n",
		"no entries":     "start,minutes,note\n",
		"missing column": "start,minutes,note\n2026-10-02T09:00:00Z,30\n",
	} {
		if _, _, err := readTimeImport(strings.NewReader(bad), importNow); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The importer against a real Postgres: entries are the person's, unattached,
// pending, tagged, audited once; a second run writes nothing.
func TestTimeImporterAgainstPostgres(t *testing.T) {
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
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Time Import Test')`, org, "ti-"+org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Pat Test')`, pat, org)
	t.Cleanup(func() {
		for _, q := range []string{
			`SELECT audit_purge_org($1)`,
			`DELETE FROM time_entry_tag_links WHERE entry_id IN (SELECT id FROM time_entries WHERE org_id=$1)`,
			`DELETE FROM time_entries WHERE org_id=$1`, `DELETE FROM time_entry_tags WHERE org_id=$1`,
			`DELETE FROM capability_grants WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = db.Exec(context.Background(), q, org)
		}
	})

	// The first run is a person who may not define tags: entries in, no tag.
	az := authz.New(db)
	imp := &timeImporter{db: db, az: az, svc: timelog.New(db, az), org: org, who: pat}
	csv := "start,minutes,note\n" +
		"2026-10-02T09:00:00-04:00,90,Reconstructed from GitHub activity: HotLoop\n" +
		"2026-10-02T14:00:00-04:00,30,Reconstructed from GitHub activity: Hearth\n"
	rows, sum, err := readTimeImport(strings.NewReader(csv), importNow)
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer

	count := func() (n int) {
		t.Helper()
		if err := db.QueryRow(ctx, `SELECT count(*) FROM time_entries WHERE org_id=$1 AND principal_id=$2 AND document_id IS NULL AND decided_at IS NULL AND deleted_at IS NULL`, org, pat).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	res, err := imp.run(ctx, rows, sum, "reconstructed", false, false, &log)
	if err != nil || res.Written != 2 || res.Minutes != 120 || count() != 0 {
		t.Fatalf("dry run = %+v %v, entries %d", res, err, count())
	}
	res, err = imp.run(ctx, rows, sum, "reconstructed", true, false, &log)
	if err != nil || res.Written != 2 || res.Minutes != 120 || count() != 2 {
		t.Fatalf("apply = %+v %v, entries %d, log %s", res, err, count(), log.String())
	}
	var start time.Time
	var mins int
	if err := db.QueryRow(ctx, `SELECT started_at, minutes FROM time_entries WHERE org_id=$1 AND minutes=90`, org).Scan(&start, &mins); err != nil || !start.Equal(rows[0].Start) {
		t.Fatalf("entry start = %v %v", start, err)
	}
	var tagged, audits int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM time_entry_tag_links l JOIN time_entries e ON e.id = l.entry_id WHERE e.org_id=$1`, org).Scan(&tagged)
	if err := db.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE org_id=$1 AND actor_id=$2 AND action='time.import'`, org, pat).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit rows = %d %v", audits, err)
	}
	if tagged != 0 || !strings.Contains(log.String(), "no tag") {
		t.Fatalf("a person without the people capability got %d tagged, log %q", tagged, log.String())
	}
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'people',$2)`, org, pat)
	tagRows := []timeImportRow{{Start: rows[0].Start.Add(48 * time.Hour), Minutes: 20, Note: "Reconstructed from GitHub activity: Flux"}}
	if _, err := imp.run(ctx, tagRows, sum, "reconstructed", true, false, &log); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(ctx, `SELECT count(*) FROM time_entry_tag_links l JOIN time_entries e ON e.id = l.entry_id WHERE e.org_id=$1`, org).Scan(&tagged)
	if tagged != 1 {
		t.Fatalf("a person who may define tags got %d tagged, want 1", tagged)
	}

	// A second import writes nothing, and says so.
	if _, err := imp.run(ctx, rows, sum, "reconstructed", true, false, &log); err == nil || count() != 3 {
		t.Fatalf("second import = %v, entries %d", err, count())
	}
	// A partly finished run is finished with -skip-existing and not otherwise.
	more := append([]timeImportRow{}, rows...)
	more = append(more, timeImportRow{Start: rows[1].Start.Add(24 * time.Hour), Minutes: 45, Note: "Reconstructed from GitHub activity: EmberCORE"})
	if _, err := imp.run(ctx, more, sum, "", true, false, &log); err == nil || count() != 3 {
		t.Fatalf("partial overlap without skip = %v, entries %d", err, count())
	}
	res, err = imp.run(ctx, more, sum, "", true, true, &log)
	if err != nil || res.Written != 1 || res.Skipped != 2 || count() != 4 {
		t.Fatalf("skip-existing = %+v %v, entries %d", res, err, count())
	}
}
