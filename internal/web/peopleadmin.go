package web

// Granting capabilities, and filling in the org chart.
//
// All four handlers are thin on purpose. Every authority decision is made in
// authz, inside the method that does the writing, so a handler cannot forget
// one and a second caller added later cannot skip it. What is left here is
// reading a form and choosing a sentence for the refusal.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
)

// refuseAdmin turns an authz refusal into the right status and sentence.
func (s *Server) refuseAdmin(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, authz.ErrNotSuperAdmin):
		s.fail(w, r, http.StatusForbidden,
			"only a super admin can change what somebody may reach")
	case errors.Is(err, authz.ErrDenied):
		s.fail(w, r, http.StatusForbidden, "your account cannot manage people")
	default:
		s.log.WarnContext(r.Context(), "people admin refused", "error", err)
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "authz: "))
	}
	return true
}

// postCapability grants or revokes one capability for one person.
func (s *Server) postCapability(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	// Refused rather than ignored. A POST naming a capability that does not
	// exist is a bug or an attempt, and granting nothing quietly makes both
	// look like success.
	cap, err := authz.ParseCapability(r.FormValue("capability"))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "that is not a capability")
		return
	}
	target := r.PathValue("id")

	switch r.FormValue("action") {
	case "grant":
		err = s.az.GrantCapability(r.Context(), id.OrgID, id.PrincipalID, target, cap)
	case "revoke":
		err = s.az.RevokeCapability(r.Context(), id.OrgID, id.PrincipalID, target, cap)
	default:
		s.fail(w, r, http.StatusBadRequest, "grant or revoke, nothing else")
		return
	}
	if s.refuseAdmin(w, r, err) {
		return
	}
	http.Redirect(w, r, backTo(r, "/people"), http.StatusSeeOther)
}

// postDepartments replaces somebody's departments with the set that was ticked.
//
// r.Form["department"] rather than FormValue, which returns only the first of a
// repeated field. A multiple select posts one pair per selection, so reading
// one value would file somebody in Marketing and quietly drop the other three.
//
// An empty set is a real submission: it means every box was cleared, and it has
// to reach SetDepartments rather than being treated as nothing to do.
func (s *Server) postDepartments(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	var spaces []string
	for _, v := range r.Form["department"] {
		if v = strings.TrimSpace(v); v != "" {
			spaces = append(spaces, v)
		}
	}
	person := r.PathValue("id")

	// The Add form asks, straight after, whether they can also see that
	// department's work -- working somewhere and seeing its work are separate
	// on purpose, and this is the moment somebody is deciding both.
	var added string
	if r.FormValue("ask") != "" {
		if chart, err := s.az.StructureFor(r.Context(), id.OrgID); err == nil {
			for _, sp := range spaces {
				if !chart[person].In(sp) {
					added = sp
				}
			}
		}
	}

	err := s.az.SetDepartments(r.Context(), id.OrgID, id.PrincipalID, person, spaces)
	if s.refuseAdmin(w, r, err) {
		return
	}
	dest := backTo(r, "/people")
	if added != "" {
		dest += "?ask=" + added
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// postGrantSpace gives somebody a department: the space, what's under it, and
// what was already filed there. See authz.GrantSpace for why it has to reach
// backward onto existing documents rather than only the space itself.
func (s *Server) postGrantSpace(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	// Several at once from the "only some parts" prompt, one from anywhere else.
	var spaces []string
	for _, v := range r.Form["space"] {
		if v = strings.TrimSpace(v); v != "" {
			spaces = append(spaces, v)
		}
	}
	if len(spaces) == 0 {
		s.fail(w, r, http.StatusBadRequest, "choose a department")
		return
	}
	for _, sp := range spaces {
		err := s.az.GrantSpace(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id"), sp)
		if s.refuseAdmin(w, r, err) {
			return
		}
	}
	http.Redirect(w, r, backTo(r, "/people"), http.StatusSeeOther)
}

// postRevokeSpace takes a department grant back.
func (s *Server) postRevokeSpace(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	space := strings.TrimSpace(r.FormValue("space"))
	if space == "" {
		s.fail(w, r, http.StatusBadRequest, "no department named")
		return
	}
	err := s.az.RevokeSpace(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id"), space)
	if s.refuseAdmin(w, r, err) {
		return
	}
	http.Redirect(w, r, backTo(r, "/people"), http.StatusSeeOther)
}

// postApprover names the individual who signs off somebody's hours.
func (s *Server) postApprover(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	err := s.az.SetApprover(r.Context(), id.OrgID, id.PrincipalID,
		r.PathValue("id"), strings.TrimSpace(r.FormValue("approver")))
	if s.refuseAdmin(w, r, err) {
		return
	}
	// With the EmberOPS back end the Employee's approver link follows, so EmberOPS asks the same person Hearth does.
	if err == nil && s.people != nil {
		if perr := s.people.Ensure(r.Context(), id.OrgID, r.PathValue("id")); perr != nil {
			s.log.WarnContext(r.Context(), "could not copy the approver to EmberOPS", "error", perr)
		}
	}
	http.Redirect(w, r, backTo(r, "/people"), http.StatusSeeOther)
}

// postTimeAutoApprove sets whether somebody's time is approved as they log it,
// for salaried staff. A super admin only; the sentence for anybody else comes
// from refuseAdmin.
func (s *Server) postTimeAutoApprove(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	err := s.az.SetTimeAutoApprove(r.Context(), id.OrgID, id.PrincipalID,
		r.PathValue("id"), r.FormValue("auto") == "on")
	if s.refuseAdmin(w, r, err) {
		return
	}
	http.Redirect(w, r, backTo(r, "/people"), http.StatusSeeOther)
}

// postManages puts somebody in charge of a department, or takes them off it.
func (s *Server) postManages(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	space := strings.TrimSpace(r.FormValue("department"))
	if space == "" {
		s.fail(w, r, http.StatusBadRequest, "which department?")
		return
	}

	var err error
	if r.FormValue("action") == "remove" {
		err = s.az.RemoveManager(r.Context(), id.OrgID, id.PrincipalID, space, r.PathValue("id"))
	} else {
		err = s.az.AddManager(r.Context(), id.OrgID, id.PrincipalID, space, r.PathValue("id"))
	}
	if s.refuseAdmin(w, r, err) {
		return
	}
	http.Redirect(w, r, backTo(r, "/people"), http.StatusSeeOther)
}

// PersonSync makes sure a person exists in the back end that holds the records (EmberOPS): *stores.People.
type PersonSync interface {
	Ensure(ctx context.Context, orgID, principalID string) error
}
