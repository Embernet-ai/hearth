package crmsync

// People made in EmberOps, carried into Hearth.
//
// Hearth is the CRM and sends its people to EmberOps. A person somebody types straight into EmberOps Contacts (a staff member adding a
// contact there, or a web form) has no Hearth id, so the phone, which reads Hearth, would never find them. This is the way back:
// every EmberOps Contact that carries no Hearth id, that the sync did not make itself, and that holds an email or a number is
//
//   - matched to a Hearth contact by email or by phone (E.164, the rule Hearth's index uses), and when it matches by email it is linked
//     the way every other pre-existing record is, which sets its hearth_id and nothing else; or
//   - made in Hearth, under the Hearth company of the Customer or Supplier it is linked to in EmberOps when that company has a Hearth
//     id, and with no company otherwise. The next run then matches it by email and links it.
//
// Nothing in EmberOps is changed except that hearth_id, and a person with neither an email nor a number is left alone.

import (
	"context"
	"fmt"
	"strings"

	"github.com/Embernet-ai/hearth/internal/crm"
)

// ContactMaker creates a person in Hearth the way the CRM does, shared with the sales team.
type ContactMaker interface {
	MakeContact(ctx context.Context, name, email, phone, title, companyID string) (id string, err error)
}

type hearthPerson struct{ ID, Name, Email string }

func (s *Syncer) personByEmail(ctx context.Context, email string) (*hearthPerson, error) {
	var p hearthPerson
	err := s.db.QueryRow(ctx, `SELECT id::text, name, email FROM contacts WHERE org_id=$1 AND deleted_at IS NULL AND lower(email)=lower($2)
		ORDER BY updated_at DESC LIMIT 1`, s.org, email).Scan(&p.ID, &p.Name, &p.Email)
	if err != nil {
		return nil, nil //nolint:nilerr // no row is the answer "nobody"
	}
	return &p, nil
}

func (s *Syncer) personByPhone(ctx context.Context, e164 string) (*hearthPerson, error) {
	var p hearthPerson
	err := s.db.QueryRow(ctx, `SELECT id::text, name, email FROM contacts WHERE org_id=$1 AND deleted_at IS NULL
		AND (phone_e164(phone) = $2 OR EXISTS (SELECT 1 FROM unnest(coalesce(extra_phones, '{}')) x WHERE phone_e164(x) = $2))
		ORDER BY updated_at DESC LIMIT 1`, s.org, e164).Scan(&p.ID, &p.Name, &p.Email)
	if err != nil {
		return nil, nil //nolint:nilerr // no row is the answer "nobody"
	}
	return &p, nil
}

func trimmed(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func (s *Syncer) contactsFromOps(ctx context.Context, rep *Report, maker ContactMaker, write bool) {
	rows, err := s.all(ctx, "Contact")
	if err != nil {
		s.add(rep, Action{"union", "", "Contact", "failed", err.Error()})
		return
	}
	for _, r := range rows {
		if h, _ := r["hearth_id"].(string); h != "" {
			continue
		}
		name := trimmed(r["name"])
		if name == "" || s.syncMade(ctx, r) {
			continue
		}
		doc, err := s.ops.Get(ctx, "Contact", name)
		if err != nil {
			s.add(rep, Action{"union", "", name, "failed", "read the Contact: " + err.Error()})
			continue
		}
		var email, phone string
		for _, e := range rowsOf(doc["email_ids"]) {
			if email == "" || e["is_primary"] == float64(1) {
				if v := trimmed(e["email_id"]); v != "" {
					email = v
				}
			}
		}
		for _, p := range rowsOf(doc["phone_nos"]) {
			if phone == "" || p["is_primary_phone"] == float64(1) || p["is_primary_mobile_no"] == float64(1) {
				if v := trimmed(p["phone"]); v != "" {
					phone = v
				}
			}
		}
		if email == "" && crm.PhoneE164(phone) == "" {
			continue
		}
		var found *hearthPerson
		if email != "" {
			if found, err = s.personByEmail(ctx, email); err != nil {
				s.add(rep, Action{"union", "", name, "failed", err.Error()})
				continue
			}
		}
		if found == nil && crm.PhoneE164(phone) != "" {
			if found, err = s.personByPhone(ctx, crm.PhoneE164(phone)); err != nil {
				s.add(rep, Action{"union", "", name, "failed", err.Error()})
				continue
			}
		}
		if found != nil {
			// Hearth has this person. By email they are linked, which sets the hearth_id; by number alone there is nothing to match them on.
			if email != "" && strings.EqualFold(found.Email, email) {
				s.link(ctx, rep, "contact", "Contact", found.ID, found.Name, name, email, write)
			}
			continue
		}
		if !write {
			s.add(rep, Action{"union", "", name, "would create contact", "an EmberOps Contact Hearth does not have"})
			continue
		}
		if maker == nil {
			s.add(rep, Action{"union", "", name, "held", "no way to create a contact here"})
			continue
		}
		company := ""
		for _, l := range rowsOf(doc["links"]) {
			dt, ln := trimmed(l["link_doctype"]), trimmed(l["link_name"])
			if dt != "Customer" && dt != "Supplier" && dt != "Lead" || ln == "" {
				continue
			}
			prows, err := s.all(ctx, dt)
			if err != nil {
				continue
			}
			for _, pr := range prows {
				if trimmed(pr["name"]) == ln {
					if h, _ := pr["hearth_id"].(string); h != "" {
						company = h
					}
				}
			}
			if company != "" {
				break
			}
		}
		first, last := trimmed(doc["first_name"]), trimmed(doc["last_name"])
		full := strings.TrimSpace(first + " " + last)
		if full == "" {
			full = name
		}
		id, err := maker.MakeContact(ctx, full, email, phone, trimmed(doc["designation"]), company)
		if err != nil {
			s.add(rep, Action{"union", "", name, "failed", err.Error()})
			continue
		}
		s.add(rep, Action{"union", id, name, "create contact", fmt.Sprintf("from the EmberOps Contact %s", name)})
	}
}
