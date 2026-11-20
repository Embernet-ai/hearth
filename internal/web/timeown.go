package web

// A person's own time, and the manager's queue.
//
// Everything in timepage.go hangs off a document, because that is what time
// meant here until now: a timer started from a work order, hours logged against
// a job. This is the other half. Somebody has to be able to say "I worked four
// hours today" without first deciding which task to blame it on, and then see
// their own week back.

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/deptreq"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// weekOf reads the week being viewed from the query string, defaulting to the
// one containing today.
//
// Shared by both pages here so that moving between them keeps the week, and so
// there is one place that decides a week starts on Monday.
func weekOf(r *http.Request) (from, to time.Time) {
	from = startOfWeek(time.Now())
	if v := r.URL.Query().Get("from"); v != "" {
		if d, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
			from = startOfWeek(d)
		}
	}
	return from, from.AddDate(0, 0, 7)
}

// getMyTime is the page somebody opens to record and read their own hours.
func (s *Server) getMyTime(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	from, to := weekOf(r)
	// A month, from the dashboard's "this month" number: the first of the month
	// the address names, to the end of it.
	month := r.URL.Query().Get("span") == "month"
	if month {
		from = time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.Local)
		if v := r.URL.Query().Get("from"); v != "" {
			if d, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
				from = time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.Local)
			}
		}
		to = from.AddDate(0, 1, 0)
	}
	showRemoved := r.URL.Query().Get("removed") == "1"

	// Whose hours. A colleague's are read-only here, behind the same permission
	// as the workload page, and the forms on the page still record your own.
	whose := id.PrincipalID
	var viewing *personRef
	if v := r.URL.Query().Get("person"); v != "" && v != id.PrincipalID {
		allowed := false
		if s.az != nil {
			if held, err := s.az.Capabilities(r.Context(), id.OrgID, id.PrincipalID); err == nil {
				allowed = held.Has(authz.TimeAll)
			}
		}
		if !allowed {
			s.fail(w, r, http.StatusForbidden, "you can only see your own time")
			return
		}
		viewing = &personRef{ID: v, Name: v}
		if s.auth != nil {
			if list, err := s.auth.People(r.Context(), id.OrgID); err == nil {
				for _, pe := range list {
					if pe.PrincipalID == v {
						viewing.Name = pe.DisplayName
					}
				}
			}
		}
		whose = v
	}

	entries, err := s.time.Mine(r.Context(), id.OrgID, whose, from, to, showRemoved)
	if err != nil {
		s.log.ErrorContext(r.Context(), "my time", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not read your time")
		return
	}
	total, pending, err := s.time.MineTotal(r.Context(), id.OrgID, whose, from, to)
	if err != nil {
		s.log.ErrorContext(r.Context(), "my time total", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not add up your time")
		return
	}

	// The optional task picker: this person's own open work, the same query the
	// dashboard uses for "what needs me". Failure here is not failure of the
	// page. Somebody recording four hours does not lose the ability to do so
	// because a list they were not going to use did not load.
	var open []*docs.Row
	if s.docs != nil {
		q := docs.Query{
			Filters: []docs.Filter{
				{Field: docs.Col("assignee"), Op: docs.OpIs, Values: []string{id.PrincipalID}},
				{Field: docs.Col("status"), Op: docs.OpAnyOf, Values: []string{
					docs.StatusTodo, docs.StatusInProgress, docs.StatusBlocked}},
			},
			Sorts: []docs.Sort{{Field: docs.Col("due"), Desc: false}},
			Limit: 100,
		}
		if rows, err := s.docs.Search(r.Context(), id.OrgID, id.Subjects, q); err == nil {
			open = rows
		}
	}
	tags, err := s.time.ListTags(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load tags", "error", err)
	}

	// MyTimeBack is where every action form on this page should return to --
	// this exact week, with Show removed exactly as it was. Every one of
	// those forms carries it as a hidden "back" field, read by backTo, so
	// correcting or restoring an entry does not reset the view the same way
	// it used to.
	myTimeBack := "/time?from=" + from.Format("2006-01-02")
	if month {
		myTimeBack += "&span=month"
	}
	if viewing != nil {
		myTimeBack += "&person=" + url.QueryEscape(viewing.ID)
	}
	if showRemoved {
		myTimeBack += "&removed=1"
	}
	prev, next := from.AddDate(0, 0, -7), from.AddDate(0, 0, 7)
	if month {
		prev, next = from.AddDate(0, -1, 0), from.AddDate(0, 1, 0)
	}

	s.render(w, r, "time", page{
		TimeForm:    refusedOwnTime(r.URL.Query()),
		TimeOf:      viewing,
		TimeMonth:   month,
		Title:       "My time",
		MyTime:      entries,
		MyMinutes:   total,
		MyPending:   pending,
		MyOpenWork:  open,
		Tags:        tags,
		ShowRemoved: showRemoved,
		JustRemoved: r.URL.Query().Get("justRemoved"),
		MyTimeBack:  myTimeBack,
		WeekFrom:    from, WeekTo: to,
		PrevWeek: prev.Format("2006-01-02"),
		NextWeek: next.Format("2006-01-02"),
	})
}

// postMyTime records an hour against a job, or against nothing.
func (s *Server) postMyTime(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	on := time.Now()
	if v := strings.TrimSpace(r.FormValue("on")); v != "" {
		if d, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
			on = d
		}
	}
	// An empty document is the ordinary case rather than an error.
	docID := strings.TrimSpace(r.FormValue("document"))

	minutes, blank, err := formDuration(r, "hours", "mins", "minutes")
	if err != nil || blank {
		s.refuseOwnTime(w, r, err, "")
		return
	}
	entry, err := s.time.LogOwn(r.Context(), id.OrgID, id.Subjects, docID,
		minutes, on, r.FormValue("note"))
	if err != nil {
		s.log.WarnContext(r.Context(), "log own time refused", "error", err)
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "timelog: "))
		return
	}
	for _, tagID := range r.Form["tags"] {
		if err := s.time.TagEntry(r.Context(), id.OrgID, id.Subjects, entry.ID, tagID); err != nil {
			s.log.WarnContext(r.Context(), "tag at log time failed", "tag", tagID, "error", err)
		}
	}
	s.backToTime(w, r)
}

// startMyTimer starts a clock that points at nothing, from the time page.
func (s *Server) startMyTimer(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	_, err := s.time.Start(r.Context(), id.OrgID, id.Subjects,
		strings.TrimSpace(r.FormValue("document")), r.FormValue("note"))
	switch {
	case errors.Is(err, timelog.ErrAlreadyRunning):
		s.fail(w, r, http.StatusConflict,
			"you already have a timer running. Stop that one first, so the same hour is not on two jobs.")
		return
	case err != nil:
		s.log.WarnContext(r.Context(), "start own timer refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot record time against this")
		return
	}
	s.backToTime(w, r)
}

// backToTime returns to the week the form was submitted from, so recording an
// hour against last week does not silently move the reader to this one.
func (s *Server) backToTime(w http.ResponseWriter, r *http.Request) {
	back := "/time"
	if v := strings.TrimSpace(r.FormValue("week")); v != "" {
		if _, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
			back += "?from=" + v
		}
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// getApprovals is the manager's queue.
//
// No capability check and none wanted. The queue is defined by the routing
// itself: it returns what this person may decide, which for almost everybody is
// nothing and renders as an empty page saying so. Gating it on a capability as
// well would be a second answer to a question the query already answers, and
// the two would eventually disagree.
func (s *Server) getApprovals(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())

	waiting, err := s.time.Pending(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "approvals", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not read what is waiting")
		return
	}
	// What has already been decided, so the record is visible without anybody
	// signing it off twice. Somebody granted everybody's time sees the whole
	// company; everybody else sees what they themselves decided.
	seeAll := false
	if s.az != nil {
		if held, err := s.az.Capabilities(r.Context(), id.OrgID, id.PrincipalID); err == nil {
			seeAll = held.Has(authz.TimeAll)
		}
	}
	from := time.Now().AddDate(0, -3, 0)
	decided, err := s.time.Decided(r.Context(), id.OrgID, id.PrincipalID, from, time.Now().AddDate(0, 0, 1), seeAll)
	if err != nil {
		s.log.ErrorContext(r.Context(), "decided time", "error", err)
	}

	var flash string
	if n := r.URL.Query().Get("approved"); n != "" {
		flash = "Approved " + n + " entries."
	}

	// Departments for everybody on the page, in one read. Without the chart the
	// page still works, as one list with no department to pick.
	var chart map[string]*authz.Structure
	if s.az != nil {
		if c, err := s.az.StructureFor(r.Context(), id.OrgID); err == nil {
			chart = c
		} else {
			s.log.ErrorContext(r.Context(), "approvals chart", "error", err)
		}
	}
	q := r.URL.Query()
	view := approvalView(groupByPerson(waiting), chart, q.Get("dept"), q.Get("person"))
	decided = view.narrow(decided, chart)

	// Department requests sit above the hours: somebody waiting to be put in a
	// department is somebody whose hours have nowhere to go yet.
	var deptAsks []*deptreq.Request
	var allDepts []deptreq.Department
	if s.deptreq != nil {
		if deptAsks, err = s.deptreq.ToDecide(r.Context(), id.OrgID, id.PrincipalID); err != nil {
			s.log.ErrorContext(r.Context(), "department requests", "error", err)
		}
		if len(deptAsks) > 0 {
			if allDepts, err = s.deptreq.Departments(r.Context(), id.OrgID); err != nil {
				s.log.ErrorContext(r.Context(), "departments", "error", err)
			}
		}
	}

	s.render(w, r, "approvals", page{
		Title: "Approvals", Approvals: waiting, ApprovalGroups: view.People, Approval: view,
		DecidedTime: decided, SeeAllTime: seeAll, Flash: flash,
		DeptToDecide: deptAsks, AllDepartments: allDepts,
	})
}

// ApprovalGroup is one person's waiting entries, so a whole person's worth can
// be reviewed together and signed off in one go.
type ApprovalGroup struct {
	PersonID    string
	PersonName  string
	Minutes     int
	Entries     []timelog.Entry
	Departments []authz.Department
}

// noDepartment is the choice for people in no department. They are the ones
// most likely to be missed, so they get an entry of their own rather than
// disappearing the moment a department is picked.
const noDepartment = "none"

// ApprovalOption is one choice in the department picker, with what waits there.
type ApprovalOption struct {
	Value   string
	Label   string
	Entries int
	Minutes int
}

// ApprovalView is Approvals narrowed to a department and then a person.
//
// Picked rather than scrolled: with the ClickUp import waiting on sign-off the
// page was dozens of people long, each a table, and the one somebody came to
// approve was found by opening them in turn.
type ApprovalView struct {
	Dept   string
	Person string
	// Depts are the departments with something waiting, each with its total.
	Depts []ApprovalOption
	// People are everybody waiting in the chosen department (all of them when
	// none is chosen), which is both the second picker and the summary table.
	People []ApprovalGroup
	// Selected is the chosen person's entries, nil until somebody is picked.
	Selected *ApprovalGroup
	// The totals across everything waiting, whatever is picked.
	Entries, Minutes, Waiting int
}

// Back is the address of this exact view, so a decision returns to it rather
// than to the top of the page.
func (v *ApprovalView) Back() string {
	q := url.Values{}
	if v.Dept != "" {
		q.Set("dept", v.Dept)
	}
	if v.Person != "" {
		q.Set("person", v.Person)
	}
	if len(q) == 0 {
		return "/time/approvals"
	}
	return "/time/approvals?" + q.Encode()
}

func approvalView(groups []ApprovalGroup, chart map[string]*authz.Structure, dept, person string) *ApprovalView {
	v := &ApprovalView{Waiting: len(groups)}
	index := map[string]int{}
	for i := range groups {
		g := &groups[i]
		if st := chart[g.PersonID]; st != nil {
			g.Departments = st.Departments
		}
		v.Entries += len(g.Entries)
		v.Minutes += g.Minutes
		for _, d := range deptsOf(*g) {
			j, ok := index[d.ID]
			if !ok {
				j = len(v.Depts)
				index[d.ID] = j
				v.Depts = append(v.Depts, ApprovalOption{Value: d.ID, Label: d.Name})
			}
			v.Depts[j].Entries += len(g.Entries)
			v.Depts[j].Minutes += g.Minutes
		}
	}
	// By name, with "No department" last so it reads as the leftover it is.
	sort.Slice(v.Depts, func(i, j int) bool {
		a, b := v.Depts[i], v.Depts[j]
		if (a.Value == noDepartment) != (b.Value == noDepartment) {
			return b.Value == noDepartment
		}
		return a.Label < b.Label
	})

	// A department or person that is not waiting any more, typically because
	// the last entry was just approved, falls back to the wider view.
	if _, ok := index[dept]; ok {
		v.Dept = dept
	}
	for _, g := range groups {
		if v.Dept != "" && !inDept(g, v.Dept) {
			continue
		}
		v.People = append(v.People, g)
		if g.PersonID == person {
			sel := g
			v.Selected, v.Person = &sel, person
		}
	}
	return v
}

// deptsOf is a person's departments, or the no-department choice.
func deptsOf(g ApprovalGroup) []authz.Department {
	if len(g.Departments) == 0 {
		return []authz.Department{{ID: noDepartment, Name: "No department"}}
	}
	return g.Departments
}

func inDept(g ApprovalGroup, dept string) bool {
	for _, d := range deptsOf(g) {
		if d.ID == dept {
			return true
		}
	}
	return false
}

// narrow applies the same choice to the already-decided list, so picking
// somebody shows their history beside what is waiting for them.
func (v *ApprovalView) narrow(entries []timelog.Entry, chart map[string]*authz.Structure) []timelog.Entry {
	if v.Dept == "" && v.Person == "" {
		return entries
	}
	var out []timelog.Entry
	for _, e := range entries {
		if v.Person != "" && e.PrincipalID != v.Person {
			continue
		}
		if v.Dept != "" {
			g := ApprovalGroup{PersonID: e.PrincipalID}
			if st := chart[e.PrincipalID]; st != nil {
				g.Departments = st.Departments
			}
			if !inDept(g, v.Dept) {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

func groupByPerson(entries []timelog.Entry) []ApprovalGroup {
	index := map[string]int{}
	var out []ApprovalGroup
	for _, e := range entries {
		i, ok := index[e.PrincipalID]
		if !ok {
			i = len(out)
			index[e.PrincipalID] = i
			out = append(out, ApprovalGroup{PersonID: e.PrincipalID, PersonName: e.PersonName})
		}
		out[i].Entries = append(out[i].Entries, e)
		out[i].Minutes += e.Elapsed()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PersonName < out[j].PersonName })
	return out
}

// postApprovalsBulk approves every entry the form names, all or nothing.
func (s *Server) postApprovalsBulk(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	n, err := s.time.ApproveMany(r.Context(), id.OrgID, id.PrincipalID, r.Form["entry"])
	switch {
	case errors.Is(err, timelog.ErrBatchRefused):
		s.fail(w, r, http.StatusConflict, err.Error()+". Reload the page and try again.")
		return
	case err != nil && n == 0 && len(r.Form["entry"]) == 0:
		http.Redirect(w, r, backTo(r, "/time/approvals"), http.StatusSeeOther)
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "approve many", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not record those approvals")
		return
	}
	// Back to the department and person they were on, with the count added.
	back := backTo(r, "/time/approvals")
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, fmt.Sprintf("%s%sapproved=%d", back, sep, n), http.StatusSeeOther)
}

// postApproval decides one entry, or puts a decision back.
func (s *Server) postApproval(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	entry := r.PathValue("entry")

	var err error
	switch r.FormValue("decision") {
	case "approve":
		err = s.time.Decide(r.Context(), id.OrgID, id.PrincipalID, entry, true, r.FormValue("note"))
	case "back":
		err = s.time.Decide(r.Context(), id.OrgID, id.PrincipalID, entry, false, r.FormValue("note"))
	case "reopen":
		err = s.time.Reopen(r.Context(), id.OrgID, id.PrincipalID, entry)
	default:
		s.fail(w, r, http.StatusBadRequest, "that is not a decision")
		return
	}

	switch {
	case errors.Is(err, timelog.ErrNotYours):
		// Refused and absent read the same on purpose. Telling somebody an
		// entry exists but is not theirs to decide is telling them about work
		// they are not entitled to know about.
		s.fail(w, r, http.StatusForbidden, "that time is not yours to decide")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "decide time", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not record that decision")
		return
	}
	http.Redirect(w, r, backTo(r, "/time/approvals"), http.StatusSeeOther)
}

// refuseOwnTime sends somebody whose hours and minutes could not be taken back to My time, the page that has the form, with the reason
// and what they typed in the address so they are shown and kept. The reason travels as a short code, never as text: an address is not
// somewhere to put words that a page then prints. entry is the entry being corrected, empty when it was a new one.
func (s *Server) refuseOwnTime(w http.ResponseWriter, r *http.Request, err error, entry string) {
	code := "blank"
	var de *durationError
	if errors.As(err, &de) {
		code = ownDurationCode(de.msg)
	}
	q := url.Values{"te": {code}}
	for k, v := range map[string]string{"th": r.FormValue("hours"), "tm": r.FormValue("mins"), "tn": r.FormValue("note"),
		"to": strings.TrimSpace(r.FormValue("on")), "tk": entry, "tx": r.FormValue("minutes")} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if week := strings.TrimSpace(r.FormValue("week")); week != "" {
		q.Set("from", week)
	}
	http.Redirect(w, r, "/time?"+q.Encode(), http.StatusSeeOther)
}

var ownDurationMessages = map[string]string{
	"blank": blankDuration,
	"hours": "Hours must be a whole number, like 2.",
	"mins":  "Minutes must be a whole number from 0 to 59.",
	"range": "Minutes go from 0 to 59. Put whole hours in the Hours box.",
	"big":   "That is too many hours for one entry.",
	"old":   "How long did it take? Enter hours, minutes, or both.",
}

func ownDurationCode(msg string) string {
	for code, m := range ownDurationMessages {
		if m == msg {
			return code
		}
	}
	return "old"
}

// refusedOwnTime reads back what refuseOwnTime put in the address: the reason (one of a fixed set of sentences) and what was typed.
func refusedOwnTime(q url.Values) *TimeForm {
	msg, ok := ownDurationMessages[q.Get("te")]
	if !ok {
		return nil
	}
	tf := &TimeForm{Error: msg, Hours: q.Get("th"), Mins: q.Get("tm"), Minutes: q.Get("tx"), On: q.Get("to"), Note: q.Get("tn")}
	if k := q.Get("tk"); k != "" {
		tf.Error = ""
		tf.Fix = &EntryFix{ID: k, Error: msg, Hours: tf.Hours, Mins: tf.Mins, On: tf.On, Note: tf.Note}
	}
	return tf
}
