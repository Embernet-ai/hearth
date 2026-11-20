package web

// Projects.
//
// They were never missing. 139 of the space tree's rows are kind='project',
// filed under a department or a folder inside one, and they came across from
// ClickUp that way. What was missing was any way to reach them: the rail
// renders depth 0 and 1, a project sits at depth 2, and the only page that
// listed the whole tree was /spaces, which is a management screen with rename
// and move forms on every row and is not on the rail either.
//
// So the complaint was "still not able to see projects", and the answer is a
// page rather than a migration. Nothing here creates a project or changes what
// one is.

import (
	"net/http"
	"sort"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/plant"
)

// projectGroup is one department, with the projects under it.
//
// Grouped rather than a flat list of 139, because "Marketing has eleven
// projects" is the shape somebody reads and a single alphabetical run of a
// hundred and thirty-nine is a wall.
type projectGroup struct {
	Department string
	Projects   []*plant.Space
	// Documents is the total across the group, so a department that holds
	// nothing says so without the reader adding up.
	Documents int
}

func (s *Server) getProjects(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())

	tree, err := s.plant.SpaceTree(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(r.Context(), "space tree", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not read the projects")
		return
	}

	// The department a project belongs to is the first segment of its path,
	// which SpaceTree already builds. Reading it from there rather than walking
	// parents again means the page and the rail agree about the tree.
	byDept := map[string]*projectGroup{}
	for _, sp := range tree {
		if sp.Kind != "project" {
			continue
		}
		dept := sp.Path
		if i := strings.Index(dept, " / "); i >= 0 {
			dept = dept[:i]
		}
		if dept == "" || dept == sp.Name {
			dept = "Unfiled"
		}
		g := byDept[dept]
		if g == nil {
			g = &projectGroup{Department: dept}
			byDept[dept] = g
		}
		g.Projects = append(g.Projects, sp)
		g.Documents += sp.Documents
	}

	groups := make([]*projectGroup, 0, len(byDept))
	for _, g := range byDept {
		sort.Slice(g.Projects, func(i, j int) bool { return g.Projects[i].Name < g.Projects[j].Name })
		groups = append(groups, g)
	}
	// Departments alphabetically, except Unfiled last: it is a residue rather
	// than a department and putting it in the run implies it is one.
	sort.Slice(groups, func(i, j int) bool {
		if (groups[i].Department == "Unfiled") != (groups[j].Department == "Unfiled") {
			return groups[j].Department == "Unfiled"
		}
		return groups[i].Department < groups[j].Department
	})

	var total int
	for _, g := range groups {
		total += len(g.Projects)
	}

	s.render(w, r, "projects", page{
		Title: "Projects", ProjectGroups: groups, ProjectCount: total,
	})
}
