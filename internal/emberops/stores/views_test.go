package stores_test

import (
	"errors"
	"testing"

	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
	"github.com/Embernet-ai/hearth/internal/views"
)

func TestViewsSaveReplaceListDelete(t *testing.T) {
	e := newEnv(t)
	s := &stores.Views{Base: e.base, Dir: e.dir}

	v1, err := s.Save(e.ctx, org, ann, views.View{Name: "Mine", Query: docs.Query{}})
	if err != nil {
		t.Fatal(err)
	}
	if v1.OwnerID != ann || v1.Kind != docs.ViewTable || v1.ID == "" {
		t.Fatalf("saved = %+v", v1)
	}
	// Saving the same name again replaces it and keeps the id.
	v2, err := s.Save(e.ctx, org, ann, views.View{Name: "Mine", Kind: docs.ViewBoard, Shared: true})
	if err != nil || v2.ID != v1.ID || v2.Kind != docs.ViewBoard || !v2.Shared {
		t.Fatalf("replace = %+v, %v", v2, err)
	}
	if n := len(e.f.All("Hearth View")); n != 1 {
		t.Fatalf("%d view records", n)
	}
	if _, err := s.Save(e.ctx, org, bob, views.View{Name: "Private"}); err != nil {
		t.Fatal(err)
	}

	list, err := s.List(e.as("ann-tok"), org, ann)
	if err != nil {
		t.Fatal(err)
	}
	// ann's own first, then bob's... bob's is not shared, so only ann's.
	if len(list) != 1 || list[0].Name != "Mine" {
		t.Fatalf("list = %+v", list)
	}

	// bob can open ann's shared view but not delete it.
	if _, err := s.Get(e.as("bob-tok"), org, bob, v1.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(e.as("bob-tok"), org, bob, v1.ID); !errors.Is(err, views.ErrNotFound) {
		t.Fatalf("bob deleted ann's view: %v", err)
	}
	if err := s.Delete(e.ctx, org, ann, v1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(e.ctx, org, ann, v1.ID); !errors.Is(err, views.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestViewsRejectBadInput(t *testing.T) {
	e := newEnv(t)
	s := &stores.Views{Base: e.base, Dir: e.dir}
	if _, err := s.Save(e.ctx, org, ann, views.View{Name: "  "}); !errors.Is(err, views.ErrName) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.Save(e.ctx, org, ann, views.View{Name: "x", Kind: "weird"}); !errors.Is(err, views.ErrKind) {
		t.Fatalf("got %v", err)
	}
}

func TestViewsListIsNotNPlusOne(t *testing.T) {
	e := newEnv(t)
	s := &stores.Views{Base: e.base, Dir: e.dir}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		if _, err := s.Save(e.ctx, org, ann, views.View{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	before := len(e.f.Calls)
	if _, err := s.List(e.ctx, org, ann); err != nil {
		t.Fatal(err)
	}
	if calls := len(e.f.Calls) - before; calls > 3 {
		t.Fatalf("listing 5 views took %d calls: %v", calls, e.f.Calls[before:])
	}
}
