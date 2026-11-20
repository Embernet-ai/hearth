package web

// Adding a subtask from a task's page: the task as a folder.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/docs"
)

// addSubtask files a new task inside this one. Same kind as the parent when the
// parent is a task or a work order -- a subtask of a job is part of the job --
// and a plain task otherwise.
func (s *Server) addSubtask(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	parentID := r.PathValue("id")
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		s.fail(w, r, http.StatusBadRequest, "a subtask needs a title")
		return
	}
	parent, err := s.docs.Get(r.Context(), id.OrgID, id.Subjects, parentID)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "no such task")
		return
	}
	kind := docs.KindTask
	if docs.HasTaskFields(parent.Kind) {
		kind = parent.Kind
	}
	_, err = s.docs.Create(r.Context(), id.OrgID, id.Subjects, docs.CreateParams{
		Kind: kind, Title: title, ParentID: &parentID,
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	switch {
	case errors.Is(err, docs.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such task")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "add subtask", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not add that subtask")
		return
	}
	s.notify(id.OrgID, "documents")
	http.Redirect(w, r, "/documents/"+parentID+"#subtasks", http.StatusSeeOther)
}

// IsAssigned reports whether a person is on this task, for the assignee picker.
func (p page) IsAssigned(id string) bool {
	for _, a := range p.AssigneeIDs {
		if a == id {
			return true
		}
	}
	return false
}

// IsPrimaryAssignee reports whether a person is the primary assignee.
func (p page) IsPrimaryAssignee(id string) bool { return id != "" && id == p.AssigneePrimary }

// setAssignees records who a task is assigned to. Only the creator, a manager
// of its department, or an admin may; the service enforces that.
func (s *Server) setAssignees(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	docID := r.PathValue("id")
	err := s.docs.Assign(r.Context(), id.OrgID, id.Subjects, docID, r.Form["assignee"], r.FormValue("primary"))
	switch {
	case errors.Is(err, docs.ErrNotAllowed):
		s.fail(w, r, http.StatusForbidden, "only the task's creator, a manager of its department, or an admin can change who it's assigned to")
		return
	case errors.Is(err, docs.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such task, or one of those people is not here")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "set assignees", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not save the assignees")
		return
	}
	s.notify(id.OrgID, "documents")
	http.Redirect(w, r, "/documents/"+docID+"#assignees", http.StatusSeeOther)
}
