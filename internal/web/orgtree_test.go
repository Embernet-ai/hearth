package web

import (
	"testing"

	"github.com/Embernet-ai/hearth/internal/plant"
)

// The breadcrumb walks up from a space through the spaces the reader can see.
// A space that is not among them -- a link to a folder they were never shown --
// ends the walk; it used to dereference nil and turn the whole list page into
// a 500 (Oct 15).
func TestSpaceCrumbsStopAtWhatTheReaderCannotSee(t *testing.T) {
	ops := "ops"
	training := "training"
	flat := []*plant.Space{
		{ID: ops, Name: "Operations"},
		{ID: training, Name: "Training", ParentID: &ops},
		{ID: "technical", Name: "Technical", ParentID: &training},
	}

	got := spaceCrumbs(flat, "technical")
	if len(got) != 3 || got[0].Name != "Operations" || got[2].Name != "Technical" {
		t.Fatalf("full path: got %+v", got)
	}

	// The space itself is not visible: no breadcrumb, no panic.
	if got := spaceCrumbs(flat, "somebody-elses-folder"); len(got) != 0 {
		t.Fatalf("an unseen space produced a breadcrumb: %+v", got)
	}

	// Visible, but its parent is not: the path starts where visibility does.
	hidden := "hidden"
	partial := []*plant.Space{{ID: "project", Name: "Project", ParentID: &hidden}}
	if got := spaceCrumbs(partial, "project"); len(got) != 1 || got[0].Name != "Project" {
		t.Fatalf("a hidden parent: got %+v", got)
	}

	if got := spaceCrumbs(flat, ""); got != nil {
		t.Fatalf("no space: got %+v", got)
	}
}
