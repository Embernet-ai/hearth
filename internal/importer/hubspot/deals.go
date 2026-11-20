package hubspot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/crm"
)

type dealRow struct {
	id, name, stage, currency, closeDate, company, contact, owner, notes string
	amount                                                               int64
	updated                                                              time.Time
	deleted                                                              bool
}

func (r dealRow) fields() map[string]string {
	amt := ""
	if r.amount != 0 {
		amt = itoa(r.amount)
	}
	return map[string]string{"name": r.name, "stage": r.stage, "amount": amt, "close_date": r.closeDate,
		"owner": r.owner, "company": r.company, "contact": r.contact}
}

func (im *Importer) dealWant(o Object) map[string]string {
	p := o.Props
	w := map[string]string{}
	put := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			w[k] = v
		}
	}
	put("name", p["dealname"])
	if id := strings.TrimSpace(p["dealstage"]); id != "" {
		label := im.stages[id]
		if label == "" {
			label = id
		}
		if st, known := stageFor(label); known {
			w["stage"] = st
		} else {
			im.rep.UnknownStages[label]++
		}
	}
	if c, ok := centsOf(p["amount"]); ok && c > 0 {
		w["amount"] = itoa(c)
	}
	put("close_date", day(p["closedate"]))
	if owner := im.ownerOf(p["hubspot_owner_id"]); owner != "" {
		w["owner"] = owner
	}
	if a := o.Assoc[Companies]; len(a) > 0 {
		if id := im.companyID(a[0]); id != "" {
			w["company"] = id
		}
	}
	if a := o.Assoc[Contacts]; len(a) > 0 {
		if id := im.contactID(a[0]); id != "" {
			w["contact"] = id
		}
	}
	return w
}

func (im *Importer) loadDeals(ctx context.Context, ids []string) (map[string]dealRow, error) {
	out := map[string]dealRow{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := im.db.Query(ctx, `
		SELECT id::text, name, stage, amount_cents, currency, coalesce(to_char(close_date,'YYYY-MM-DD'),''),
		       coalesce(company_id::text,''), coalesce(contact_id::text,''), coalesce(owner_id::text,''), notes,
		       updated_at, deleted_at IS NOT NULL
		  FROM deals WHERE org_id=$1 AND id = ANY($2::uuid[])`, im.org, ids)
	if err != nil {
		return nil, fmt.Errorf("read deals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r dealRow
		if err := rows.Scan(&r.id, &r.name, &r.stage, &r.amount, &r.currency, &r.closeDate, &r.company, &r.contact, &r.owner,
			&r.notes, &r.updated, &r.deleted); err != nil {
			return nil, err
		}
		out[r.id] = r
	}
	return out, rows.Err()
}

func (r dealRow) deal(set map[string]string) crm.Deal {
	d := crm.Deal{ID: r.id, Name: r.name, Stage: r.stage, AmountCents: r.amount, Currency: r.currency, Notes: r.notes}
	opt := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	d.CompanyID, d.ContactID, d.OwnerID = opt(r.company), opt(r.contact), opt(r.owner)
	if r.closeDate != "" {
		if t, err := time.Parse("2006-01-02", r.closeDate); err == nil {
			d.CloseDate = &t
		}
	}
	for f, v := range set {
		switch f {
		case "name":
			d.Name = v
		case "stage":
			d.Stage = v
		case "amount":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				d.AmountCents = n
			}
		case "close_date":
			if t, err := time.Parse("2006-01-02", v); err == nil {
				d.CloseDate = &t
			}
		case "owner":
			d.OwnerID = opt(v)
		case "company":
			d.CompanyID = opt(v)
		case "contact":
			d.ContactID = opt(v)
		}
	}
	return d
}

func (im *Importer) deals(ctx context.Context, objs []Object, cnt *Count) error {
	if err := im.hs.Associations(ctx, Deals, Companies, objs); err != nil {
		return fmt.Errorf("read deal associations: %w", err)
	}
	if err := im.hs.Associations(ctx, Deals, Contacts, objs); err != nil {
		return fmt.Errorf("read deal associations: %w", err)
	}
	var cids []string
	for _, o := range objs {
		if a := o.Assoc[Companies]; len(a) > 0 {
			cids = append(cids, a[0])
		}
	}
	if err := im.pullCompanies(ctx, cids); err != nil {
		return err
	}
	type hit struct {
		r   ref
		via string
	}
	hits := map[string]hit{}
	var ids []string
	for _, o := range objs {
		if r, via, ok := im.lookup(Deals, o.ID); ok {
			hits[o.ID] = hit{r, via}
			if !strings.HasPrefix(r.id, "dry-run:") {
				ids = append(ids, r.id)
			}
		}
	}
	rows, err := im.loadDeals(ctx, ids)
	if err != nil {
		return err
	}
	for _, o := range objs {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := im.dealWant(o)
		if h, ok := hits[o.ID]; ok {
			im.updateDeal(ctx, o, want, h.r, h.via, rows, cnt)
		} else {
			im.createDeal(ctx, o, want, cnt)
		}
	}
	return nil
}

func (im *Importer) updateDeal(ctx context.Context, o Object, want map[string]string, r ref, via string,
	rows map[string]dealRow, cnt *Count) {

	if strings.HasPrefix(r.id, "dry-run:") {
		cnt.Same++
		return
	}
	row, ok := rows[r.id]
	if !ok {
		cnt.Failed++
		im.rep.problem("deal %s: Hearth record %s is missing", o.ID, r.id)
		return
	}
	if row.deleted {
		cnt.skip("deleted in Hearth, left deleted")
		return
	}
	mode, snap := im.modeFor("deals", o.ID, via, row.updated)
	cur := row.fields()
	p := plan(want, cur, snap, mode, nil)
	im.addConflicts("deals", o.ID, row.id, p, cur, mode)
	if len(p.conflicts) > 0 {
		cnt.Conflicts++
	}
	if p.changed() && im.opt.Apply {
		if err := im.svc.UpdateDeal(ctx, im.org, im.actor, row.deal(p.set)); err != nil {
			cnt.Failed++
			im.rep.problem("deal %s: %v", o.ID, err)
			return
		}
		if err := im.audit(ctx, "crm.hubspot_import.update", "deal", row.id, "deals", o.ID, before(cur, p.set), p.set); err != nil {
			cnt.Failed++
			im.rep.problem("deal %s: %v", o.ID, err)
			return
		}
	}
	if p.changed() {
		cnt.Update++
	} else {
		cnt.Same++
	}
	if err := im.record(ctx, "deals", o.ID, "deal", row.id, p.next, o.Modified); err != nil {
		cnt.Failed++
		im.rep.problem("deal %s: %v", o.ID, err)
		return
	}
	im.giveType(ctx, row.company, want["stage"])
}

func (im *Importer) createDeal(ctx context.Context, o Object, want map[string]string, cnt *Count) {
	name := want["name"]
	if name == "" {
		name = "(unnamed deal " + o.ID + ")"
	}
	id := im.fakeID("deal", o.ID)
	if im.opt.Apply {
		d := dealRow{name: name, stage: "new", currency: "USD"}.deal(want)
		d.Name = name
		made, err := im.svc.CreateDeal(ctx, im.org, im.actor, d)
		if err != nil {
			cnt.Failed++
			im.rep.problem("deal %s: %v", o.ID, err)
			return
		}
		id = made.ID
		if err := im.audit(ctx, "crm.hubspot_import.create", "deal", id, "deals", o.ID, nil, want); err != nil {
			cnt.Failed++
			im.rep.problem("deal %s: %v", o.ID, err)
			return
		}
	}
	cnt.Create++
	if err := im.record(ctx, "deals", o.ID, "deal", id, copyMap(want), o.Modified); err != nil {
		cnt.Failed++
		im.rep.problem("deal %s: %v", o.ID, err)
		return
	}
	im.giveType(ctx, want["company"], want["stage"])
}

// giveType types a company this run made from what its deals say, the same evidence the first import used: a deal in
// the Customer or Won stage makes a client, any other deal a prospect, and a company with no deal gets none. It never
// touches a company that was already in Hearth.
func (im *Importer) giveType(ctx context.Context, company, stage string) {
	if company == "" {
		return
	}
	have, made := im.madeCompanies[company]
	if !made {
		return
	}
	want := "prospect"
	if stage == "customer" || stage == "won" {
		want = "client"
	}
	if have == "client" || have == want {
		return
	}
	if im.opt.Apply && !strings.HasPrefix(company, "dry-run:") {
		if err := im.svc.SetCompanyLabels(ctx, im.org, im.actor, company, &[]string{want}, nil); err != nil {
			im.rep.problem("company %s: could not give it the %s type: %v", company, want, err)
			return
		}
	}
	im.madeCompanies[company] = want
}
