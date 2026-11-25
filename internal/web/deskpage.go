package web

// The daily work of selling, on screen: the follow-up queue, the forecast, the sales report, a change to many
// records at once, and mail and meetings sent from a record through the person's own connected account.
//
// Every page here is for people who work sales (the Sales team) and super admins. Anybody else is told there is no
// such page. Nothing defaults to "mine": the queue, the forecast, and the report start with everybody's, and a person
// narrows them with a filter.

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/connect"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/crmforms"
	"github.com/Embernet-ai/hearth/internal/crmmarketing"
	"github.com/Embernet-ai/hearth/internal/crmops"
)

// deskData is what the sales desk pages carry, in one field of the page.
type deskData struct {
	Followups []*crm.Followup
	Total     int
	Who       string
	Kind      string
	Status    string
	Due       string
	Forecast  *crm.Forecast
	Report    *crm.SalesReport
	From, To  string
	// MailOn says the person can send mail and book meetings from a record through a connected account.
	MailOn bool
	Today  string
	Me     string
	// Dups is the groups of probable duplicates on the duplicates page, of the kind DupType.
	Dups    []crm.DupGroup
	DupType string
	// EmberOps is the record's quotes and call logs from EmberOps, and Quoted what has been quoted in the report's period.
	EmberOps *crmops.Panel
	Quoted   *crmops.Totals
	// Mautic is a contact's segments and campaigns, and MauticNote says why there are none to show.
	Mautic     *crmmarketing.Panel
	MauticNote string
	// Forms is the form responses this person gave on Formbricks.
	Forms []crmforms.Entry
	// MauticChoices are the segments and campaigns a salesperson may enrol people into from the contacts list.
	MauticChoices []crmmarketing.Choice
	// Properties are a record's custom fields with its values; PropDefs, PropKind, PropTypes and CanManage drive the page where an
	// admin defines them.
	Properties []crm.PropertyValue
	PropDefs   []crm.PropertyDef
	PropKind   string
	PropTypes  []string
	CanManage  bool
	// PropSel and PropVal are the custom field filter a list is under.
	PropSel string
	PropVal string
}

func (s *Server) deskRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /crm/followups", s.followupsPage)
	mux.HandleFunc("GET /crm/followups.csv", s.followupsCSV)
	mux.HandleFunc("POST /crm/followups", s.addFollowup)
	mux.HandleFunc("POST /crm/followups/bulk", s.bulkFollowups)
	mux.HandleFunc("POST /crm/followups/{id}/done", s.doneFollowup)
	mux.HandleFunc("POST /crm/followups/{id}/snooze", s.snoozeFollowup)
	mux.HandleFunc("POST /crm/followups/{id}/due", s.dueFollowup)
	mux.HandleFunc("POST /crm/followups/{id}/delete", s.deleteFollowup)
	mux.HandleFunc("GET /crm/forecast", s.forecastPage)
	mux.HandleFunc("GET /crm/forecast.csv", s.forecastCSV)
	mux.HandleFunc("GET /crm/reports", s.reportsPage)
	mux.HandleFunc("GET /crm/reports.csv", s.reportsCSV)
	mux.HandleFunc("POST /crm/bulk", s.bulkChange)
	mux.HandleFunc("GET /crm/duplicates", s.duplicatesPage)
	mux.HandleFunc("POST /crm/merge", s.mergeRecords)
	mux.HandleFunc("GET /crm/properties", s.propertiesPage)
	mux.HandleFunc("POST /crm/properties", s.saveProperty)
	mux.HandleFunc("POST /crm/properties/{id}/delete", s.deleteProperty)
	mux.HandleFunc("POST /crm/properties/values", s.setPropertyValues)
	mux.HandleFunc("POST /activities/{id}/edit", s.editActivity)
	mux.HandleFunc("POST /crm/mautic/segment", s.mauticSegment)
	mux.HandleFunc("POST /crm/mautic/campaign", s.mauticCampaign)
	mux.HandleFunc("POST /crm/mautic/enrol", s.mauticEnrol)
	mux.HandleFunc("POST /crm/email", s.emailFromRecord)
	mux.HandleFunc("POST /crm/meeting", s.meetingFromRecord)
}

// salesAccess is the one check every desk route makes: the deployment has a sales side, and this person works it.
func (s *Server) salesAccess(w http.ResponseWriter, r *http.Request) (crm.Desk, bool) {
	if !s.salesOn(w, r) {
		return nil, false
	}
	desk, ok := s.sales.(crm.Desk)
	id := auth.From(r.Context())
	if !ok || id == nil {
		s.fail(w, r, http.StatusNotFound, "the sales pages are not available on this deployment")
		return nil, false
	}
	has, err := s.sales.HasSalesAccess(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(r.Context(), "sales access", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not check your access")
		return nil, false
	}
	if !has {
		s.fail(w, r, http.StatusNotFound, "no such page")
		return nil, false
	}
	return desk, true
}

// salesNav is whether the rail offers the sales pages to this person.
func (s *Server) salesNav(r *http.Request) bool {
	id := auth.From(r.Context())
	if s.sales == nil || id == nil {
		return false
	}
	has, err := s.sales.HasSalesAccess(r.Context(), id.OrgID, id.Subjects)
	return err == nil && has
}

// localBack keeps a redirect on this site.
func localBack(v, fallback string) string {
	if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.Contains(v, "\\") {
		return fallback
	}
	return v
}

func dayOf(v string) *time.Time {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(v))
	if err != nil {
		return nil
	}
	return &t
}

func ownerParam(r *http.Request) string {
	v := r.URL.Query().Get("owner")
	if v == "me" {
		if id := auth.From(r.Context()); id != nil {
			return id.PrincipalID
		}
	}
	return v
}

// -----------------------------------------------------------------------------
// Follow-ups
// -----------------------------------------------------------------------------

func (s *Server) followupQuery(r *http.Request) (crm.FollowupQuery, deskData) {
	v := r.URL.Query()
	who := v.Get("who")
	q := crm.FollowupQuery{Status: v.Get("status"), Due: v.Get("due"), Assignee: who, Kind: v.Get("kind")}
	if who == "me" {
		q.Assignee = auth.From(r.Context()).PrincipalID
	}
	if q.Status == "" {
		q.Status = "open"
	}
	if q.Status == "all" {
		q.Status = ""
	}
	pg, _ := strconv.Atoi(v.Get("page"))
	if pg < 1 {
		pg = 1
	}
	q.Limit, q.Offset = pageSize, (pg-1)*pageSize
	return q, deskData{Who: who, Status: v.Get("status"), Due: q.Due, Kind: q.Kind}
}

func (s *Server) followupsPage(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	q, d := s.followupQuery(r)
	rows, total, err := desk.Followups(r.Context(), id.OrgID, id.Subjects, q)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list follow-ups", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the follow-ups")
		return
	}
	d.Followups, d.Total, d.Today, d.Me = rows, total, time.Now().Format("2006-01-02"), id.PrincipalID
	if d.Status == "" {
		d.Status = "open"
	}
	s.render(w, r, "followups", page{Title: "Follow-ups", SalesOn: true, Owners: s.owners(r), Desk: &d,
		Pager: pagerFor(r, crm.ListQuery{Limit: pageSize, Offset: q.Offset}, len(rows), total)})
}

func (s *Server) followupsCSV(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	q, _ := s.followupQuery(r)
	q.Limit, q.Offset = 500, 0
	rows, _, err := desk.Followups(r.Context(), id.OrgID, id.Subjects, q)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not export the follow-ups")
		return
	}
	cw := s.csvHead(w, "follow-ups")
	_ = cw.Write([]string{"Follow-up", "About", "Type", "Due", "Assigned to", "Status"})
	for _, f := range rows {
		due := ""
		if f.DueAt != nil {
			due = f.DueAt.Format("2006-01-02")
		}
		_ = cw.Write([]string{safeCell(f.Title), safeCell(f.TargetName), string(f.Target.Type), due, f.AssigneeName, f.Status})
	}
	cw.Flush()
}

func (s *Server) addFollowup(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	ref := core.Ref{Type: core.EntityType(r.FormValue("target_type")), ID: strings.TrimSpace(r.FormValue("target_id"))}
	assignee := strings.TrimSpace(r.FormValue("assignee"))
	if assignee == "me" {
		assignee = id.PrincipalID
	}
	_, err := desk.AddFollowup(r.Context(), id.OrgID, id.Subjects, crm.FollowupParams{
		Kind: r.FormValue("kind"), Title: r.FormValue("title"), Body: r.FormValue("body"), DueAt: dayOf(r.FormValue("due")), Assignee: assignee, Target: ref})
	if err != nil {
		s.crmFail(w, r, err, "you cannot add a follow-up there")
		return
	}
	http.Redirect(w, r, localBack(r.FormValue("back"), recordPath(ref)+"#followups"), http.StatusSeeOther)
}

func recordPath(ref core.Ref) string {
	switch ref.Type {
	case core.EntityCompany:
		return "/companies/" + ref.ID
	case core.EntityContact:
		return "/contacts/" + ref.ID
	case core.EntityDeal:
		return "/deals/" + ref.ID
	}
	return "/crm/followups"
}

func (s *Server) doneFollowup(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	if err := desk.SetFollowupDone(r.Context(), id.OrgID, id.Subjects, r.PathValue("id"), r.FormValue("done") != "0"); err != nil {
		s.crmFail(w, r, err, "you cannot change that follow-up")
		return
	}
	http.Redirect(w, r, localBack(r.FormValue("back"), "/crm/followups"), http.StatusSeeOther)
}

func (s *Server) snoozeFollowup(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	days, _ := strconv.Atoi(r.FormValue("days"))
	if err := desk.SnoozeFollowup(r.Context(), id.OrgID, id.Subjects, r.PathValue("id"), days); err != nil {
		s.crmFail(w, r, err, "you cannot change that follow-up")
		return
	}
	http.Redirect(w, r, localBack(r.FormValue("back"), "/crm/followups"), http.StatusSeeOther)
}

func (s *Server) bulkFollowups(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	res, err := desk.CompleteFollowups(r.Context(), id.OrgID, id.Subjects, r.Form["ids"], r.FormValue("done") != "0")
	if err != nil {
		s.crmFail(w, r, err, "you cannot change those")
		return
	}
	back := localBack(r.FormValue("back"), "/crm/followups")
	msg := fmt.Sprintf("Changed %d.", res.Done)
	if n := len(res.Failed); n > 0 {
		msg += fmt.Sprintf(" %d could not be changed.", n)
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"notice="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (s *Server) dueFollowup(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	assignee := strings.TrimSpace(r.FormValue("assignee"))
	if assignee == "me" {
		assignee = id.PrincipalID
	}
	if err := desk.RescheduleFollowup(r.Context(), id.OrgID, id.Subjects, r.PathValue("id"), dayOf(r.FormValue("due")), assignee); err != nil {
		s.crmFail(w, r, err, "you cannot change that follow-up")
		return
	}
	http.Redirect(w, r, localBack(r.FormValue("back"), "/crm/followups"), http.StatusSeeOther)
}

func (s *Server) deleteFollowup(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	_ = r.ParseForm()
	if err := desk.DeleteFollowup(r.Context(), id.OrgID, id.Subjects, r.PathValue("id")); err != nil {
		s.crmFail(w, r, err, "you cannot remove that follow-up")
		return
	}
	http.Redirect(w, r, localBack(r.FormValue("back"), "/crm/followups"), http.StatusSeeOther)
}

// deskFor puts the follow-ups, and whether mail can go out, on a record's page.
func (s *Server) deskFor(r *http.Request, ref core.Ref, p *page) {
	id := auth.From(r.Context())
	if !s.salesNav(r) {
		return
	}
	desk, ok := s.sales.(crm.Desk)
	if !ok {
		return
	}
	d := &deskData{Today: time.Now().Format("2006-01-02"), Me: id.PrincipalID, MailOn: s.connectorsOn()}
	rows, _, err := desk.Followups(r.Context(), id.OrgID, id.Subjects, crm.FollowupQuery{Target: &ref, Limit: 100})
	if err != nil {
		s.log.WarnContext(r.Context(), "load follow-ups", "error", err)
	}
	d.Followups = rows
	d.EmberOps = s.emberOpsPanel(r, ref, p)
	if props, ok := s.sales.(crm.Props); ok {
		if vals, err := props.PropertiesFor(r.Context(), id.OrgID, id.Subjects, ref); err == nil {
			d.Properties = vals
		}
	}
	if ref.Type == core.EntityContact {
		d.Mautic, d.MauticNote = s.mauticPanel(r, ref, p)
		d.Forms = s.formResponses(r, p)
	}
	p.Desk = d
}

// newCRMOps is the EmberOps reader, or nil when there is no EmberOps client: a nil client inside the interface would not be nil.
func newCRMOps(l crmops.Lister) *crmops.Service {
	if l == nil {
		return nil
	}
	return crmops.New(l)
}

// emberOpsPanel is what EmberOps holds about this record: its quotations and the calls it logged. A failure leaves the
// panel out and the page whole.
func (s *Server) emberOpsPanel(r *http.Request, ref core.Ref, p *page) *crmops.Panel {
	if s.crmOps == nil {
		return nil
	}
	var phones []string
	if ref.Type == core.EntityContact {
		if p.Contact != nil {
			phones = append(phones, p.Contact.Phone)
		}
		if p.Details != nil {
			phones = append(phones, p.Details.ExtraPhones...)
		}
	}
	panel, err := s.crmOps.For(r.Context(), p.EmberOps, phones)
	if err != nil {
		s.log.WarnContext(r.Context(), "load EmberOps quotes and calls", "error", err)
		return nil
	}
	if len(panel.Quotes) == 0 && len(panel.Calls) == 0 && panel.NewQuoteURL == "" {
		return nil
	}
	return panel
}

// -----------------------------------------------------------------------------
// Forecast and the report
// -----------------------------------------------------------------------------

func (s *Server) forecastPage(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	owner := ownerParam(r)
	f, err := desk.Forecast(r.Context(), id.OrgID, id.Subjects, owner, time.Now())
	if err != nil {
		s.log.ErrorContext(r.Context(), "forecast", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not build the forecast")
		return
	}
	s.render(w, r, "forecast", page{Title: "Forecast", SalesOn: true, Owners: s.owners(r), OwnerSel: owner,
		ExportURL: exportLink(r, "/crm/forecast.csv"), Desk: &deskData{Forecast: f}})
}

func (s *Server) forecastCSV(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	f, err := desk.Forecast(r.Context(), id.OrgID, id.Subjects, ownerParam(r), time.Now())
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not export the forecast")
		return
	}
	cw := s.csvHead(w, "forecast")
	writeForecast(cw, f)
	cw.Flush()
}

func writeForecast(cw *csv.Writer, f *crm.Forecast) {
	_ = cw.Write([]string{"Expected close", "Deal", "Stage", "Chance", "Amount", "Weighted", "Company", "Owner"})
	for _, b := range f.Buckets {
		for _, d := range b.Deals {
			w := d.AmountCents * int64(crm.Probability(d.Stage)) / 100
			_ = cw.Write([]string{b.Label, safeCell(d.Name), crm.StageLabel(d.Stage), strconv.Itoa(crm.Probability(d.Stage)) + "%",
				fmt.Sprintf("%.2f", float64(d.AmountCents)/100), fmt.Sprintf("%.2f", float64(w)/100), safeCell(d.CompanyName), d.OwnerName})
		}
	}
}

func (s *Server) reportsPage(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	now := time.Now()
	to := now
	if t := dayOf(r.URL.Query().Get("to")); t != nil {
		to = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, now.Location())
	}
	from := to.AddDate(0, 0, -29)
	if t := dayOf(r.URL.Query().Get("from")); t != nil {
		from = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, now.Location())
	}
	if from.After(to) {
		from, to = to, from
	}
	owner := ownerParam(r)
	rep, err := desk.SalesReport(r.Context(), id.OrgID, id.Subjects, from, to, owner)
	if err != nil {
		s.log.ErrorContext(r.Context(), "sales report", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not build the report")
		return
	}
	var quoted *crmops.Totals
	if s.crmOps != nil {
		if q, err := s.crmOps.QuotedBetween(r.Context(), from.Format("2006-01-02"), to.Format("2006-01-02")); err != nil {
			s.log.WarnContext(r.Context(), "read quoted totals", "error", err)
		} else {
			quoted = q
		}
	}
	s.render(w, r, "salesreports", page{Title: "Sales reports", SalesOn: true, Owners: s.owners(r), OwnerSel: owner,
		Desk: &deskData{Report: rep, Quoted: quoted, From: from.Format("2006-01-02"), To: to.Format("2006-01-02")}})
}

// -----------------------------------------------------------------------------
// Bulk change
// -----------------------------------------------------------------------------

func (s *Server) bulkChange(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	b := crm.BulkSpec{Type: core.EntityType(r.FormValue("type")), IDs: r.Form["ids"], Stage: r.FormValue("stage"),
		AddTag: r.FormValue("add_tag"), AddType: r.FormValue("add_type")}
	switch o := strings.TrimSpace(r.FormValue("owner")); o {
	case "":
	case "none":
		empty := ""
		b.Owner = &empty
	case "me":
		b.Owner = &id.PrincipalID
	default:
		b.Owner = &o
	}
	res, err := desk.BulkApply(r.Context(), id.OrgID, id.Subjects, b)
	if err != nil {
		s.crmFail(w, r, err, "you cannot change those")
		return
	}
	back := localBack(r.FormValue("back"), "/deals")
	msg := fmt.Sprintf("Changed %d.", res.Done)
	if n := len(res.Failed); n > 0 {
		msg += fmt.Sprintf(" %d could not be changed.", n)
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"notice="+url.QueryEscape(msg), http.StatusSeeOther)
}

// -----------------------------------------------------------------------------
// Mail and meetings from a record, through the person's own account
// -----------------------------------------------------------------------------

// recordTargets is the records an email or a meeting about this one lands on: the record, and the company or contact
// it belongs with, so the same message shows on each timeline the way a call does.
func (s *Server) recordTargets(ctx context.Context, id *auth.Identity, ref core.Ref) []core.Ref {
	out := []core.Ref{ref}
	switch ref.Type {
	case core.EntityDeal:
		if d, err := s.sales.GetDeal(ctx, id.OrgID, id.Subjects, ref.ID); err == nil {
			if d.ContactID != nil {
				out = append(out, core.Ref{Type: core.EntityContact, ID: *d.ContactID})
			}
			if d.CompanyID != nil {
				out = append(out, core.Ref{Type: core.EntityCompany, ID: *d.CompanyID})
			}
		}
	case core.EntityContact:
		if c, err := s.crm.GetContact(ctx, id.OrgID, id.Subjects, ref.ID); err == nil && c.CompanyID != nil && *c.CompanyID != "" {
			out = append(out, core.Ref{Type: core.EntityCompany, ID: *c.CompanyID})
		}
	}
	return out
}

func (s *Server) mailPrecheck(w http.ResponseWriter, r *http.Request) (*auth.Identity, core.Ref, connect.Provider, bool) {
	var none core.Ref
	if _, ok := s.salesAccess(w, r); !ok {
		return nil, none, "", false
	}
	if !s.connectorsOn() {
		s.fail(w, r, http.StatusNotFound, "the connectors are not set up on this installation")
		return nil, none, "", false
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return nil, none, "", false
	}
	id := auth.From(r.Context())
	ref := core.Ref{Type: core.EntityType(r.FormValue("target_type")), ID: strings.TrimSpace(r.FormValue("target_id"))}
	if ref.Type != core.EntityCompany && ref.Type != core.EntityContact && ref.Type != core.EntityDeal || ref.ID == "" {
		s.fail(w, r, http.StatusBadRequest, "nothing to attach that to")
		return nil, none, "", false
	}
	if err := s.az.Require(r.Context(), id.Subjects, ref, core.AccessEdit); err != nil {
		s.fail(w, r, http.StatusNotFound, "no such thing")
		return nil, none, "", false
	}
	p, err := connect.ParseProvider(r.FormValue("provider"))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "choose Google or Microsoft")
		return nil, none, "", false
	}
	return id, ref, p, true
}

func (s *Server) connectFail(w http.ResponseWriter, r *http.Request, err error, what string) {
	switch {
	case errors.Is(err, connect.ErrNotConnected), errors.Is(err, connect.ErrReconnect), errors.Is(err, connect.ErrDisabled), errors.Is(err, connect.ErrScope):
		s.fail(w, r, http.StatusConflict, "Connect your account first: "+what+" needs it. Settings, Connectors.")
	default:
		s.log.WarnContext(r.Context(), "connector", "what", what, "error", err)
		s.fail(w, r, http.StatusBadGateway, "the provider would not do that: "+err.Error())
	}
}

func (s *Server) emailFromRecord(w http.ResponseWriter, r *http.Request) {
	id, ref, p, ok := s.mailPrecheck(w, r)
	if !ok {
		return
	}
	out := outgoingFrom(r)
	if len(out.To) == 0 || strings.TrimSpace(out.Body) == "" {
		s.fail(w, r, http.StatusBadRequest, "an email needs somebody to go to, and something to say")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.connect.SendMail(ctx, id.OrgID, id.PrincipalID, p, out); err != nil {
		s.connectFail(w, r, err, "sending mail")
		return
	}
	// Sent. Now it is on the timeline of the record, and of the company or person it belongs with.
	var targets []core.Ref
	for _, t := range s.recordTargets(ctx, id, ref) {
		if s.az.Require(ctx, id.Subjects, t, core.AccessEdit) == nil {
			targets = append(targets, t)
		}
	}
	_, err := s.sales.AddActivity(ctx, id.OrgID, id.Subjects, crm.Activity{Kind: crm.ActEmail, Title: out.Subject,
		Body: "To: " + strings.Join(out.To, ", ") + "\n\n" + out.Body, Direction: crm.Outbound, Targets: targets})
	if err != nil {
		s.log.WarnContext(ctx, "log sent email", "error", err)
	}
	http.Redirect(w, r, recordPath(ref)+"#timeline", http.StatusSeeOther)
}

func (s *Server) meetingFromRecord(w http.ResponseWriter, r *http.Request) {
	id, ref, p, ok := s.mailPrecheck(w, r)
	if !ok {
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	start, err := time.ParseInLocation("2006-01-02T15:04", strings.TrimSpace(r.FormValue("when")), time.Local)
	if title == "" || err != nil {
		s.fail(w, r, http.StatusBadRequest, "a meeting needs a title and a start time")
		return
	}
	mins, _ := strconv.Atoi(r.FormValue("minutes"))
	if mins <= 0 {
		mins = 30
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	ev, err := s.connect.CreateEvent(ctx, id.OrgID, id.PrincipalID, p, connect.Event{Title: title, Notes: r.FormValue("notes"),
		Place: strings.TrimSpace(r.FormValue("place")), Start: start, End: start.Add(time.Duration(mins) * time.Minute)}, true)
	if err != nil {
		s.connectFail(w, r, err, "booking a meeting")
		return
	}
	body := strings.TrimSpace(r.FormValue("notes"))
	if ev.JoinURL != "" {
		body += "\nJoin: " + ev.JoinURL
	}
	// The invitation goes by mail, because the calendar entry is the person's own.
	if to := addrs(r.FormValue("invite")); len(to) > 0 {
		text := fmt.Sprintf("%s\n\nWhen: %s (%d minutes)\n", title, start.Format("Monday 2 January 2006, 15:04 MST"), mins)
		if ev.JoinURL != "" {
			text += "Join: " + ev.JoinURL + "\n"
		}
		if pl := strings.TrimSpace(r.FormValue("place")); pl != "" {
			text += "Where: " + pl + "\n"
		}
		if err := s.connect.SendMail(ctx, id.OrgID, id.PrincipalID, p, connect.Outgoing{To: to, Subject: "Meeting: " + title, Body: text}); err != nil {
			s.log.WarnContext(ctx, "send meeting invitation", "error", err)
		} else {
			body += "\nInvited: " + strings.Join(to, ", ")
		}
	}
	ms := int64(mins) * 60000
	var targets []core.Ref
	for _, t := range s.recordTargets(ctx, id, ref) {
		if s.az.Require(ctx, id.Subjects, t, core.AccessEdit) == nil {
			targets = append(targets, t)
		}
	}
	if _, err := s.sales.AddActivity(ctx, id.OrgID, id.Subjects, crm.Activity{Kind: crm.ActMeeting, Title: title, Body: strings.TrimSpace(body),
		OccurredAt: start, DurationMs: &ms, Targets: targets}); err != nil {
		s.log.WarnContext(ctx, "log meeting", "error", err)
	}
	http.Redirect(w, r, recordPath(ref)+"#timeline", http.StatusSeeOther)
}

// -----------------------------------------------------------------------------
// Duplicates and merging
// -----------------------------------------------------------------------------

func (s *Server) duplicatesPage(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	typ := core.EntityContact
	if r.URL.Query().Get("type") == "company" {
		typ = core.EntityCompany
	}
	groups, err := desk.Duplicates(r.Context(), id.OrgID, id.Subjects, typ)
	if err != nil {
		s.log.ErrorContext(r.Context(), "find duplicates", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not look for duplicates")
		return
	}
	s.render(w, r, "duplicates", page{Title: "Duplicates", SalesOn: true, Desk: &deskData{Dups: groups, DupType: string(typ)}})
}

func (s *Server) mergeRecords(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	typ := core.EntityType(r.FormValue("type"))
	survivor := strings.TrimSpace(r.FormValue("survivor"))
	if err := desk.Merge(r.Context(), id.OrgID, id.Subjects, typ, survivor, r.Form["merge"]); err != nil {
		s.crmFail(w, r, err, "you cannot merge those")
		return
	}
	http.Redirect(w, r, recordPath(core.Ref{Type: typ, ID: survivor}), http.StatusSeeOther)
}

// -----------------------------------------------------------------------------
// A contact's place in Mautic
// -----------------------------------------------------------------------------

func (s *Server) mauticPanel(r *http.Request, ref core.Ref, p *page) (*crmmarketing.Panel, string) {
	if s.crmMkt == nil {
		return nil, ""
	}
	id := auth.From(r.Context())
	optedOut := p.Details != nil && p.Details.MarketingOptOutAt != nil
	panel, err := s.crmMkt.For(r.Context(), id.OrgID, ref.ID, optedOut)
	if errors.Is(err, crmmarketing.ErrNotLinked) {
		return nil, "This person is not in Mautic yet. The marketing sync adds anybody with an email address."
	}
	if err != nil {
		s.log.WarnContext(r.Context(), "load the Mautic panel", "error", err)
		return nil, ""
	}
	return panel, ""
}

func (s *Server) mauticMove(w http.ResponseWriter, r *http.Request, set func(ctx context.Context, org, contact string, n int64, on bool) error) {
	if _, ok := s.salesAccess(w, r); !ok {
		return
	}
	if s.crmMkt == nil {
		s.fail(w, r, http.StatusNotFound, "Mautic is not connected on this installation")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	id := auth.From(r.Context())
	ref := core.Ref{Type: core.EntityContact, ID: strings.TrimSpace(r.FormValue("contact_id"))}
	if err := s.az.Require(r.Context(), id.Subjects, ref, core.AccessEdit); err != nil {
		s.fail(w, r, http.StatusNotFound, "no such thing")
		return
	}
	n, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "which one?")
		return
	}
	switch err := set(r.Context(), id.OrgID, ref.ID, n, r.FormValue("on") != "0"); {
	case err == nil:
	case errors.Is(err, crmmarketing.ErrNotLinked), errors.Is(err, crmmarketing.ErrLocked):
		s.fail(w, r, http.StatusConflict, strings.TrimPrefix(err.Error(), "crmmarketing: "))
		return
	default:
		s.log.WarnContext(r.Context(), "move a contact in Mautic", "error", err)
		s.fail(w, r, http.StatusBadGateway, "Mautic would not do that: "+err.Error())
		return
	}
	http.Redirect(w, r, "/contacts/"+ref.ID+"#mautic", http.StatusSeeOther)
}

func (s *Server) mauticSegment(w http.ResponseWriter, r *http.Request) {
	s.mauticMove(w, r, func(ctx context.Context, org, c string, n int64, on bool) error {
		return s.crmMkt.SetSegment(ctx, org, c, n, on)
	})
}

func (s *Server) mauticCampaign(w http.ResponseWriter, r *http.Request) {
	s.mauticMove(w, r, func(ctx context.Context, org, c string, n int64, on bool) error {
		return s.crmMkt.SetCampaign(ctx, org, c, n, on)
	})
}

// formResponses is what this person answered on Formbricks forms, matched on their email addresses. A failure leaves it out.
func (s *Server) formResponses(r *http.Request, p *page) []crmforms.Entry {
	if s.crmForms == nil || p.Contact == nil {
		return nil
	}
	emails := []string{p.Contact.Email}
	if p.Details != nil {
		emails = append(emails, p.Details.ExtraEmails...)
	}
	got, err := s.crmForms.ForEmails(r.Context(), emails)
	if err != nil {
		s.log.WarnContext(r.Context(), "load form responses", "error", err)
		return nil
	}
	return got
}

// -----------------------------------------------------------------------------
// Editing a timeline entry
// -----------------------------------------------------------------------------

func (s *Server) editActivity(w http.ResponseWriter, r *http.Request) {
	desk, ok := s.salesAccess(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	e := crm.ActivityEdit{}
	set := func(name string) *string {
		if _, has := r.Form[name]; !has {
			return nil
		}
		v := r.FormValue(name)
		return &v
	}
	e.Title, e.Body, e.Direction, e.Disposition = set("title"), set("body"), set("direction"), set("disposition")
	if v := strings.TrimSpace(r.FormValue("when")); v != "" {
		t, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "that is not a date and time")
			return
		}
		e.OccurredAt = &t
	}
	if v := strings.TrimSpace(r.FormValue("minutes")); v != "" {
		m, err := strconv.ParseFloat(v, 64)
		if err != nil || m < 0 {
			s.fail(w, r, http.StatusBadRequest, "minutes is a number")
			return
		}
		ms := int64(m * 60000)
		e.DurationMs = &ms
	}
	if _, err := desk.UpdateActivity(r.Context(), id.OrgID, id.Subjects, r.PathValue("id"), e); err != nil {
		s.crmFail(w, r, err, "you cannot change that")
		return
	}
	http.Redirect(w, r, localBack(r.FormValue("back"), "/deals")+"#timeline", http.StatusSeeOther)
}

// mauticChoices is what the contacts list offers for enrolling people, for the Sales team only and only when Mautic is connected.
func (s *Server) mauticChoices(r *http.Request) *deskData {
	if s.crmMkt == nil || !s.salesNav(r) {
		return nil
	}
	ch, err := s.crmMkt.Choices(r.Context())
	if err != nil {
		s.log.WarnContext(r.Context(), "load the Mautic choices", "error", err)
		return nil
	}
	return &deskData{MauticChoices: ch}
}

func (s *Server) mauticEnrol(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.salesAccess(w, r); !ok {
		return
	}
	if s.crmMkt == nil {
		s.fail(w, r, http.StatusNotFound, "Mautic is not connected on this installation")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	id := auth.From(r.Context())
	ids := r.Form["ids"]
	if len(ids) == 0 || len(ids) > 200 {
		s.fail(w, r, http.StatusBadRequest, "tick between 1 and 200 people")
		return
	}
	kind, num, _ := strings.Cut(r.FormValue("target"), ":")
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "choose a segment or a campaign")
		return
	}
	var may []string
	for _, c := range ids {
		if s.az.Require(r.Context(), id.Subjects, core.Ref{Type: core.EntityContact, ID: strings.TrimSpace(c)}, core.AccessEdit) == nil {
			may = append(may, strings.TrimSpace(c))
		}
	}
	res, err := s.crmMkt.Enrol(r.Context(), id.OrgID, may, kind, n)
	switch {
	case err == nil:
	case errors.Is(err, crmmarketing.ErrLocked):
		s.fail(w, r, http.StatusConflict, strings.TrimPrefix(err.Error(), "crmmarketing: "))
		return
	case strings.Contains(err.Error(), "no such segment"), strings.Contains(err.Error(), "enrol into"):
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "crmmarketing: "))
		return
	default:
		s.log.WarnContext(r.Context(), "enrol in Mautic", "error", err)
		s.fail(w, r, http.StatusBadGateway, "Mautic would not do that: "+err.Error())
		return
	}
	back := localBack(r.FormValue("back"), "/contacts")
	msg := fmt.Sprintf("Enrolled %d.", res.Done)
	if res.NotInTool > 0 {
		msg += fmt.Sprintf(" %d are not in Mautic yet; the marketing sync adds them.", res.NotInTool)
	}
	if skipped := len(ids) - len(may); skipped > 0 {
		msg += fmt.Sprintf(" %d you cannot edit were left out.", skipped)
	}
	if len(res.Failed) > 0 {
		msg += fmt.Sprintf(" Mautic would not take %d.", len(res.Failed))
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"notice="+url.QueryEscape(msg), http.StatusSeeOther)
}

// -----------------------------------------------------------------------------
// Custom properties
// -----------------------------------------------------------------------------

func (s *Server) propsFor(w http.ResponseWriter, r *http.Request) (crm.Props, bool) {
	if _, ok := s.salesAccess(w, r); !ok {
		return nil, false
	}
	p, ok := s.sales.(crm.Props)
	if !ok {
		s.fail(w, r, http.StatusNotFound, "custom fields are not available on this deployment")
		return nil, false
	}
	return p, true
}

func propKind(v string) core.EntityType {
	switch v {
	case "company":
		return core.EntityCompany
	case "deal":
		return core.EntityDeal
	}
	return core.EntityContact
}

func (s *Server) propertiesPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.propsFor(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	kind := propKind(r.URL.Query().Get("type"))
	defs, err := p.PropertyDefs(r.Context(), id.OrgID, kind)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not load the custom fields")
		return
	}
	s.render(w, r, "properties", page{Title: "Custom fields", SalesOn: true,
		Desk: &deskData{PropDefs: defs, PropKind: string(kind), PropTypes: crm.PropertyTypes, CanManage: id.Subjects.Super()}})
}

func (s *Server) saveProperty(w http.ResponseWriter, r *http.Request) {
	p, ok := s.propsFor(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	var opts []string
	for _, o := range strings.Split(r.FormValue("options"), "\n") {
		opts = append(opts, o)
	}
	kind := propKind(r.FormValue("type"))
	_, err := p.SaveProperty(r.Context(), id.OrgID, id.Subjects, crm.PropertyInput{ID: strings.TrimSpace(r.FormValue("id")), Kind: kind,
		Key: r.FormValue("key"), Label: r.FormValue("label"), DataType: r.FormValue("data_type"), Options: opts})
	if err != nil {
		s.crmFail(w, r, err, "only an admin defines custom fields")
		return
	}
	http.Redirect(w, r, "/crm/properties?type="+string(kind), http.StatusSeeOther)
}

func (s *Server) deleteProperty(w http.ResponseWriter, r *http.Request) {
	p, ok := s.propsFor(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	_ = r.ParseForm()
	kind := propKind(r.FormValue("type"))
	if err := p.DeleteProperty(r.Context(), id.OrgID, id.Subjects, kind, r.PathValue("id")); err != nil {
		s.crmFail(w, r, err, "only an admin removes custom fields")
		return
	}
	http.Redirect(w, r, "/crm/properties?type="+string(kind), http.StatusSeeOther)
}

// setPropertyValues saves the custom fields form on a record: every field named p_<key> is set, and an empty one is cleared.
func (s *Server) setPropertyValues(w http.ResponseWriter, r *http.Request) {
	p, ok := s.propsFor(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	ref := core.Ref{Type: core.EntityType(r.FormValue("target_type")), ID: strings.TrimSpace(r.FormValue("target_id"))}
	if !(ref.Type == core.EntityCompany || ref.Type == core.EntityContact || ref.Type == core.EntityDeal) || ref.ID == "" {
		s.fail(w, r, http.StatusBadRequest, "nothing to attach that to")
		return
	}
	values := map[string]any{}
	for k, v := range r.Form {
		if strings.HasPrefix(k, "p_") && len(v) > 0 {
			values[strings.TrimPrefix(k, "p_")] = v[0]
		}
	}
	if err := p.SetProperties(r.Context(), id.OrgID, id.Subjects, ref, values); err != nil {
		s.crmFail(w, r, err, "you cannot change that")
		return
	}
	http.Redirect(w, r, recordPath(ref)+"#fields", http.StatusSeeOther)
}
