package web

import "github.com/Embernet-ai/hearth/internal/plant"

// orgNode is the sidebar's view of one space, with its children attached so
// the template can recurse over a real tree instead of filtering a flat list
// by depth.
//
// SpaceTree already does the hard part: it returns every space in a single
// query, pre-order sorted, with a cycle guard. This just turns that flat,
// correctly-ordered list into parent/child pointers -- nothing here re-derives
// order or re-checks for cycles, both of which the query already settled.
type orgNode struct {
	*plant.Space
	Children []*orgNode

	// Selected is true when this is the department currently being viewed
	// (p.SpaceID), so the template can mark it without reaching outside its
	// own context -- {{template}} resets $, so a node carries what it needs
	// to render itself rather than the page trying to hand it down.
	Selected bool

	// Open is true when this node or one of its descendants is Selected, so
	// the path to wherever the reader is stays expanded rather than
	// collapsing every time they follow a link deeper into the tree.
	Open bool
}

// buildOrgTree turns SpaceTree's flat list into a real tree, and marks the
// path down to selectedID as open.
func buildOrgTree(flat []*plant.Space, selectedID string) []*orgNode {
	// The panel is for departments: areas and their folders. A project (a list
	// of work, such as a ClickUp list) and anything inside one stays out of it
	// and is reached under Projects. Nothing is changed or moved, only drawn.
	all := make(map[string]*plant.Space, len(flat))
	for _, sp := range flat {
		all[sp.ID] = sp
	}
	inProject := func(sp *plant.Space) bool {
		for depth := 0; sp != nil && depth < 64; depth++ {
			if sp.Kind == "project" {
				return true
			}
			if sp.ParentID == nil {
				return false
			}
			sp = all[*sp.ParentID]
		}
		return false
	}
	byID := make(map[string]*orgNode, len(flat))
	for _, sp := range flat {
		if inProject(sp) {
			continue
		}
		byID[sp.ID] = &orgNode{Space: sp, Selected: sp.ID == selectedID}
	}

	var roots []*orgNode
	for _, sp := range flat {
		n := byID[sp.ID]
		if n == nil {
			continue
		}
		if sp.ParentID != nil {
			if parent, ok := byID[*sp.ParentID]; ok {
				parent.Children = append(parent.Children, n)
				continue
			}
		}
		// No parent, or a parent the caller cannot see -- SpaceTree already
		// treats that second case as a root, for the same reason: hiding a
		// child because its parent is private would lose work somebody was
		// explicitly granted.
		roots = append(roots, n)
	}

	if selectedID != "" {
		// Walked on the full list, so standing in a project keeps the
		// department above it open.
		for sp := all[selectedID]; sp != nil; {
			if n := byID[sp.ID]; n != nil {
				n.Open = true
			}
			if sp.ParentID == nil {
				break
			}
			sp = all[*sp.ParentID]
		}
	}

	return roots
}

// crumb is one link in a breadcrumb: a space's id and name, nothing else --
// the template builds the URL, the same way every other space link does.
type crumb struct {
	ID   string
	Name string
}

// spaceCrumbs returns the path from the root down to id, root first, so a
// template can range over it in reading order and print a separator between
// each.
//
// Built from the same flat list SpaceTree already returned for the rail, not
// a second query: the tree and the breadcrumb are two views of one fetch, and
// asking the database twice for what render() already has on hand is the
// kind of thing that is cheap once and expensive at scale.
func spaceCrumbs(flat []*plant.Space, id string) []crumb {
	if id == "" {
		return nil
	}
	byID := make(map[string]*plant.Space, len(flat))
	for _, sp := range flat {
		byID[sp.ID] = sp
	}

	// A space the reader cannot see is not in flat, so the walk stops there
	// rather than at a nil: a link to somebody else's folder, or a member
	// arriving at a project whose parent they were never shown, gets a shorter
	// breadcrumb instead of a 500. It used to start with ok forced true, which
	// dereferenced nil the moment the first space was missing.
	var reversed []crumb
	for sp, ok := byID[id]; ok; sp, ok = byID[id] {
		reversed = append(reversed, crumb{ID: sp.ID, Name: sp.Name})
		if sp.ParentID == nil {
			break
		}
		id = *sp.ParentID
	}

	out := make([]crumb, len(reversed))
	for i, c := range reversed {
		out[len(reversed)-1-i] = c
	}
	return out
}
