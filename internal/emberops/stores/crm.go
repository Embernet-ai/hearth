package stores

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
)

// CRM keeps companies (Customer for customers, Supplier for every other kind),
// contacts (Contact), parts (Item) and part movements (Stock Entry) in
// EmberOPS.
//
// What changes from the Postgres back end: companies and contacts carry no
// Hearth grants here. Who may see or change them is EmberOPS's own role
// permissions (Sales, Purchase, Stock), as in any ERPNext site, and Hearth does
// not filter these lists by its grants. Parts and movements check the document
// a movement is filed against, as before.
//
// A movement is a Stock Entry, submitted so stock moves; removing one cancels
// it. Quantity on hand is the Bin balance, "used" the quantity issued from
// stock.
type CRM struct {
	crm.Store
	Base
	Dir   Directory
	Authz plantAuthorizer
	Docs  docs.Store
	// CurrencyPerCent is the value of one cent in the site's currency.
	CurrencyPerCent float64
}

type plantAuthorizer interface {
	Require(ctx context.Context, subjects core.Subjects, ref core.Ref, required core.AccessLevel) error
}

var _ crm.Store = (*CRM)(nil)

func (s *CRM) perCent() float64 {
	if s.CurrencyPerCent <= 0 {
		return 0.01
	}
	return s.CurrencyPerCent
}

func (s *CRM) cents(v float64) int { return int(math.Round(v / s.perCent())) }

// ---- companies ----

var companyFields = []string{"name", "hearth_id", "hearth_company_kind", "hearth_domain", "hearth_notes", "hearth_deleted_at", "creation", "modified"}

func companyDoctype(kind string) string {
	if kind == "customer" {
		return "Customer"
	}
	return "Supplier"
}

func (s *CRM) companyRows(ctx context.Context, extra ...emberops.Filter) ([]companyRow, error) {
	var out []companyRow
	for _, dt := range []string{"Customer", "Supplier"} {
		fields := append([]string{}, companyFields...)
		fields = append(fields, map[string]string{"Customer": "customer_name", "Supplier": "supplier_name"}[dt])
		rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: dt, Fields: fields, OrderBy: "name asc",
			Filters: append([]emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"}, {Field: "hearth_deleted_at", Op: "is", Value: "not set"}}, extra...)}, 5000)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, companyRow{dt: dt, Row: r})
		}
	}
	return out, nil
}

type companyRow struct {
	dt string
	emberops.Row
}

func (s *CRM) company(r companyRow, contacts int) *crm.Company {
	name := r.S("customer_name")
	if r.dt == "Supplier" {
		name = r.S("supplier_name")
	}
	kind := r.S("hearth_company_kind")
	if kind == "" {
		kind = "vendor"
		if r.dt == "Customer" {
			kind = "customer"
		}
	}
	return &crm.Company{ID: r.S("hearth_id"), Name: name, Domain: r.S("hearth_domain"), Kind: kind, Notes: r.S("hearth_notes"),
		Contacts: contacts, CreatedAt: r.T("creation", s.loc()), UpdatedAt: r.T("modified", s.loc())}
}

func (s *CRM) findCompany(ctx context.Context, id string) (*companyRow, error) {
	for _, dt := range []string{"Customer", "Supplier"} {
		fields := append([]string{}, companyFields...)
		fields = append(fields, map[string]string{"Customer": "customer_name", "Supplier": "supplier_name"}[dt])
		rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: dt, Fields: fields, Limit: 1,
			Filters: []emberops.Filter{emberops.F("hearth_id", strings.ToLower(id)), {Field: "hearth_deleted_at", Op: "is", Value: "not set"}}})
		if err != nil {
			return nil, err
		}
		if len(rows) == 1 {
			return &companyRow{dt: dt, Row: rows[0]}, nil
		}
	}
	return nil, crm.ErrNotFound
}

// contactCounts is how many contacts each company has, from one read of the
// contacts' links.
func (s *CRM) contactCounts(ctx context.Context) (map[string]int, error) {
	links, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Dynamic Link", Parent: "Contact", Fields: []string{"parent", "link_doctype", "link_name"},
		Filters: []emberops.Filter{{Field: "link_doctype", Op: "in", Value: []any{"Customer", "Supplier"}}}}, 0)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, l := range links {
		out[l.S("link_doctype")+"\x00"+l.S("link_name")]++
	}
	return out, nil
}

func (s *CRM) Companies(ctx context.Context, orgID string, actor core.Subjects, kind, search string) ([]*crm.Company, error) {
	rows, err := s.companyRows(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := s.contactCounts(ctx)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(search))
	var out []*crm.Company
	for _, r := range rows {
		c := s.company(r, counts[r.dt+"\x00"+r.S("name")])
		if kind != "" && c.Kind != kind {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(c.Name), q) && !strings.Contains(c.Domain, q) {
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func (s *CRM) GetCompany(ctx context.Context, orgID string, actor core.Subjects, id string) (*crm.Company, error) {
	r, err := s.findCompany(ctx, id)
	if err != nil {
		return nil, err
	}
	counts, err := s.contactCounts(ctx)
	if err != nil {
		return nil, err
	}
	return s.company(*r, counts[r.dt+"\x00"+r.S("name")]), nil
}

func normaliseDomain(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, p := range []string{"https://", "http://", "www."} {
		v = strings.TrimPrefix(v, p)
	}
	if i := strings.IndexAny(v, "/?#"); i >= 0 {
		v = v[:i]
	}
	return v
}

func (s *CRM) saveCompany(ctx context.Context, id string, c crm.Company) error {
	rec := mapping.Rec{"id": id, "name": c.Name, "kind": c.Kind, "domain": c.Domain, "notes": c.Notes}
	doc := mapping.TZ{Loc: s.loc()}.CompanyDoc(rec)
	dt, _ := doc["doctype"].(string)
	delete(doc, "doctype")
	delete(doc, "hearth_id")
	if c.Domain == "" {
		doc["hearth_domain"] = nil
	}
	if c.Notes == "" {
		doc["hearth_notes"] = nil
	}
	if _, err := s.C.Upsert(ctx, dt, id, doc); err != nil {
		if errors.Is(err, emberops.ErrDuplicate) {
			return crm.ErrDuplicate
		}
		if isForbidden(err) {
			return crm.ErrNotFound
		}
		return err
	}
	return nil
}

func (s *CRM) CreateCompany(ctx context.Context, orgID string, actor core.Subjects, c crm.Company) (*crm.Company, error) {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return nil, fmt.Errorf("%w: a company needs a name", crm.ErrInvalid)
	}
	if c.Kind == "" {
		c.Kind = "vendor"
	}
	c.Domain = normaliseDomain(c.Domain)
	if c.Domain != "" {
		if rows, err := s.companyRows(ctx, emberops.F("hearth_domain", c.Domain)); err != nil {
			return nil, err
		} else if len(rows) > 0 {
			return nil, crm.ErrDuplicate
		}
	}
	c.ID = uuid.NewString()
	if err := s.saveCompany(ctx, c.ID, c); err != nil {
		return nil, err
	}
	c.OrgID, c.CreatedAt = orgID, time.Now()
	return &c, nil
}

func (s *CRM) UpdateCompany(ctx context.Context, orgID string, actor core.Subjects, c crm.Company) error {
	cur, err := s.findCompany(ctx, c.ID)
	if err != nil {
		return err
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return fmt.Errorf("%w: a company needs a name", crm.ErrInvalid)
	}
	if c.Kind == "" {
		c.Kind = "vendor"
	}
	if companyDoctype(c.Kind) != cur.dt {
		// A Customer and a Supplier are different documents in ERPNext.
		return fmt.Errorf("%w: a customer cannot become a vendor or the reverse; make a new company", crm.ErrInvalid)
	}
	c.Domain = normaliseDomain(c.Domain)
	if c.Domain != "" {
		rows, err := s.companyRows(ctx, emberops.F("hearth_domain", c.Domain))
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.S("hearth_id") != strings.ToLower(c.ID) {
				return crm.ErrDuplicate
			}
		}
	}
	return s.saveCompany(ctx, c.ID, c)
}

func (s *CRM) softDelete(ctx context.Context, doctype, id string) error {
	now := time.Now()
	_, err := s.C.Upsert(ctx, doctype, id, map[string]any{"hearth_deleted_at": s.ts(&now)})
	return err
}

func (s *CRM) DeleteCompany(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	cur, err := s.findCompany(ctx, id)
	if err != nil {
		return err
	}
	return s.softDelete(ctx, cur.dt, id)
}

// ---- contacts ----

var contactFields = []string{"name", "hearth_id", "first_name", "last_name", "email_id", "phone", "designation", "hearth_notes",
	"hearth_deleted_at", "creation", "modified"}

func (s *CRM) contacts(ctx context.Context, rows []emberops.Row) ([]*crm.Contact, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	var names []any
	for _, r := range rows {
		names = append(names, r.S("name"))
	}
	links := map[string][2]string{} // contact name -> company doctype, name
	for len(names) > 0 {
		n := min(len(names), 200)
		ls, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Dynamic Link", Parent: "Contact", Fields: []string{"parent", "link_doctype", "link_name"},
			Filters: []emberops.Filter{{Field: "parent", Op: "in", Value: names[:n]}, {Field: "link_doctype", Op: "in", Value: []any{"Customer", "Supplier"}}}}, 0)
		if err != nil {
			return nil, err
		}
		for _, l := range ls {
			links[l.S("parent")] = [2]string{l.S("link_doctype"), l.S("link_name")}
		}
		names = names[n:]
	}
	titles := map[string]string{}
	ids := map[string]string{}
	for _, dt := range []string{"Customer", "Supplier"} {
		var in []any
		for _, l := range links {
			if l[0] == dt {
				in = append(in, l[1])
			}
		}
		if len(in) == 0 {
			continue
		}
		field := map[string]string{"Customer": "customer_name", "Supplier": "supplier_name"}[dt]
		got, err := s.C.List(ctx, emberops.ListQuery{Doctype: dt, Fields: []string{"name", "hearth_id", field}, Limit: 500,
			Filters: []emberops.Filter{{Field: "name", Op: "in", Value: in}}})
		if err != nil {
			return nil, err
		}
		for _, g := range got {
			titles[dt+"\x00"+g.S("name")], ids[dt+"\x00"+g.S("name")] = g.S(field), idOf(g)
		}
	}
	out := make([]*crm.Contact, 0, len(rows))
	for _, r := range rows {
		c := &crm.Contact{ID: idOf(r), Name: strings.TrimSpace(r.S("first_name") + " " + r.S("last_name")), Email: r.S("email_id"),
			Phone: r.S("phone"), Title: r.S("designation"), Notes: r.S("hearth_notes"),
			CreatedAt: r.T("creation", s.loc()), UpdatedAt: r.T("modified", s.loc())}
		if l, ok := links[r.S("name")]; ok {
			id := ids[l[0]+"\x00"+l[1]]
			c.CompanyID, c.CompanyName = &id, titles[l[0]+"\x00"+l[1]]
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *CRM) Contacts(ctx context.Context, orgID string, actor core.Subjects, companyID, search string) ([]*crm.Contact, error) {
	filters := []emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"}, {Field: "hearth_deleted_at", Op: "is", Value: "not set"}}
	rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Contact", Fields: contactFields, OrderBy: "first_name asc", Filters: filters}, 5000)
	if err != nil {
		return nil, err
	}
	all, err := s.contacts(ctx, rows)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(search))
	var out []*crm.Contact
	for _, c := range all {
		if companyID != "" && (c.CompanyID == nil || !strings.EqualFold(*c.CompanyID, companyID)) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(c.Name), q) && !strings.Contains(strings.ToLower(c.Email), q) &&
			!strings.Contains(strings.ToLower(c.CompanyName), q) {
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func (s *CRM) oneContact(ctx context.Context, id string) (*crm.Contact, error) {
	rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Contact", Fields: contactFields, Limit: 1,
		Filters: []emberops.Filter{emberops.F("hearth_id", strings.ToLower(id)), {Field: "hearth_deleted_at", Op: "is", Value: "not set"}}})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, crm.ErrNotFound
	}
	cs, err := s.contacts(ctx, rows)
	if err != nil || len(cs) == 0 {
		return nil, err
	}
	return cs[0], nil
}

func (s *CRM) GetContact(ctx context.Context, orgID string, actor core.Subjects, id string) (*crm.Contact, error) {
	return s.oneContact(ctx, id)
}

func (s *CRM) ByEmail(ctx context.Context, orgID, email string) (*crm.Contact, error) {
	rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Contact", Fields: contactFields, Limit: 1,
		Filters: []emberops.Filter{emberops.F("email_id", strings.TrimSpace(email)), {Field: "hearth_id", Op: "is", Value: "set"},
			{Field: "hearth_deleted_at", Op: "is", Value: "not set"}}})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	cs, err := s.contacts(ctx, rows)
	if err != nil || len(cs) == 0 {
		return nil, err
	}
	return cs[0], nil
}

func (s *CRM) saveContact(ctx context.Context, c crm.Contact) error {
	rec := mapping.Rec{"id": c.ID, "name": c.Name, "email": c.Email, "phone": c.Phone, "title": c.Title, "notes": c.Notes}
	if c.CompanyID != nil && *c.CompanyID != "" {
		co, err := s.findCompany(ctx, *c.CompanyID)
		if err != nil {
			return fmt.Errorf("%w: that company does not exist", crm.ErrInvalid)
		}
		rec["company_id"], rec["company_kind"] = *c.CompanyID, co.S("hearth_company_kind")
		if co.dt == "Customer" {
			rec["company_kind"] = "customer"
		}
	}
	doc := mapping.TZ{Loc: s.loc()}.ContactDoc(rec)
	delete(doc, "doctype")
	delete(doc, "hearth_id")
	if c.Title == "" {
		doc["designation"] = nil
	}
	if c.Notes == "" {
		doc["hearth_notes"] = nil
	}
	if _, err := s.C.Upsert(ctx, "Contact", c.ID, doc); err != nil {
		if isForbidden(err) {
			return crm.ErrNotFound
		}
		return err
	}
	return nil
}

func (s *CRM) CreateContact(ctx context.Context, orgID string, actor core.Subjects, c crm.Contact) (*crm.Contact, error) {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return nil, fmt.Errorf("%w: a contact needs a name", crm.ErrInvalid)
	}
	c.ID = uuid.NewString()
	if err := s.saveContact(ctx, c); err != nil {
		return nil, err
	}
	c.OrgID, c.CreatedAt = orgID, time.Now()
	return &c, nil
}

func (s *CRM) UpdateContact(ctx context.Context, orgID string, actor core.Subjects, c crm.Contact) error {
	if _, err := s.oneContact(ctx, c.ID); err != nil {
		return err
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return fmt.Errorf("%w: a contact needs a name", crm.ErrInvalid)
	}
	return s.saveContact(ctx, c)
}

func (s *CRM) DeleteContact(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	if _, err := s.oneContact(ctx, id); err != nil {
		return err
	}
	return s.softDelete(ctx, "Contact", id)
}

// ---- parts ----

var itemFields = []string{"name", "hearth_id", "item_code", "item_name", "valuation_rate", "hearth_reorder_at", "hearth_supplier_id", "hearth_deleted_at"}

func (s *CRM) parts(ctx context.Context, rows []emberops.Row) ([]*crm.Part, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	var codes, suppliers []any
	var supplierNames []string
	for _, r := range rows {
		codes = append(codes, r.S("item_code"))
		if r.Has("hearth_supplier_id") {
			supplierNames = append(supplierNames, r.S("hearth_supplier_id"))
		}
	}
	// On hand is the Bin balance across warehouses; used is what was issued.
	onHand := map[string]float64{}
	used := map[string]float64{}
	for len(codes) > 0 {
		n := min(len(codes), 200)
		bins, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Bin", Fields: []string{"item_code", "actual_qty"},
			Filters: []emberops.Filter{{Field: "item_code", Op: "in", Value: codes[:n]}}}, 0)
		if err != nil {
			return nil, err
		}
		for _, b := range bins {
			onHand[b.S("item_code")] += b.F("actual_qty")
		}
		issued, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Stock Entry Detail", Parent: "Stock Entry", Fields: []string{"item_code", "qty", "s_warehouse", "t_warehouse"},
			Filters: []emberops.Filter{{Field: "item_code", Op: "in", Value: codes[:n]}, {Field: "s_warehouse", Op: "is", Value: "set"}}}, 0)
		if err != nil {
			return nil, err
		}
		for _, d := range issued {
			if !d.Has("t_warehouse") {
				used[d.S("item_code")] += d.F("qty")
			}
		}
		codes = codes[n:]
	}
	names := map[string]string{}
	if len(supplierNames) > 0 {
		for _, n := range supplierNames {
			suppliers = append(suppliers, n)
		}
		got, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Supplier", Fields: []string{"name", "hearth_id", "supplier_name"}, Limit: 500,
			Filters: []emberops.Filter{{Field: "hearth_id", Op: "in", Value: suppliers}}})
		if err != nil {
			return nil, err
		}
		for _, g := range got {
			names[idOf(g)] = g.S("supplier_name")
		}
	}
	out := make([]*crm.Part, 0, len(rows))
	for _, r := range rows {
		p := &crm.Part{ID: idOf(r), SKU: r.S("item_code"), Name: r.S("item_name"), UnitCostCents: s.cents(r.F("valuation_rate")),
			ReorderAt: int(r.I("hearth_reorder_at")), OnHand: onHand[r.S("item_code")], Used: used[r.S("item_code")]}
		// The supplier is a company in Hearth's CRM, which stays in Hearth, so the part keeps its id and not a Supplier link
		// (a part whose supplier has no Supplier in EmberOps would be refused). The name is EmberOps's when it has the
		// Supplier, and otherwise left to the page to look up.
		if sup := r.S("hearth_supplier_id"); sup != "" {
			id := sup
			p.SupplierID, p.SupplierName = &id, names[sup]
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *CRM) Parts(ctx context.Context, orgID, search string) ([]*crm.Part, error) {
	rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Item", Fields: itemFields, OrderBy: "item_name asc",
		Filters: []emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"}, {Field: "hearth_deleted_at", Op: "is", Value: "not set"}}}, 5000)
	if err != nil {
		return nil, err
	}
	ps, err := s.parts(ctx, rows)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(search))
	if q == "" {
		return ps, nil
	}
	var out []*crm.Part
	for _, p := range ps {
		if strings.Contains(strings.ToLower(p.Name), q) || strings.Contains(strings.ToLower(p.SKU), q) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *CRM) itemRow(ctx context.Context, id string) (emberops.Row, error) {
	rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Item", Fields: itemFields, Limit: 1,
		Filters: []emberops.Filter{emberops.F("hearth_id", strings.ToLower(id)), {Field: "hearth_deleted_at", Op: "is", Value: "not set"}}})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, crm.ErrNotFound
	}
	return rows[0], nil
}

func (s *CRM) GetPart(ctx context.Context, orgID, id string) (*crm.Part, error) {
	r, err := s.itemRow(ctx, id)
	if err != nil {
		return nil, err
	}
	ps, err := s.parts(ctx, []emberops.Row{r})
	if err != nil || len(ps) == 0 {
		return nil, err
	}
	return ps[0], nil
}

func (s *CRM) SavePart(ctx context.Context, orgID string, actor core.Subjects, p crm.Part) (*crm.Part, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.SKU = strings.TrimSpace(p.SKU)
	if p.Name == "" {
		return nil, fmt.Errorf("%w: a part needs a name", crm.ErrInvalid)
	}
	if p.UnitCostCents < 0 || p.ReorderAt < 0 {
		return nil, fmt.Errorf("%w: cost and reorder point cannot be negative", crm.ErrInvalid)
	}
	if p.ID == "" {
		p.ID = uuid.NewString()
	} else if _, err := s.itemRow(ctx, p.ID); err != nil {
		return nil, err
	}
	rec := mapping.Rec{"id": p.ID, "sku": p.SKU, "name": p.Name, "unit_cost_cents": float64(p.UnitCostCents), "reorder_at": float64(p.ReorderAt)}
	if p.SupplierID != nil && *p.SupplierID != "" {
		rec["supplier_id"] = *p.SupplierID
	}
	doc := mapping.TZ{Loc: s.loc()}.PartDoc(rec, s.perCent())
	delete(doc, "doctype")
	delete(doc, "hearth_id")
	if p.SupplierID == nil || *p.SupplierID == "" {
		doc["hearth_supplier_id"] = nil
	}
	if _, err := s.C.Upsert(ctx, "Item", p.ID, doc); err != nil {
		if errors.Is(err, emberops.ErrDuplicate) {
			return nil, crm.ErrDuplicate
		}
		return nil, err
	}
	return s.GetPart(ctx, orgID, p.ID)
}

func (s *CRM) DeletePart(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	if _, err := s.itemRow(ctx, id); err != nil {
		return err
	}
	return s.softDelete(ctx, "Item", id)
}

// ---- movements ----

func (s *CRM) move(ctx context.Context, orgID string, actor core.Subjects, docID, partID string, quantity float64, reason, note string) (*crm.Movement, error) {
	if actor.Principal() == "" {
		return nil, errors.New("crm: no acting principal")
	}
	rec := mapping.Rec{"id": uuid.NewString(), "reason": reason, "note": strings.TrimSpace(note), "quantity": quantity,
		"moved_at": time.Now().UTC().Format(time.RFC3339)}
	if docID != "" {
		if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessComment); err != nil {
			return nil, err
		}
		d, err := s.Docs.Get(ctx, orgID, actor, docID)
		if err != nil {
			return nil, crm.ErrNotFound
		}
		rec["document_id"], rec["target_kind"] = docID, targetKind(d.Kind)
	}
	item, err := s.itemRow(ctx, partID)
	if err != nil {
		return nil, err
	}
	cost := s.cents(item.F("valuation_rate"))
	rec["unit_cost_cents"] = float64(cost)
	id := rec.S("id")
	doc := mapping.TZ{Loc: s.loc()}.PartMovementDoc(rec, item.S("item_code"), "", s.perCent())
	delete(doc, "doctype")
	delete(doc, "hearth_id")
	res, err := s.C.Upsert(ctx, "Stock Entry", id, doc)
	if err != nil {
		if isForbidden(err) {
			return nil, crm.ErrNotFound
		}
		return nil, err
	}
	// Submitting is what moves stock.
	if _, err := s.C.Update(ctx, "Stock Entry", res.Name, map[string]any{"docstatus": 1}); err != nil {
		return nil, fmt.Errorf("crm: post the stock entry: %w", err)
	}
	m := &crm.Movement{ID: id, PartID: partID, PartName: item.S("item_name"), SKU: item.S("item_code"), Quantity: quantity,
		UnitCostCents: cost, Reason: reason, Note: strings.TrimSpace(note), MovedAt: time.Now(), MovedBy: actor.Principal()}
	if docID != "" {
		m.DocumentID = &docID
	}
	return m, nil
}

func positive(q float64, what string) error {
	if q <= 0 {
		return fmt.Errorf("%w: %s must be more than nothing", crm.ErrInvalid, what)
	}
	return nil
}

func (s *CRM) Consume(ctx context.Context, orgID string, actor core.Subjects, docID, partID string, quantity float64, note string) (*crm.Movement, error) {
	if err := positive(quantity, "the quantity"); err != nil {
		return nil, err
	}
	if docID == "" {
		return nil, fmt.Errorf("%w: say which job used it", crm.ErrInvalid)
	}
	return s.move(ctx, orgID, actor, docID, partID, -quantity, crm.ReasonConsumed, note)
}

func (s *CRM) Receive(ctx context.Context, orgID string, actor core.Subjects, docID, partID string, quantity float64, unitCostCents int, note string) (*crm.Movement, error) {
	if err := positive(quantity, "the quantity"); err != nil {
		return nil, err
	}
	m, err := s.move(ctx, orgID, actor, docID, partID, quantity, crm.ReasonReceived, note)
	if err == nil && unitCostCents > 0 {
		// A receipt at a new price revalues the part.
		_, _ = s.C.Upsert(ctx, "Item", partID, map[string]any{"valuation_rate": float64(unitCostCents) * s.perCent()})
		m.UnitCostCents = unitCostCents
	}
	return m, err
}

func (s *CRM) Return(ctx context.Context, orgID string, actor core.Subjects, docID, partID string, quantity float64, note string) (*crm.Movement, error) {
	if err := positive(quantity, "the quantity"); err != nil {
		return nil, err
	}
	return s.move(ctx, orgID, actor, docID, partID, quantity, crm.ReasonReturned, note)
}

func (s *CRM) Adjust(ctx context.Context, orgID string, actor core.Subjects, partID string, delta float64, note string) (*crm.Movement, error) {
	if delta == 0 {
		return nil, fmt.Errorf("%w: nothing to adjust", crm.ErrInvalid)
	}
	if strings.TrimSpace(note) == "" {
		return nil, fmt.Errorf("%w: say why the count changed", crm.ErrInvalid)
	}
	return s.move(ctx, orgID, actor, "", partID, delta, crm.ReasonAdjusted, note)
}

func (s *CRM) RemoveMovement(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	r, err := s.C.GetByHearthID(ctx, "Stock Entry", id)
	if err != nil {
		return notFound(err, crm.ErrNotFound)
	}
	if wo := r.S("hearth_maintenance_work_order"); wo != "" {
		ids, err := s.Dir.HearthIDs(ctx, "Maintenance Work Order", []string{wo})
		if err != nil {
			return err
		}
		if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: ids[wo]}, core.AccessEdit); err != nil {
			return err
		}
	}
	// A submitted entry is cancelled, which puts the stock back; a draft is deleted.
	if r.I("docstatus") == 1 {
		return s.C.Call(ctx, "frappe.client.cancel", map[string]any{"doctype": "Stock Entry", "name": r.S("name")}, nil)
	}
	return s.C.Delete(ctx, "Stock Entry", r.S("name"))
}

var movementFields = []string{"name", "hearth_id", "stock_entry_type", "posting_date", "remarks", "hearth_maintenance_work_order",
	"owner", "creation", "docstatus"}

// movements turns Stock Entry headers and their item rows into movements, in
// two reads for the whole set.
func (s *CRM) movements(ctx context.Context, heads []emberops.Row, only string) ([]crm.Movement, error) {
	var live []emberops.Row
	for _, h := range heads {
		if h.I("docstatus") != 2 { // cancelled entries are gone
			live = append(live, h)
		}
	}
	if len(live) == 0 {
		return nil, nil
	}
	var names []any
	for _, h := range live {
		names = append(names, h.S("name"))
	}
	var items []emberops.Row
	for len(names) > 0 {
		n := min(len(names), 200)
		got, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Stock Entry Detail", Parent: "Stock Entry",
			Fields:  []string{"parent", "item_code", "item_name", "qty", "basic_rate"},
			Filters: []emberops.Filter{{Field: "parent", Op: "in", Value: names[:n]}}}, 0)
		if err != nil {
			return nil, err
		}
		items = append(items, got...)
		names = names[n:]
	}
	byParent := map[string]emberops.Row{}
	var codes []any
	for _, it := range items {
		byParent[it.S("parent")] = it
		codes = append(codes, it.S("item_code"))
	}
	partIDs := map[string]string{}
	if len(codes) > 0 {
		got, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Item", Fields: []string{"item_code", "hearth_id"}, Limit: 500, Reference: true,
			Filters: []emberops.Filter{{Field: "item_code", Op: "in", Value: codes}}})
		if err != nil {
			return nil, err
		}
		for _, g := range got {
			partIDs[g.S("item_code")] = g.S("hearth_id")
		}
	}
	var woNames []string
	for _, h := range live {
		if h.Has("hearth_maintenance_work_order") {
			woNames = append(woNames, h.S("hearth_maintenance_work_order"))
		}
	}
	woIDs, err := s.Dir.HearthIDs(ctx, "Maintenance Work Order", woNames)
	if err != nil {
		return nil, err
	}
	titles := map[string]string{}
	if len(woNames) > 0 {
		var in []any
		for _, n := range woNames {
			in = append(in, n)
		}
		got, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Maintenance Work Order", Fields: []string{"name", "title"}, Limit: 500,
			Filters: []emberops.Filter{{Field: "name", Op: "in", Value: in}}})
		if err != nil {
			return nil, err
		}
		for _, g := range got {
			titles[g.S("name")] = g.S("title")
		}
	}
	users, err := s.Dir.UserIDs(ctx)
	if err != nil {
		return nil, err
	}
	names2, err := s.Dir.Names(ctx)
	if err != nil {
		return nil, err
	}
	var out []crm.Movement
	for _, h := range live {
		it, ok := byParent[h.S("name")]
		if !ok || (only != "" && partIDs[it.S("item_code")] != only) {
			continue
		}
		reason, note := mapping.ParseMovementRemarks(h.S("stock_entry_type"), h.S("remarks"))
		q := it.F("qty")
		if h.S("stock_entry_type") == "Material Issue" {
			q = -q
		}
		m := crm.Movement{ID: idOf(h), PartID: partIDs[it.S("item_code")], PartName: it.S("item_name"), SKU: it.S("item_code"),
			Quantity: q, UnitCostCents: s.cents(it.F("basic_rate")), Reason: reason, Note: note,
			MovedAt: h.T("creation", s.loc()), MovedBy: users[h.S("owner")]}
		m.ByName = names2[m.MovedBy]
		if wo := h.S("hearth_maintenance_work_order"); wo != "" {
			id := woIDs[wo]
			m.DocumentID, m.DocumentTitle = &id, titles[wo]
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].MovedAt.After(out[j].MovedAt) })
	return out, nil
}

func (s *CRM) woName(ctx context.Context, docID string) (string, error) {
	return s.C.Resolve(ctx, "Maintenance Work Order", docID)
}

func (s *CRM) MovementsFor(ctx context.Context, orgID string, actor core.Subjects, docID string) ([]crm.Movement, error) {
	if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessView); err != nil {
		return nil, crm.ErrNotFound
	}
	name, err := s.woName(ctx, docID)
	if err != nil || name == "" {
		return nil, err
	}
	heads, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Stock Entry", Fields: movementFields,
		Filters: []emberops.Filter{emberops.F("hearth_maintenance_work_order", name)}}, 0)
	if err != nil {
		return nil, err
	}
	return s.movements(ctx, heads, "")
}

func (s *CRM) HistoryFor(ctx context.Context, orgID, partID string, limit int) ([]crm.Movement, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	item, err := s.itemRow(ctx, partID)
	if err != nil {
		return nil, err
	}
	rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Stock Entry Detail", Parent: "Stock Entry", Fields: []string{"parent"},
		Filters: []emberops.Filter{emberops.F("item_code", item.S("item_code"))}}, 5000)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	var parents []any
	for _, r := range rows {
		parents = append(parents, r.S("parent"))
	}
	var heads []emberops.Row
	for len(parents) > 0 {
		n := min(len(parents), 200)
		got, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Stock Entry", Fields: movementFields, Limit: 500,
			Filters: []emberops.Filter{{Field: "name", Op: "in", Value: parents[:n]}, {Field: "hearth_id", Op: "is", Value: "set"}}})
		if err != nil {
			return nil, err
		}
		heads = append(heads, got...)
		parents = parents[n:]
	}
	out, err := s.movements(ctx, heads, partID)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, err
}

// cost is what consumption less returns came to, never below nothing.
func cost(ms []crm.Movement) int {
	total := 0
	for _, m := range ms {
		if m.Reason == crm.ReasonConsumed || m.Reason == crm.ReasonReturned {
			total += int(math.Round(-m.Quantity * float64(m.UnitCostCents)))
		}
	}
	if total < 0 {
		return 0
	}
	return total
}

func (s *CRM) PartsCost(ctx context.Context, orgID, docID string) (int, error) {
	name, err := s.woName(ctx, docID)
	if err != nil || name == "" {
		return 0, err
	}
	heads, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Stock Entry", Fields: movementFields,
		Filters: []emberops.Filter{emberops.F("hearth_maintenance_work_order", name)}}, 0)
	if err != nil {
		return 0, err
	}
	ms, err := s.movements(ctx, heads, "")
	return cost(ms), err
}

// PartsCostForAsset is the parts on every work order that concerns the asset.
func (s *CRM) PartsCostForAsset(ctx context.Context, orgID, assetID string) (int, error) {
	asset, err := s.C.Resolve(ctx, "Asset", assetID)
	if err != nil || asset == "" {
		return 0, err
	}
	wos, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Maintenance Work Order", Fields: []string{"name"},
		Filters: []emberops.Filter{emberops.F("asset", asset), {Field: "hearth_deleted_at", Op: "is", Value: "not set"}}}, 0)
	if err != nil || len(wos) == 0 {
		return 0, err
	}
	var in []any
	for _, w := range wos {
		in = append(in, w.S("name"))
	}
	heads, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Stock Entry", Fields: movementFields,
		Filters: []emberops.Filter{{Field: "hearth_maintenance_work_order", Op: "in", Value: in}}}, 0)
	if err != nil {
		return 0, err
	}
	ms, err := s.movements(ctx, heads, "")
	return cost(ms), err
}

// PartiesFor lists the contacts and companies attached to a document, from the
// links Hearth copies into EmberOPS.
func (s *CRM) PartiesFor(ctx context.Context, orgID string, actor core.Subjects, docID string) ([]crm.Party, error) {
	d, err := s.Docs.Get(ctx, orgID, actor, docID)
	if err != nil {
		return nil, crm.ErrNotFound
	}
	dt := doctypeFor(d.Kind)
	name, err := s.C.Resolve(ctx, dt, docID)
	if err != nil || name == "" {
		return nil, err
	}
	var other []string // doctype + name of the party ends
	for _, side := range [][2]string{{"from", "to"}, {"to", "from"}} {
		rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Hearth Entity Link", Fields: []string{side[1] + "_doctype", side[1] + "_name"},
			Filters: []emberops.Filter{emberops.F(side[0]+"_doctype", dt), emberops.F(side[0]+"_name", name),
				{Field: side[1] + "_doctype", Op: "in", Value: []any{"Contact", "Customer", "Supplier"}}}}, 0)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			other = append(other, r.S(side[1]+"_doctype")+"\x00"+r.S(side[1]+"_name"))
		}
	}
	var out []crm.Party
	for _, group := range []string{"Contact", "Customer", "Supplier"} {
		var in []any
		for _, o := range other {
			if dt2, n, _ := strings.Cut(o, "\x00"); dt2 == group {
				in = append(in, n)
			}
		}
		if len(in) == 0 {
			continue
		}
		fields := []string{"name", "hearth_id", "hearth_deleted_at"}
		switch group {
		case "Contact":
			fields = append(fields, "first_name", "last_name", "designation", "phone")
		case "Customer":
			fields = append(fields, "customer_name", "hearth_company_kind", "hearth_domain")
		default:
			fields = append(fields, "supplier_name", "hearth_company_kind", "hearth_domain")
		}
		rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: group, Fields: fields, Limit: 500,
			Filters: []emberops.Filter{{Field: "name", Op: "in", Value: in}}})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.Has("hearth_deleted_at") {
				continue
			}
			p := crm.Party{ID: idOf(r)}
			switch group {
			case "Contact":
				p.Type, p.Name = core.EntityContact, strings.TrimSpace(r.S("first_name")+" "+r.S("last_name"))
				p.Subtext = strings.Join(nonEmpty(r.S("designation"), r.S("phone")), " · ")
			default:
				p.Type, p.Name = core.EntityCompany, r.S(map[string]string{"Customer": "customer_name", "Supplier": "supplier_name"}[group])
				p.Subtext = strings.Join(nonEmpty(r.S("hearth_company_kind"), r.S("hearth_domain")), " · ")
			}
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func nonEmpty(in ...string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
