package web

// Time, estimates, and workload.
//
// The running timer rides on every page rather than living on the document it
// belongs to. A timer you can only see by navigating back to the job is a
// timer that runs all night, and the person who left it running is the last
// one to find out.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

func (s *Server) startTimer(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	_, err := s.time.Start(r.Context(), id.OrgID, id.Subjects, docID, r.FormValue("note"))
	switch {
	case errors.Is(err, timelog.ErrAlreadyRunning):
		s.fail(w, r, http.StatusConflict,
			"you already have a timer running. Stop that one first, so the same hour is not on two jobs.")
		return
	case err != nil:
		s.log.WarnContext(r.Context(), "start timer refused", "doc", docID, "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot record time against this")
		return
	}
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

func (s *Server) stopTimer(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())

	entry, err := s.time.Stop(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil && !errors.Is(err, timelog.ErrNotRunning) {
		s.log.ErrorContext(r.Context(), "stop timer", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not stop the timer")
		return
	}
	back := "/documents"
	if entry != nil {
		back = "/documents/" + entry.DocumentID
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// logTime is the manual path, and the one most entries come through. The timer
// is always forgotten at least once.
func (s *Server) logTime(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	// What was typed, kept for the form when it is refused. The person stays on the document they were logging against, with the
	// reason beside the field, not on an error page at the address the form posts to.
	typed := &TimeForm{Minutes: r.FormValue("minutes"), Hours: r.FormValue("hours"), Mins: r.FormValue("mins"),
		On: strings.TrimSpace(r.FormValue("on")), Note: r.FormValue("note"), Tags: r.Form["tags"]}
	refuse := func(status int, msg string) {
		typed.Error = msg
		s.showDocument(w, r, docID, &refused{Status: status, Message: msg, Time: typed})
	}

	minutes, blank, err := formDuration(r, "hours", "mins", "minutes")
	if err != nil || blank {
		msg := blankDuration
		if err != nil {
			msg = err.Error()
		}
		refuse(http.StatusBadRequest, msg)
		return
	}
	on := time.Now()
	if v := strings.TrimSpace(r.FormValue("on")); v != "" {
		if d, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
			on = d
		}
	}

	entry, err := s.time.Log(r.Context(), id.OrgID, id.Subjects, docID, minutes, on,
		r.FormValue("note"))
	if err != nil {
		s.log.WarnContext(r.Context(), "log time refused", "doc", docID, "error", err)
		refuse(http.StatusBadRequest, strings.TrimPrefix(err.Error(), "timelog: "))
		return
	}
	// Best-effort: a tag that failed to attach is not a reason to lose an hour
	// that was otherwise logged correctly.
	for _, tagID := range r.Form["tags"] {
		if err := s.time.TagEntry(r.Context(), id.OrgID, id.Subjects, entry.ID, tagID); err != nil {
			s.log.WarnContext(r.Context(), "tag at log time failed", "tag", tagID, "error", err)
		}
	}
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

// parseCorrectedDate reads the optional "on" field a Correct form carries.
// Blank means "the day is not changing" -- nil, not today's date -- so
// leaving the field alone (the ordinary case) can never accidentally move an
// entry to whatever day somebody happened to be fixing a typo on.
func parseCorrectedDate(v string) *time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	d, err := time.ParseInLocation("2006-01-02", v, time.Local)
	if err != nil {
		return nil
	}
	return &d
}

// correctError picks the response for a failed Correct/Remove. ErrDeleted
// gets its own message -- "you cannot" reads like a permission refusal, and
// this is not one, it is "restore it first", a completely different next step.
func (s *Server) correctError(w http.ResponseWriter, r *http.Request, action string, err error) {
	s.log.WarnContext(r.Context(), action+" time refused", "error", err)
	if errors.Is(err, timelog.ErrDeleted) {
		s.fail(w, r, http.StatusConflict, "that entry was removed -- restore it first")
		return
	}
	s.fail(w, r, http.StatusForbidden, "you cannot "+action+" that entry")
}

func (s *Server) correctTime(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	minutes, blank, err := formDuration(r, "hours", "mins", "minutes")
	if err != nil || blank {
		msg := blankDuration
		if err != nil {
			msg = err.Error()
		}
		s.showDocument(w, r, docID, &refused{Status: http.StatusBadRequest, Message: msg, Time: &TimeForm{Fix: &EntryFix{
			ID: r.PathValue("entry"), Error: msg, Hours: r.FormValue("hours"), Mins: r.FormValue("mins"),
			On: strings.TrimSpace(r.FormValue("on")), Note: r.FormValue("note")}}})
		return
	}
	if err := s.time.Correct(r.Context(), id.OrgID, id.Subjects, r.PathValue("entry"),
		minutes, parseCorrectedDate(r.FormValue("on")),
		r.FormValue("note")); err != nil {
		s.correctError(w, r, "change", err)
		return
	}
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

func (s *Server) removeTime(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	entryID := r.PathValue("entry")
	if err := s.time.Remove(r.Context(), id.OrgID, id.Subjects, entryID); err != nil {
		s.correctError(w, r, "remove", err)
		return
	}
	// justRemoved, not the showRemoved toggle: this is the one-page-visit
	// "you just did that, want to undo it" banner, separate from browsing
	// everything already removed.
	http.Redirect(w, r, "/documents/"+docID+"?justRemoved="+entryID, http.StatusSeeOther)
}

// correctMyTime and removeMyTime are the same two actions as correctTime and
// removeTime above, reachable from the weekly "My time" list instead of a
// document's own page. They have to exist separately rather than reusing the
// document-scoped routes: an entry logged as "No job, just my time" has no
// document to put in the URL, and until now that meant no document page to
// go to meant no way to fix or remove it at all, anywhere.
func (s *Server) correctMyTime(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	minutes, blank, err := formDuration(r, "hours", "mins", "minutes")
	if err != nil || blank {
		s.refuseOwnTime(w, r, err, r.PathValue("entry"))
		return
	}
	if err := s.time.Correct(r.Context(), id.OrgID, id.Subjects, r.PathValue("entry"),
		minutes, parseCorrectedDate(r.FormValue("on")),
		r.FormValue("note")); err != nil {
		s.correctError(w, r, "change", err)
		return
	}
	http.Redirect(w, r, backTo(r, "/time"), http.StatusSeeOther)
}

// withParam appends a query parameter to a redirect target that may already
// have its own -- backTo's "back" value is "/time?from=...", and justRemoved
// needs to join that with "&" rather than a second "?".
func withParam(base, key, value string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + key + "=" + value
}

func (s *Server) removeMyTime(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	entryID := r.PathValue("entry")
	if err := s.time.Remove(r.Context(), id.OrgID, id.Subjects, entryID); err != nil {
		s.correctError(w, r, "remove", err)
		return
	}
	http.Redirect(w, r, withParam(backTo(r, "/time"), "justRemoved", entryID), http.StatusSeeOther)
}

// restoreMyTime is reachable from both My Time (the "Show removed" list and
// its own Undo banner) and a document page's Undo banner -- same as tagging,
// restoring an entry is not document-scoped, so one route serves both, and
// backTo sends the reader back to whichever page the form was actually on.
func (s *Server) restoreMyTime(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	if err := s.time.Restore(r.Context(), id.OrgID, id.Subjects, r.PathValue("entry")); err != nil {
		s.log.WarnContext(r.Context(), "restore time refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "could not restore that entry")
		return
	}
	http.Redirect(w, r, backTo(r, "/time"), http.StatusSeeOther)
}

func (s *Server) tagMyTime(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	if err := s.time.TagEntry(r.Context(), id.OrgID, id.Subjects,
		r.PathValue("entry"), r.FormValue("tag")); err != nil {
		s.log.WarnContext(r.Context(), "tag entry refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot tag that entry")
		return
	}
	http.Redirect(w, r, backTo(r, "/time"), http.StatusSeeOther)
}

func (s *Server) untagMyTime(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	if err := s.time.UntagEntry(r.Context(), id.OrgID, id.Subjects,
		r.PathValue("entry"), r.PathValue("tag")); err != nil {
		s.log.WarnContext(r.Context(), "untag entry refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot untag that entry")
		return
	}
	http.Redirect(w, r, backTo(r, "/time"), http.StatusSeeOther)
}

// batchTag applies one tag to a week of a person's own history in one
// action -- the backfill helper. Self-service is enforced in the service
// layer (BatchTag), not here: id.PrincipalID is the only principal this
// handler ever passes, so there is no argument to smuggle another person's ID
// through even if the form were tampered with.
func (s *Server) batchTag(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	from, err := time.ParseInLocation("2006-01-02", r.FormValue("from"), time.Local)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "that is not a date")
		return
	}
	n, err := s.time.BatchTag(r.Context(), id.OrgID, id.Subjects, id.PrincipalID,
		r.FormValue("tag"), from, from.AddDate(0, 0, 7))
	if err != nil {
		s.log.WarnContext(r.Context(), "batch tag refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "could not tag that week")
		return
	}
	s.log.InfoContext(r.Context(), "batch tagged", "count", n)
	http.Redirect(w, r, backTo(r, "/time?from="+r.FormValue("from")), http.StatusSeeOther)
}

// setEstimate finally writes the column the substrate has carried unused since
// the beginning.
func (s *Server) setEstimate(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	var minutes *int
	// Empty fields clear the estimate rather than setting it to zero.
	// "Nobody has estimated this" and "this takes no time" are different
	// answers and only one of them is ever true.
	_, hasH := r.Form["estimate_hours"]
	_, hasM := r.Form["estimate_mins"]
	if hasH || hasM || strings.TrimSpace(r.FormValue("estimate")) != "" {
		n, blank, err := formDuration(r, "estimate_hours", "estimate_mins", "estimate")
		if err != nil {
			msg := err.Error()
			s.showDocument(w, r, docID, &refused{Status: http.StatusBadRequest, Message: msg, Time: &TimeForm{Estimate: &EstimateFix{
				Error: msg, Hours: r.FormValue("estimate_hours"), Mins: r.FormValue("estimate_mins")}}})
			return
		}
		if !blank {
			minutes = &n
		}
	}
	if err := s.time.SetEstimate(r.Context(), id.OrgID, id.Subjects, docID, minutes); err != nil {
		s.log.WarnContext(r.Context(), "set estimate refused", "doc", docID, "error", err)
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "timelog: "))
		return
	}
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

// getWorkload answers who is overloaded.
func (s *Server) getWorkload(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())

	// A week from Monday by default, because that is the window a planner
	// works in and asking them to pick dates before showing anything is a page
	// people bounce off.
	from := startOfWeek(time.Now())
	if v := r.URL.Query().Get("from"); v != "" {
		if d, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
			from = d
		}
	}
	to := from.AddDate(0, 0, 7)

	load, err := s.time.Workload(r.Context(), id.OrgID, id.PrincipalID, from, to)
	switch {
	case errors.Is(err, timelog.ErrNotAllowed):
		// Not a 500, and not a blank table either. This page is everybody
		// else's hours, so somebody without the grant is being told the page is
		// not theirs rather than being shown an empty one they might read as
		// "nobody logged anything".
		s.fail(w, r, http.StatusForbidden,
			"Workload shows everybody's hours, and that is not something your "+
				"account can see. Your own time is on the time page.")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "workload", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not work out the workload")
		return
	}
	admin, _ := s.auth.IsAdmin(r.Context(), id.OrgID, id.PrincipalID)

	s.render(w, r, "workload", page{
		Title: "Workload", Load: load, IsAdmin: admin,
		WeekFrom: from, WeekTo: to,
		PrevWeek: from.AddDate(0, 0, -7).Format("2006-01-02"),
		NextWeek: from.AddDate(0, 0, 7).Format("2006-01-02"),
	})
}

func startOfWeek(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
	for d.Weekday() != time.Monday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// setCapacity records somebody's week and their rate. Owner only: a person
// setting their own capacity to two hours is not a feature.
func (s *Server) setCapacity(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	hours, err := strconv.Atoi(strings.TrimSpace(r.FormValue("hours")))
	if err != nil || hours < 0 || hours > 168 {
		s.fail(w, r, http.StatusBadRequest, "that is not a number of hours in a week")
		return
	}
	var rate *int
	// Blank leaves cost unmeasured, which reads as unknown rather than free.
	if v := strings.TrimSpace(r.FormValue("rate")); v != "" {
		pounds, err := strconv.ParseFloat(v, 64)
		if err != nil || pounds < 0 {
			s.fail(w, r, http.StatusBadRequest, "that is not an hourly rate")
			return
		}
		cents := int(pounds * 100)
		rate = &cents
	}

	if err := s.time.SetCapacity(r.Context(), id.OrgID, r.PathValue("id"), hours*60, rate); err != nil {
		s.log.ErrorContext(r.Context(), "set capacity", "error", err)
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "timelog: "))
		return
	}
	http.Redirect(w, r, "/workload", http.StatusSeeOther)
}
