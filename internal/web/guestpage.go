package web

// Sharing a task with a guest: a person given view, comment or edit on this one
// task without being assigned to it. See internal/docs/guestshare.go.

import (
	"errors"
	"net/http"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
)

func (s *Server) shareGuest(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	docID := r.PathValue("id")
	err := s.docs.ShareWith(r.Context(), id.OrgID, id.Subjects, docID,
		r.FormValue("person"), core.AccessLevel(r.FormValue("level")))
	s.afterGuestChange(w, r, docID, err)
}

func (s *Server) unshareGuest(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	docID := r.PathValue("id")
	err := s.docs.UnshareGuest(r.Context(), id.OrgID, id.Subjects, docID, r.FormValue("person"))
	s.afterGuestChange(w, r, docID, err)
}

func (s *Server) afterGuestChange(w http.ResponseWriter, r *http.Request, docID string, err error) {
	id := auth.From(r.Context())
	switch {
	case errors.Is(err, docs.ErrNotAllowed):
		s.fail(w, r, http.StatusForbidden, "only the task's creator, a manager of its department, or an admin can share it")
		return
	case errors.Is(err, docs.ErrField):
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, docs.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such task or person")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "share guest", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not change who it's shared with")
		return
	}
	s.notify(id.OrgID, "documents")
	http.Redirect(w, r, "/documents/"+docID+"#guests", http.StatusSeeOther)
}
