package docs_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

type fx struct {
	svc   *docs.Service
	az    *authz.Service
	pools *pg.Pools
	org   string
	alice string
	bob   string // in the channel
	dave  string // in nothing
	chID  string
}

func setup(t *testing.T) *fx {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pools.Close)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := pg.Migrate(ctx, pools, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	az := authz.New(pools.Web)
	f := &fx{
		svc: docs.New(pools.Web, az), az: az, pools: pools,
		org: uuid.NewString(), alice: uuid.NewString(),
		bob: uuid.NewString(), dave: uuid.NewString(), chID: uuid.NewString(),
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Docs Test')`, f.org, "docs-"+f.org[:8])
	for n, id := range map[string]string{"Alice": f.alice, "Bob": f.bob, "Dave": f.dave} {
		exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user',$3)`, id, f.org, n)
	}
	exec(`INSERT INTO channels (id, org_id, slug, name, created_by) VALUES ($1,$2,$3,'Maintenance',$4)`,
		f.chID, f.org, "maint-"+f.chID[:8], f.alice)
	exec(`INSERT INTO channel_members (channel_id, principal_id) VALUES ($1,$2)`, f.chID, f.bob)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM entity_links WHERE org_id=$1`,
			`DELETE FROM checklist_items WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM property_values WHERE def_id IN (SELECT id FROM property_defs WHERE org_id=$1)`,
			`DELETE FROM property_defs WHERE org_id=$1`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM spaces WHERE org_id=$1`,
			`DELETE FROM channel_members WHERE channel_id IN (SELECT id FROM channels WHERE org_id=$1)`,
			`DELETE FROM channels WHERE org_id=$1`,
			`DELETE FROM capability_grants WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

func (f *fx) subj(t *testing.T, p string) core.Subjects {
	t.Helper()
	s, err := f.az.SubjectsFor(context.Background(), f.org, p)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	return s
}

// Creating something you cannot then see is the failure this guards against.
// It is easy to write, invisible in review, and only discovered by a confused
// user asking where their document went.
func TestCreatorCanImmediatelySeeWhatTheyCreated(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Line 3 conveyor faulting",
		Body: "Motor current spiking on start.",
		Task: &docs.TaskFields{Status: docs.StatusTodo, Priority: 2},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := f.svc.Get(ctx, f.org, alice, d.ID)
	if err != nil {
		t.Fatalf("creator cannot see their own document: %v", err)
	}
	if got.Title != "Line 3 conveyor faulting" {
		t.Errorf("title = %q", got.Title)
	}
	if got.Task == nil || got.Task.Status != docs.StatusTodo {
		t.Errorf("work order has no task fields: %+v", got.Task)
	}
	if got.Version != 1 {
		t.Errorf("version = %d, want 1", got.Version)
	}
}

// A document filed into a space has to be visible to whoever already holds
// that space, not only to whoever created it. Without Create copying the
// space's own grants onto the new document, granting somebody a department
// shows them the space in their tree and nothing that is actually filed in
// it -- the grant would exist on the folder and never on what the folder
// holds.
func TestCreateInheritsGrantsFromItsSpace(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	spaceID := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO spaces (id, org_id, name, kind, created_by) VALUES ($1,$2,'Engineering','area',$3)`,
		spaceID, f.org, f.alice); err != nil {
		t.Fatalf("create space: %v", err)
	}
	// Dave holds the space directly, and nothing else -- he is otherwise "in
	// nothing" per the fixture's own naming, which is what makes this the
	// space grant doing the work rather than something else already covering
	// him.
	if err := f.az.Grant(ctx, f.org, core.Ref{Type: core.EntitySpace, ID: spaceID},
		core.Subject{Kind: core.SubjectPrincipal, ID: f.dave}, core.AccessEdit, f.alice); err != nil {
		t.Fatalf("grant space to dave: %v", err)
	}

	d, err := f.svc.Create(ctx, f.org, f.subj(t, f.alice), docs.CreateParams{
		Kind: docs.KindTask, Title: "Fix the line", SpaceID: &spaceID,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	level, err := f.az.LevelFor(ctx, f.subj(t, f.dave), core.Ref{Type: core.EntityDocument, ID: d.ID})
	if err != nil {
		t.Fatalf("LevelFor: %v", err)
	}
	if level != core.AccessEdit {
		t.Errorf("dave has %q on a document filed in a space he holds edit on, want edit", level)
	}
}

// The denial. A document nobody shared with Dave must not exist as far as Dave
// is concerned, and he must be told "not found" rather than "forbidden".
func TestUnrelatedPrincipalIsDeniedAndToldNothing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d, err := f.svc.Create(ctx, f.org, f.subj(t, f.alice), docs.CreateParams{
		Kind: docs.KindDoc, Title: "Rates for the Q3 contract",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = f.svc.Get(ctx, f.org, f.subj(t, f.dave), d.ID)
	if !errors.Is(err, docs.ErrNotFound) {
		t.Fatalf("dave got something other than not-found: %v", err)
	}
	// And it must not appear in a listing either. A denial on Get is worth
	// little if List leaks the title.
	list, err := f.svc.List(ctx, f.org, f.subj(t, f.dave), docs.ListParams{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, item := range list {
		if item.ID == d.ID {
			t.Fatal("a document Dave cannot read appeared in his listing")
		}
	}
}

// Attaching a document to a channel grants the channel. This is the claim
// "channel membership IS the permission model" being true rather than described.
func TestChannelMembershipGrantsAccess(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d, err := f.svc.Create(ctx, f.org, f.subj(t, f.alice), docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Replace bearing", ChannelID: &f.chID,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Bob is in the channel and was never mentioned by name.
	if _, err := f.svc.Get(ctx, f.org, f.subj(t, f.bob), d.ID); err != nil {
		t.Fatalf("channel member cannot see a document in their channel: %v", err)
	}
	// Dave is not.
	if _, err := f.svc.Get(ctx, f.org, f.subj(t, f.dave), d.ID); !errors.Is(err, docs.ErrNotFound) {
		t.Fatalf("non-member could see a channel document: %v", err)
	}
}

func TestUpdateBumpsVersionAndRecordsHistory(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	d, _ := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindDoc, Title: "Runbook", Body: "v1"})

	body := "v2"
	updated, err := f.svc.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Body: &body, ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Version != 2 {
		t.Errorf("version = %d, want 2", updated.Version)
	}

	var versions int
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT count(*) FROM document_versions WHERE document_id=$1`, d.ID).Scan(&versions); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	// Creation plus one edit. History starting at the first EDIT would mean the
	// original is unrecoverable.
	if versions != 2 {
		t.Errorf("version rows = %d, want 2 (creation and the edit)", versions)
	}
}

// Without this, two people editing at once means one silently loses their work
// and never finds out. There is no CRDT, so the version check IS the answer.
func TestStaleEditIsRejectedRatherThanSilentlyWinning(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	d, _ := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindDoc, Title: "Procedure", Body: "original"})

	first := "alice's edit"
	if _, err := f.svc.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{Body: &first, ExpectedVersion: 1}); err != nil {
		t.Fatalf("first update: %v", err)
	}

	// Bob was looking at version 1 the whole time.
	second := "bob's edit, based on the old text"
	_, err := f.svc.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{Body: &second, ExpectedVersion: 1})
	if !errors.Is(err, docs.ErrConflict) {
		t.Fatalf("a stale edit overwrote a newer one: %v", err)
	}

	got, _ := f.svc.Get(ctx, f.org, alice, d.ID)
	if got.Body != first {
		t.Errorf("body = %q, want alice's edit preserved", got.Body)
	}
}

func TestCompletedAtFollowsStatus(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	d, _ := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindTask, Title: "Grease the bearing",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})

	got, err := f.svc.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Task: &docs.TaskPatch{Status: ptr(docs.StatusDone)}, ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	// Derived, not accepted from the caller, so "done" and "when" cannot disagree.
	if got.Task.CompletedAt == nil {
		t.Error("status is done but completed_at was not set")
	}

	got, err = f.svc.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Task: &docs.TaskPatch{Status: ptr(docs.StatusInProgress)}, ExpectedVersion: got.Version,
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got.Task.CompletedAt != nil {
		t.Error("task was reopened but completed_at survived")
	}
}

// A work order pointing at the asset it concerns, which is the thing a
// per-seat tool cannot do because its tasks and its docs are different products.
func TestWorkOrderLinksToAsset(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	asset, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindAsset, Title: "Conveyor, Line 3",
	})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	wo, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Bearing replacement",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	if err != nil {
		t.Fatalf("create work order: %v", err)
	}

	if err := f.svc.Link(ctx, f.org, alice, wo.Ref(), asset.Ref(), "concerns"); err != nil {
		t.Fatalf("link: %v", err)
	}

	// Indexed both directions, so the asset can show its history.
	var reverse int
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT count(*) FROM entity_links WHERE to_type='document' AND to_id=$1`, asset.ID).Scan(&reverse); err != nil {
		t.Fatalf("reverse lookup: %v", err)
	}
	if reverse != 1 {
		t.Errorf("asset has %d inbound links, want 1", reverse)
	}
}

func TestListFiltersByKind(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	for _, k := range []string{docs.KindWorkOrder, docs.KindAsset, docs.KindDoc} {
		if _, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: k, Title: k}); err != nil {
			t.Fatalf("create %s: %v", k, err)
		}
	}
	list, err := f.svc.List(ctx, f.org, alice, docs.ListParams{Kind: docs.KindAsset})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Kind != docs.KindAsset {
		t.Fatalf("kind filter returned %d rows: %+v", len(list), list)
	}
}

func TestUnknownKindIsRejected(t *testing.T) {
	f := setup(t)
	_, err := f.svc.Create(context.Background(), f.org, f.subj(t, f.alice),
		docs.CreateParams{Kind: "invoice", Title: "nope"})
	if !errors.Is(err, docs.ErrKind) {
		t.Fatalf("an unknown kind was accepted: %v", err)
	}
}

func TestDeleteRequiresAdminNotEdit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	d, _ := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindDoc, Title: "Shared"})

	// Bob gets edit, deliberately not admin.
	if err := f.az.Grant(ctx, f.org, d.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessEdit, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	bob := f.subj(t, f.bob)

	title := "bob was here"
	if _, err := f.svc.Update(ctx, f.org, bob, d.ID, docs.UpdateParams{Title: &title}); err != nil {
		t.Fatalf("bob should be able to edit: %v", err)
	}
	// Removing something is not the same authority as changing it.
	if err := f.svc.Delete(ctx, f.org, bob, d.ID); !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("edit access was enough to delete: %v", err)
	}
	if err := f.svc.Delete(ctx, f.org, alice, d.ID); err != nil {
		t.Fatalf("admin could not delete: %v", err)
	}
}
