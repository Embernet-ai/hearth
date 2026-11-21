package web

// Imported hours: moving what an import put under one name onto the people
// who did the work. See timelog.Reassign for why it lands on one name at all.

import (
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/importer/clickup"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// ImportedGroup is the imported hours a task's sole assignee points at, per
// person, so a whole person's worth can be moved and reviewed in one go.
type ImportedGroup struct {
	PersonID   string
	PersonName string
	Count      int
	Minutes    int
}

func (s *Server) getImported(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	list, err := s.time.Imported(r.Context(), id.OrgID, id.PrincipalID, clickup.ImportedTimeNote)
	if s.importedFailed(w, r, err) {
		return
	}

	groups := map[string]*ImportedGroup{}
	var manual []timelog.ImportedEntry
	total, settled := 0, 0
	for _, e := range list {
		total += e.Minutes
		switch {
		case e.SuggestedID == "":
			manual = append(manual, e)
		case e.SuggestedID == e.PrincipalID:
			// Already on the one person the task names; only approval is left.
			settled++
		default:
			g := groups[e.SuggestedID]
			if g == nil {
				g = &ImportedGroup{PersonID: e.SuggestedID, PersonName: e.SuggestedName}
				groups[e.SuggestedID] = g
			}
			g.Count++
			g.Minutes += e.Minutes
		}
	}
	byPerson := make([]ImportedGroup, 0, len(groups))
	for _, g := range groups {
		byPerson = append(byPerson, *g)
	}
	sort.Slice(byPerson, func(i, j int) bool { return byPerson[i].PersonName < byPerson[j].PersonName })

	people, err := s.assignable(r)
	if err != nil {
		s.log.ErrorContext(r.Context(), "imported hours people", "error", err)
	}

	var flash string
	if n := r.URL.Query().Get("moved"); n != "" {
		flash = "Moved " + n + " entries. They are still pending, in their approver's queue."
	}
	s.render(w, r, "imported", page{
		Title: "Imported hours", People: people, Flash: flash,
		ImportedByPerson: byPerson, ImportedManual: manual,
		ImportedCount: len(list), ImportedMinutes: total, ImportedSettled: settled,
	})
}

// postImportedApply moves every entry with a clear suggestion onto it, or just
// one person's worth when the form names them.
func (s *Server) postImportedApply(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	only := r.FormValue("person")

	// Worked out again here rather than trusting ids from the page: the
	// suggestion is a fact about the task, and the server can read it.
	list, err := s.time.Imported(r.Context(), id.OrgID, id.PrincipalID, clickup.ImportedTimeNote)
	if s.importedFailed(w, r, err) {
		return
	}
	var moves []timelog.Move
	for _, e := range list {
		if e.SuggestedID == "" || e.SuggestedID == e.PrincipalID {
			continue
		}
		if only != "" && e.SuggestedID != only {
			continue
		}
		moves = append(moves, timelog.Move{EntryID: e.ID, PrincipalID: e.SuggestedID})
	}
	s.reassign(w, r, moves)
}

// postImportedMove moves one entry to whoever the form names -- the ones with
// several assignees or none, where somebody has to decide.
func (s *Server) postImportedMove(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	to := r.FormValue("to")
	if to == "" {
		http.Redirect(w, r, "/time/imported", http.StatusSeeOther)
		return
	}
	s.reassign(w, r, []timelog.Move{{EntryID: r.PathValue("entry"), PrincipalID: to}})
}

func (s *Server) reassign(w http.ResponseWriter, r *http.Request, moves []timelog.Move) {
	id := auth.From(r.Context())
	n, err := s.time.Reassign(r.Context(), id.OrgID, id.PrincipalID, clickup.ImportedTimeNote, moves)
	if s.importedFailed(w, r, err) {
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/time/imported?moved=%d", n), http.StatusSeeOther)
}

func (s *Server) importedFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, timelog.ErrNotAllowed):
		s.fail(w, r, http.StatusForbidden,
			"Moving imported hours is a billing tool, and that is not something your account can do.")
		return true
	case err != nil:
		s.log.ErrorContext(r.Context(), "imported hours", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not read the imported hours")
		return true
	}
	return false
}
