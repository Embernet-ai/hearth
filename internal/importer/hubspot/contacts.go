package hubspot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/crm"
)

type contactRow struct {
	id, company, name, email, phone, title, notes, linkedin, owner string
	extraEmails, extraPhones                                       []string
	updated                                                        time.Time
	deleted                                                        bool
}

func (r contactRow) fields() map[string]string {
	return map[string]string{"name": r.name, "email": r.email, "phone": r.phone, "title": r.title,
		"linkedin": r.linkedin, "owner": r.owner, "company": r.company}
}

func (im *Importer) contactWant(o Object) map[string]string {
	p := o.Props
	w := map[string]string{}
	put := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			w[k] = v
		}
	}
	put("name", strings.TrimSpace(strings.TrimSpace(p["firstname"])+" "+strings.TrimSpace(p["lastname"])))
	if e := strings.ToLower(strings.TrimSpace(p["email"])); strings.Contains(e, "@") {
		w["email"] = e
	}
	put("phone", phoneOf(p["phone"], p["mobilephone"]))
	put("title", p["jobtitle"])
	put("linkedin", p["hs_linkedin_url"])
	if owner := im.ownerOf(p["hubspot_owner_id"]); owner != "" {
		w["owner"] = owner
	}
	if len(o.Assoc[Companies]) > 0 {
		if id := im.companyID(o.Assoc[Companies][0]); id != "" {
			w["company"] = id
		}
	}
	return w
}

func (im *Importer) loadContacts(ctx context.Context, ids []string) (map[string]contactRow, error) {
	out := map[string]contactRow{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := im.db.Query(ctx, `
		SELECT id::text, coalesce(company_id::text,''), name, email, phone, title, notes, linkedin_url, extra_emails,
		       extra_phones, coalesce(owner_id::text,''), updated_at, deleted_at IS NOT NULL
		  FROM contacts WHERE org_id=$1 AND id = ANY($2::uuid[])`, im.org, ids)
	if err != nil {
		return nil, fmt.Errorf("read contacts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r contactRow
		if err := rows.Scan(&r.id, &r.company, &r.name, &r.email, &r.phone, &r.title, &r.notes, &r.linkedin, &r.extraEmails,
			&r.extraPhones, &r.owner, &r.updated, &r.deleted); err != nil {
			return nil, err
		}
		out[r.id] = r
	}
	return out, rows.Err()
}

func (im *Importer) contacts(ctx context.Context, objs []Object, cnt *Count) error {
	if err := im.hs.Associations(ctx, Contacts, Companies, objs); err != nil {
		return fmt.Errorf("read contact associations: %w", err)
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
	adopt := map[string]string{}
	var ids []string
	for _, o := range objs {
		if r, via, ok := im.lookup(Contacts, o.ID); ok {
			hits[o.ID] = hit{r, via}
			if !strings.HasPrefix(r.id, "dry-run:") {
				ids = append(ids, r.id)
			}
			continue
		}
		if e := strings.ToLower(strings.TrimSpace(o.Props["email"])); e != "" {
			if holder, ok := im.emails[e]; ok && !im.linked[holder] {
				adopt[o.ID] = holder
				ids = append(ids, holder)
			}
		}
	}
	rows, err := im.loadContacts(ctx, ids)
	if err != nil {
		return err
	}
	for _, o := range objs {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := im.contactWant(o)
		func() {
			if h, ok := hits[o.ID]; ok {
				im.updateContact(ctx, o, want, h.r, h.via, rows, cnt)
				return
			}
			if holder, ok := adopt[o.ID]; ok && !im.linked[holder] {
				if row, found := rows[holder]; found && !row.deleted {
					im.linkContact(ctx, o, want, row, cnt)
					return
				}
			}
			im.createContact(ctx, o, want, cnt)
		}()
		im.carryOptOut(ctx, o)
	}
	return nil
}

func (im *Importer) writeContact(ctx context.Context, row contactRow, set map[string]string) (map[string]string, error) {
	rejected := map[string]string{}
	c := crm.Contact{ID: row.id, Name: row.name, Email: row.email, Phone: row.phone, Title: row.title, Notes: row.notes}
	if row.company != "" {
		c.CompanyID = &row.company
	}
	apply := func(set map[string]string) {
		for f, v := range set {
			switch f {
			case "name":
				c.Name = v
			case "email":
				c.Email = v
			case "phone":
				c.Phone = v
			case "title":
				c.Title = v
			case "company":
				c.CompanyID = &v
			}
		}
	}
	core := false
	for _, f := range []string{"name", "email", "phone", "title", "company"} {
		if _, ok := set[f]; ok {
			core = true
		}
	}
	if core {
		apply(set)
		err := im.svc.UpdateContact(ctx, im.org, im.actor, c)
		if errors.Is(err, crm.ErrDuplicate) && set["email"] != "" {
			rejected["email"] = set["email"]
			delete(set, "email")
			c.Email = row.email
			err = im.svc.UpdateContact(ctx, im.org, im.actor, c)
		}
		if err != nil {
			return rejected, err
		}
	}
	_, lk := set["linkedin"]
	_, ow := set["owner"]
	if lk || ow {
		d := crm.Details{OwnerID: row.owner, LinkedIn: row.linkedin, ExtraEmails: row.extraEmails, ExtraPhones: row.extraPhones}
		if v, ok := set["linkedin"]; ok {
			d.LinkedIn = v
		}
		if v, ok := set["owner"]; ok {
			d.OwnerID = v
		}
		if err := im.svc.SetContactDetails(ctx, im.org, im.actor, row.id, d); err != nil {
			return rejected, err
		}
	}
	return rejected, nil
}

func (im *Importer) updateContact(ctx context.Context, o Object, want map[string]string, r ref, via string,
	rows map[string]contactRow, cnt *Count) {

	if strings.HasPrefix(r.id, "dry-run:") {
		cnt.Same++
		return
	}
	row, ok := rows[r.id]
	if !ok {
		cnt.Failed++
		im.rep.problem("contact %s: Hearth record %s is missing", o.ID, r.id)
		return
	}
	if row.deleted {
		cnt.skip("deleted in Hearth, left deleted")
		return
	}
	mode, snap := im.modeFor("contacts", o.ID, via, row.updated)
	cur := row.fields()
	rejected := map[string]string{}
	if e := strings.ToLower(want["email"]); e != "" {
		if holder, taken := im.emails[e]; taken && holder != row.id && !fieldsEqual("email", row.email, e) {
			rejected["email"] = want["email"]
			delete(want, "email")
		}
	}
	p := plan(want, cur, snap, mode, map[string]bool{"name": row.email != "" && strings.EqualFold(row.name, row.email)})
	im.countFields("contacts", o.ID, p, cur)
	for f, v := range rejected {
		p.conflicts[f] = v
	}
	im.addConflicts("contacts", o.ID, row.id, p, cur, mode)
	if len(p.conflicts) > 0 {
		cnt.Conflicts++
	}
	if p.changed() && im.opt.Apply {
		rej, err := im.writeContact(ctx, row, copyMap(p.set))
		if err != nil {
			cnt.Failed++
			im.rep.problem("contact %s: %v", o.ID, err)
			return
		}
		for f, v := range rej {
			p.conflicts[f] = v
			delete(p.set, f)
			delete(p.next, f)
			im.addConflicts("contacts", o.ID, row.id, fieldPlan{conflicts: map[string]string{f: v}}, cur, mode)
		}
		if e, ok := p.set["email"]; ok {
			im.emails[strings.ToLower(e)] = row.id
		}
		if err := im.audit(ctx, "crm.hubspot_import.update", "contact", row.id, "contacts", o.ID, before(cur, p.set), p.set); err != nil {
			cnt.Failed++
			im.rep.problem("contact %s: %v", o.ID, err)
			return
		}
	}
	if p.changed() {
		cnt.Update++
	} else {
		cnt.Same++
	}
	if err := im.record(ctx, "contacts", o.ID, "contact", row.id, p.next, o.Modified); err != nil {
		cnt.Failed++
		im.rep.problem("contact %s: %v", o.ID, err)
	}
}

func (im *Importer) linkContact(ctx context.Context, o Object, want map[string]string, row contactRow, cnt *Count) {
	cur := row.fields()
	p := plan(want, cur, nil, fillOnly, map[string]bool{"name": row.email != "" && strings.EqualFold(row.name, row.email)})
	im.countFields("contacts", o.ID, p, cur)
	delete(p.set, "email")
	im.addConflicts("contacts", o.ID, row.id, p, cur, fillOnly)
	if len(p.conflicts) > 0 {
		cnt.Conflicts++
	}
	if im.opt.Apply && p.changed() {
		if _, err := im.writeContact(ctx, row, copyMap(p.set)); err != nil {
			cnt.Failed++
			im.rep.problem("contact %s: %v", o.ID, err)
			return
		}
	}
	if err := im.audit(ctx, "crm.hubspot_import.link", "contact", row.id, "contacts", o.ID, before(cur, p.set), p.set); err != nil {
		cnt.Failed++
		im.rep.problem("contact %s: %v", o.ID, err)
		return
	}
	cnt.Link++
	if err := im.record(ctx, "contacts", o.ID, "contact", row.id, p.next, o.Modified); err != nil {
		cnt.Failed++
		im.rep.problem("contact %s: %v", o.ID, err)
	}
}

func (im *Importer) createContact(ctx context.Context, o Object, want map[string]string, cnt *Count) {
	name := want["name"]
	if name == "" {
		name = want["email"]
	}
	if name == "" {
		cnt.skip("no name and no email")
		return
	}
	// An address another person here already holds goes on this one as an extra address, never as a second owner of it.
	var extra []string
	email := want["email"]
	if email != "" {
		if _, taken := im.emails[strings.ToLower(email)]; taken {
			extra = []string{email}
			delete(want, "email")
			email = ""
		}
	}
	id := im.fakeID("contact", o.ID)
	if im.opt.Apply {
		c := crm.Contact{Name: name, Email: email, Phone: want["phone"], Title: want["title"]}
		if cid := want["company"]; cid != "" {
			c.CompanyID = &cid
		}
		made, err := im.svc.CreateContact(ctx, im.org, im.actor, c)
		if err != nil {
			cnt.Failed++
			im.rep.problem("contact %s: %v", o.ID, err)
			return
		}
		id = made.ID
		if want["linkedin"] != "" || want["owner"] != "" || len(extra) > 0 {
			if err := im.svc.SetContactDetails(ctx, im.org, im.actor, id,
				crm.Details{OwnerID: want["owner"], LinkedIn: want["linkedin"], ExtraEmails: extra}); err != nil {
				cnt.Failed++
				im.rep.problem("contact %s: made %s but could not set its details: %v", o.ID, id, err)
				return
			}
		}
		if err := im.svc.Share(ctx, im.org, im.actor, made.Ref()); err != nil {
			cnt.Failed++
			im.rep.problem("contact %s: made %s but could not share it with sales: %v", o.ID, id, err)
			return
		}
		if err := im.audit(ctx, "crm.hubspot_import.create", "contact", id, "contacts", o.ID, nil, want); err != nil {
			cnt.Failed++
			im.rep.problem("contact %s: %v", o.ID, err)
			return
		}
	}
	if email != "" {
		im.emails[strings.ToLower(email)] = id
	}
	cnt.Create++
	if err := im.record(ctx, "contacts", o.ID, "contact", id, copyMap(want), o.Modified); err != nil {
		cnt.Failed++
		im.rep.problem("contact %s: %v", o.ID, err)
	}
}

// contactID is the Hearth contact a HubSpot contact id is.
func (im *Importer) contactID(hid string) string {
	if r, _, ok := im.lookup(Contacts, hid); ok {
		return r.id
	}
	return ""
}
