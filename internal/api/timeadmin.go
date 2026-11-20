package api

// Removing time entries, as a super admin.
//
//	POST /v1/time/remove   {"ids": [...], "reason": "...", "apply": false}
//
// Without "apply": true nothing changes and the answer says what would go. With
// it, every entry still standing is removed and gets its own audit row naming the
// token's person and the reason, in one transaction. Only a super admin may; the
// check is in timelog.RemoveMany, the same call the command line makes.

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/timelog"
)

// WithTime switches the admin time removal on. Nil leaves the route answering 404.
func (s *Service) WithTime(st timelog.Store) *Service {
	s.time = st
	return s
}

func (s *Service) timeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/time/remove", s.removeTime)
}

func (s *Service) removeTime(w http.ResponseWriter, r *http.Request) {
	if s.time == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	c := from(r)
	var body struct {
		IDs    []string `json:"ids"`
		Reason string   `json:"reason"`
		Apply  bool     `json:"apply"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	lines, err := s.time.RemoveMany(r.Context(), c.OrgID, c.Subjects, body.IDs, body.Reason, body.Apply)
	switch {
	case errors.Is(err, timelog.ErrNotAllowed):
		writeErr(w, http.StatusForbidden, "only a super admin may remove time entries this way")
		return
	case errors.Is(err, timelog.ErrNeedsReason):
		writeErr(w, http.StatusBadRequest, "say why these entries are being removed")
		return
	case errors.Is(err, timelog.ErrNotFound):
		writeErr(w, http.StatusNotFound, strings.TrimPrefix(err.Error(), "timelog: "))
		return
	case err != nil:
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "timelog: "))
		return
	}
	type line struct {
		ID             string    `json:"id"`
		PersonID       string    `json:"person_id"`
		Person         string    `json:"person"`
		Minutes        int       `json:"minutes"`
		StartedAt      time.Time `json:"started_at"`
		Note           string    `json:"note"`
		AlreadyRemoved bool      `json:"already_removed"`
		Removed        bool      `json:"removed"`
	}
	out := make([]line, 0, len(lines))
	n := 0
	for _, l := range lines {
		out = append(out, line{l.ID, l.Person, l.PersonName, l.Minutes, l.StartedAt, l.Note, l.AlreadyRemoved, l.Removed})
		if l.Removed {
			n++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"apply": body.Apply, "removed": n, "entries": out})
}
