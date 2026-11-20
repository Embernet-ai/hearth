package web

// Department requests: say which departments you belong in, and answer other
// people's. See internal/deptreq for who may decide what.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/deptreq"
	"github.com/Embernet-ai/hearth/internal/notify"
)

func (s *Server) getDeptRequest(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	all, err := s.deptreq.Departments(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "departments", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the departments")
		return
	}
	cur, err := s.deptreq.Current(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "my departments", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load your departments")
		return
	}
	last, err := s.deptreq.Latest(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "my department request", "error", err)
	}
	s.render(w, r, "deptrequest", page{
		Title: "My departments", AllDepartments: all, MyDepartments: cur, MyDeptRequest: last,
		DeptApplyNow: s.az != nil && s.az.MayAdminister(r.Context(), id.OrgID, id.PrincipalID),
		Message:      r.URL.Query().Get("done"),
	})
}

func (s *Server) postDeptRequest(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	// Somebody who may place people in departments has nobody to ask, and a
	// request they filed for themselves could never be answered: the decider
	// is never the requester. Their own choice is saved at once.
	if s.az != nil && s.az.MayAdminister(r.Context(), id.OrgID, id.PrincipalID) {
		spaces := make([]string, 0, len(r.Form["space"]))
		for _, v := range r.Form["space"] {
			if v = strings.TrimSpace(v); v != "" {
				spaces = append(spaces, v)
			}
		}
		if len(spaces) == 0 {
			s.fail(w, r, http.StatusBadRequest, "choose at least one department")
			return
		}
		if err := s.az.SetDepartments(r.Context(), id.OrgID, id.PrincipalID, id.PrincipalID, spaces); err != nil {
			s.log.ErrorContext(r.Context(), "save own departments", "error", err)
			s.fail(w, r, http.StatusInternalServerError, "could not save your departments")
			return
		}
		http.Redirect(w, r, "/departments/request?done=Saved.", http.StatusSeeOther)
		return
	}
	_, err := s.deptreq.Ask(r.Context(), id.OrgID, id.PrincipalID, r.Form["space"], r.FormValue("note"))
	if errors.Is(err, deptreq.ErrInvalid) {
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "deptreq: invalid: "))
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "ask departments", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not send your request")
		return
	}
	http.Redirect(w, r, "/departments/request?done=Sent.+A+manager+or+an+admin+will+confirm+it.", http.StatusSeeOther)
}

func (s *Server) postDeptRequestCancel(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	err := s.deptreq.Cancel(r.Context(), id.OrgID, id.PrincipalID)
	if errors.Is(err, deptreq.ErrDenied) {
		s.fail(w, r, http.StatusConflict, "there is no request waiting to cancel")
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "cancel department request", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not cancel the request")
		return
	}
	http.Redirect(w, r, "/departments/request?done=Request+cancelled.", http.StatusSeeOther)
}

// postDeptDecide answers somebody's request from the Approvals page. The
// ticked boxes are the decider's final set, which may differ from what was
// asked.
func (s *Server) postDeptDecide(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	approve := r.FormValue("decision") == "approve"
	err := s.deptreq.Decide(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id"),
		approve, r.Form["space"], r.FormValue("note"))
	switch {
	case errors.Is(err, deptreq.ErrInvalid):
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "deptreq: invalid: "))
		return
	case errors.Is(err, deptreq.ErrDenied):
		s.fail(w, r, http.StatusForbidden, "you can only decide for departments you manage, "+
			"or that request was already decided. An admin can decide any department.")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "decide department request", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not save the decision")
		return
	}
	http.Redirect(w, r, backTo(r, "/time/approvals"), http.StatusSeeOther)
}

// getDeptRequestLink is where a notification about a request points: the
// requester to their own page, anybody else to Approvals, where it is decided.
func (s *Server) getDeptRequestLink(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	who, err := s.deptreq.RequesterOf(r.Context(), id.OrgID, r.PathValue("id"))
	if err != nil {
		s.log.ErrorContext(r.Context(), "department request link", "error", err)
	}
	if who == id.PrincipalID {
		http.Redirect(w, r, "/departments/request", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/time/approvals#departments", http.StatusSeeOther)
}

// NewDeptRequestNotifier tells deciders about a request and the person about
// the answer, through the ordinary inbox.
func NewDeptRequestNotifier(n *notify.Service) deptreq.Notifier { return deptNotifier{n} }

type deptNotifier struct{ n *notify.Service }

func deptNames(ds []deptreq.Department) string {
	names := make([]string, 0, len(ds))
	for _, d := range ds {
		names = append(names, d.Name)
	}
	return strings.Join(names, ", ")
}

func (d deptNotifier) DepartmentsRequested(ctx context.Context, orgID string, r *deptreq.Request, deciders []string) {
	for _, who := range deciders {
		_, _ = d.n.Deliver(ctx, notify.Delivery{
			OrgID: orgID, PrincipalID: who, Reason: notify.ReasonAssigned,
			Ref:   core.Ref{Type: core.EntityDepartmentRequest, ID: r.ID},
			Title: "a department request from " + r.RequesterName, ActorID: r.RequesterID,
			DedupeKey: "dept-asked:" + r.ID,
		})
	}
}

func (d deptNotifier) DepartmentsDecided(ctx context.Context, orgID, deciderID string, r *deptreq.Request) {
	title := "your department request was not approved"
	if r.Status == "approved" {
		title = "you are now in " + deptNames(r.Granted)
	}
	_, _ = d.n.Deliver(ctx, notify.Delivery{
		OrgID: orgID, PrincipalID: r.RequesterID, Reason: notify.ReasonChanged,
		Ref:   core.Ref{Type: core.EntityDepartmentRequest, ID: r.ID},
		Title: title, ActorID: deciderID,
		DedupeKey: "dept-decided:" + r.ID,
	})
}

// ApprovalsWaiting is everything on the Approvals page waiting on this person:
// hours and department requests, for the one badge on the rail.
func (p page) ApprovalsWaiting() int { return p.ToApprove + p.DeptPending }

// DeptTicked is whether a department starts ticked on the request form: the
// person's pending request if they have one, otherwise where they are now.
func (p page) DeptTicked(id string) bool {
	if p.MyDeptRequest != nil && p.MyDeptRequest.Status == "pending" {
		return p.MyDeptRequest.AskedHas(id)
	}
	for _, d := range p.MyDepartments {
		if d.ID == id {
			return true
		}
	}
	return false
}
