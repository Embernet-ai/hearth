package web

// Tags: the org's own taxonomy for sorting a logged hour by. Reading the list
// is open to anybody who can log time -- picking a tag does not require the
// capability that defines new ones, the same way choosing an assignee does
// not require the People capability. Only this page's create form does.

import (
	"net/http"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

func (s *Server) getTags(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())

	tags, err := s.time.ListTags(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list tags", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the tags")
		return
	}
	held, err := s.az.Capabilities(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "read capabilities", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not check what you may reach")
		return
	}

	s.render(w, r, "tags", page{
		Title: "Tags", Tags: tags, CanManageTags: held.Has(authz.People),
	})
}

func (s *Server) postTags(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	_, err := s.time.CreateTag(r.Context(), id.OrgID, id.PrincipalID,
		r.FormValue("name"), r.FormValue("description"))
	switch {
	case err == timelog.ErrTagExists:
		s.fail(w, r, http.StatusConflict, "a tag with that name already exists")
		return
	case err == timelog.ErrNotAllowed:
		s.fail(w, r, http.StatusForbidden, "your account cannot define tags")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "create tag", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not create that tag")
		return
	}
	http.Redirect(w, r, "/tags", http.StatusSeeOther)
}

func (s *Server) postTagUpdate(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	err := s.time.UpdateTag(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id"),
		r.FormValue("name"), r.FormValue("description"))
	switch {
	case err == timelog.ErrTagExists:
		s.fail(w, r, http.StatusConflict, "a tag with that name already exists")
		return
	case err == timelog.ErrNotAllowed:
		s.fail(w, r, http.StatusForbidden, "your account cannot edit tags")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "update tag", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not update that tag")
		return
	}
	http.Redirect(w, r, "/tags", http.StatusSeeOther)
}

func (s *Server) postTagDelete(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	err := s.time.DeleteTag(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id"))
	switch {
	case err == timelog.ErrNotAllowed:
		s.fail(w, r, http.StatusForbidden, "your account cannot delete tags")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "delete tag", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not delete that tag")
		return
	}
	http.Redirect(w, r, "/tags", http.StatusSeeOther)
}
