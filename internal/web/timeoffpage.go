package web

// Time off: ask for days away, and decide on other people's. See
// internal/timeoff for who may decide and who may see.

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/notify"
	"github.com/Embernet-ai/hearth/internal/timelog"
	"github.com/Embernet-ai/hearth/internal/timeoff"
)

func (s *Server) getTimeOff(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	mine, err := s.timeoff.Mine(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "time off", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load your time off")
		return
	}
	decide, err := s.timeoff.ToDecide(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "time off to decide", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the requests to decide")
		return
	}
	s.render(w, r, "timeoff", page{
		Title: "Time off", TimeOff: mine, TimeOffToDecide: decide, TimeOffKinds: timeoff.Kinds,
		Message: r.URL.Query().Get("done"),
	})
}

func (s *Server) postTimeOff(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	date := func(k string) time.Time {
		d, _ := time.Parse("2006-01-02", strings.TrimSpace(r.FormValue(k)))
		return d
	}
	_, err := s.timeoff.Ask(r.Context(), id.OrgID, id.PrincipalID, timeoff.Params{
		StartsOn: date("starts_on"), EndsOn: date("ends_on"), Kind: r.FormValue("kind"),
		Reason: r.FormValue("reason"), Comments: r.FormValue("comments"), HalfDay: r.FormValue("half_day") != "",
		Acknowledged: r.FormValue("acknowledged") != "",
	})
	if errors.Is(err, timeoff.ErrInvalid) {
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "timeoff: invalid request: "))
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "ask time off", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not send your request")
		return
	}
	http.Redirect(w, r, "/time-off?done=Request+sent.+Your+approver+has+been+told.", http.StatusSeeOther)
}

func (s *Server) postDecideTimeOff(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	approve := r.FormValue("approve") == "1"
	err := s.timeoff.Decide(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id"), approve, r.FormValue("note"))
	if errors.Is(err, timeoff.ErrDenied) {
		s.fail(w, r, http.StatusForbidden, "you cannot decide that request, or it was already decided")
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "decide time off", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not save the decision")
		return
	}
	http.Redirect(w, r, "/time-off#to-decide", http.StatusSeeOther)
}

func (s *Server) postCancelTimeOff(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	err := s.timeoff.Cancel(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id"))
	if errors.Is(err, timeoff.ErrDenied) {
		s.fail(w, r, http.StatusForbidden, "that request can no longer be cancelled")
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "cancel time off", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not cancel the request")
		return
	}
	http.Redirect(w, r, "/time-off?done=Request+cancelled.", http.StatusSeeOther)
}

// postChangeTimeOff is an approver or admin changing somebody else's request: dates, type,
// half day and note. The form carries every field, so every field is applied; whatever is
// the same as it was changes nothing, and a form that changes nothing is refused.
func (s *Server) postChangeTimeOff(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	date := func(k string) *time.Time {
		d, err := time.Parse("2006-01-02", strings.TrimSpace(r.FormValue(k)))
		if err != nil {
			return nil
		}
		return &d
	}
	kind, comments, half := r.FormValue("kind"), r.FormValue("comments"), r.FormValue("half_day") != ""
	c := timeoff.Change{StartsOn: date("starts_on"), EndsOn: date("ends_on"), Kind: &kind, HalfDay: &half,
		Comments: &comments, Note: r.FormValue("note")}
	_, err := s.timeoff.Change(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id"), c)
	switch {
	case errors.Is(err, timeoff.ErrDenied):
		s.fail(w, r, http.StatusForbidden, "you cannot change that request, or it is already finished")
		return
	case errors.Is(err, timeoff.ErrInvalid):
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "timeoff: invalid request: "))
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "change time off", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not change the request")
		return
	}
	http.Redirect(w, r, "/time-off?done=Request+changed.+The+person+has+been+told.#to-decide", http.StatusSeeOther)
}

// postCancelForTimeOff is an approver or admin cancelling somebody else's request.
func (s *Server) postCancelForTimeOff(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	err := s.timeoff.CancelFor(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id"), r.FormValue("note"))
	if errors.Is(err, timeoff.ErrDenied) {
		s.fail(w, r, http.StatusForbidden, "you cannot cancel that request, or it is already finished")
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "cancel time off for", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not cancel the request")
		return
	}
	http.Redirect(w, r, "/time-off?done=Request+cancelled.+The+person+has+been+told.#to-decide", http.StatusSeeOther)
}

// NewTimeOffNotifier tells approvers about a new request and the requester
// about the answer, through the ordinary inbox.
func NewTimeOffNotifier(n *notify.Service) timeoff.Notifier { return timeOffNotifier{n} }

type timeOffNotifier struct{ n *notify.Service }

func span(r *timeoff.Request) string {
	return r.StartsOn.Format("2 Jan") + " – " + r.EndsOn.Format("2 Jan 2006")
}

func (t timeOffNotifier) TimeOffRequested(ctx context.Context, orgID string, r *timeoff.Request, approvers []string) {
	for _, a := range approvers {
		_, _ = t.n.Deliver(ctx, notify.Delivery{
			OrgID: orgID, PrincipalID: a, Reason: notify.ReasonAssigned,
			Ref:   core.Ref{Type: core.EntityTimeOff, ID: r.ID},
			Title: "a time off request (" + span(r) + ")", ActorID: r.RequesterID,
			DedupeKey: "timeoff-asked:" + r.ID,
		})
	}
}

// TimeOffChanged tells the person that somebody else changed or cancelled their request.
func (t timeOffNotifier) TimeOffChanged(ctx context.Context, orgID, actorID string, r *timeoff.Request, note string) {
	title := "your time off request was changed, it is now " + span(r)
	if r.Status == "cancelled" {
		title = "your time off request (" + span(r) + ") was cancelled"
	}
	_, _ = t.n.Deliver(ctx, notify.Delivery{
		OrgID: orgID, PrincipalID: r.RequesterID, Reason: notify.ReasonChanged,
		Ref:   core.Ref{Type: core.EntityTimeOff, ID: r.ID},
		Title: title, Excerpt: note, ActorID: actorID,
		DedupeKey: "timeoff-changed:" + r.ID + ":" + strconv.FormatInt(time.Now().UnixNano(), 10),
	})
}

func (t timeOffNotifier) TimeOffDecided(ctx context.Context, orgID, deciderID string, r *timeoff.Request) {
	_, _ = t.n.Deliver(ctx, notify.Delivery{
		OrgID: orgID, PrincipalID: r.RequesterID, Reason: notify.ReasonChanged,
		Ref:   core.Ref{Type: core.EntityTimeOff, ID: r.ID},
		Title: "your time off request (" + span(r) + "): " + r.Status, ActorID: deciderID,
		DedupeKey: "timeoff-decided:" + r.ID,
	})
}

// NewTimeNotifier tells a person their hours were sent back, with the manager's
// comment, and tells the manager when they are fixed and waiting again.
func NewTimeNotifier(n *notify.Service) timelog.Notifier { return timeNotifier{n} }

type timeNotifier struct{ n *notify.Service }

func hoursLabel(d timelog.Decision) string {
	return d.StartedAt.Format("2 Jan") + ", " + hoursAndMinutes(d.Minutes)
}

func (t timeNotifier) HoursSentBack(ctx context.Context, orgID string, d timelog.Decision) {
	excerpt := d.Note
	if excerpt == "" {
		excerpt = "No comment was left. Open your hours to see which entry came back."
	}
	_, _ = t.n.Deliver(ctx, notify.Delivery{
		OrgID: orgID, PrincipalID: d.PersonID, Reason: notify.ReasonChanged,
		Ref:   core.Ref{Type: core.EntityTimeEntry, ID: d.EntryID},
		Title: "sent back your hours (" + hoursLabel(d) + ") to fix", Excerpt: excerpt, ActorID: d.DeciderID,
		DedupeKey: "time-sentback:" + d.EntryID + ":" + strconv.FormatInt(time.Now().UnixNano(), 10),
	})
}

func (t timeNotifier) HoursResubmitted(ctx context.Context, orgID string, d timelog.Decision) {
	_, _ = t.n.Deliver(ctx, notify.Delivery{
		OrgID: orgID, PrincipalID: d.DeciderID, Reason: notify.ReasonChanged,
		Ref:   core.Ref{Type: core.EntityTimeApproval, ID: d.EntryID},
		Title: "fixed the hours you sent back (" + hoursLabel(d) + "), they are waiting for you again", ActorID: d.PersonID,
		DedupeKey: "time-resubmitted:" + d.EntryID + ":" + strconv.FormatInt(time.Now().UnixNano(), 10),
	})
}
