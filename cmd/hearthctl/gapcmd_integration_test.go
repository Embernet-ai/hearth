package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/importer/clickup"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// The lookups the gap import reads Hearth through, against a real Postgres: a
// wrong column here is a live surprise, and the unit tests use a fake.
func TestGapLookupsAndWritersAgainstPostgres(t *testing.T) {
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
	org, nina, yara, gone := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Gap Test')`, org, "gap-"+org[:8])
	for id, n := range map[string]string{nina: "Nina Alvarez", yara: "Yara Stone", gone: "Departed Person"} {
		exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user',$3)`, id, org, n)
	}
	exec(`UPDATE principals SET status = 'suspended' WHERE id = $1`, gone)
	exec(`INSERT INTO local_credentials (principal_id, org_id, email, password_hash) VALUES ($1,$2,'nina@x.test','')`, nina, org)
	t.Cleanup(func() {
		for _, q := range []string{
			`SELECT audit_purge_org($1)`,
			`DELETE FROM time_entries WHERE org_id=$1`, `DELETE FROM external_refs WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`, `DELETE FROM document_assignees WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM outbox WHERE org_id=$1`, `DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM spaces WHERE org_id=$1`, `DELETE FROM local_credentials WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = db.Exec(context.Background(), q, org)
		}
	})

	az := authz.New(db)
	svc := docs.New(db, az)
	subj, err := az.SubjectsFor(ctx, org, yara)
	if err != nil {
		t.Fatal(err)
	}
	d, err := svc.Create(ctx, org, subj, docs.CreateParams{Kind: docs.KindTask, Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id) VALUES ($1,'clickup','TASK1','document',$2)`, org, d.ID)
	exec(`INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id) VALUES ($1,'clickup','comment:C1','message',gen_random_uuid())`, org)
	exec(`INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id) VALUES ($1,'clickup','attachment:A1','file',gen_random_uuid())`, org)

	exec(`INSERT INTO spaces (id, org_id, name, kind, created_by) VALUES (gen_random_uuid(), $1, 'Sales', 'area', $2)`, org, yara)

	g := sqlGap{db, org}
	docsMap, err := g.TaskDocs(ctx)
	if err != nil || docsMap["TASK1"] != d.ID || len(docsMap) != 1 {
		t.Fatalf("TaskDocs = %v (%v): comments and files must not be read as tasks", docsMap, err)
	}
	if kc, _ := g.KnownComments(ctx); !kc["comment:C1"] {
		t.Fatalf("KnownComments = %v", kc)
	}
	if ka, _ := g.KnownAttachments(ctx); ka["A1"] == "" {
		t.Fatalf("KnownAttachments = %v", ka)
	}
	people, err := g.ActivePeople(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if people["nina@x.test"] != nina || people["nina alvarez"] != nina || people["yara stone"] != yara {
		t.Fatalf("ActivePeople = %v", people)
	}
	if _, ok := people["departed person"]; ok {
		t.Fatal("a suspended account is not an active person")
	}
	top, err := g.TopSpaces(ctx)
	if err != nil || top["Sales"] == "" {
		t.Fatalf("TopSpaces = %v (%v)", top, err)
	}
	var kind string
	if err := db.QueryRow(ctx, `SELECT kind FROM spaces WHERE id = $1`, top["Sales"]).Scan(&kind); err != nil || kind != "area" {
		t.Fatalf("TopSpaces picked a %q, want the area", kind)
	}

	// Time: written for a person, found again as known, and the one-total entry
	// retired without being deleted.
	tl := gapTime{db}
	start := time.Date(2026, 8, 15, 14, 0, 0, 0, time.UTC)
	id, err := tl.LogFor(ctx, org, nina, d.ID, 90, start, clickup.TimeEntryNotePrefix+"9001: wiring")
	if err != nil || id == "" {
		t.Fatalf("LogFor: %v", err)
	}
	if _, err := tl.LogFor(ctx, org, yara, d.ID, 30, start, clickup.TimeEntryNotePrefix+"9002 [logged in ClickUp by X, who has no active account here]"); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO time_entries (id, org_id, document_id, principal_id, started_at, ended_at, minutes, note) VALUES (gen_random_uuid(), $1, $2, $3, now(), now(), 600, $4)`,
		org, d.ID, yara, clickup.ImportedTimeNote)
	known, err := g.KnownTimeEntries(ctx)
	if err != nil || !known["9001"] || !known["9002"] || len(known) != 2 {
		t.Fatalf("KnownTimeEntries = %v (%v)", known, err)
	}
	totals, err := g.ImportedTotals(ctx)
	if err != nil || len(totals[d.ID]) != 1 {
		t.Fatalf("ImportedTotals = %v (%v)", totals, err)
	}
	if err := tl.Retire(ctx, totals[d.ID]); err != nil {
		t.Fatal(err)
	}
	var live, hidden int
	_ = db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE deleted_at IS NULL), count(*) FILTER (WHERE deleted_at IS NOT NULL) FROM time_entries WHERE org_id = $1`, org).Scan(&live, &hidden)
	if live != 2 || hidden != 1 {
		t.Fatalf("after retiring: %d live, %d hidden", live, hidden)
	}
	if again, _ := g.ImportedTotals(ctx); len(again[d.ID]) != 0 {
		t.Fatal("a retired total is still offered for retiring")
	}
	if !strings.Contains(clickup.TimeEntryNotePrefix, "ClickUp time entry") {
		t.Fatal("note prefix changed")
	}

	// New work is assigned quietly through Reassign.
	if err := (gapAssign{svc}).AssignQuietly(ctx, org, yara, []docs.Reassignment{{DocID: d.ID, Assignees: []string{nina}, Primary: nina, SkipExpect: true}}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := svc.Assignees(ctx, d.ID); len(ids) != 1 || ids[0] != nina {
		t.Fatalf("assignees = %v", ids)
	}
}
