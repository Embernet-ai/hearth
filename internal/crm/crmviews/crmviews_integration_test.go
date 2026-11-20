package crmviews_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/crm/crmviews"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

func TestSavedCRMViews(t *testing.T) {
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
	org, alice, bob := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, q := range [][]any{
		{`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'CRM Views Test')`, org, "crmviews-" + org[:8]},
		{`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Alice')`, alice, org},
		{`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Bob')`, bob, org},
	} {
		if _, err := pools.Web.Exec(ctx, q[0].(string), q[1:]...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	svc := crmviews.New(pools.Web)

	mine, err := svc.Save(ctx, org, alice, crmviews.View{List: "deals", Name: "Proposals", Query: "stage=proposal&group=owner"})
	if err != nil || mine.ID == "" {
		t.Fatalf("save: %v", err)
	}
	// saving the same name again replaces it
	_, err = svc.Save(ctx, org, alice, crmviews.View{List: "deals", Name: "Proposals", Query: "stage=proposal&group=company", Shared: true})
	if err != nil {
		t.Fatalf("resave: %v", err)
	}
	if _, err := svc.Save(ctx, org, alice, crmviews.View{List: "companies", Name: "Proposals", Query: "kind=vendor"}); err != nil {
		t.Fatalf("a name is per list: %v", err)
	}
	if _, err := svc.Save(ctx, org, alice, crmviews.View{List: "deals", Name: "  "}); !errors.Is(err, crmviews.ErrName) {
		t.Fatalf("a view needs a name: %v", err)
	}
	if _, err := svc.Save(ctx, org, alice, crmviews.View{List: "documents", Name: "x"}); !errors.Is(err, crmviews.ErrList) {
		t.Fatalf("only the CRM lists: %v", err)
	}

	got, err := svc.List(ctx, org, alice, "deals")
	if err != nil || len(got) != 1 || got[0].Query != "stage=proposal&group=company" || !got[0].Shared {
		t.Fatalf("alice sees her one deals view, replaced: %+v %v", got, err)
	}
	// Bob sees Alice's shared view, not her companies view, and cannot delete it
	seen, _ := svc.List(ctx, org, bob, "deals")
	if len(seen) != 1 || seen[0].OwnerName != "Alice" || seen[0].Own(bob) {
		t.Fatalf("bob sees the shared view as hers: %+v", seen)
	}
	if other, _ := svc.List(ctx, org, bob, "companies"); len(other) != 0 {
		t.Fatalf("an unshared view is private: %+v", other)
	}
	if err := svc.Delete(ctx, org, bob, got[0].ID); !errors.Is(err, crmviews.ErrNotFound) {
		t.Fatalf("somebody else's view is not his to delete: %v", err)
	}
	if err := svc.Delete(ctx, org, alice, got[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if left, _ := svc.List(ctx, org, alice, "deals"); len(left) != 0 {
		t.Fatalf("deleted: %+v", left)
	}
}
