package web

// Contacts, companies, and parts on screen.
//
// The contact page is a page about a person and everything they have been near:
// the assets they service and the jobs they were called for. That is the
// maintenance-shaped reason to have a CRM, and it is why the links section
// matters more than the fields above it.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/calls"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/docs"
)

func (s *Server) listCompanies(w http.ResponseWriter, r *http.Request) {
	if s.companiesPage(w, r) {
		return
	}
	id := auth.From(r.Context())
	kind := r.URL.Query().Get("kind")
	search := r.URL.Query().Get("q")

	companies, err := s.crm.Companies(r.Context(), id.OrgID, id.Subjects, kind, search)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list companies", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the companies")
		return
	}
	s.render(w, r, "companies", page{
		Title: "Companies", Companies: companies, Filter: kind,
		SearchText: search, CompanyKinds: crm.Kinds,
	})
}

func (s *Server) createCompany(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	c, err := s.crm.CreateCompany(r.Context(), id.OrgID, id.Subjects, crm.Company{
		Name: r.FormValue("name"), Domain: r.FormValue("domain"),
		Kind: r.FormValue("kind"), Notes: r.FormValue("notes"),
	})
	if err != nil {
		s.crmFail(w, r, err, "you cannot add a company")
		return
	}
	if s.sales != nil {
		if err := s.sales.Share(r.Context(), id.OrgID, id.Subjects, c.Ref()); err != nil {
			s.log.WarnContext(r.Context(), "share new company", "error", err)
		}
	}
	http.Redirect(w, r, "/companies/"+c.ID, http.StatusSeeOther)
}

func (s *Server) getCompany(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	companyID := r.PathValue("id")

	c, err := s.crm.GetCompany(r.Context(), id.OrgID, id.Subjects, companyID)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "no such company")
		return
	}
	people, err := s.crm.Contacts(r.Context(), id.OrgID, id.Subjects, companyID, "")
	if err != nil {
		s.log.ErrorContext(r.Context(), "load contacts", "error", err)
	}
	// What this firm has been near. Through the same entity_links everything
	// else uses, which is why a vendor page can show the machines they service
	// without a table of its own.
	linked, err := s.docs.LinkedTo(r.Context(), id.OrgID, id.Subjects, c.Ref())
	if err != nil {
		s.log.ErrorContext(r.Context(), "load company links", "error", err)
	}
	assignable, err := s.docs.List(r.Context(), id.OrgID, id.Subjects,
		docs.ListParams{Limit: 200})
	if err != nil {
		s.log.ErrorContext(r.Context(), "load link candidates", "error", err)
	}

	pg := page{
		Title: c.Name, Company: c, Contacts: people, Links: linked,
		Assignable: assignable, CompanyKinds: crm.Kinds,
	}
	s.salesFor(r, c.Ref(), &pg)
	s.render(w, r, "company", pg)
}

func (s *Server) updateCompany(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	companyID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	if err := s.crm.UpdateCompany(r.Context(), id.OrgID, id.Subjects, crm.Company{
		ID: companyID, Name: r.FormValue("name"), Domain: r.FormValue("domain"),
		Kind: r.FormValue("kind"), Notes: r.FormValue("notes"),
	}); err != nil {
		s.crmFail(w, r, err, "you cannot change that company")
		return
	}
	http.Redirect(w, r, "/companies/"+companyID, http.StatusSeeOther)
}

func (s *Server) deleteCompany(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := s.crm.DeleteCompany(r.Context(), id.OrgID, id.Subjects, r.PathValue("id")); err != nil {
		s.crmFail(w, r, err, "you cannot remove that company")
		return
	}
	http.Redirect(w, r, "/companies", http.StatusSeeOther)
}

// -----------------------------------------------------------------------------

func (s *Server) listContacts(w http.ResponseWriter, r *http.Request) {
	if s.contactsPage(w, r) {
		return
	}
	id := auth.From(r.Context())
	search := r.URL.Query().Get("q")

	people, err := s.crm.Contacts(r.Context(), id.OrgID, id.Subjects, "", search)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list contacts", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the contacts")
		return
	}
	companies, err := s.crm.Companies(r.Context(), id.OrgID, id.Subjects, "", "")
	if err != nil {
		s.log.ErrorContext(r.Context(), "load companies", "error", err)
	}
	s.render(w, r, "contacts", page{
		Title: "Contacts", Contacts: people, Companies: companies, SearchText: search,
	})
}

func (s *Server) createContact(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	c := crm.Contact{
		Name: r.FormValue("name"), Email: r.FormValue("email"),
		Phone: r.FormValue("phone"), Title: r.FormValue("title"),
		Notes: r.FormValue("notes"),
	}
	if v := strings.TrimSpace(r.FormValue("company")); v != "" {
		c.CompanyID = &v
	}
	made, err := s.crm.CreateContact(r.Context(), id.OrgID, id.Subjects, c)
	if err != nil {
		s.crmFail(w, r, err, "you cannot add a contact there")
		return
	}
	if s.sales != nil {
		if err := s.sales.Share(r.Context(), id.OrgID, id.Subjects, made.Ref()); err != nil {
			s.log.WarnContext(r.Context(), "share new contact", "error", err)
		}
	}
	http.Redirect(w, r, "/contacts/"+made.ID, http.StatusSeeOther)
}

func (s *Server) getContact(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	contactID := r.PathValue("id")

	c, err := s.crm.GetContact(r.Context(), id.OrgID, id.Subjects, contactID)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "no such contact")
		return
	}
	// The whole point of the page: the machines this person has been near and
	// the jobs they were called for.
	linked, err := s.docs.LinkedTo(r.Context(), id.OrgID, id.Subjects, c.Ref())
	if err != nil {
		s.log.ErrorContext(r.Context(), "load contact links", "error", err)
	}
	companies, err := s.crm.Companies(r.Context(), id.OrgID, id.Subjects, "", "")
	if err != nil {
		s.log.ErrorContext(r.Context(), "load companies", "error", err)
	}
	assignable, err := s.docs.List(r.Context(), id.OrgID, id.Subjects,
		docs.ListParams{Limit: 200})
	if err != nil {
		s.log.ErrorContext(r.Context(), "load link candidates", "error", err)
	}

	// What was said, and when. The history somebody wants before ringing
	// somebody again.
	history, err := s.calls.ForContact(r.Context(), id.OrgID, id.Subjects, contactID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load call history", "error", err)
	}

	pg := page{
		Title: c.Name, Contact: c, Links: linked,
		Companies: companies, Assignable: assignable,
		Calls: history, CallOutcomes: calls.Outcomes,
	}
	if s.connect != nil {
		// Messages from this person's own mailbox that name this contact. Only they see them.
		if lm, err := s.connect.MailFor(r.Context(), id.OrgID, id.PrincipalID, contactID, 25); err == nil {
			pg.LinkedMail = lm
		}
	}
	s.salesFor(r, c.Ref(), &pg)
	s.render(w, r, "contact", pg)
}

func (s *Server) updateContact(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	contactID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	c := crm.Contact{
		ID: contactID, Name: r.FormValue("name"), Email: r.FormValue("email"),
		Phone: r.FormValue("phone"), Title: r.FormValue("title"),
		Notes: r.FormValue("notes"),
	}
	if v := strings.TrimSpace(r.FormValue("company")); v != "" {
		c.CompanyID = &v
	}
	if err := s.crm.UpdateContact(r.Context(), id.OrgID, id.Subjects, c); err != nil {
		s.crmFail(w, r, err, "you cannot change that contact")
		return
	}
	http.Redirect(w, r, "/contacts/"+contactID, http.StatusSeeOther)
}

func (s *Server) deleteContact(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := s.crm.DeleteContact(r.Context(), id.OrgID, id.Subjects, r.PathValue("id")); err != nil {
		s.crmFail(w, r, err, "you cannot remove that contact")
		return
	}
	http.Redirect(w, r, "/contacts", http.StatusSeeOther)
}

// linkParty attaches a contact or a company to a document, in either direction.
//
// One handler, because "this vendor services that compressor" and "that
// compressor is serviced by this vendor" are the same row in entity_links, and
// two handlers would be two chances to write it the wrong way round.
func (s *Server) linkContact(w http.ResponseWriter, r *http.Request) {
	s.linkParty(w, r, core.EntityContact, "/contacts/")
}

func (s *Server) linkCompany(w http.ResponseWriter, r *http.Request) {
	s.linkParty(w, r, core.EntityCompany, "/companies/")
}

func (s *Server) linkParty(w http.ResponseWriter, r *http.Request,
	kind core.EntityType, back string) {

	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	partyID := r.PathValue("id")
	target := strings.TrimSpace(r.FormValue("target"))
	if target == "" {
		s.fail(w, r, http.StatusBadRequest, "nothing to link to")
		return
	}

	from := core.Ref{Type: core.EntityDocument, ID: target}
	to := core.Ref{Type: kind, ID: partyID}
	var err error
	if r.FormValue("unlink") == "1" {
		err = s.docs.UnlinkRefs(r.Context(), id.OrgID, id.Subjects, from, to, "concerns")
	} else {
		err = s.docs.Link(r.Context(), id.OrgID, id.Subjects, from, to, "concerns")
	}
	if err != nil {
		s.log.WarnContext(r.Context(), "link party refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot link those")
		return
	}
	http.Redirect(w, r, back+partyID, http.StatusSeeOther)
}

// -----------------------------------------------------------------------------
// Parts
// -----------------------------------------------------------------------------

func (s *Server) listParts(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	search := r.URL.Query().Get("q")

	list, err := s.crm.Parts(r.Context(), id.OrgID, search)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list parts", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the parts")
		return
	}
	suppliers, err := s.crm.Companies(r.Context(), id.OrgID, id.Subjects, "", "")
	if err != nil {
		s.log.ErrorContext(r.Context(), "load suppliers", "error", err)
	}
	s.render(w, r, "parts", page{
		Title: "Parts", Parts: list, Companies: suppliers, SearchText: search,
	})
}

func (s *Server) savePart(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	p := crm.Part{
		ID:  strings.TrimSpace(r.FormValue("id")),
		SKU: r.FormValue("sku"), Name: r.FormValue("name"),
		UnitCostCents: parseMoney(r.FormValue("cost")),
	}
	if v := strings.TrimSpace(r.FormValue("supplier")); v != "" {
		p.SupplierID = &v
	}
	if n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("reorder"))); err == nil {
		p.ReorderAt = n
	}
	if _, err := s.crm.SavePart(r.Context(), id.OrgID, id.Subjects, p); err != nil {
		s.crmFail(w, r, err, "you cannot change the parts list")
		return
	}
	http.Redirect(w, r, "/parts", http.StatusSeeOther)
}

func (s *Server) deletePart(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := s.crm.DeletePart(r.Context(), id.OrgID, id.Subjects, r.PathValue("id")); err != nil {
		s.crmFail(w, r, err, "you cannot remove that part")
		return
	}
	http.Redirect(w, r, "/parts", http.StatusSeeOther)
}

func (s *Server) adjustPart(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	delta, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("delta")), 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "that is not a quantity")
		return
	}
	if _, err := s.crm.Adjust(r.Context(), id.OrgID, id.Subjects,
		r.PathValue("id"), delta, r.FormValue("note")); err != nil {
		s.crmFail(w, r, err, "you cannot adjust the stock")
		return
	}
	http.Redirect(w, r, "/parts", http.StatusSeeOther)
}

// usePart fits a part to a job, or books one in against a purchase order. One
// form, because both are a quantity against a document and the difference is
// which button was pressed.
func (s *Server) usePart(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	partID := strings.TrimSpace(r.FormValue("part"))
	quantity, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("quantity")), 64)
	if err != nil || quantity <= 0 {
		s.fail(w, r, http.StatusBadRequest, "that is not a quantity")
		return
	}

	switch r.FormValue("action") {
	case "receive":
		_, err = s.crm.Receive(r.Context(), id.OrgID, id.Subjects, docID, partID,
			quantity, parseMoney(r.FormValue("cost")), r.FormValue("note"))
	case "return":
		_, err = s.crm.Return(r.Context(), id.OrgID, id.Subjects, docID, partID,
			quantity, r.FormValue("note"))
	default:
		_, err = s.crm.Consume(r.Context(), id.OrgID, id.Subjects, docID, partID,
			quantity, r.FormValue("note"))
	}
	if err != nil {
		s.crmFail(w, r, err, "you cannot record parts against this")
		return
	}
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

func (s *Server) removeMovement(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := s.crm.RemoveMovement(r.Context(), id.OrgID, id.Subjects, r.PathValue("entry")); err != nil {
		s.crmFail(w, r, err, "you cannot remove that record")
		return
	}
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

// -----------------------------------------------------------------------------

func (s *Server) crmFail(w http.ResponseWriter, r *http.Request, err error, forbidden string) {
	switch {
	case errors.Is(err, crm.ErrDuplicate):
		s.fail(w, r, http.StatusConflict, strings.TrimPrefix(err.Error(), "crm: "))
	case errors.Is(err, crm.ErrInvalid):
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "crm: "))
	case errors.Is(err, crm.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such thing")
	default:
		s.log.WarnContext(r.Context(), "crm refused", "error", err)
		s.fail(w, r, http.StatusForbidden, forbidden)
	}
}

// parseMoney reads pounds and pence into cents.
//
// Through a float and back, because somebody types 12.50 and the alternative is
// a parser that treats "12.5" as twelve pounds and five pence. Rounded rather
// than truncated, so 0.1 does not become nine pence.
func parseMoney(v string) int {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "£"))
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0
	}
	return int(f*100 + 0.5)
}
