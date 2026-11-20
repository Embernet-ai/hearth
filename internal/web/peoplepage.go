package web

// The People pages: a short list of everybody, and one page per person with
// everything about them in one place. It used to be four tables that each
// listed every person again, doing four unrelated jobs -- and the one that
// grants access sat third, far enough down that people concluded it was gone.

import (
	"net/http"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/plant"
)

// peopleData loads everything both People pages read, after the People check.
// ok is false when a response has already been written.
func (s *Server) peopleData(w http.ResponseWriter, r *http.Request) (p page, ok bool) {
	id := auth.From(r.Context())

	// The People capability rather than ownership, so somebody can run
	// accounts without being handed the whole installation.
	held, err := s.az.Capabilities(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "read capabilities", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not check what you may reach")
		return p, false
	}
	if !held.Has(authz.People) {
		s.fail(w, r, http.StatusForbidden, "your account cannot manage people")
		return p, false
	}

	canMove, err := s.az.MayMovePeopleIn(r.Context(), id.OrgID, held)
	if err != nil {
		s.log.ErrorContext(r.Context(), "may move people", "error", err)
	}

	fail := func(what string, err error) (page, bool) {
		s.log.ErrorContext(r.Context(), what, "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load "+what)
		return page{}, false
	}
	list, err := s.auth.People(r.Context(), id.OrgID)
	if err != nil {
		return fail("the accounts", err)
	}
	grants, err := s.az.HeldBy(r.Context(), id.OrgID)
	if err != nil {
		return fail("what people may open", err)
	}
	spaceGrants, err := s.az.SpaceGrantsBy(r.Context(), id.OrgID)
	if err != nil {
		return fail("who can see which department", err)
	}
	chart, err := s.az.StructureFor(r.Context(), id.OrgID)
	if err != nil {
		return fail("the org chart", err)
	}

	// Departments are the top two levels: Red Team and White Team sit under
	// Engineering, and people are in them rather than in the parent.
	var departments []*plant.Space
	if s.plant != nil {
		if tree, err := s.plant.SpaceTree(r.Context(), id.OrgID, id.Subjects); err == nil {
			for _, sp := range tree {
				if sp.Depth <= 1 {
					departments = append(departments, sp)
				}
			}
		}
	}

	return page{
		Title: "People", Persons: list,
		Grants: grants, Chart: chart,
		Departments:   departments,
		SpaceGrants:   spaceGrants,
		Grantable:     authz.Grantable,
		CanGrant:      held.IsSuperAdmin(),
		CanMovePeople: canMove,
	}, true
}

func (s *Server) getPeople(w http.ResponseWriter, r *http.Request) {
	p, ok := s.peopleData(w, r)
	if !ok {
		return
	}
	// Search by name or email, on the server: no script, and the list is
	// short enough that there is nothing to page.
	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))); q != "" {
		kept := p.Persons[:0]
		for _, person := range p.Persons {
			if strings.Contains(strings.ToLower(person.DisplayName), q) ||
				strings.Contains(strings.ToLower(person.Email), q) {
				kept = append(kept, person)
			}
		}
		p.Persons = kept
		p.SearchText = r.URL.Query().Get("q")
	}
	s.render(w, r, "people", p)
}

func (s *Server) getPerson(w http.ResponseWriter, r *http.Request) {
	p, ok := s.peopleData(w, r)
	if !ok {
		return
	}
	want := r.PathValue("id")
	for i := range p.Persons {
		if p.Persons[i].PrincipalID == want {
			p.Person = &p.Persons[i]
		}
	}
	if p.Person == nil {
		s.fail(w, r, http.StatusNotFound, "there is nobody with that id")
		return
	}
	p.Title = p.Person.DisplayName

	// The chart stores what somebody manages by name. The remove buttons need
	// the id, so match the names back to the department list.
	if st := p.Chart[want]; st != nil {
		for _, name := range st.Manages {
			for _, d := range p.Departments {
				if d.Name == name {
					p.PersonManaged = append(p.PersonManaged, authz.Department{ID: d.ID, Name: d.Name})
					break
				}
			}
		}
	}
	s.askAboutAccess(r, &p, want)
	s.accessTree(r, &p, want)
	s.render(w, r, "person", p)
}

// AccessNode is one box in the "Can see work in" editor. Covered means a
// department above it is ticked, so it is already included and not a choice.
type AccessNode struct {
	Space   *plant.Space
	Indent  int
	Checked bool
	Covered bool
}

// AccessGroup is one department and the parts of it offered in the editor.
type AccessGroup struct {
	Dept  AccessNode
	Parts []AccessNode
	// Open is whether anything inside is ticked, so the page opens on what
	// somebody already has and leaves the rest folded.
	Open bool
}

// accessEditorDepth is how deep the editor goes: departments, their teams, and
// the teams' folders. Below that is individual jobs, too fine to grant here.
const accessEditorDepth = 2

func (s *Server) accessTree(r *http.Request, p *page, person string) {
	if s.plant == nil {
		return
	}
	id := auth.From(r.Context())
	tree, err := s.plant.SpaceTree(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		return
	}
	granted, err := s.az.SpaceGrantSet(r.Context(), id.OrgID, person)
	if err != nil {
		return
	}
	byID := make(map[string]*plant.Space, len(tree))
	for _, sp := range tree {
		byID[sp.ID] = sp
	}
	coveredByAncestor := func(sp *plant.Space) bool {
		for cur := sp; cur.ParentID != nil; {
			parent := byID[*cur.ParentID]
			if parent == nil {
				return false
			}
			if granted[parent.ID] {
				return true
			}
			cur = parent
		}
		return false
	}

	var groups []AccessGroup
	for _, sp := range tree {
		if sp.Depth > accessEditorDepth {
			continue
		}
		node := AccessNode{Space: sp, Indent: sp.Depth, Checked: granted[sp.ID], Covered: coveredByAncestor(sp)}
		if sp.Depth == 0 {
			groups = append(groups, AccessGroup{Dept: node, Open: node.Checked})
			continue
		}
		if len(groups) == 0 {
			continue
		}
		g := &groups[len(groups)-1]
		g.Parts = append(g.Parts, node)
		if node.Checked {
			g.Open = true
		}
	}
	p.AccessGroups = groups
}

// postAccess applies the "Can see work in" editor: grant what was newly
// ticked, and take away each granted branch that was unticked. A branch under
// a ticked department is left alone -- its box was shown as included, not as
// a choice, so it never arrives in the form either way.
func (s *Server) postAccess(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	person := r.PathValue("id")
	wanted := map[string]bool{}
	for _, v := range r.Form["space"] {
		if v = strings.TrimSpace(v); v != "" {
			wanted[v] = true
		}
	}

	tree, err := s.plant.SpaceTree(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(r.Context(), "access tree", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the departments")
		return
	}
	granted, err := s.az.SpaceGrantSet(r.Context(), id.OrgID, person)
	if err != nil {
		s.log.ErrorContext(r.Context(), "access set", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load their access")
		return
	}
	byID := make(map[string]*plant.Space, len(tree))
	for _, sp := range tree {
		byID[sp.ID] = sp
	}
	wantedAbove := func(sp *plant.Space) bool {
		for cur := sp; cur.ParentID != nil; {
			parent := byID[*cur.ParentID]
			if parent == nil {
				return false
			}
			if wanted[parent.ID] {
				return true
			}
			cur = parent
		}
		return false
	}

	// Take away first, so granting a department afterward cannot be undone by
	// removing a branch inside it in the same save.
	for _, sp := range tree {
		if sp.Depth > accessEditorDepth || !granted[sp.ID] {
			continue
		}
		isTop := sp.ParentID == nil || !granted[*sp.ParentID]
		if isTop && !wanted[sp.ID] && !wantedAbove(sp) {
			if s.refuseAdmin(w, r, s.az.RevokeSpace(r.Context(), id.OrgID, id.PrincipalID, person, sp.ID)) {
				return
			}
		}
	}
	for sp := range wanted {
		if byID[sp] == nil || granted[sp] {
			continue
		}
		if s.refuseAdmin(w, r, s.az.GrantSpace(r.Context(), id.OrgID, id.PrincipalID, person, sp)) {
			return
		}
	}
	http.Redirect(w, r, backTo(r, "/people"), http.StatusSeeOther)
}

// AskPart is one part of a department offered in the access prompt, with how
// far below the department it sits, for indenting.
type AskPart struct {
	Space  *plant.Space
	Indent int
}

// askAboutAccess fills in the "can they see this department's work?" prompt
// shown right after somebody is added to a department -- unless they can
// already see it, in which case there is nothing to ask.
func (s *Server) askAboutAccess(r *http.Request, p *page, person string) {
	ask := r.URL.Query().Get("ask")
	if ask == "" || s.plant == nil {
		return
	}
	for _, d := range p.SpaceGrants[person] {
		if d.ID == ask {
			return
		}
	}
	id := auth.From(r.Context())
	tree, err := s.plant.SpaceTree(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		return
	}
	for _, sp := range tree {
		if sp.ID == ask {
			p.AskDept = sp
		}
	}
	if p.AskDept == nil {
		return
	}
	// Two levels down: its teams, and their folders. Below that is individual
	// jobs, which is too fine a grain to grant people one by one here.
	prefix := p.AskDept.Path + " / "
	for _, sp := range tree {
		if below := sp.Depth - p.AskDept.Depth; below >= 1 && below <= 2 &&
			strings.HasPrefix(sp.Path, prefix) {
			p.AskParts = append(p.AskParts, AskPart{Space: sp, Indent: below})
		}
	}
}
