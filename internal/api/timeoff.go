package api

// Time off, for the people who decide it.
//
//	GET   /v1/time-off?scope=decide|mine   requests the caller may change (default), or their own
//	GET   /v1/time-off/{id}                one request, for its person or anybody who may decide it
//	PATCH /v1/time-off/{id}                {"starts_on","ends_on","kind","half_day","reason","comments","note"}
//	POST  /v1/time-off/{id}/cancel         {"note"}
//
// Changing and cancelling are the page's own actions (timeoff.Store.Change and
// CancelFor), so the same people may do them: the person's approver, a manager of their
// department, a super admin, People; never the person themselves through this route (their
// own cancel is on the page). Every change is audited and the person is told. An agent has
// the same four as tools (TimeOffTools).

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/agents"
	"github.com/Embernet-ai/hearth/internal/timeoff"
)

// WithTimeOff switches the time off routes on. Nil leaves them answering 404.
func (s *Service) WithTimeOff(st timeoff.Store) *Service {
	s.timeoff = st
	return s
}

func (s *Service) timeOffRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/time-off", s.timeOff(s.listTimeOff))
	mux.HandleFunc("GET /v1/time-off/{id}", s.timeOff(s.getTimeOff))
	mux.HandleFunc("PATCH /v1/time-off/{id}", s.timeOff(s.changeTimeOff))
	mux.HandleFunc("POST /v1/time-off/{id}/cancel", s.timeOff(s.cancelTimeOff))
}

func (s *Service) timeOff(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.timeoff == nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		h(w, r)
	}
}

type timeOffView struct {
	ID           string     `json:"id"`
	PersonID     string     `json:"person_id"`
	Person       string     `json:"person"`
	StartsOn     string     `json:"starts_on"`
	EndsOn       string     `json:"ends_on"`
	Days         float64    `json:"days"`
	HalfDay      bool       `json:"half_day"`
	Kind         string     `json:"kind"`
	Reason       string     `json:"reason"`
	Comments     string     `json:"comments"`
	Status       string     `json:"status"`
	DecidedBy    string     `json:"decided_by,omitempty"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
	DecisionNote string     `json:"decision_note,omitempty"`
}

func viewOfTimeOff(r *timeoff.Request) timeOffView {
	return timeOffView{ID: r.ID, PersonID: r.RequesterID, Person: r.RequesterName, StartsOn: r.StartsOn.Format("2006-01-02"),
		EndsOn: r.EndsOn.Format("2006-01-02"), Days: r.Length(), HalfDay: r.HalfDay, Kind: r.Kind, Reason: r.Reason,
		Comments: r.Comments, Status: r.Status, DecidedBy: r.DeciderName, DecidedAt: r.DecidedAt, DecisionNote: r.DecisionNote}
}

func (s *Service) listTimeOff(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	var rows []*timeoff.Request
	var err error
	switch scope := r.URL.Query().Get("scope"); scope {
	case "", "decide":
		rows, err = s.timeoff.ToDecide(r.Context(), c.OrgID, c.PrincipalID)
	case "mine":
		rows, err = s.timeoff.Mine(r.Context(), c.OrgID, c.PrincipalID)
	default:
		writeErr(w, http.StatusBadRequest, "scope is decide or mine")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read time off")
		return
	}
	out := make([]timeOffView, 0, len(rows))
	for _, x := range rows {
		out = append(out, viewOfTimeOff(x))
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

func (s *Service) getTimeOff(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	x, err := s.timeoff.Get(r.Context(), c.OrgID, c.PrincipalID, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such request")
		return
	}
	writeJSON(w, http.StatusOK, viewOfTimeOff(x))
}

func timeOffErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, timeoff.ErrDenied), errors.Is(err, timeoff.ErrNotFound):
		writeErr(w, http.StatusForbidden, "you cannot change that request, or it is already finished")
	case errors.Is(err, timeoff.ErrInvalid):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "timeoff: invalid request: "))
	default:
		writeErr(w, http.StatusInternalServerError, "could not save the change")
	}
}

func (s *Service) changeTimeOff(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	var body struct {
		StartsOn *string `json:"starts_on"`
		EndsOn   *string `json:"ends_on"`
		Kind     *string `json:"kind"`
		HalfDay  *bool   `json:"half_day"`
		Reason   *string `json:"reason"`
		Comments *string `json:"comments"`
		Note     string  `json:"note"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	date := func(v *string) (*time.Time, bool) {
		if v == nil {
			return nil, true
		}
		d, err := time.Parse("2006-01-02", strings.TrimSpace(*v))
		return &d, err == nil
	}
	ch := timeoff.Change{Kind: body.Kind, HalfDay: body.HalfDay, Reason: body.Reason, Comments: body.Comments, Note: body.Note}
	var ok1, ok2 bool
	ch.StartsOn, ok1 = date(body.StartsOn)
	ch.EndsOn, ok2 = date(body.EndsOn)
	if !ok1 || !ok2 {
		writeErr(w, http.StatusBadRequest, "dates are YYYY-MM-DD")
		return
	}
	x, err := s.timeoff.Change(r.Context(), c.OrgID, c.PrincipalID, r.PathValue("id"), ch)
	if err != nil {
		timeOffErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewOfTimeOff(x))
}

func (s *Service) cancelTimeOff(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	var body struct {
		Note string `json:"note"`
	}
	// The note is optional, so a cancel with no body at all is a cancel.
	if r.ContentLength != 0 {
		if err := decode(r, &body); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	id := r.PathValue("id")
	if err := s.timeoff.CancelFor(r.Context(), c.OrgID, c.PrincipalID, id, body.Note); err != nil {
		timeOffErr(w, err)
		return
	}
	x, err := s.timeoff.Get(r.Context(), c.OrgID, c.PrincipalID, id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "cancelled"})
		return
	}
	writeJSON(w, http.StatusOK, viewOfTimeOff(x))
}

var timeOffTools = []crmTool{
	{name: "time_off_list", method: "GET", path: "/v1/time-off",
		desc:  "List time off requests: by default the ones the caller may change or cancel (the people they approve for, or everybody for an admin): pending, approved ones still to come, and recent decisions. scope mine lists the caller's own.",
		props: map[string]any{"scope": p("string", "Optional. decide (default) or mine.")}},
	{name: "time_off_get", method: "GET", path: "/v1/time-off/{id}", desc: "Read one time off request.",
		props: map[string]any{"id": p("string", "The request id.")}, required: []string{"id"}},
	{name: "time_off_change", method: "PATCH", path: "/v1/time-off/{id}", write: true,
		desc: "Change only the fields named on somebody else's pending or approved time off request: dates, type, half day, reason, comments. The person is told and an audit row is kept. Only the person's approver and admins may; an approved request keeps its calendar entry in step.",
		props: map[string]any{"id": p("string", "The request id."), "starts_on": p("string", "YYYY-MM-DD."), "ends_on": p("string", "YYYY-MM-DD."),
			"kind":     p("string", "vacation, sick, personal, medical, family, bereavement, jury_duty or other."),
			"half_day": p("boolean", "True when the first day counts as half a day."), "reason": p("string", "Optional."), "comments": p("string", "Optional. The note on the request."),
			"note": p("string", "Why it was changed. Sent to the person and kept in the audit log.")}, required: []string{"id"}},
	{name: "time_off_cancel", method: "POST", path: "/v1/time-off/{id}/cancel", write: true,
		desc:  "Cancel somebody else's pending or approved time off request, off the calendar. The person is told and an audit row is kept. Only the person's approver and admins may.",
		props: map[string]any{"id": p("string", "The request id."), "note": p("string", "Why it was cancelled. Sent to the person and kept in the audit log.")}, required: []string{"id"}},
}

// TimeOffTools is time off as connector tools, or nil when it is not switched on. Each is one
// route run through its own handler with the caller's own identity, like the CRM's.
func (s *Service) TimeOffTools() agents.ToolSet {
	if s.timeoff == nil {
		return nil
	}
	mux := http.NewServeMux()
	s.timeOffRoutes(mux)
	t := &crmMCP{mux: mux, byKey: map[string]crmTool{}, tools: timeOffTools, changes: "time off"}
	for _, x := range timeOffTools {
		t.byKey[x.name] = x
	}
	return t
}
