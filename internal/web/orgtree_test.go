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

func treeSpace(id, parent, name, kind string) *plant.Space {
	s := &plant.Space{ID: id, Name: name, Kind: kind}
	if parent != "" {
		s.ParentID = &parent
	}
	return s
}

func treeNames(ns []*orgNode) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.Name)
	}
	return out
}

// The Departments panel draws departments and their folders as a tree. A list
// of work (a project) and everything inside one stays out of it; it is reached
// under Projects. This is why "SUSE White Label" and "Mexico Manufacturing
// Setup" stopped showing up beside Sales.
func TestOrgTreeDrawsTheHierarchyAndLeavesProjectsOut(t *testing.T) {
	flat := []*plant.Space{
		treeSpace("eng", "", "Engineering", "area"),
		treeSpace("wt", "eng", "White Team", "folder"),
		treeSpace("mcsi", "wt", "MCSI", "folder"),
		treeSpace("mcs1001", "mcsi", "MCS1001 Duke Oklahoma", "folder"),
		treeSpace("list", "mcsi", "MCS0342 Alpha Shear", "project"),
		treeSpace("sub", "list", "A folder inside a list", "folder"),
		treeSpace("sales", "", "Sales", "area"),
		treeSpace("suse", "sales", "SUSE White Label", "project"),
	}
	roots := buildOrgTree(flat, "")

	if got := treeNames(roots); len(got) != 2 || got[0] != "Engineering" || got[1] != "Sales" {
		t.Fatalf("roots = %v, want Engineering and Sales", got)
	}
	wt := roots[0].Children[0]
	if wt.Name != "White Team" || len(wt.Children) != 1 || wt.Children[0].Name != "MCSI" {
		t.Fatalf("White Team should hold MCSI, got %+v", treeNames(wt.Children))
	}
	mcsi := wt.Children[0]
	if got := treeNames(mcsi.Children); len(got) != 1 || got[0] != "MCS1001 Duke Oklahoma" {
		t.Fatalf("MCSI children = %v, want only the folder (the list and what is in it stay out)", got)
	}
	if len(roots[1].Children) != 0 {
		t.Fatalf("Sales children = %v, want none (SUSE White Label is a project)", treeNames(roots[1].Children))
	}
}

// Standing in a project keeps the department above it open, so the reader
// does not lose their place in the panel.
func TestOrgTreeKeepsThePathToTheSelectionOpen(t *testing.T) {
	flat := []*plant.Space{
		treeSpace("eng", "", "Engineering", "area"),
		treeSpace("wt", "eng", "White Team", "folder"),
		treeSpace("list", "wt", "A list", "project"),
		treeSpace("fin", "", "Finance", "area"),
	}
	roots := buildOrgTree(flat, "list")
	if !roots[0].Open || !roots[0].Children[0].Open {
		t.Fatalf("the path down to a project should be open: %+v", roots[0])
	}
	if roots[1].Open {
		t.Fatal("an unrelated department should stay closed")
	}
	roots = buildOrgTree(flat, "wt")
	if !roots[0].Children[0].Selected || roots[0].Selected {
		t.Fatal("only the viewed folder is marked selected")
	}
}
