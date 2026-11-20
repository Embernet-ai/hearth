package views_test

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
	"github.com/Embernet-ai/hearth/internal/views"
)

type fx struct {
	svc   *views.Service
	docs  *docs.Service
	az    *authz.Service
	pools *pg.Pools
	org   string
	alice string
	bob   string
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
		svc: views.New(pools.Web), docs: docs.New(pools.Web, az), az: az, pools: pools,
		org: uuid.NewString(), alice: uuid.NewString(), bob: uuid.NewString(),
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Views Test')`, f.org, "views-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Alice')`, f.alice, f.org)
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Bob')`, f.bob, f.org)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM saved_views WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

func sample() docs.Query {
	return docs.Query{
		Kind: docs.KindWorkOrder,
		Filters: []docs.Filter{
			{Field: docs.Col("status"), Op: docs.OpIs, Values: []string{docs.StatusTodo}},
		},
		Sorts: []docs.Sort{{Field: docs.Col("due")}},
	}
}

// The query has to survive the round trip through jsonb intact. A view that
// loses a filter on the way to the database is worse than no saved views,
// because it silently shows more than the person who saved it expects.
func TestASavedQueryComesBackWhole(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	saved, err := f.svc.Save(ctx, f.org, f.alice, views.View{
		Name: "Open work", Kind: docs.ViewBoard, Query: sample(),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := f.svc.Get(ctx, f.org, f.alice, saved.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Kind != docs.ViewBoard {
		t.Errorf("kind came back %q", got.Kind)
	}
	if len(got.Query.Filters) != 1 || got.Query.Filters[0].Values[0] != docs.StatusTodo {
		t.Fatalf("filters came back %+v", got.Query.Filters)
	}
	if len(got.Query.Sorts) != 1 || got.Query.Sorts[0].Field != docs.Col("due") {
		t.Fatalf("sorts came back %+v", got.Query.Sorts)
	}
}

// The whole point of sharing: a second person opens the same view and runs the
// same query. What they SEE is still filtered by their own grants, which is the
// separate test below.
func TestASharedViewOpensForASecondPerson(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	shared, err := f.svc.Save(ctx, f.org, f.alice, views.View{
		Name: "Shared", Query: sample(), Shared: true,
	})
	if err != nil {
		t.Fatalf("save shared: %v", err)
	}
	private, err := f.svc.Save(ctx, f.org, f.alice, views.View{
		Name: "Private", Query: sample(),
	})
	if err != nil {
		t.Fatalf("save private: %v", err)
	}

	got, err := f.svc.Get(ctx, f.org, f.bob, shared.ID)
	if err != nil {
		t.Fatalf("bob cannot open a shared view: %v", err)
	}
	if len(got.Query.Filters) != 1 {
		t.Errorf("bob got a different query: %+v", got.Query)
	}

	// A private view is not merely hidden from the list, it is unreachable by
	// id, and reported as missing rather than forbidden.
	if _, err := f.svc.Get(ctx, f.org, f.bob, private.ID); !errors.Is(err, views.ErrNotFound) {
		t.Fatalf("bob reached a private view: %v", err)
	}

	list, err := f.svc.List(ctx, f.org, f.bob)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != shared.ID {
		t.Fatalf("bob's list has %d views, want just the shared one", len(list))
	}
}

// Two people open one shared view and each sees their own rows. This is the
// reason a view stores a question and not answers: sharing a list must not be a
// way to disclose what is in it.
func TestASharedViewStillFiltersByTheViewersGrants(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	aliceSubjects, err := f.az.SubjectsFor(ctx, f.org, f.alice)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	bobSubjects, err := f.az.SubjectsFor(ctx, f.org, f.bob)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}

	// Alice raises two work orders and shares only one of them with Bob.
	both, err := f.docs.Create(ctx, f.org, aliceSubjects, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Shared work order",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	hers, err := f.docs.Create(ctx, f.org, aliceSubjects, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Hers alone",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.az.Grant(ctx, f.org, both.Ref(),
		coreSubject(f.bob), core.AccessView, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}

	view, err := f.svc.Save(ctx, f.org, f.alice, views.View{
		Name: "Everything open", Query: sample(), Shared: true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	opened, err := f.svc.Get(ctx, f.org, f.bob, view.ID)
	if err != nil {
		t.Fatalf("bob opens the view: %v", err)
	}

	aliceRows, err := f.docs.Search(ctx, f.org, aliceSubjects, opened.Query)
	if err != nil {
		t.Fatalf("search as alice: %v", err)
	}
	bobRows, err := f.docs.Search(ctx, f.org, bobSubjects, opened.Query)
	if err != nil {
		t.Fatalf("search as bob: %v", err)
	}

	if len(aliceRows) != 2 {
		t.Fatalf("alice sees %d rows through her own view, want 2", len(aliceRows))
	}
	if len(bobRows) != 1 || bobRows[0].ID != both.ID {
		t.Fatalf("bob sees %d rows, want only the one shared with him", len(bobRows))
	}
	for _, r := range bobRows {
		if r.ID == hers.ID {
			t.Fatal("a shared view disclosed a work order Bob has no grant for")
		}
	}
}

// Saving twice under the same name replaces rather than failing, because "save
// this view" pressed again means the second one wins.
func TestSavingTheSameNameReplaces(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	first, err := f.svc.Save(ctx, f.org, f.alice, views.View{Name: "Mine", Query: sample()})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	narrower := sample()
	narrower.Filters = append(narrower.Filters, docs.Filter{
		Field: docs.Col("priority"), Op: docs.OpIs, Values: []string{"3"},
	})
	second, err := f.svc.Save(ctx, f.org, f.alice, views.View{
		Name: "Mine", Query: narrower, Kind: docs.ViewBoard,
	})
	if err != nil {
		t.Fatalf("save again: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("saving the same name made a second view rather than replacing")
	}

	got, _ := f.svc.Get(ctx, f.org, f.alice, first.ID)
	if len(got.Query.Filters) != 2 || got.Kind != docs.ViewBoard {
		t.Errorf("the replacement did not take: %+v kind=%s", got.Query.Filters, got.Kind)
	}

	// Bob is allowed the same name, because names are per person.
	if _, err := f.svc.Save(ctx, f.org, f.bob, views.View{Name: "Mine", Query: sample()}); err != nil {
		t.Errorf("bob cannot have a view called Mine because alice does: %v", err)
	}
}

// Only the owner can delete, including when it is shared. Ignoring somebody
// else's list is not the same authority as removing it from everyone.
func TestOnlyTheOwnerCanDelete(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	v, err := f.svc.Save(ctx, f.org, f.alice, views.View{
		Name: "Shared", Query: sample(), Shared: true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := f.svc.Delete(ctx, f.org, f.bob, v.ID); !errors.Is(err, views.ErrNotFound) {
		t.Fatalf("bob deleted alice's shared view: %v", err)
	}
	if err := f.svc.Delete(ctx, f.org, f.alice, v.ID); err != nil {
		t.Fatalf("alice cannot delete her own view: %v", err)
	}
	if _, err := f.svc.Get(ctx, f.org, f.alice, v.ID); !errors.Is(err, views.ErrNotFound) {
		t.Fatalf("the view survived its own deletion: %v", err)
	}
}

func TestAViewNeedsANameAndAKnownKind(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	if _, err := f.svc.Save(ctx, f.org, f.alice, views.View{Name: "   ", Query: sample()}); !errors.Is(err, views.ErrName) {
		t.Errorf("a blank name was accepted: %v", err)
	}
	if _, err := f.svc.Save(ctx, f.org, f.alice, views.View{
		Name: "Gantt", Kind: "gantt", Query: sample(),
	}); !errors.Is(err, views.ErrKind) {
		t.Errorf("an unknown view kind was accepted: %v", err)
	}
}

// coreSubject builds the grant subject for one person. Written here rather than
// imported so the test reads as the sentence it is asserting.
func coreSubject(principalID string) core.Subject {
	return core.Subject{Kind: core.SubjectPrincipal, ID: principalID}
}
