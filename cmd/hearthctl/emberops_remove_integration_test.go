package main

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/fake"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// Taking documents and spaces out from a shell has to reach EmberOps the way the pages do. With HEARTH_BACKEND=emberops,
// space-remove and doc-delete went to the Postgres services, so every task and space they removed stayed in EmberOps: the task in
// EmberOps's own list, and a deleted space that could never go because its tasks still linked it. They now take their stores from
// the same place the pages do, and these run the real Postgres services under the EmberOps stores against a fake Frappe.
type removeFx struct {
	set           stores.Set
	pgSet         stores.Set
	f             *fake.Frappe
	az            *authz.Service
	db            *pgxpool.Pool
	org, admin    string
	actor         core.Subjects
	space, spaceN string
}

func newRemoveFx(t *testing.T) *removeFx {
	t.Helper()
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
	az := authz.New(db)
	fx := &removeFx{az: az, db: db, org: uuid.NewString(), admin: uuid.NewString()}
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Remove Test')`, fx.org, "rm-"+fx.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Admin')`, fx.admin, fx.org)
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$2)`, fx.org, fx.admin)
	t.Cleanup(func() {
		for _, q := range []string{`SELECT audit_purge_org($1)`, `DELETE FROM outbox WHERE org_id=$1`, `DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM entity_links WHERE org_id=$1`, `DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`, `DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM spaces WHERE org_id=$1`, `DELETE FROM capability_grants WHERE org_id=$1`, `DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`} {
			_, _ = db.Exec(context.Background(), q, fx.org)
		}
	})

	fx.f = fake.New(map[string]string{"svc-tok": "svc@example.com"})
	t.Cleanup(fx.f.Close)
	u, _ := url.Parse(fx.f.URL)
	client := emberops.New(emberops.Config{BaseURL: u, Service: emberops.StaticToken("svc-tok")})
	docsSvc, plantSvc := docs.New(db, az), plant.New(db, az)
	pgp := stores.PG{Docs: docsSvc, Plant: plantSvc, Time: timelog.New(db, az)}
	spaces := &stores.SpaceSync{C: client, Source: plantSvc, Dir: stores.Directory{C: client}}
	fx.set = stores.EmberOPS(client, pgp, stores.Options{Authz: az, Spaces: spaces})
	fx.pgSet = stores.Postgres(pgp)
	var err2 error
	if fx.actor, err2 = az.SubjectsFor(ctx, fx.org, fx.admin); err2 != nil {
		t.Fatal(err2)
	}
	return fx
}

// a space with two tasks and a document in it, made through the stores.
func (fx *removeFx) fill(t *testing.T, set stores.Set) (spaceID string, ids []string) {
	t.Helper()
	ctx := context.Background()
	sp, err := set.Plant.CreateSpace(ctx, fx.org, fx.actor, "Proof "+uuid.NewString()[:6], "folder", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []docs.CreateParams{
		{Kind: docs.KindTask, Title: "one", SpaceID: &sp.ID, Task: &docs.TaskFields{Status: docs.StatusTodo}},
		{Kind: docs.KindTask, Title: "two", SpaceID: &sp.ID, Task: &docs.TaskFields{Status: docs.StatusDone}},
		{Kind: docs.KindDoc, Title: "notes", SpaceID: &sp.ID},
	} {
		d, err := set.Docs.Create(ctx, fx.org, fx.actor, p)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
	}
	return sp.ID, ids
}

func TestDocDeleteReachesEmberOps(t *testing.T) {
	fx := newRemoveFx(t)
	ctx := context.Background()
	_, ids := fx.fill(t, fx.set)
	if len(fx.f.All("Task")) != 2 || len(fx.f.All("Hearth Document")) != 1 {
		t.Fatalf("before: %d tasks, %d documents in EmberOps", len(fx.f.All("Task")), len(fx.f.All("Hearth Document")))
	}
	var list []docRef
	for _, id := range ids {
		list = append(list, docRef{id, id})
	}
	if err := deleteDocuments(ctx, fx.set.Docs, fx.az, fx.org, fx.admin, fx.actor, list, io.Discard); err != nil {
		t.Fatal(err)
	}
	if n := len(fx.f.All("Task")); n != 0 {
		t.Errorf("%d tasks left in EmberOps after doc-delete", n)
	}
	if n := len(fx.f.All("Hearth Document")); n != 0 {
		t.Errorf("%d documents left in EmberOps after doc-delete", n)
	}
	// The control: the Postgres services never touched EmberOps, which is what these commands used to do.
	_, ids = fx.fill(t, fx.set)
	var list2 []docRef
	for _, id := range ids {
		list2 = append(list2, docRef{id, id})
	}
	if err := deleteDocuments(ctx, fx.pgSet.Docs, fx.az, fx.org, fx.admin, fx.actor, list2, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(fx.f.All("Task")) != 2 {
		t.Errorf("control: through the Postgres services the EmberOps tasks should have stayed, found %d", len(fx.f.All("Task")))
	}
}

func TestSpaceRemoveReachesEmberOps(t *testing.T) {
	fx := newRemoveFx(t)
	ctx := context.Background()
	space, ids := fx.fill(t, fx.set)
	if err := fx.set.Plant.(*stores.Plant).Spaces.Sync(ctx, fx.org); err != nil {
		t.Fatal(err)
	}
	if len(fx.f.All("Hearth Space")) != 1 {
		t.Fatalf("before: %d spaces in EmberOps", len(fx.f.All("Hearth Space")))
	}
	items := []spaceItem{{ids[0], docs.KindTask, "one", docs.StatusTodo}, {ids[1], docs.KindTask, "two", docs.StatusDone}, {ids[2], docs.KindDoc, "notes", ""}}
	if err := closeOutSpace(ctx, fx.set.Docs, fx.set.Plant, fx.az, fx.org, fx.admin, fx.actor, space, items, io.Discard); err != nil {
		t.Fatal(err)
	}
	if n := len(fx.f.All("Hearth Space")); n != 0 {
		t.Errorf("%d spaces left in EmberOps after space-remove: the space the command deleted is still there", n)
	}
	// The documents are kept, as the command always did, and the open task was cancelled in Hearth.
	var status string
	if err := fx.db.QueryRow(ctx,
		`SELECT status FROM task_fields WHERE document_id = $1`, ids[0]).Scan(&status); err != nil || status != docs.StatusCancelled {
		t.Errorf("the open task is %q (%v), want cancelled", status, err)
	}
}
