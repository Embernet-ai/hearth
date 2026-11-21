package crmsync

// People the phone system made, with no company.
//
// A contact is sent to EmberOps with its company, and a company is sent when it has an accounting role or prospects are on. A
// person saved from the phone (a stranger who rang, or one typed into the app) has no company yet, so the sync would never carry
// them across, and the phone's contacts would be in Hearth and nowhere in EmberOps. The phone marks every contact it makes with
// external_refs source "phone", and those, and only those, go as Contacts with no link until somebody gives them a company.

import (
	"context"
	"fmt"
)

func (s *Syncer) phoneContacts(ctx context.Context, rep *Report, write bool) {
	rows, err := s.db.Query(ctx, `SELECT c.id::text, c.name, c.email, c.phone, c.title, c.notes,
		       coalesce((SELECT p.display_name FROM principals p WHERE p.id = c.owner_id), ''), c.marketing_opt_out_at IS NOT NULL
		FROM contacts c
		JOIN external_refs r ON r.org_id = c.org_id AND r.source = $2 AND r.entity_type = 'contact' AND r.entity_id = c.id
		WHERE c.org_id=$1 AND c.company_id IS NULL AND c.deleted_at IS NULL ORDER BY c.name, c.id`, s.org, PhoneSource)
	if err != nil {
		s.add(rep, Action{"contact", "", "phone contacts", "failed", fmt.Sprintf("read the phone's contacts: %v", err)})
		return
	}
	var cts []contact
	for rows.Next() {
		var c contact
		if err := rows.Scan(&c.ID, &c.Name, &c.Email, &c.Phone, &c.Title, &c.Notes, &c.Owner, &c.OptOut); err != nil {
			rows.Close()
			s.add(rep, Action{"contact", "", "phone contacts", "failed", err.Error()})
			return
		}
		cts = append(cts, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.add(rep, Action{"contact", "", "phone contacts", "failed", err.Error()})
		return
	}
	if len(cts) == 0 {
		return
	}
	s.people(ctx, rep, company{Name: "no company"}, cts, "", write)
}
