package web

// The sales side of the CRM on screen: the pipeline, the deal page, the
// timeline of notes and calls, owners, and the sorted, filtered, paged lists
// with a CSV of whatever the list is showing.

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
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/crm/crmviews"
)

// dealCol is one column of the pipeline.
type dealCol struct {
	Stage string
	Label string
	Deals []*crm.Deal
	Total int64
	Count int
}

// pager is the footer of a paged list.
type pager struct {
	From, To, Total int
	Prev, Next      string
}

const pageSize = 50

func (s *Server) salesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /deals", s.listDeals)
	mux.HandleFunc("GET /deals.csv", s.dealsCSV)
	mux.HandleFunc("POST /deals", s.createDeal)
	mux.HandleFunc("GET /deals/{id}", s.getDeal)
	mux.HandleFunc("POST /deals/{id}", s.updateDeal)
	mux.HandleFunc("POST /deals/{id}/move", s.moveDeal)
	mux.HandleFunc("POST /deals/{id}/delete", s.deleteDeal)
	mux.HandleFunc("POST /activities", s.addActivity)
	mux.HandleFunc("POST /activities/{id}/delete", s.deleteActivity)
	mux.HandleFunc("POST /companies/{id}/sales", s.saveCompanyDetails)
	mux.HandleFunc("POST /contacts/{id}/sales", s.saveContactDetails)
	mux.HandleFunc("GET /crm/types", s.companyTypesPage)
	mux.HandleFunc("POST /crm/types", s.saveCompanyType)
	mux.HandleFunc("GET /crm/unassigned", s.unassignedQueue)
	mux.HandleFunc("POST /crm/owner", s.setOwner)
	mux.HandleFunc("GET /companies.csv", s.companiesCSV)
	mux.HandleFunc("GET /contacts.csv", s.contactsCSV)
}

// salesOn answers 404 on a deployment with no sales side, rather than letting a
// nil service panic in a handler.
func (s *Server) salesOn(w http.ResponseWriter, r *http.Request) bool {
	if s.sales == nil {
		s.fail(w, r, http.StatusNotFound, "the sales pages are not available on this deployment")
		return false
	}
	return true
}

func (s *Server) owners(r *http.Request) []auth.Person {
	id := auth.From(r.Context())
	if s.auth == nil || id == nil {
		return nil
	}
	list, err := s.auth.People(r.Context(), id.OrgID)
	if err != nil {
		s.log.WarnContext(r.Context(), "load owners", "error", err)
		return nil
	}
	out := list[:0:0]
	for _, p := range list {
		if p.Status == "" || p.Status == "active" {
			out = append(out, p)
		}
	}
	return out
}

// listQueryOf reads the filters, sort and page off the address bar.
func listQueryOf(r *http.Request) crm.ListQuery {
	v := r.URL.Query()
	page, _ := strconv.Atoi(v.Get("page"))
	if page < 1 {
		page = 1
	}
	day := func(k string) *time.Time {
		d, err := time.ParseInLocation("2006-01-02", v.Get(k), time.Local)
		if err != nil {
			return nil
		}
		return &d
	}
	return crm.ListQuery{
		Search: v.Get("q"), Kind: v.Get("kind"), Stage: v.Get("stage"), Owner: v.Get("owner"),
		Type: v.Get("type"), Tag: v.Get("tag"), PropKey: v.Get("pk"), PropValue: v.Get("pv"),
		Open: v.Get("open") == "1", Overdue: v.Get("overdue") == "1",
		ClosedFrom: day("closedfrom"), ClosedTo: day("closedto"),
		Sort: v.Get("sort"), Desc: v.Get("desc") == "1", Group: v.Get("group"),
		Limit: pageSize, Offset: (page - 1) * pageSize,
	}
}

// with returns the current address with some parameters changed.
func with(r *http.Request, set map[string]string) string {
	q := url.Values{}
	for k, vs := range r.URL.Query() {
		q[k] = vs
	}
	for k, v := range set {
		if v == "" {
			q.Del(k)
		} else {
			q.Set(k, v)
		}
	}
	enc := q.Encode()
	if enc == "" {
		return r.URL.Path
	}
	return r.URL.Path + "?" + enc
}

// exportLink is the CSV of whatever the list is showing: the same filters and
// sort, every page.
func exportLink(r *http.Request, path string) string {
	q := url.Values{}
	for k, vs := range r.URL.Query() {
		if k != "page" && k != "view" {
			q[k] = vs
		}
	}
	if enc := q.Encode(); enc != "" {
		return path + "?" + enc
	}
	return path
}

func sortLinks(r *http.Request, cols ...string) map[string]string {
	cur, desc := r.URL.Query().Get("sort"), r.URL.Query().Get("desc") == "1"
	out := map[string]string{}
	for _, c := range cols {
		d := ""
		if cur == c && !desc {
			d = "1"
		}
		out[c] = with(r, map[string]string{"sort": c, "desc": d, "page": ""})
	}
	return out
}

func pagerFor(r *http.Request, q crm.ListQuery, n, total int) *pager {
	p := &pager{Total: total}
	if total > 0 {
		p.From, p.To = q.Offset+1, q.Offset+n
	}
	page := q.Offset/pageSize + 1
	if page > 1 {
		p.Prev = with(r, map[string]string{"page": strconv.Itoa(page - 1)})
	}
	if q.Offset+n < total {
		p.Next = with(r, map[string]string{"page": strconv.Itoa(page + 1)})
	}
	return p
}

// -----------------------------------------------------------------------------
// Companies and contacts, paged
// -----------------------------------------------------------------------------

func (s *Server) companiesPage(w http.ResponseWriter, r *http.Request) bool {
	if s.sales == nil {
		return false
	}
	id := auth.From(r.Context())
	q := listQueryOf(r)
	rows, total, err := s.sales.CompaniesPage(r.Context(), id.OrgID, id.Subjects, q)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list companies", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the companies")
		return true
	}
	types, _ := s.sales.Types(r.Context(), id.OrgID, false)
	tagList, _ := s.sales.TagsInUse(r.Context(), id.OrgID, id.Subjects)
	admin, _ := s.auth.IsAdmin(r.Context(), id.OrgID, id.PrincipalID)
	var extras page
	s.groupAndViews(r, id, "companies", q, len(rows), func(i int, key string) string { return crm.CompanyGroup(rows[i], key) }, &extras)
	s.render(w, r, "companies", page{
		CRMGroup: extras.CRMGroup, CRMGroupOptions: extras.CRMGroupOptions, CRMRowGroups: extras.CRMRowGroups, CRMGroupCounts: extras.CRMGroupCounts,
		CRMViews: extras.CRMViews, CRMViewsOn: extras.CRMViewsOn, ListFrom: extras.ListFrom, Me: extras.Me,
		CompanyTypes: types, TypeSel: q.Type, TagSel: q.Tag, TagList: tagList, CanManageTypes: admin,
		Title: "Companies", SalesOn: true, CompanyRows: rows, Filter: q.Kind, Desk: s.listFields(r, core.EntityCompany, nil),
		SearchText: q.Search, CompanyKinds: crm.Kinds, Owners: s.owners(r), OwnerSel: q.Owner,
		Sort: q.Sort, SortLinks: sortLinks(r, "name", "kind", "owner", "revenue", "contacts", "deals", "updated"),
		Pager: pagerFor(r, q, len(rows), total), ExportURL: exportLink(r, "/companies.csv"),
	})
	return true
}

func (s *Server) contactsPage(w http.ResponseWriter, r *http.Request) bool {
	if s.sales == nil {
		return false
	}
	id := auth.From(r.Context())
	q := listQueryOf(r)
	rows, total, err := s.sales.ContactsPage(r.Context(), id.OrgID, id.Subjects, q)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list contacts", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the contacts")
		return true
	}
	// The add form's company picker is capped: 6,000 options in a <select> is a
	// page that hangs a phone.
	companies, _, _ := s.sales.CompaniesPage(r.Context(), id.OrgID, id.Subjects,
		crm.ListQuery{Limit: 300})
	var picker []*crm.Company
	for _, c := range companies {
		c := c.Company
		picker = append(picker, &c)
	}
	var extras page
	s.groupAndViews(r, id, "contacts", q, len(rows), func(i int, key string) string { return crm.ContactGroup(rows[i], key) }, &extras)
	s.render(w, r, "contacts", page{
		CRMGroup: extras.CRMGroup, CRMGroupOptions: extras.CRMGroupOptions, CRMRowGroups: extras.CRMRowGroups, CRMGroupCounts: extras.CRMGroupCounts,
		CRMViews: extras.CRMViews, CRMViewsOn: extras.CRMViewsOn, ListFrom: extras.ListFrom, Me: extras.Me,
		Title: "Contacts", SalesOn: true, ContactRows: rows, Companies: picker,
		SearchText: q.Search, Owners: s.owners(r), OwnerSel: q.Owner, Sort: q.Sort,
		SortLinks: sortLinks(r, "name", "company", "title", "email", "owner", "updated"),
		Pager:     pagerFor(r, q, len(rows), total), ExportURL: exportLink(r, "/contacts.csv"),
		Desk: s.listFields(r, core.EntityContact, s.mauticChoices(r)),
	})
	return true
}

func (s *Server) csvHead(w http.ResponseWriter, name string) *csv.Writer {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`-`+time.Now().Format("2006-01-02")+`.csv"`)
	return csv.NewWriter(w)
}

// safeCell stops a spreadsheet running a cell as a formula. A name that starts
// with = or + is an attack, not a name.
func safeCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

func (s *Server) companiesCSV(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	q := listQueryOf(r)
	q.Limit, q.Offset = 5000, 0
	rows, _, err := s.sales.CompaniesPage(r.Context(), id.OrgID, id.Subjects, q)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not export the companies")
		return
	}
	cw := s.csvHead(w, "companies")
	ids := make([]string, len(rows))
	for i, c := range rows {
		ids[i] = c.ID
	}
	extra := s.exportFields(r, core.EntityCompany, ids)
	_ = cw.Write(append([]string{"Name", "Kind", "Domain", "Website", "Owner", "City", "State", "Annual revenue", "People", "Deals", "Types", "Tags", "Notes", "Created"}, extra.header()...))
	for _, c := range rows {
		rev := ""
		if c.RevenueCents != nil {
			rev = crm.Money(*c.RevenueCents, c.Currency)
		}
		_ = cw.Write(append([]string{safeCell(c.Name), c.Kind, c.Domain, safeCell(c.Website), c.OwnerName, c.City, c.State,
			rev, strconv.Itoa(c.Contacts), strconv.Itoa(c.Deals), strings.Join(c.TypeLabels, "; "), safeCell(strings.Join(c.Tags, "; ")), safeCell(c.Notes), c.CreatedAt.Format("2006-01-02")}, extra.cells(c.ID)...))
	}
	cw.Flush()
}

func (s *Server) contactsCSV(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	q := listQueryOf(r)
	q.Limit, q.Offset = 5000, 0
	rows, _, err := s.sales.ContactsPage(r.Context(), id.OrgID, id.Subjects, q)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not export the contacts")
		return
	}
	cw := s.csvHead(w, "contacts")
	ids := make([]string, len(rows))
	for i, c := range rows {
		ids[i] = c.ID
	}
	extra := s.exportFields(r, core.EntityContact, ids)
	_ = cw.Write(append([]string{"Name", "Company", "Title", "Email", "Phone", "Owner", "LinkedIn", "Notes", "Created"}, extra.header()...))
	for _, c := range rows {
		_ = cw.Write(append([]string{safeCell(c.Name), safeCell(c.CompanyName), c.Title, c.Email, c.Phone, c.OwnerName,
			c.LinkedIn, safeCell(c.Notes), c.CreatedAt.Format("2006-01-02")}, extra.cells(c.ID)...))
	}
	cw.Flush()
}

// saveCompanyDetails and saveContactDetails write the sales columns, from a
// form of their own so that the existing form keeps saving exactly what it did.
func (s *Server) saveCompanyDetails(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	d := crm.Details{
		OwnerID: strings.TrimSpace(r.FormValue("owner")), Website: r.FormValue("website"),
		LinkedIn: r.FormValue("linkedin"), TaxID: r.FormValue("tax_id"), Street: r.FormValue("street"), City: r.FormValue("city"),
		State: r.FormValue("state"), Postcode: r.FormValue("postcode"), Country: r.FormValue("country"),
		Currency: "USD",
	}
	if v := strings.TrimSpace(r.FormValue("revenue")); v != "" {
		c := int64(parseMoney(v))
		d.RevenueCents = &c
	}
	cid := r.PathValue("id")
	// A form from a page without the tax ID field must not blank it.
	if _, sent := r.Form["tax_id"]; !sent {
		if cur, err := s.sales.CompanyDetails(r.Context(), id.OrgID, id.Subjects, cid); err == nil {
			d.TaxID = cur.TaxID
		}
	}
	if err := s.sales.SetCompanyDetails(r.Context(), id.OrgID, id.Subjects, cid, d); err != nil {
		s.crmFail(w, r, err, "you cannot change that company")
		return
	}
	// The types and tags ride the same form. A marker says they were on the
	// page, so a form from an older page cannot clear them by omission.
	if r.FormValue("labels") == "1" {
		keys := r.Form["types"]
		var tags []string
		for _, t := range strings.FieldsFunc(r.FormValue("tags"), func(c rune) bool { return c == ',' || c == '\n' }) {
			tags = append(tags, t)
		}
		if err := s.sales.SetCompanyLabels(r.Context(), id.OrgID, id.Subjects, cid, &keys, &tags); err != nil {
			s.crmFail(w, r, err, "you cannot change that company")
			return
		}
	}
	http.Redirect(w, r, "/companies/"+cid, http.StatusSeeOther)
}

func (s *Server) saveContactDetails(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	lines := func(v string) []string {
		var out []string
		for _, l := range strings.Split(v, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		return out
	}
	cid := r.PathValue("id")
	if err := s.sales.SetContactDetails(r.Context(), id.OrgID, id.Subjects, cid, crm.Details{
		OwnerID: strings.TrimSpace(r.FormValue("owner")), LinkedIn: r.FormValue("linkedin"),
		ExtraEmails: lines(r.FormValue("extra_emails")), ExtraPhones: lines(r.FormValue("extra_phones")),
	}); err != nil {
		s.crmFail(w, r, err, "you cannot change that contact")
		return
	}
	http.Redirect(w, r, "/contacts/"+cid, http.StatusSeeOther)
}

// salesFor loads what the company and contact pages show beside their forms: the
// sales columns, the deals, and the timeline.
func (s *Server) salesFor(r *http.Request, ref core.Ref, p *page) {
	if s.sales == nil {
		return
	}
	id := auth.From(r.Context())
	p.SalesOn = true
	p.Owners = s.owners(r)
	var err error
	if ref.Type == core.EntityCompany {
		p.Details, err = s.sales.CompanyDetails(r.Context(), id.OrgID, id.Subjects, ref.ID)
	} else {
		p.Details, err = s.sales.ContactDetails(r.Context(), id.OrgID, id.Subjects, ref.ID)
	}
	if err != nil {
		s.log.WarnContext(r.Context(), "load sales details", "error", err)
		p.Details = &crm.Details{}
	}
	if ref.Type == core.EntityCompany {
		if p.Labels, err = s.sales.CompanyLabels(r.Context(), id.OrgID, id.Subjects, ref.ID); err != nil {
			s.log.WarnContext(r.Context(), "load company labels", "error", err)
			p.Labels = &crm.Labels{}
		}
		p.CompanyTypes, _ = s.sales.Types(r.Context(), id.OrgID, false)
		p.TagList, _ = s.sales.TagsInUse(r.Context(), id.OrgID, id.Subjects)
	}
	if p.Deals, err = s.sales.DealsFor(r.Context(), id.OrgID, id.Subjects, ref); err != nil {
		s.log.WarnContext(r.Context(), "load deals", "error", err)
	}
	if p.Activities, err = s.sales.ActivitiesFor(r.Context(), id.OrgID, id.Subjects, ref); err != nil {
		s.log.WarnContext(r.Context(), "load timeline", "error", err)
	}
	p.DealStages = crm.Stages
	p.EmberOps = s.emberOpsLinks(r, ref)
	s.markFavorite(r, ref, p)
	s.deskFor(r, ref, p)
}

// -----------------------------------------------------------------------------
// Deals
// -----------------------------------------------------------------------------

func (s *Server) dealForm(r *http.Request) (crm.Deal, error) {
	if err := r.ParseForm(); err != nil {
		return crm.Deal{}, err
	}
	d := crm.Deal{
		Name: r.FormValue("name"), Stage: r.FormValue("stage"), Currency: "USD",
		Notes: r.FormValue("notes"), AmountCents: int64(parseMoney(r.FormValue("amount"))),
	}
	if v := strings.TrimSpace(r.FormValue("company")); v != "" {
		d.CompanyID = &v
	}
	if v := strings.TrimSpace(r.FormValue("contact")); v != "" {
		d.ContactID = &v
	}
	if v := strings.TrimSpace(r.FormValue("owner")); v != "" {
		d.OwnerID = &v
	}
	if v := strings.TrimSpace(r.FormValue("close")); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			d.CloseDate = &t
		}
	}
	return d, nil
}

func (s *Server) listDeals(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	q := listQueryOf(r)
	mode := r.URL.Query().Get("view")
	p := page{Title: "Deals", SalesOn: true, SearchText: q.Search, Owners: s.owners(r),
		OwnerSel: q.Owner, DealStages: crm.Stages, DealStage: q.Stage, ViewMode: mode,
		ExportURL: exportLink(r, "/deals.csv")}
	if mode == "table" {
		rows, total, err := s.sales.Deals(r.Context(), id.OrgID, id.Subjects, q)
		if err != nil {
			s.log.ErrorContext(r.Context(), "list deals", "error", err)
			s.fail(w, r, http.StatusInternalServerError, "could not load the deals")
			return
		}
		p.Deals, p.Sort = rows, q.Sort
		s.groupAndViews(r, id, "deals", q, len(rows), func(i int, key string) string { return crm.DealGroup(rows[i], key) }, &p)
		p.SortLinks = sortLinks(r, "name", "stage", "amount", "close", "company", "owner", "updated")
		p.Pager = pagerFor(r, q, len(rows), total)
	} else {
		p.ViewMode = "board"
		q.Limit, q.Offset, q.Sort, q.Stage, q.Group = 5000, 0, "position", "", ""
		s.groupAndViews(r, id, "deals", q, 0, nil, &p)
		rows, _, err := s.sales.Deals(r.Context(), id.OrgID, id.Subjects, q)
		if err != nil {
			s.log.ErrorContext(r.Context(), "list deals", "error", err)
			s.fail(w, r, http.StatusInternalServerError, "could not load the deals")
			return
		}
		cols := map[string]*dealCol{}
		for _, st := range crm.Stages {
			c := &dealCol{Stage: st, Label: crm.StageLabel(st)}
			cols[st] = c
		}
		for _, d := range rows {
			c := cols[d.Stage]
			c.Deals = append(c.Deals, d)
			c.Total += d.AmountCents
			c.Count++
		}
		for _, st := range crm.Stages {
			p.DealCols = append(p.DealCols, *cols[st])
		}
	}
	companies, _, _ := s.sales.CompaniesPage(r.Context(), id.OrgID, id.Subjects, crm.ListQuery{Limit: 300})
	for _, c := range companies {
		c := c.Company
		p.Companies = append(p.Companies, &c)
	}
	p.Desk = s.listFields(r, core.EntityDeal, nil)
	s.render(w, r, "deals", p)
}

func (s *Server) dealsCSV(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	q := listQueryOf(r)
	q.Limit, q.Offset = 5000, 0
	rows, _, err := s.sales.Deals(r.Context(), id.OrgID, id.Subjects, q)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not export the deals")
		return
	}
	cw := s.csvHead(w, "deals")
	ids := make([]string, len(rows))
	for i, d := range rows {
		ids[i] = d.ID
	}
	extra := s.exportFields(r, core.EntityDeal, ids)
	_ = cw.Write(append([]string{"Deal", "Stage", "Amount", "Close date", "Company", "Contact", "Owner", "Created"}, extra.header()...))
	for _, d := range rows {
		cd := ""
		if d.CloseDate != nil {
			cd = d.CloseDate.Format("2006-01-02")
		}
		_ = cw.Write(append([]string{safeCell(d.Name), crm.StageLabel(d.Stage), fmt.Sprintf("%.2f", float64(d.AmountCents)/100),
			cd, safeCell(d.CompanyName), safeCell(d.ContactName), d.OwnerName, d.CreatedAt.Format("2006-01-02")}, extra.cells(d.ID)...))
	}
	cw.Flush()
}

func (s *Server) createDeal(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	d, err := s.dealForm(r)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	made, err := s.sales.CreateDeal(r.Context(), id.OrgID, id.Subjects, d)
	if err != nil {
		s.crmFail(w, r, err, "you cannot add a deal")
		return
	}
	http.Redirect(w, r, "/deals/"+made.ID, http.StatusSeeOther)
}

func (s *Server) getDeal(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	d, err := s.sales.GetDeal(r.Context(), id.OrgID, id.Subjects, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "no such deal")
		return
	}
	p := page{Title: d.Name, SalesOn: true, Deal: d, Owners: s.owners(r), DealStages: crm.Stages}
	s.markFavorite(r, d.Ref(), &p)
	p.Activities, _ = s.sales.ActivitiesFor(r.Context(), id.OrgID, id.Subjects, d.Ref())
	p.EmberOps = s.emberOpsLinks(r, d.Ref())
	s.deskFor(r, d.Ref(), &p)
	companies, _, _ := s.sales.CompaniesPage(r.Context(), id.OrgID, id.Subjects, crm.ListQuery{Limit: 300})
	for _, c := range companies {
		c := c.Company
		p.Companies = append(p.Companies, &c)
	}
	if d.CompanyID != nil {
		p.Contacts, _ = s.crm.Contacts(r.Context(), id.OrgID, id.Subjects, *d.CompanyID, "")
	}
	s.render(w, r, "deal", p)
}

func (s *Server) updateDeal(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	d, err := s.dealForm(r)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	d.ID = r.PathValue("id")
	if err := s.sales.UpdateDeal(r.Context(), id.OrgID, id.Subjects, d); err != nil {
		s.crmFail(w, r, err, "you cannot change that deal")
		return
	}
	if _, sent := r.Form["lost_reason"]; sent {
		if desk, ok := s.sales.(crm.Desk); ok {
			if err := desk.SetLostReason(r.Context(), id.OrgID, id.Subjects, d.ID, r.FormValue("lost_reason")); err != nil {
				s.crmFail(w, r, err, "you cannot change that deal")
				return
			}
		}
	}
	http.Redirect(w, r, "/deals/"+d.ID, http.StatusSeeOther)
}

// moveDeal is the write behind dragging a card: field and value are what the
// shared board script sends, and here the field is always the stage.
func (s *Server) moveDeal(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	stage := r.FormValue("value")
	if stage == "" {
		stage = r.FormValue("stage")
	}
	var before *string
	if v := strings.TrimSpace(r.FormValue("before")); v != "" {
		before = &v
	}
	if err := s.sales.MoveDeal(r.Context(), id.OrgID, id.Subjects, r.PathValue("id"), stage, before); err != nil {
		s.crmFail(w, r, err, "you cannot move that deal")
		return
	}
	if r.Header.Get("X-Requested-With") != "" || strings.Contains(r.Header.Get("Content-Type"), "urlencoded") && r.FormValue("field") != "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/deals", http.StatusSeeOther)
}

func (s *Server) deleteDeal(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := s.sales.DeleteDeal(r.Context(), id.OrgID, id.Subjects, r.PathValue("id")); err != nil {
		s.crmFail(w, r, err, "you cannot remove that deal")
		return
	}
	http.Redirect(w, r, "/deals", http.StatusSeeOther)
}

// -----------------------------------------------------------------------------
// The timeline
// -----------------------------------------------------------------------------

func (s *Server) addActivity(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	typ, tid := r.FormValue("target_type"), strings.TrimSpace(r.FormValue("target_id"))
	back := map[string]string{"company": "/companies/", "contact": "/contacts/", "deal": "/deals/"}[typ]
	if back == "" || tid == "" {
		s.fail(w, r, http.StatusBadRequest, "nothing to attach that to")
		return
	}
	a := crm.Activity{
		Kind: r.FormValue("kind"), Title: r.FormValue("title"), Body: r.FormValue("body"),
		Direction: r.FormValue("direction"), Disposition: r.FormValue("disposition"),
		Targets: []core.Ref{{Type: core.EntityType(typ), ID: tid}},
	}
	if v := strings.TrimSpace(r.FormValue("when")); v != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local); err == nil {
			a.OccurredAt = t
		}
	}
	if v := strings.TrimSpace(r.FormValue("minutes")); v != "" {
		if m, err := strconv.ParseFloat(v, 64); err == nil && m >= 0 {
			ms := int64(m * 60000)
			a.DurationMs = &ms
		}
	}
	// "Also about": other companies named in the form, by name or domain, comma
	// separated. A name that matches nothing, or more than one, is refused
	// rather than guessed at, so the note never lands on the wrong firm.
	for _, name := range strings.Split(r.FormValue("also_company"), ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		rows, _, err := s.sales.CompaniesPage(r.Context(), id.OrgID, id.Subjects, crm.ListQuery{Search: name, Limit: 10})
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "could not look up "+name)
			return
		}
		var hit *crm.CompanyRow
		for _, row := range rows {
			if strings.EqualFold(row.Name, name) || strings.EqualFold(row.Domain, name) {
				if hit != nil {
					hit = nil
					break
				}
				hit = row
			}
		}
		if hit == nil && len(rows) == 1 {
			hit = rows[0]
		}
		if hit == nil {
			s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("%q does not match exactly one company you can see. Use the full name or the domain.", name))
			return
		}
		a.Targets = append(a.Targets, core.Ref{Type: core.EntityCompany, ID: hit.ID})
	}
	if _, err := s.sales.AddActivity(r.Context(), id.OrgID, id.Subjects, a); err != nil {
		s.crmFail(w, r, err, "you cannot write on that")
		return
	}
	http.Redirect(w, r, back+tid+"#timeline", http.StatusSeeOther)
}

func (s *Server) deleteActivity(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := s.sales.DeleteActivity(r.Context(), id.OrgID, id.Subjects, r.PathValue("id")); err != nil {
		s.crmFail(w, r, err, "you cannot remove that")
		return
	}
	back := r.Header.Get("Referer")
	if u, err := url.Parse(back); err != nil || u.Path == "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		back = "/deals"
	} else {
		back = u.Path + "#timeline"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// unassignedQueue is the list of records nobody owns, with a button to claim
// each and a picker to hand it to somebody else.
func (s *Server) unassignedQueue(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.salesAccess(w, r); !ok {
		return
	}
	id := auth.From(r.Context())
	typ := core.EntityType(r.URL.Query().Get("type"))
	if typ != core.EntityContact && typ != core.EntityDeal {
		typ = core.EntityCompany
	}
	q := listQueryOf(r)
	rows, total, err := s.sales.Unassigned(r.Context(), id.OrgID, id.Subjects, typ, q)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list unassigned", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the queue")
		return
	}
	counts, _ := s.sales.UnassignedCounts(r.Context(), id.OrgID, id.Subjects)
	s.render(w, r, "unassigned", page{
		Title: "Unassigned", SalesOn: true, Unowned: rows, UnownedType: string(typ),
		UnownedCounts: map[string]int{"company": counts[core.EntityCompany],
			"contact": counts[core.EntityContact], "deal": counts[core.EntityDeal]},
		Owners: s.owners(r), SearchText: q.Search, Pager: pagerFor(r, q, len(rows), total),
	})
}

// setOwner claims (owner=me), hands off (owner=<principal>), or clears (empty).
func (s *Server) setOwner(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	owner := strings.TrimSpace(r.FormValue("owner"))
	if owner == "me" {
		owner = id.PrincipalID
	}
	ref := core.Ref{Type: core.EntityType(r.FormValue("type")), ID: strings.TrimSpace(r.FormValue("id"))}
	if err := s.sales.SetOwner(r.Context(), id.OrgID, id.Subjects, ref, owner); err != nil {
		s.crmFail(w, r, err, "you cannot change that owner")
		return
	}
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/crm/unassigned"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// companyTypesPage is the list of company types. Anybody in sales may read it; an
// admin edits it.

func (s *Server) companyTypesPage(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	types, err := s.sales.Types(r.Context(), id.OrgID, true)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not load the types")
		return
	}
	admin, _ := s.auth.IsAdmin(r.Context(), id.OrgID, id.PrincipalID)
	s.render(w, r, "companytypes", page{Title: "Company types", SalesOn: true, CompanyTypes: types, CanManageTypes: admin})
}

func (s *Server) saveCompanyType(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	admin, _ := s.auth.IsAdmin(r.Context(), id.OrgID, id.PrincipalID)
	if !admin {
		s.fail(w, r, http.StatusForbidden, "only an admin edits the list of company types")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	pos, _ := strconv.Atoi(r.FormValue("position"))
	t := crm.CompanyType{
		ID: strings.TrimSpace(r.FormValue("id")), Label: r.FormValue("label"),
		ERPRole: r.FormValue("erp_role"), Position: pos, Archived: r.FormValue("archived") == "1",
	}
	if _, err := s.sales.SaveType(r.Context(), id.OrgID, t); err != nil {
		s.crmFail(w, r, err, "you cannot change that")
		return
	}
	http.Redirect(w, r, "/crm/types", http.StatusSeeOther)
}

// groupAndViews fills what the CRM lists share: the grouping choices and, when one is chosen, each row's heading and each
// heading's total across the whole list, and the saved views of the list with the address a new one would save.
func (s *Server) groupAndViews(r *http.Request, id *auth.Identity, list string, q crm.ListQuery, n int,
	label func(i int, key string) string, p *page) {

	p.CRMGroupOptions = crm.GroupOptions(list)
	for _, g := range p.CRMGroupOptions {
		if g.Key == q.Group && label != nil {
			p.CRMGroup = g.Key
		}
	}
	if p.CRMGroup != "" {
		for i := 0; i < n; i++ {
			p.CRMRowGroups = append(p.CRMRowGroups, label(i, p.CRMGroup))
		}
		counts, err := s.sales.GroupCounts(r.Context(), id.OrgID, id.Subjects, list, q)
		if err != nil {
			s.log.WarnContext(r.Context(), "group totals", "list", list, "error", err)
		}
		p.CRMGroupCounts = counts
	}
	if s.crmViews != nil {
		views, err := s.crmViews.List(r.Context(), id.OrgID, id.PrincipalID, list)
		if err != nil {
			s.log.WarnContext(r.Context(), "list crm views", "list", list, "error", err)
		}
		p.CRMViews, p.CRMViewsOn, p.ListFrom, p.Me = views, true, r.URL.RequestURI(), id.PrincipalID
	}
}

// saveCRMView names the CRM list somebody is looking at. The query is read from the address of the list, as the document
// views do, so what is saved is exactly what is on screen.
func (s *Server) saveCRMView(w http.ResponseWriter, r *http.Request) {
	if s.crmViews == nil {
		s.fail(w, r, http.StatusNotFound, "views are not available on this deployment")
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	list, query, err := crmviews.Clean(r.FormValue("from"))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "could not read the list that was being saved")
		return
	}
	v, err := s.crmViews.Save(r.Context(), id.OrgID, id.PrincipalID, crmviews.View{
		List: list, Name: r.FormValue("name"), Query: query, Shared: r.FormValue("shared") != "",
	})
	switch {
	case errors.Is(err, crmviews.ErrName):
		s.fail(w, r, http.StatusBadRequest, "a view needs a name")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "save crm view", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not save that view")
		return
	}
	http.Redirect(w, r, v.Path(), http.StatusSeeOther)
}

func (s *Server) deleteCRMView(w http.ResponseWriter, r *http.Request) {
	if s.crmViews == nil {
		s.fail(w, r, http.StatusNotFound, "views are not available on this deployment")
		return
	}
	id := auth.From(r.Context())
	back := "/companies"
	if list, _, err := crmviews.Clean(r.FormValue("from")); err == nil {
		back = "/" + list
	}
	switch err := s.crmViews.Delete(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id")); {
	case errors.Is(err, crmviews.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such view")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "delete crm view", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not remove that view")
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// emberOpsLinks is the EmberOps pages the CRM sync recorded for a record. A store that does not keep them (the
// EmberOps-backed one) has none to show.
func (s *Server) emberOpsLinks(r *http.Request, ref core.Ref) []crm.EmberOpsLink {
	l, ok := s.crm.(interface {
		EmberOpsLinks(ctx context.Context, orgID string, ref core.Ref) ([]crm.EmberOpsLink, error)
	})
	if !ok {
		return nil
	}
	links, err := l.EmberOpsLinks(r.Context(), auth.From(r.Context()).OrgID, ref)
	if err != nil {
		s.log.WarnContext(r.Context(), "load EmberOps links", "error", err)
	}
	return links
}

// exportFields is the custom columns of a CSV: one per field of the kind, in label order, with each row's values.
type exportFields struct {
	defs []crm.PropertyDef
	vals map[string]map[string]string
}

func (e exportFields) header() []string {
	out := make([]string, 0, len(e.defs))
	for _, d := range e.defs {
		out = append(out, safeCell(d.Label))
	}
	return out
}

func (e exportFields) cells(id string) []string {
	out := make([]string, 0, len(e.defs))
	for _, d := range e.defs {
		out = append(out, safeCell(e.vals[id][d.Key]))
	}
	return out
}

func (s *Server) exportFields(r *http.Request, kind core.EntityType, ids []string) exportFields {
	p, ok := s.sales.(crm.Props)
	if !ok {
		return exportFields{}
	}
	id := auth.From(r.Context())
	defs, err := p.PropertyDefs(r.Context(), id.OrgID, kind)
	if err != nil || len(defs) == 0 {
		return exportFields{}
	}
	vals, err := p.PropertyTexts(r.Context(), id.OrgID, kind, ids)
	if err != nil {
		s.log.WarnContext(r.Context(), "load custom field values for an export", "error", err)
		return exportFields{}
	}
	return exportFields{defs: defs, vals: vals}
}

// listFields puts the custom fields of a kind on a list page, with the filter in use, so the filter bar can offer them.
func (s *Server) listFields(r *http.Request, kind core.EntityType, d *deskData) *deskData {
	p, ok := s.sales.(crm.Props)
	if !ok {
		return d
	}
	id := auth.From(r.Context())
	defs, err := p.PropertyDefs(r.Context(), id.OrgID, kind)
	if err != nil || len(defs) == 0 {
		return d
	}
	if d == nil {
		d = &deskData{}
	}
	d.PropDefs, d.PropKind = defs, string(kind)
	d.PropSel, d.PropVal = r.URL.Query().Get("pk"), r.URL.Query().Get("pv")
	return d
}
