package web

// Preventive maintenance on screen.
//
// The page leads with compliance rather than with the schedule list, because
// "did we do what we said we would" is the question this feature exists to
// answer. A list of schedules with no record of whether they were kept is a
// calendar, and everybody already has one of those.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/maint"
)

func (s *Server) getMaintenance(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())

	// Ninety days back by default, which is one quarter and the window an audit
	// asks about. Anything shorter reads as clean because it has not been open
	// long enough for a miss to appear in it.
	to := time.Now().AddDate(0, 0, 1).Truncate(24 * time.Hour)
	from := to.AddDate(0, 0, -90)
	if v := r.URL.Query().Get("from"); v != "" {
		if d, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
			from = d
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if d, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
			to = d
		}
	}

	schedules, err := s.maint.List(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list schedules", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the schedules")
		return
	}
	report, err := s.maint.Report(r.Context(), id.OrgID, id.Subjects, from, to)
	if err != nil {
		s.log.ErrorContext(r.Context(), "compliance", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not work out compliance")
		return
	}

	// The pickers. Neither is fatal: a page that will not render because the
	// template list failed is a page that hides the schedules somebody came for.
	assets, err := s.docs.List(r.Context(), id.OrgID, id.Subjects,
		docs.ListParams{Kind: docs.KindAsset, Limit: 500})
	if err != nil {
		s.log.ErrorContext(r.Context(), "load assets", "error", err)
	}
	tmpls, err := s.docs.Templates(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load templates", "error", err)
	}

	s.render(w, r, "maintenance", page{
		Title: "Maintenance", Schedules: schedules, Compliance: report,
		Assignable: assets, Templates: tmpls,
		WeekFrom: from, WeekTo: to,
		PrevWeek: from.AddDate(0, 0, -90).Format("2006-01-02"),
		NextWeek: from.AddDate(0, 0, 90).Format("2006-01-02"),
	})
}

// saveSchedule creates or updates one.
func (s *Server) saveSchedule(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	sch := maint.Schedule{
		ID:      strings.TrimSpace(r.FormValue("id")),
		AssetID: strings.TrimSpace(r.FormValue("asset")),
		Name:    r.FormValue("name"),
		Enabled: r.FormValue("enabled") != "off",
	}
	if v := strings.TrimSpace(r.FormValue("template")); v != "" {
		sch.TemplateID = &v
	}
	if n, ok := positiveInt(r.FormValue("every_days")); ok {
		sch.EveryDays = &n
	}
	// Both halves of the meter clock or neither. The service rejects a lone
	// threshold, and doing it here too means the message says which field is
	// missing rather than which constraint was violated.
	key := strings.TrimSpace(r.FormValue("meter_key"))
	units, unitsOK := positiveFloat(r.FormValue("every_units"))
	if key != "" && unitsOK {
		sch.MeterKey, sch.EveryUnits = &key, &units
	}
	if n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("lead_days"))); err == nil {
		sch.LeadDays = n
	} else {
		sch.LeadDays = 7
	}

	if _, err := s.maint.Save(r.Context(), id.OrgID, id.Subjects, sch); err != nil {
		if errors.Is(err, maint.ErrInvalid) {
			s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "maint: "))
			return
		}
		s.log.WarnContext(r.Context(), "save schedule refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot set a schedule on that asset")
		return
	}
	http.Redirect(w, r, backTo(r, "/maintenance"), http.StatusSeeOther)
}

func (s *Server) toggleSchedule(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	on := r.FormValue("enabled") == "on"
	if err := s.maint.SetEnabled(r.Context(), id.OrgID, id.Subjects, r.PathValue("id"), on); err != nil {
		s.log.WarnContext(r.Context(), "toggle schedule refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot change that schedule")
		return
	}
	http.Redirect(w, r, backTo(r, "/maintenance"), http.StatusSeeOther)
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := s.maint.Delete(r.Context(), id.OrgID, id.Subjects, r.PathValue("id")); err != nil {
		s.log.WarnContext(r.Context(), "delete schedule refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot remove that schedule")
		return
	}
	http.Redirect(w, r, backTo(r, "/maintenance"), http.StatusSeeOther)
}

// raiseSchedule generates the job now.
//
// It goes through the same path the sweep does, so pressing it twice produces
// one work order rather than two.
func (s *Server) raiseSchedule(w http.ResponseWriter, r *http.Request) {
	if s.pm == nil {
		// The EmberOPS back end: its maintenance addon raises the work.
		s.fail(w, r, http.StatusConflict, "preventive work orders are raised by maintenance in EmberOps")
		return
	}
	id := auth.From(r.Context())
	docID, err := s.pm.RaiseNow(r.Context(), id.OrgID, id.Subjects, r.PathValue("id"))
	if err != nil {
		s.log.WarnContext(r.Context(), "raise refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot raise work on that asset")
		return
	}
	if docID != "" {
		http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
		return
	}
	// Nothing raised means one is already open, which is the design working
	// rather than a failure. Back to where they were, where the open job is
	// already on screen.
	http.Redirect(w, r, backTo(r, "/maintenance"), http.StatusSeeOther)
}

// recordReading takes a meter value off the asset page.
func (s *Server) recordReading(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	assetID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	value, ok := positiveFloat(r.FormValue("value"))
	if !ok {
		s.fail(w, r, http.StatusBadRequest, "that is not a meter reading")
		return
	}
	_, err := s.maint.RecordReading(r.Context(), id.OrgID, id.Subjects, assetID,
		r.FormValue("key"), value, r.FormValue("note"))
	if err != nil {
		if errors.Is(err, maint.ErrInvalid) {
			s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "maint: "))
			return
		}
		s.log.WarnContext(r.Context(), "reading refused", "asset", assetID, "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot record readings against this")
		return
	}
	http.Redirect(w, r, "/documents/"+assetID, http.StatusSeeOther)
}

// backTo sends somebody back where they came from, defaulting to the page the
// action belongs to. A form on the asset page should not land on the schedule
// list, and a form on the schedule list should not land on an asset.
func backTo(r *http.Request, fallback string) string {
	v := strings.TrimSpace(r.FormValue("back"))
	// Only a path on this site. An open redirect is one unchecked form field.
	if strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//") {
		return v
	}
	return fallback
}

func positiveInt(v string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func positiveFloat(v string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return f, true
}
