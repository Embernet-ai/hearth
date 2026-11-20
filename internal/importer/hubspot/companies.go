package hubspot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/crm"
)

type companyRow struct {
	id, name, domain, notes, website, linkedin string
	street, city, state, postcode, country     string
	currency, taxID, owner                     string
	revenue                                    *int64
	updated                                    time.Time
	deleted                                    bool
}

func (r companyRow) fields() map[string]string {
	rev := ""
	if r.revenue != nil {
		rev = itoa(*r.revenue)
	}
	return map[string]string{"name": r.name, "domain": r.domain, "website": r.website, "linkedin": r.linkedin,
		"street": r.street, "city": r.city, "state": r.state, "postcode": r.postcode, "country": r.country,
		"revenue": rev, "owner": r.owner}
}

func (r companyRow) details(set map[string]string) crm.Details {
	d := crm.Details{OwnerID: r.owner, Website: r.website, LinkedIn: r.linkedin, TaxID: r.taxID, Street: r.street, City: r.city,
		State: r.state, Postcode: r.postcode, Country: r.country, RevenueCents: r.revenue, Currency: r.currency}
	for f, v := range set {
		switch f {
		case "website":
			d.Website = v
		case "linkedin":
			d.LinkedIn = v
		case "street":
			d.Street = v
		case "city":
			d.City = v
		case "state":
			d.State = v
		case "postcode":
			d.Postcode = v
		case "country":
			d.Country = v
		case "owner":
			d.OwnerID = v
		case "revenue":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				d.RevenueCents = &n
			}
		}
	}
	return d
}

var detailFields = map[string]bool{"website": true, "linkedin": true, "street": true, "city": true, "state": true,
	"postcode": true, "country": true, "revenue": true, "owner": true}

func hasDetail(set map[string]string) bool {
	for f := range set {
		if detailFields[f] {
			return true
		}
	}
	return false
}

// companyWant is what HubSpot says about a company, in Hearth's fields. A field HubSpot has no value for is left out.
func (im *Importer) companyWant(o Object) map[string]string {
	p := o.Props
	w := map[string]string{}
	put := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			w[k] = v
		}
	}
	put("name", p["name"])
	dom := domainOf(p["domain"])
	if dom == "" {
		dom = domainOf(p["website"])
	}
	put("domain", dom)
	site := strings.TrimSpace(p["website"])
	if site == "" {
		site = strings.TrimSpace(p["domain"])
	}
	put("website", site)
	put("linkedin", p["linkedin_company_page"])
	put("street", p["address"])
	put("city", p["city"])
	put("state", p["state"])
	put("postcode", p["zip"])
	put("country", p["country"])
	if c, ok := centsOf(p["annualrevenue"]); ok {
		w["revenue"] = itoa(c)
	}
	if owner := im.ownerOf(p["hubspot_owner_id"]); owner != "" {
		w["owner"] = owner
	}
	return w
}

func (im *Importer) loadCompanies(ctx context.Context, ids []string) (map[string]companyRow, error) {
	out := map[string]companyRow{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := im.db.Query(ctx, `
		SELECT id::text, name, domain, notes, website, linkedin_url, address_street, address_city, address_state,
		       address_postcode, address_country, annual_revenue_cents, revenue_currency, tax_id,
		       coalesce(owner_id::text,''), updated_at, deleted_at IS NOT NULL
		  FROM companies WHERE org_id=$1 AND id = ANY($2::uuid[])`, im.org, ids)
	if err != nil {
		return nil, fmt.Errorf("read companies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r companyRow
		if err := rows.Scan(&r.id, &r.name, &r.domain, &r.notes, &r.website, &r.linkedin, &r.street, &r.city, &r.state,
			&r.postcode, &r.country, &r.revenue, &r.currency, &r.taxID, &r.owner, &r.updated, &r.deleted); err != nil {
			return nil, err
		}
		out[r.id] = r
	}
	return out, rows.Err()
}

func placeholderName(r companyRow) bool {
	n := strings.TrimSpace(r.name)
	return unnamed.MatchString(n) || (r.domain != "" && strings.EqualFold(n, r.domain))
}

func (im *Importer) companies(ctx context.Context, objs []Object, cnt *Count) error {
	type hit struct {
		r   ref
		via string
	}
	hits := map[string]hit{}
	adopt := map[string]string{} // HubSpot id to the unlinked Hearth company holding its web address
	var ids []string
	for _, o := range objs {
		if r, via, ok := im.lookup(Companies, o.ID); ok {
			hits[o.ID] = hit{r, via}
			if !strings.HasPrefix(r.id, "dry-run:") {
				ids = append(ids, r.id)
			}
			continue
		}
		w := im.companyWant(o)
		if holder, ok := im.domains[strings.ToLower(w["domain"])]; ok && w["domain"] != "" && !im.linked[holder] {
			adopt[o.ID] = holder
			ids = append(ids, holder)
		}
	}
	rows, err := im.loadCompanies(ctx, ids)
	if err != nil {
		return err
	}
	for _, o := range objs {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := im.companyWant(o)
		if h, ok := hits[o.ID]; ok {
			im.updateCompany(ctx, o, want, h.r, h.via, rows, cnt)
			continue
		}
		// The adoption is re-checked: an earlier record in this batch may have taken the holder.
		if holder, ok := adopt[o.ID]; ok && !im.linked[holder] {
			if row, found := rows[holder]; found && !row.deleted {
				im.linkCompany(ctx, o, want, row, cnt)
				continue
			}
		}
		im.createCompany(ctx, o, want, cnt)
	}
	return nil
}

// writeCompany applies the fields to a company through the service.
func (im *Importer) writeCompany(ctx context.Context, row companyRow, set map[string]string) (map[string]string, error) {
	rejected := map[string]string{}
	name, domain := row.name, row.domain
	if v, ok := set["name"]; ok {
		name = v
	}
	if v, ok := set["domain"]; ok {
		domain = v
	}
	if _, nameSet := set["name"]; nameSet || set["domain"] != "" {
		err := im.svc.UpdateCompany(ctx, im.org, im.actor, crm.Company{ID: row.id, Name: name, Domain: domain, Notes: row.notes})
		if errors.Is(err, crm.ErrDuplicate) && set["domain"] != "" {
			// Another company holds that web address. The rest still goes in.
			rejected["domain"] = set["domain"]
			delete(set, "domain")
			err = nil
			if _, nameSet := set["name"]; nameSet {
				err = im.svc.UpdateCompany(ctx, im.org, im.actor, crm.Company{ID: row.id, Name: name, Domain: row.domain, Notes: row.notes})
			}
		}
		if err != nil {
			return rejected, err
		}
	}
	if hasDetail(set) {
		if err := im.svc.SetCompanyDetails(ctx, im.org, im.actor, row.id, row.details(set)); err != nil {
			return rejected, err
		}
	}
	return rejected, nil
}

func (im *Importer) updateCompany(ctx context.Context, o Object, want map[string]string, r ref, via string,
	rows map[string]companyRow, cnt *Count) {

	if strings.HasPrefix(r.id, "dry-run:") {
		cnt.Same++
		return
	}
	row, ok := rows[r.id]
	if !ok {
		cnt.Failed++
		im.rep.problem("company %s: Hearth record %s is missing", o.ID, r.id)
		return
	}
	if row.deleted {
		cnt.skip("deleted in Hearth, left deleted")
		return
	}
	mode, snap := im.modeFor("companies", o.ID, via, row.updated)
	cur := row.fields()
	// A web address another company holds cannot be given to this one.
	rejected := map[string]string{}
	if d := strings.ToLower(want["domain"]); d != "" {
		if holder, taken := im.domains[d]; taken && holder != row.id && !fieldsEqual("domain", row.domain, d) {
			rejected["domain"] = want["domain"]
			delete(want, "domain")
		}
	}
	p := plan(want, cur, snap, mode, map[string]bool{"name": placeholderName(row)})
	for f, v := range rejected {
		// A company with no web address of its own that wants one another company holds is the usual case of two sites of
		// one firm; its website carries the address and there is nothing to decide, so it is not reported on every run.
		if f == "domain" && strings.TrimSpace(row.domain) == "" {
			continue
		}
		p.conflicts[f] = v
	}
	im.addConflicts("companies", o.ID, row.id, p, cur, mode)
	if len(p.conflicts) > 0 {
		cnt.Conflicts++
	}
	if p.changed() && im.opt.Apply {
		set := map[string]string{}
		for f, v := range p.set {
			set[f] = v
		}
		rej, err := im.writeCompany(ctx, row, set)
		if err != nil {
			cnt.Failed++
			im.rep.problem("company %s: %v", o.ID, err)
			return
		}
		for f, v := range rej {
			p.conflicts[f] = v
			delete(p.set, f)
			delete(p.next, f)
			im.addConflicts("companies", o.ID, row.id, fieldPlan{conflicts: map[string]string{f: v}}, cur, mode)
		}
		if d, ok := p.set["domain"]; ok {
			im.domains[strings.ToLower(d)] = row.id
		}
		if err := im.audit(ctx, "crm.hubspot_import.update", "company", row.id, "companies", o.ID, before(cur, p.set), p.set); err != nil {
			cnt.Failed++
			im.rep.problem("company %s: %v", o.ID, err)
			return
		}
	}
	if p.changed() {
		cnt.Update++
	} else {
		cnt.Same++
	}
	if err := im.record(ctx, "companies", o.ID, "company", row.id, p.next, o.Modified); err != nil {
		cnt.Failed++
		im.rep.problem("company %s: %v", o.ID, err)
	}
}

func before(cur, set map[string]string) map[string]string {
	b := map[string]string{}
	for f := range set {
		b[f] = cur[f]
	}
	return b
}

// linkCompany ties a company somebody made in Hearth to its HubSpot record and fills what it left empty.
func (im *Importer) linkCompany(ctx context.Context, o Object, want map[string]string, row companyRow, cnt *Count) {
	cur := row.fields()
	p := plan(want, cur, nil, fillOnly, map[string]bool{"name": placeholderName(row)})
	delete(p.set, "domain") // it already matched on the web address
	im.addConflicts("companies", o.ID, row.id, p, cur, fillOnly)
	if len(p.conflicts) > 0 {
		cnt.Conflicts++
	}
	if im.opt.Apply && p.changed() {
		if _, err := im.writeCompany(ctx, row, copyMap(p.set)); err != nil {
			cnt.Failed++
			im.rep.problem("company %s: %v", o.ID, err)
			return
		}
	}
	if err := im.audit(ctx, "crm.hubspot_import.link", "company", row.id, "companies", o.ID, before(cur, p.set), p.set); err != nil {
		cnt.Failed++
		im.rep.problem("company %s: %v", o.ID, err)
		return
	}
	cnt.Link++
	if err := im.record(ctx, "companies", o.ID, "company", row.id, p.next, o.Modified); err != nil {
		cnt.Failed++
		im.rep.problem("company %s: %v", o.ID, err)
	}
}

func copyMap(m map[string]string) map[string]string {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func (im *Importer) createCompany(ctx context.Context, o Object, want map[string]string, cnt *Count) {
	name := want["name"]
	if name == "" {
		name = want["domain"]
	}
	if name == "" {
		name = "(unnamed " + o.ID + ")"
	}
	// A web address another company holds stays on the website; the address is one company's.
	dom := strings.ToLower(want["domain"])
	if _, taken := im.domains[dom]; taken || dom == "" {
		delete(want, "domain")
		dom = ""
	}
	id := im.fakeID("company", o.ID)
	if im.opt.Apply {
		c, err := im.svc.CreateCompany(ctx, im.org, im.actor, crm.Company{Name: name, Domain: dom, Kind: crm.KindOther})
		if err != nil {
			cnt.Failed++
			im.rep.problem("company %s: %v", o.ID, err)
			return
		}
		id = c.ID
		row := companyRow{id: id, currency: "USD"}
		set := copyMap(want)
		if hasDetail(set) {
			if err := im.svc.SetCompanyDetails(ctx, im.org, im.actor, id, row.details(set)); err != nil {
				cnt.Failed++
				im.rep.problem("company %s: made %s but could not set its details: %v", o.ID, id, err)
				return
			}
		}
		if err := im.svc.Share(ctx, im.org, im.actor, c.Ref()); err != nil {
			cnt.Failed++
			im.rep.problem("company %s: made %s but could not share it with sales: %v", o.ID, id, err)
			return
		}
		if err := im.audit(ctx, "crm.hubspot_import.create", "company", id, "companies", o.ID, nil, want); err != nil {
			cnt.Failed++
			im.rep.problem("company %s: %v", o.ID, err)
			return
		}
	}
	if dom != "" {
		im.domains[dom] = id
	}
	im.madeCompanies[id] = ""
	next := copyMap(want)
	if _, ok := next["name"]; !ok {
		delete(next, "name")
	}
	cnt.Create++
	if err := im.record(ctx, "companies", o.ID, "company", id, next, o.Modified); err != nil {
		cnt.Failed++
		im.rep.problem("company %s: %v", o.ID, err)
	}
}

// companyID is the Hearth company a HubSpot company id is, after the companies phase and anything pulled in.
func (im *Importer) companyID(hid string) string {
	if r, _, ok := im.lookup(Companies, hid); ok {
		return r.id
	}
	return ""
}

// pullCompanies reads, by id, the companies that contacts or deals point at which Hearth does not have, and imports
// them first. Without it a contact whose company the first import dropped would be left with no company.
func (im *Importer) pullCompanies(ctx context.Context, ids []string) error {
	var missing []string
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if _, _, ok := im.lookup(Companies, id); !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	objs, err := im.hs.Get(ctx, Companies, props[Companies], missing)
	if err != nil {
		return fmt.Errorf("read the companies that are not in Hearth yet: %w", err)
	}
	cnt := im.rep.Objects[Companies]
	if cnt == nil {
		cnt = &Count{}
		im.rep.Objects[Companies] = cnt
	}
	cnt.Pulled += len(objs)
	return im.companies(ctx, objs, cnt)
}
