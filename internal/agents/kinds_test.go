package agents

import (
	"slices"
	"testing"
)

func TestKindsFor(t *testing.T) {
	open := &Caller{}
	confined := &Caller{Kinds: []string{"task", "work_order"}}

	got, err := open.kindsFor("")
	if err != nil || !slices.Equal(got, []string{""}) {
		t.Fatalf("unconfined, no kind: %v %v, want one empty kind (everything)", got, err)
	}
	got, err = open.kindsFor("asset")
	if err != nil || !slices.Equal(got, []string{"asset"}) {
		t.Fatalf("unconfined, asset: %v %v", got, err)
	}

	got, err = confined.kindsFor("")
	if err != nil || !slices.Equal(got, []string{"task", "work_order"}) {
		t.Fatalf("confined, no kind: %v %v, want its own kinds", got, err)
	}
	got, err = confined.kindsFor("work_order")
	if err != nil || !slices.Equal(got, []string{"work_order"}) {
		t.Fatalf("confined, inside: %v %v", got, err)
	}
	if _, err = confined.kindsFor("asset"); err == nil {
		t.Fatal("a confined caller must be refused a kind outside its tool")
	}
}

func TestCoversKind(t *testing.T) {
	if !(&Caller{}).coversKind("anything") {
		t.Error("an unconfined caller covers every kind")
	}
	c := &Caller{Kinds: []string{"doc", "wiki"}}
	if !c.coversKind("wiki") || c.coversKind("asset") {
		t.Error("a confined caller covers exactly its kinds")
	}
}
