package migrate_test

import (
	"context"
	"log/slog"
	"net/url"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/fake"
	"github.com/Embernet-ai/hearth/internal/emberops/migrate"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// TestPostgresStepsRunAgainstARealSchema is what proves the steps' SQL: every
// query executes against the migrated schema, joins included, and the rows they
// read come out as the documents the addon expects.
func TestPostgresStepsRunAgainstARealSchema(t *testing.T) {
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

	org, ann, task, sub, space := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	// A fresh organisation sorts after any older one, so make this the oldest.
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name, created_at) VALUES ($1, $2, 'migrate-test', '1970-01-01')`, org, "migrate-"+org[:8])
	// principals and spaces have no cascade from orgs, so an org with people in it
	// cannot be deleted directly. Left behind, it is the oldest org in the
	// database and the next run migrates it instead of this one.
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM documents WHERE org_id = $1`,
			`DELETE FROM spaces WHERE org_id = $1`,
			`DELETE FROM principals WHERE org_id = $1`,
			`DELETE FROM orgs WHERE id = $1`,
		} {
			if _, err := db.Exec(ctx, q, org); err != nil {
				t.Errorf("cleanup: %v\n%s", err, q)
			}
		}
	})
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Ann')`, ann, org)
	exec(`INSERT INTO local_credentials (principal_id, org_id, email, password_hash) VALUES ($1,$2,$3,'x')`, ann, org, "ann-"+ann[:8]+"@example.com")
	exec(`INSERT INTO spaces (id, org_id, name, kind, created_by) VALUES ($1,$2,'Ops','folder',$3)`, space, org, ann)
	exec(`INSERT INTO documents (id, org_id, space_id, kind, title, body, owner_id, created_by, updated_by) VALUES ($1,$2,$3,'task','Fix belt','b',$4,$4,$4)`, task, org, space, ann)
	exec(`INSERT INTO task_fields (document_id, status, priority, assignee_id) VALUES ($1,'in_progress',2,$2)`, task, ann)
	exec(`INSERT INTO documents (id, org_id, space_id, kind, title, body, owner_id, created_by, updated_by, parent_id) VALUES ($1,$2,$3,'task','Sub','',$4,$4,$4,$5)`, sub, org, space, ann, task)
	exec(`INSERT INTO task_fields (document_id, status, priority) VALUES ($1,'todo',0)`, sub)
	exec(`INSERT INTO document_assignees (document_id, principal_id, is_primary) VALUES ($1,$2,true)`, task, ann)
	exec(`INSERT INTO checklist_items (id, document_id, position, text) VALUES ($1,$2,1,'step one')`, uuid.NewString(), task)

	dry, err := migrate.NewRunner(db, nil).Run(ctx, migrate.Options{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	by := map[string]migrate.StepReport{}
	for _, s := range dry.Steps {
		by[s.Name] = s
	}
	if by["tasks"].Source != 2 || by["tasks"].Mapped != 2 || by["spaces"].Mapped != 1 || by["users"].Mapped != 1 {
		t.Fatalf("dry run counts: tasks=%+v spaces=%+v users=%+v", by["tasks"], by["spaces"], by["users"])
	}

	f := fake.New(map[string]string{"svc": "svc@example.com"})
	defer f.Close()
	u, _ := url.Parse(f.URL)
	c := emberops.New(emberops.Config{BaseURL: u, Service: emberops.StaticToken("svc")})
	rep, err := migrate.NewRunner(db, c).Run(ctx, migrate.Options{Restart: true})
	if err != nil || !rep.Ok() {
		t.Fatalf("run: %v\n%+v", err, rep)
	}
	var parent, child map[string]any
	for _, d := range f.All("Task") {
		switch d["hearth_id"] {
		case task:
			parent = d
		case sub:
			child = d
		}
	}
	if parent == nil || child == nil {
		t.Fatalf("tasks in EmberOPS: %v", f.All("Task"))
	}
	if parent["status"] != "Working" || parent["priority"] != "High" || child["parent_task"] != parent["name"] {
		t.Fatalf("parent=%v child=%v", parent, child)
	}
	if rows, _ := parent["hearth_checklist"].([]any); len(rows) != 1 {
		t.Fatalf("checklist = %v", parent["hearth_checklist"])
	}
}
