package api

// The CRM over the API: companies, contacts, deals, and the notes and calls on
// their timelines.
//
// Same rule as the documents endpoints and for the same reason. Every handler
// resolves the token to a person, takes that person's subject set, and calls the
// service method the page calls, so the API refuses what the pages refuse for
// the same caller. A token whose person is not on the Sales team, and is not an
// admin, sees none of it.
//
// # Safe to send twice
//
// A webhook retries, and the automations that feed this (a support chat, a
// survey, a mail campaign) retry on any error. So every create takes an optional
// external_id, and an optional source that defaults to "api". The first time a
// pair is seen it creates the record and remembers the pair in external_refs.
// Every time after, it answers 200 with the record that already exists and the
// header Idempotent-Replay: true, and changes nothing. Two requests with the same
// pair at the same moment are serialised by an advisory lock, so they cannot both
// create.
//
// A contact with an email somebody already holds answers 409 and names the
// existing record, which is what a find-then-create flow needs to carry on with.

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
)

// WithCRM switches the CRM endpoints on. store serves companies and contacts,
// sales serves everything else. Either may be nil, and then the CRM routes answer
// 404 and nothing else changes: a deployment that keeps its data in EmberOps has
// no sales side.
func (s *Service) WithCRM(store crm.Store, sales crm.Sales) *Service {
	s.crmStore, s.sales = store, sales
	return s
}

func (s *Service) crmRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/company-types", s.crm(s.listCompanyTypes))
	mux.HandleFunc("GET /v1/companies", s.crm(s.listCompanies))
	mux.HandleFunc("POST /v1/companies", s.crm(s.createCompany))
	mux.HandleFunc("GET /v1/companies/{id}", s.crm(s.getCompany))
	mux.HandleFunc("PATCH /v1/companies/{id}", s.crm(s.patchCompany))
	mux.HandleFunc("GET /v1/companies/{id}/activities", s.crm(s.activitiesOf(core.EntityCompany)))

	mux.HandleFunc("GET /v1/contacts", s.crm(s.listContacts))
	mux.HandleFunc("POST /v1/contacts", s.crm(s.createContact))
	mux.HandleFunc("GET /v1/contacts/{id}", s.crm(s.getContact))
	mux.HandleFunc("PATCH /v1/contacts/{id}", s.crm(s.patchContact))
	mux.HandleFunc("DELETE /v1/contacts/{id}", s.crm(s.deleteContact))
	mux.HandleFunc("GET /v1/contacts/{id}/activities", s.crm(s.activitiesOf(core.EntityContact)))

	mux.HandleFunc("GET /v1/deals", s.crm(s.listDeals))
	mux.HandleFunc("POST /v1/deals", s.crm(s.createDeal))
	mux.HandleFunc("GET /v1/deals/{id}", s.crm(s.getDeal))
	mux.HandleFunc("PATCH /v1/deals/{id}", s.crm(s.patchDeal))
	mux.HandleFunc("GET /v1/deals/{id}/activities", s.crm(s.activitiesOf(core.EntityDeal)))

	mux.HandleFunc("POST /v1/activities", s.crm(s.createActivity))
	mux.HandleFunc("GET /v1/crm/stats", s.crm(s.crmStats))
	mux.HandleFunc("GET /v1/crm/lookup", s.crm(s.lookupPhone))
	s.deskRoutes(mux)
}

// crm answers 404 when the CRM is not switched on.
func (s *Service) crm(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.crmStore == nil || s.sales == nil {
			writeErr(w, http.StatusNotFound, "the CRM is not available on this deployment")
			return
		}
		h(w, r)
	}
}

// crmErr maps a service error onto a status, the way the pages do.
func crmErr(w http.ResponseWriter, err error, forbidden string) {
	switch {
	case errors.Is(err, crm.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such record")
	case errors.Is(err, crm.ErrDuplicate):
		writeErr(w, http.StatusConflict, strings.TrimPrefix(err.Error(), "crm: "))
	case errors.Is(err, crm.ErrInvalid):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "crm: "))
	default:
		writeErr(w, http.StatusForbidden, forbidden)
	}
}

func queryInt(r *http.Request, name string, def, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || n < 0 {
		return def
	}
	if max > 0 && n > max {
		return max
	}
	return n
}

func listQuery(r *http.Request) crm.ListQuery {
	q := r.URL.Query()
	lim := queryInt(r, "limit", 50, 200)
	if lim == 0 {
		lim = 50
	}
	return crm.ListQuery{
		Search: q.Get("q"), PhoneE164: crm.PhoneE164(q.Get("phone")), Kind: q.Get("kind"), Stage: q.Get("stage"), Owner: q.Get("owner"),
		CompanyID: q.Get("company_id"), ContactID: q.Get("contact_id"), Type: q.Get("type"), Tag: q.Get("tag"), PropKey: q.Get("prop_key"), PropValue: q.Get("prop_value"), Sort: q.Get("sort"), Desc: q.Get("desc") == "1",
		Limit: lim, Offset: queryInt(r, "offset", 0, 0),
	}
}

// -----------------------------------------------------------------------------
// Safe to send twice
// -----------------------------------------------------------------------------

// once runs create the first time a (source, external id) pair is seen and
// returns the existing entity every time after.
func (s *Service) once(ctx context.Context, orgID, source, externalID, entityType string,
	create func() (string, error)) (id string, replay bool, err error) {

	if externalID == "" {
		id, err = create()
		return id, false, err
	}
	if source == "" {
		source = "api"
	}
	key := entityType + ":" + externalID

	// One connection held for the whole call, so the advisory lock and its
	// release are on the same session.
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return "", false, fmt.Errorf("api: idempotency: %w", err)
	}
	defer conn.Release()
	h := fnv.New64a()
	_, _ = h.Write([]byte(orgID + "\x00" + source + "\x00" + key))
	lock := int64(h.Sum64())
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lock); err != nil {
		return "", false, fmt.Errorf("api: idempotency lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, lock) }()

	err = conn.QueryRow(ctx, `SELECT entity_id::text FROM external_refs
		WHERE org_id=$1 AND source=$2 AND external_id=$3`, orgID, source, key).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	id, err = create()
	if err != nil {
		return "", false, err
	}
	if _, err := conn.Exec(ctx, `INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT (org_id, source, external_id) DO NOTHING`,
		orgID, source, key, entityType, id); err != nil {
		return "", false, fmt.Errorf("api: remember external id: %w", err)
	}
	return id, false, nil
}

func replayHeader(w http.ResponseWriter, replay bool) int {
	if replay {
		w.Header().Set("Idempotent-Replay", "true")
		return http.StatusOK
	}
	return http.StatusCreated
}

// -----------------------------------------------------------------------------
// Companies
// -----------------------------------------------------------------------------

type address struct {
	Street   *string `json:"street"`
	City     *string `json:"city"`
	State    *string `json:"state"`
	Postcode *string `json:"postcode"`
	Country  *string `json:"country"`
}

type companyBody struct {
	Name         *string  `json:"name"`
	Domain       *string  `json:"domain"`
	Kind         *string  `json:"kind"`
	Notes        *string  `json:"notes"`
	Website      *string  `json:"website"`
	LinkedIn     *string  `json:"linkedin_url"`
	TaxID        *string  `json:"tax_id"`
	Address      *address `json:"address"`
	RevenueCents *int64   `json:"annual_revenue_cents"`
	Currency     *string  `json:"revenue_currency"`
	OwnerID      *string  `json:"owner_id"`
	// Types are type keys from GET /v1/company-types, and Tags are free. Either
	// replaces what is there when sent, and is left alone when not.
	Types *[]string `json:"types"`
	Tags  *[]string `json:"tags"`
	// Properties are custom fields by key; a null or empty value clears one.
	Properties map[string]any `json:"properties"`
	ExternalID string         `json:"external_id"`
	Source     string         `json:"source"`
}

func str(p *string, def string) string {
	if p != nil {
		return strings.TrimSpace(*p)
	}
	return def
}

// mergeCompanyDetails applies the fields a request named over what is there.
func mergeCompanyDetails(d crm.Details, b companyBody) crm.Details {
	d.Website = str(b.Website, d.Website)
	d.LinkedIn = str(b.LinkedIn, d.LinkedIn)
	d.TaxID = str(b.TaxID, d.TaxID)
	if a := b.Address; a != nil {
		d.Street, d.City = str(a.Street, d.Street), str(a.City, d.City)
		d.State, d.Postcode = str(a.State, d.State), str(a.Postcode, d.Postcode)
		d.Country = str(a.Country, d.Country)
	}
	if b.RevenueCents != nil {
		d.RevenueCents = b.RevenueCents
	}
	d.Currency = str(b.Currency, d.Currency)
	if b.OwnerID != nil {
		d.OwnerID = strings.TrimSpace(*b.OwnerID)
	}
	return d
}

func (b companyBody) hasLabels() bool { return b.Types != nil || b.Tags != nil }

// checkTypes refuses a type key that is not on the list before anything is made,
// so a bad key cannot leave a half-made company behind.
func (s *Service) checkTypes(ctx context.Context, orgID string, keys *[]string) error {
	if keys == nil {
		return nil
	}
	list, err := s.sales.Types(ctx, orgID, false)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, t := range list {
		known[t.Key] = true
	}
	for _, k := range *keys {
		if !known[strings.TrimSpace(k)] {
			return fmt.Errorf("%w: %q is not a company type here", crm.ErrInvalid, k)
		}
	}
	return nil
}

func (b companyBody) hasDetails() bool {
	return b.Website != nil || b.LinkedIn != nil || b.TaxID != nil || b.Address != nil || b.RevenueCents != nil ||
		b.Currency != nil || b.OwnerID != nil
}

func (s *Service) companyJSON(r *http.Request, c *Caller, co *crm.Company) (map[string]any, error) {
	d, err := s.sales.CompanyDetails(r.Context(), c.OrgID, c.Subjects, co.ID)
	if err != nil {
		return nil, err
	}
	lab, err := s.sales.CompanyLabels(r.Context(), c.OrgID, c.Subjects, co.ID)
	if err != nil {
		return nil, err
	}
	tags := lab.Tags
	if tags == nil {
		tags = []string{}
	}
	out := map[string]any{
		"types": lab.Keys(), "tags": tags,
		"id": co.ID, "name": co.Name, "domain": co.Domain, "kind": co.Kind, "notes": co.Notes,
		"contacts": co.Contacts, "created_at": co.CreatedAt, "updated_at": co.UpdatedAt,
		"website": d.Website, "linkedin_url": d.LinkedIn, "tax_id": d.TaxID,
		"address": map[string]any{"street": d.Street, "city": d.City, "state": d.State,
			"postcode": d.Postcode, "country": d.Country},
		"annual_revenue_cents": d.RevenueCents, "revenue_currency": d.Currency,
		"owner_id": nilIfEmpty(d.OwnerID), "owner_name": d.OwnerName,
	}
	return out, nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Service) listCompanies(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	q := listQuery(r)
	rows, total, err := s.sales.CompaniesPage(r.Context(), c.OrgID, c.Subjects, q)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list the companies")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, x := range rows {
		out = append(out, map[string]any{
			"id": x.ID, "name": x.Name, "domain": x.Domain, "kind": x.Kind, "website": x.Website,
			"city": x.City, "state": x.State, "annual_revenue_cents": x.RevenueCents,
			"revenue_currency": x.Currency, "owner_id": nilIfEmpty(x.OwnerID), "owner_name": x.OwnerName,
			"contacts": x.Contacts, "deals": x.Deals, "created_at": x.CreatedAt, "updated_at": x.UpdatedAt,
			"types": nonNil(x.TypeKeys), "tags": nonNil(x.Tags),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"companies": out, "total": total})
}

func (s *Service) getCompany(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	co, err := s.crmStore.GetCompany(r.Context(), c.OrgID, c.Subjects, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such company")
		return
	}
	out, err := s.companyJSON(r, c, co)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such company")
		return
	}
	s.addProps(r, c, core.EntityCompany, out)
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) createCompany(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	var b companyBody
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.checkProps(w, r, c, core.EntityCompany, b.Properties) {
		return
	}
	if err := s.checkTypes(r.Context(), c.OrgID, b.Types); err != nil {
		crmErr(w, err, "you cannot add a company")
		return
	}
	id, replay, err := s.once(r.Context(), c.OrgID, b.Source, b.ExternalID, "company", func() (string, error) {
		made, err := s.crmStore.CreateCompany(r.Context(), c.OrgID, c.Subjects, crm.Company{
			Name: str(b.Name, ""), Domain: str(b.Domain, ""), Kind: str(b.Kind, ""), Notes: str(b.Notes, ""),
		})
		if err != nil {
			return "", err
		}
		if b.hasDetails() {
			if err := s.sales.SetCompanyDetails(r.Context(), c.OrgID, c.Subjects, made.ID,
				mergeCompanyDetails(crm.Details{Currency: "USD"}, b)); err != nil {
				return made.ID, err
			}
		}
		if b.hasLabels() {
			if err := s.sales.SetCompanyLabels(r.Context(), c.OrgID, c.Subjects, made.ID, b.Types, b.Tags); err != nil {
				return made.ID, err
			}
		}
		// Shared with the Sales team, like a company made on the page.
		if err := s.sales.Share(r.Context(), c.OrgID, c.Subjects, made.Ref()); err != nil {
			return made.ID, err
		}
		return made.ID, nil
	})
	if err != nil {
		crmErr(w, err, "you cannot add a company")
		return
	}
	if !replay {
		if err := s.setProps(r, c, core.Ref{Type: core.EntityCompany, ID: id}, b.Properties); err != nil {
			crmErr(w, err, "you cannot set those")
			return
		}
	}
	co, err := s.crmStore.GetCompany(r.Context(), c.OrgID, c.Subjects, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such company")
		return
	}
	out, err := s.companyJSON(r, c, co)
	if err != nil {
		crmErr(w, err, "you cannot read that company")
		return
	}
	s.addProps(r, c, core.EntityCompany, out)
	writeJSON(w, replayHeader(w, replay), out)
}

func (s *Service) patchCompany(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	id := r.PathValue("id")
	var b companyBody
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.checkProps(w, r, c, core.EntityCompany, b.Properties) {
		return
	}
	if b.ExternalID != "" || b.Source != "" {
		writeErr(w, http.StatusBadRequest, "external_id and source are for creating, not changing")
		return
	}
	cur, err := s.crmStore.GetCompany(r.Context(), c.OrgID, c.Subjects, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such company")
		return
	}
	if b.Name != nil || b.Domain != nil || b.Kind != nil || b.Notes != nil {
		cur.Name, cur.Domain = str(b.Name, cur.Name), str(b.Domain, cur.Domain)
		cur.Kind, cur.Notes = str(b.Kind, cur.Kind), str(b.Notes, cur.Notes)
		if err := s.crmStore.UpdateCompany(r.Context(), c.OrgID, c.Subjects, *cur); err != nil {
			crmErr(w, err, "you cannot change that company")
			return
		}
	}
	if b.hasLabels() {
		if err := s.checkTypes(r.Context(), c.OrgID, b.Types); err != nil {
			crmErr(w, err, "you cannot change that company")
			return
		}
		if err := s.sales.SetCompanyLabels(r.Context(), c.OrgID, c.Subjects, id, b.Types, b.Tags); err != nil {
			crmErr(w, err, "you cannot change that company")
			return
		}
	}
	if b.hasDetails() {
		d, err := s.sales.CompanyDetails(r.Context(), c.OrgID, c.Subjects, id)
		if err != nil {
			crmErr(w, err, "you cannot change that company")
			return
		}
		if err := s.sales.SetCompanyDetails(r.Context(), c.OrgID, c.Subjects, id, mergeCompanyDetails(*d, b)); err != nil {
			crmErr(w, err, "you cannot change that company")
			return
		}
	}
	if err := s.setProps(r, c, core.Ref{Type: core.EntityCompany, ID: id}, b.Properties); err != nil {
		crmErr(w, err, "you cannot set those")
		return
	}
	co, err := s.crmStore.GetCompany(r.Context(), c.OrgID, c.Subjects, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such company")
		return
	}
	out, err := s.companyJSON(r, c, co)
	if err != nil {
		crmErr(w, err, "you cannot read that company")
		return
	}
	s.addProps(r, c, core.EntityCompany, out)
	writeJSON(w, http.StatusOK, out)
}

// -----------------------------------------------------------------------------
// Contacts
// -----------------------------------------------------------------------------

type contactBody struct {
	Name        *string   `json:"name"`
	Email       *string   `json:"email"`
	Phone       *string   `json:"phone"`
	Title       *string   `json:"title"`
	Notes       *string   `json:"notes"`
	CompanyID   *string   `json:"company_id"`
	LinkedIn    *string   `json:"linkedin_url"`
	ExtraEmails *[]string `json:"extra_emails"`
	ExtraPhones *[]string `json:"extra_phones"`
	OwnerID     *string   `json:"owner_id"`
	// MarketingOptOut true stops marketing email to this person. There is no false: an opt-out is never taken back here.
	MarketingOptOut *bool `json:"marketing_opt_out"`
	// Properties are custom fields by key; a null or empty value clears one.
	Properties map[string]any `json:"properties"`
	ExternalID string         `json:"external_id"`
	Source     string         `json:"source"`
}

func (b contactBody) hasDetails() bool {
	return b.LinkedIn != nil || b.ExtraEmails != nil || b.ExtraPhones != nil || b.OwnerID != nil || b.MarketingOptOut != nil
}

func mergeContactDetails(d crm.Details, b contactBody) crm.Details {
	d.LinkedIn = str(b.LinkedIn, d.LinkedIn)
	if b.ExtraEmails != nil {
		d.ExtraEmails = *b.ExtraEmails
	}
	if b.ExtraPhones != nil {
		d.ExtraPhones = *b.ExtraPhones
	}
	if b.OwnerID != nil {
		d.OwnerID = strings.TrimSpace(*b.OwnerID)
	}
	if b.MarketingOptOut != nil && *b.MarketingOptOut {
		d.OptOut = true
	}
	return d
}

func (s *Service) contactJSON(r *http.Request, c *Caller, ct *crm.Contact) (map[string]any, error) {
	d, err := s.sales.ContactDetails(r.Context(), c.OrgID, c.Subjects, ct.ID)
	if err != nil {
		return nil, err
	}
	var company any
	if ct.CompanyID != nil {
		company = *ct.CompanyID
	}
	return map[string]any{
		"id": ct.ID, "name": ct.Name, "email": ct.Email, "phone": ct.Phone, "title": ct.Title,
		"notes": ct.Notes, "company_id": company, "company_name": ct.CompanyName,
		"linkedin_url": d.LinkedIn, "extra_emails": nonNil(d.ExtraEmails), "extra_phones": nonNil(d.ExtraPhones),
		"owner_id": nilIfEmpty(d.OwnerID), "owner_name": d.OwnerName,
		"marketing_opt_out": d.MarketingOptOutAt != nil, "marketing_opt_out_at": d.MarketingOptOutAt,
		"marketing_opt_out_reason": d.MarketingOptOutReason, "created_at": ct.CreatedAt, "updated_at": ct.UpdatedAt,
	}, nil
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (s *Service) listContacts(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	// By email first: the lookup every automation starts with. Exact, and
	// filtered by grant like everything else, so an address somebody else holds
	// out of the caller's sight is not found.
	if email := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("email"))); email != "" {
		found, err := s.crmStore.ByEmail(r.Context(), c.OrgID, email)
		out := []map[string]any{}
		if err == nil {
			if ct, gerr := s.crmStore.GetContact(r.Context(), c.OrgID, c.Subjects, found.ID); gerr == nil {
				if j, jerr := s.contactJSON(r, c, ct); jerr == nil {
					out = append(out, j)
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"contacts": out, "total": len(out)})
		return
	}
	q := listQuery(r)
	rows, total, err := s.sales.ContactsPage(r.Context(), c.OrgID, c.Subjects, q)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list the contacts")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, x := range rows {
		var company any
		if x.CompanyID != nil {
			company = *x.CompanyID
		}
		out = append(out, map[string]any{
			"id": x.ID, "name": x.Name, "email": x.Email, "phone": x.Phone, "title": x.Title,
			"company_id": company, "company_name": x.CompanyName, "linkedin_url": x.LinkedIn,
			"owner_id": nilIfEmpty(x.OwnerID), "owner_name": x.OwnerName,
			"created_at": x.CreatedAt, "updated_at": x.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"contacts": out, "total": total})
}

func (s *Service) getContact(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	ct, err := s.crmStore.GetContact(r.Context(), c.OrgID, c.Subjects, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such contact")
		return
	}
	out, err := s.contactJSON(r, c, ct)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such contact")
		return
	}
	s.addProps(r, c, core.EntityContact, out)
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) createContact(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	var b contactBody
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.checkProps(w, r, c, core.EntityContact, b.Properties) {
		return
	}
	id, replay, err := s.once(r.Context(), c.OrgID, b.Source, b.ExternalID, "contact", func() (string, error) {
		in := crm.Contact{Name: str(b.Name, ""), Email: str(b.Email, ""), Phone: str(b.Phone, ""),
			Title: str(b.Title, ""), Notes: str(b.Notes, "")}
		if v := str(b.CompanyID, ""); v != "" {
			in.CompanyID = &v
		}
		made, err := s.crmStore.CreateContact(r.Context(), c.OrgID, c.Subjects, in)
		if err != nil {
			return "", err
		}
		if b.hasDetails() {
			if err := s.sales.SetContactDetails(r.Context(), c.OrgID, c.Subjects, made.ID,
				mergeContactDetails(crm.Details{}, b)); err != nil {
				return made.ID, err
			}
		}
		if err := s.sales.Share(r.Context(), c.OrgID, c.Subjects, made.Ref()); err != nil {
			return made.ID, err
		}
		return made.ID, nil
	})
	if errors.Is(err, crm.ErrDuplicate) {
		// Tell the caller who already has the address, if they may see them: a
		// find-then-create flow carries on with that id.
		body := map[string]any{"error": strings.TrimPrefix(err.Error(), "crm: ")}
		if email := strings.ToLower(str(b.Email, "")); email != "" {
			if f, ferr := s.crmStore.ByEmail(r.Context(), c.OrgID, email); ferr == nil {
				if _, gerr := s.crmStore.GetContact(r.Context(), c.OrgID, c.Subjects, f.ID); gerr == nil {
					body["existing_id"] = f.ID
				}
			}
		}
		writeJSON(w, http.StatusConflict, body)
		return
	}
	if err != nil {
		crmErr(w, err, "you cannot add a contact")
		return
	}
	if !replay {
		if err := s.setProps(r, c, core.Ref{Type: core.EntityContact, ID: id}, b.Properties); err != nil {
			crmErr(w, err, "you cannot set those")
			return
		}
	}
	ct, err := s.crmStore.GetContact(r.Context(), c.OrgID, c.Subjects, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such contact")
		return
	}
	out, err := s.contactJSON(r, c, ct)
	if err != nil {
		crmErr(w, err, "you cannot read that contact")
		return
	}
	s.addProps(r, c, core.EntityContact, out)
	writeJSON(w, replayHeader(w, replay), out)
}

func (s *Service) patchContact(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	id := r.PathValue("id")
	var b contactBody
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.checkProps(w, r, c, core.EntityContact, b.Properties) {
		return
	}
	if b.ExternalID != "" || b.Source != "" {
		writeErr(w, http.StatusBadRequest, "external_id and source are for creating, not changing")
		return
	}
	if b.MarketingOptOut != nil && !*b.MarketingOptOut {
		writeErr(w, http.StatusBadRequest, "an opt-out cannot be taken back here: the person has to ask to be emailed again")
		return
	}
	cur, err := s.crmStore.GetContact(r.Context(), c.OrgID, c.Subjects, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such contact")
		return
	}
	if b.Name != nil || b.Email != nil || b.Phone != nil || b.Title != nil || b.Notes != nil || b.CompanyID != nil {
		cur.Name, cur.Email = str(b.Name, cur.Name), str(b.Email, cur.Email)
		cur.Phone, cur.Title = str(b.Phone, cur.Phone), str(b.Title, cur.Title)
		cur.Notes = str(b.Notes, cur.Notes)
		if b.CompanyID != nil {
			if v := strings.TrimSpace(*b.CompanyID); v != "" {
				cur.CompanyID = &v
			} else {
				cur.CompanyID = nil
			}
		}
		if err := s.crmStore.UpdateContact(r.Context(), c.OrgID, c.Subjects, *cur); err != nil {
			crmErr(w, err, "you cannot change that contact")
			return
		}
	}
	if b.hasDetails() {
		d, err := s.sales.ContactDetails(r.Context(), c.OrgID, c.Subjects, id)
		if err != nil {
			crmErr(w, err, "you cannot change that contact")
			return
		}
		if err := s.sales.SetContactDetails(r.Context(), c.OrgID, c.Subjects, id, mergeContactDetails(*d, b)); err != nil {
			crmErr(w, err, "you cannot change that contact")
			return
		}
	}
	if err := s.setProps(r, c, core.Ref{Type: core.EntityContact, ID: id}, b.Properties); err != nil {
		crmErr(w, err, "you cannot set those")
		return
	}
	ct, err := s.crmStore.GetContact(r.Context(), c.OrgID, c.Subjects, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such contact")
		return
	}
	out, err := s.contactJSON(r, c, ct)
	if err != nil {
		crmErr(w, err, "you cannot read that contact")
		return
	}
	s.addProps(r, c, core.EntityContact, out)
	writeJSON(w, http.StatusOK, out)
}

// deleteContact removes a person from the CRM (a soft delete, as on the page). 204 when it is gone.
func (s *Service) deleteContact(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	if err := s.crmStore.DeleteContact(r.Context(), c.OrgID, c.Subjects, r.PathValue("id")); err != nil {
		if errors.Is(err, crm.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "no such contact")
			return
		}
		writeErr(w, http.StatusNotFound, "no such contact")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------------
// Deals
// -----------------------------------------------------------------------------

type dealBody struct {
	Name        *string `json:"name"`
	Stage       *string `json:"stage"`
	AmountCents *int64  `json:"amount_cents"`
	Currency    *string `json:"currency"`
	CloseDate   *string `json:"close_date"`
	CompanyID   *string `json:"company_id"`
	ContactID   *string `json:"contact_id"`
	OwnerID     *string `json:"owner_id"`
	Notes       *string `json:"notes"`
	LostReason  *string `json:"lost_reason"`
	// Properties are custom fields by key; a null or empty value clears one.
	Properties map[string]any `json:"properties"`
	ExternalID string         `json:"external_id"`
	Source     string         `json:"source"`
}

func dealJSON(d *crm.Deal) map[string]any {
	out := map[string]any{
		"id": d.ID, "name": d.Name, "stage": d.Stage, "amount_cents": d.AmountCents, "currency": d.Currency,
		"company_name": d.CompanyName, "contact_name": d.ContactName, "owner_name": d.OwnerName,
		"notes": d.Notes, "created_at": d.CreatedAt, "updated_at": d.UpdatedAt,
		"lost_reason": d.LostReason, "stage_changed_at": d.StageChangedAt, "closed_at": d.ClosedAt,
		"probability_percent": crm.Probability(d.Stage), "weighted_cents": d.AmountCents * int64(crm.Probability(d.Stage)) / 100,
		"close_date": nil, "company_id": nil, "contact_id": nil, "owner_id": nil,
	}
	if d.CloseDate != nil {
		out["close_date"] = d.CloseDate.Format("2006-01-02")
	}
	if d.CompanyID != nil {
		out["company_id"] = *d.CompanyID
	}
	if d.ContactID != nil {
		out["contact_id"] = *d.ContactID
	}
	if d.OwnerID != nil {
		out["owner_id"] = *d.OwnerID
	}
	return out
}

func optID(p *string, cur *string) *string {
	if p == nil {
		return cur
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil
	}
	return &v
}

func applyDeal(d crm.Deal, b dealBody) (crm.Deal, error) {
	d.Name, d.Stage = str(b.Name, d.Name), str(b.Stage, d.Stage)
	if b.AmountCents != nil {
		d.AmountCents = *b.AmountCents
	}
	d.Currency = str(b.Currency, d.Currency)
	if b.CloseDate != nil {
		if v := strings.TrimSpace(*b.CloseDate); v == "" {
			d.CloseDate = nil
		} else {
			t, err := time.Parse("2006-01-02", v)
			if err != nil {
				return d, fmt.Errorf("%w: close_date is YYYY-MM-DD", crm.ErrInvalid)
			}
			d.CloseDate = &t
		}
	}
	d.CompanyID = optID(b.CompanyID, d.CompanyID)
	d.ContactID = optID(b.ContactID, d.ContactID)
	d.OwnerID = optID(b.OwnerID, d.OwnerID)
	d.Notes = str(b.Notes, d.Notes)
	return d, nil
}

func (s *Service) listDeals(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	rows, total, err := s.sales.Deals(r.Context(), c.OrgID, c.Subjects, listQuery(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list the deals")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, d := range rows {
		out = append(out, dealJSON(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"deals": out, "total": total})
}

func (s *Service) getDeal(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	d, err := s.sales.GetDeal(r.Context(), c.OrgID, c.Subjects, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such deal")
		return
	}
	out := dealJSON(d)
	s.addProps(r, c, core.EntityDeal, out)
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) createDeal(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	var b dealBody
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.checkProps(w, r, c, core.EntityDeal, b.Properties) {
		return
	}
	id, replay, err := s.once(r.Context(), c.OrgID, b.Source, b.ExternalID, "deal", func() (string, error) {
		in, err := applyDeal(crm.Deal{Stage: crm.StageNew, Currency: "USD"}, b)
		if err != nil {
			return "", err
		}
		made, err := s.sales.CreateDeal(r.Context(), c.OrgID, c.Subjects, in)
		if err != nil {
			return "", err
		}
		return made.ID, nil
	})
	if err != nil {
		crmErr(w, err, "you cannot add a deal")
		return
	}
	if !replay {
		if err := s.setProps(r, c, core.Ref{Type: core.EntityDeal, ID: id}, b.Properties); err != nil {
			crmErr(w, err, "you cannot set those")
			return
		}
	}
	d, err := s.sales.GetDeal(r.Context(), c.OrgID, c.Subjects, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such deal")
		return
	}
	out := dealJSON(d)
	s.addProps(r, c, core.EntityDeal, out)
	writeJSON(w, replayHeader(w, replay), out)
}

func (s *Service) patchDeal(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	id := r.PathValue("id")
	var b dealBody
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.checkProps(w, r, c, core.EntityDeal, b.Properties) {
		return
	}
	if b.ExternalID != "" || b.Source != "" {
		writeErr(w, http.StatusBadRequest, "external_id and source are for creating, not changing")
		return
	}
	cur, err := s.sales.GetDeal(r.Context(), c.OrgID, c.Subjects, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such deal")
		return
	}
	next, err := applyDeal(*cur, b)
	if err != nil {
		crmErr(w, err, "you cannot change that deal")
		return
	}
	if err := s.sales.UpdateDeal(r.Context(), c.OrgID, c.Subjects, next); err != nil {
		crmErr(w, err, "you cannot change that deal")
		return
	}
	if b.LostReason != nil {
		if desk, ok := s.sales.(crm.Desk); ok {
			if err := desk.SetLostReason(r.Context(), c.OrgID, c.Subjects, id, *b.LostReason); err != nil {
				crmErr(w, err, "you cannot change that deal")
				return
			}
		}
	}
	if err := s.setProps(r, c, core.Ref{Type: core.EntityDeal, ID: id}, b.Properties); err != nil {
		crmErr(w, err, "you cannot set those")
		return
	}
	d, err := s.sales.GetDeal(r.Context(), c.OrgID, c.Subjects, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such deal")
		return
	}
	out := dealJSON(d)
	s.addProps(r, c, core.EntityDeal, out)
	writeJSON(w, http.StatusOK, out)
}

// -----------------------------------------------------------------------------
// Timeline and stats
// -----------------------------------------------------------------------------

type targetBody struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type activityBody struct {
	Kind        string       `json:"kind"`
	Title       string       `json:"title"`
	Body        string       `json:"body"`
	OccurredAt  *time.Time   `json:"occurred_at"`
	Direction   string       `json:"direction"`
	Disposition string       `json:"disposition"`
	DurationMs  *int64       `json:"duration_ms"`
	Targets     []targetBody `json:"targets"`
	ExternalID  string       `json:"external_id"`
	Source      string       `json:"source"`
}

func activityJSON(a *crm.Activity) map[string]any {
	out := map[string]any{
		"id": a.ID, "kind": a.Kind, "title": a.Title, "body": a.Body, "occurred_at": a.OccurredAt,
		"direction": a.Direction, "disposition": a.Disposition, "duration_ms": a.DurationMs,
		"author_id": a.CreatedBy, "author_name": a.ByName, "created_at": a.CreatedAt,
	}
	return out
}

func (s *Service) createActivity(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	var b activityBody
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var made *crm.Activity
	id, replay, err := s.once(r.Context(), c.OrgID, b.Source, b.ExternalID, "activity", func() (string, error) {
		a := crm.Activity{Kind: b.Kind, Title: b.Title, Body: b.Body, Direction: b.Direction,
			Disposition: b.Disposition, DurationMs: b.DurationMs}
		if b.OccurredAt != nil {
			a.OccurredAt = *b.OccurredAt
		}
		for _, t := range b.Targets {
			a.Targets = append(a.Targets, core.Ref{Type: core.EntityType(t.Type), ID: t.ID})
		}
		m, err := s.sales.AddActivity(r.Context(), c.OrgID, c.Subjects, a)
		if err != nil {
			return "", err
		}
		made = m
		return m.ID, nil
	})
	if err != nil {
		crmErr(w, err, "you cannot write on that")
		return
	}
	if made == nil {
		// A replay: the activity already exists, so say which one.
		writeJSON(w, replayHeader(w, replay), map[string]any{"id": id})
		return
	}
	writeJSON(w, replayHeader(w, replay), activityJSON(made))
}

func (s *Service) activitiesOf(t core.EntityType) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := from(r)
		list, err := s.sales.ActivitiesFor(r.Context(), c.OrgID, c.Subjects, core.Ref{Type: t, ID: r.PathValue("id")})
		if err != nil {
			writeErr(w, http.StatusNotFound, "no such record")
			return
		}
		out := make([]map[string]any, 0, len(list))
		for _, a := range list {
			out = append(out, activityJSON(a))
		}
		writeJSON(w, http.StatusOK, map[string]any{"activities": out})
	}
}

func (s *Service) crmStats(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	st, err := s.sales.Stats(r.Context(), c.OrgID, c.Subjects)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not count")
		return
	}
	by := map[string]any{}
	for _, name := range crm.Stages {
		s := st.ByStage[name]
		by[name] = map[string]any{"count": s.Count, "value_cents": s.ValueCents}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"companies": st.Companies, "contacts": st.Contacts, "deals": st.Deals,
		"open_deals": st.OpenDeals, "open_value_cents": st.OpenValueCents, "by_stage": by,
	})
}

func (s *Service) listCompanyTypes(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	list, err := s.sales.Types(r.Context(), c.OrgID, false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list the types")
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		out = append(out, map[string]any{"key": t.Key, "label": t.Label, "erp_role": t.ERPRole, "position": t.Position})
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": out})
}

// lookupPhone answers "who is calling": the contact holding a phone number, with their company.
//
//	GET /v1/crm/lookup?phone=+12165550142   200 {"e164", "matches", "contact": {...}, "company": {...}}
//	                                          404 when nobody the caller may see holds the number
//
// The number is normalized to E.164 first (crm.PhoneE164), matched against the contact's number and extras
// through the same index every list uses, and scoped to the org and to what the token's person may see, so a
// phone system with a read token sees exactly the CRM that person sees. With more than one match the most
// recently updated contact is returned and matches says how many there were.
func (s *Service) lookupPhone(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	e164 := crm.PhoneE164(r.URL.Query().Get("phone"))
	if e164 == "" {
		writeErr(w, http.StatusBadRequest, "phone must be a number with a country code, or ten digits for North America")
		return
	}
	rows, total, err := s.sales.ContactsPage(r.Context(), c.OrgID, c.Subjects,
		crm.ListQuery{PhoneE164: e164, Sort: "updated", Desc: true, Limit: 1})
	if err != nil {
		writeErr(w, http.StatusForbidden, "you cannot look that up")
		return
	}
	if len(rows) == 0 {
		writeErr(w, http.StatusNotFound, "no contact holds that number")
		return
	}
	ct := rows[0]
	out := map[string]any{
		"e164": e164, "matches": total,
		"contact": map[string]any{"id": ct.ID, "name": ct.Name, "title": ct.Title, "company_id": ct.CompanyID},
	}
	if ct.CompanyID != nil && *ct.CompanyID != "" {
		out["company"] = map[string]any{"id": *ct.CompanyID, "name": ct.CompanyName}
	}
	writeJSON(w, http.StatusOK, out)
}
