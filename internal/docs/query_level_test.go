package docs

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Embernet-ai/hearth/internal/core"
)

const levelSpace = "11111111-2222-3333-4444-555555555555"

// A department opens on its own level, and everything inside is one parameter away. The address says which, so a link
// and a saved view keep meaning what they meant.
func TestADepartmentOpensOnItsOwnLevelAndInsideAsksForTheSubtree(t *testing.T) {
	here := ParseQuery(url.Values{"space": {levelSpace}})
	if here.SpaceID == nil || !here.Direct {
		t.Fatalf("a department link = %+v, want its own level", here)
	}
	if got := here.Encode().Get("inside"); got != "" {
		t.Errorf("the level's own address carries inside=%q", got)
	}
	inside := ParseQuery(url.Values{"space": {levelSpace}, "inside": {"1"}})
	if inside.Direct {
		t.Fatal("inside=1 must ask for the subtree")
	}
	if inside.Encode().Get("inside") != "1" {
		t.Error("a query for the subtree writes inside=1, so its address means the same when it comes back")
	}
	// A query nobody parsed (the API, the agents, a saved view) is the subtree, as it always was.
	built := Query{SpaceID: ptr(levelSpace)}
	if built.Direct || built.Encode().Get("inside") != "1" {
		t.Errorf("a built query = %+v", built)
	}
	if ParseQuery(url.Values{"kind": {"task"}}).Direct {
		t.Error("no space, no level")
	}
}

func TestTheSQLIsTheLevelOrTheSubtree(t *testing.T) {
	var b builder
	direct := b.compile("o", core.Subjects{}, Query{SpaceID: ptr(levelSpace), Direct: true})
	if !strings.Contains(direct, "d.space_id = $") || strings.Contains(direct, "RECURSIVE") {
		t.Errorf("direct SQL = %s", direct)
	}
	var c builder
	sub := c.compile("o", core.Subjects{}, Query{SpaceID: ptr(levelSpace)})
	if !strings.Contains(sub, "RECURSIVE subtree") || strings.Contains(sub, "d.space_id = $") {
		t.Errorf("subtree SQL = %s", sub)
	}
}

func ptr(s string) *string { return &s }
