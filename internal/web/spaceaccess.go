package web

// Giving one folder to one person, from that folder's own page.
//
// A manager of a department, or of any folder above one, opens a folder's
// Access page, names a person by the address they sign in with, picks view,
// comment or edit and an optional last day, and that person reaches this folder
// and everything under it and nothing else. The same page takes it back.
// plant.Delegate does the work and the checking; this is the form around it.

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/plant"
)

// accessZone is the office's zone, the one daily reports already use: a last day
// ends at midnight at the end of that day there, not in UTC.
const accessZone = "America/New_York"

func (s *Server) spaceAccessPage(w http.ResponseWriter, r *http.Request, spaceID, message string, status int) {
	id := auth.From(r.Context())
	folder, err := s.plant.ManagedFolder(r.Context(), id.OrgID, id.Subjects, spaceID)
	if err != nil {
		s.plantFail(w, r, err, "you cannot share that folder")
		return
	}
	list, err := s.plant.Delegations(r.Context(), id.OrgID, id.Subjects, spaceID)
	if err != nil {
		s.plantFail(w, r, err, "you cannot share that folder")
		return
	}
	if status != 0 {
		w.WriteHeader(status)
	}
	s.render(w, r, "spaceaccess", page{
		Title: "Access to " + folder.Name, AccessSpace: folder, Delegations: list, Message: message,
	})
}

func (s *Server) getSpaceAccess(w http.ResponseWriter, r *http.Request) {
	s.spaceAccessPage(w, r, r.PathValue("id"), "", 0)
}

func (s *Server) postSpaceDelegate(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	spaceID := r.PathValue("id")
	email := strings.TrimSpace(r.FormValue("email"))
	person, _, err := s.plant.PersonByEmail(r.Context(), id.OrgID, id.Subjects, email)
	if err == nil {
		var expires *time.Time
		if day := strings.TrimSpace(r.FormValue("expires")); day != "" {
			loc, lerr := time.LoadLocation(accessZone)
			if lerr != nil {
				loc = time.UTC
			}
			d, perr := time.ParseInLocation("2006-01-02", day, loc)
			if perr != nil {
				s.spaceAccessPage(w, r, spaceID, "That last day is not a date. Use the date picker or year-month-day.", http.StatusBadRequest)
				return
			}
			end := d.AddDate(0, 0, 1).Add(-time.Second)
			expires = &end
		}
		err = s.plant.Delegate(r.Context(), id.OrgID, id.Subjects, spaceID, person,
			core.AccessLevel(r.FormValue("level")), expires)
	}
	switch {
	case err == nil:
		http.Redirect(w, r, "/spaces/"+spaceID+"/access", http.StatusSeeOther)
	case errors.Is(err, plant.ErrNoSuchPerson):
		s.spaceAccessPage(w, r, spaceID, "Nobody here signs in with that address. Ask an admin to add the account first.", http.StatusBadRequest)
	case errors.Is(err, plant.ErrAlreadyReaches):
		s.spaceAccessPage(w, r, spaceID, "That person already reaches this folder through a folder above it. Change it there.", http.StatusBadRequest)
	case errors.Is(err, plant.ErrInvalid):
		s.spaceAccessPage(w, r, spaceID, strings.TrimPrefix(err.Error(), "plant: "), http.StatusBadRequest)
	default:
		s.plantFail(w, r, err, "you cannot share that folder")
	}
}

func (s *Server) postSpaceUndelegate(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	spaceID := r.PathValue("id")
	if err := s.plant.Undelegate(r.Context(), id.OrgID, id.Subjects, spaceID, r.PathValue("person")); err != nil {
		s.plantFail(w, r, err, "you cannot take that back")
		return
	}
	http.Redirect(w, r, "/spaces/"+spaceID+"/access", http.StatusSeeOther)
}

// postContractor marks an account as a contractor's, or clears it.
func (s *Server) postContractor(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	err := s.auth.SetContractor(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id"),
		r.FormValue("contractor") == "on")
	switch {
	case err == nil:
		http.Redirect(w, r, backTo(r, "/people/"+r.PathValue("id")), http.StatusSeeOther)
	case errors.Is(err, auth.ErrNotOwner):
		s.fail(w, r, http.StatusForbidden, "only the owner or a super admin can change an account")
	case errors.Is(err, auth.ErrIsOwner):
		s.fail(w, r, http.StatusForbidden, "the owner's account cannot be changed by anybody else")
	case errors.Is(err, auth.ErrNoPerson):
		s.fail(w, r, http.StatusNotFound, "no such person")
	default:
		s.log.ErrorContext(r.Context(), "set contractor", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not change that account")
	}
}
