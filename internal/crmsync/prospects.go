package crmsync

// The rest of the CRM, and the way back.
//
//   - Prospects: a company with no accounting role is a Lead in EmberOps, and its people are Contacts linked to that
//     Lead. Customer stays what accounting needs, and a prospect still reaches EmberOps without being made a Customer.
//     It is off until Options.Prospects turns it on.
//   - People at a company that is a Lead because of a deal go across the same way, so a deal's Lead has the people
//     Hearth knows there.
//   - Parties EmberOps made first: a Customer or Supplier somebody created in EmberOps with no Hearth record behind
//     it gets one, or its type, so every company staff deal with is in the CRM too. EmberOps owns that a Customer
//     or Supplier exists; the company's name and everything else Hearth owns stay Hearth's from then on.
//   - Links: Hearth keeps the EmberOps page of every record it sent, so its own pages can link to it.

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/emberops"
)

// CompanyMaker creates a company in Hearth the way the CRM does, with its owner and its sales sharing, so a company
// made from EmberOps is visible to the people who work the CRM.
type CompanyMaker interface {
	MakeCompany(ctx context.Context, name, kind string) (id string, err error)
}

// linkSource is the external_refs source for the EmberOps page of a record.
const linkSource = "emberops-record"

var pageOf = map[string]struct{ entity, slug string }{
	"Customer":    {"company", "customer"},
	"Supplier":    {"company", "supplier"},
	"Lead":        {"company", "lead"},
	"Contact":     {"contact", "contact"},
	"Opportunity": {"deal", "opportunity"},
}

// prospect makes the Lead for a company that has no accounting role.
func (s *Syncer) prospect(ctx context.Context, rep *Report, c company, write bool) {
	if unnamed(c.Name, c.Domain) {
		s.add(rep, Action{"lead", c.ID, c.Name, "skipped", "no real name yet; it and its people stay out of EmberOps until it has one"})
		return
	}
	name, err := s.lead(ctx, rep, c, write)
	if err != nil {
		s.add(rep, Action{"lead", c.ID, c.Name, "failed", err.Error()})
		return
	}
	s.leadOf[c.ID] = name
}

// leadContacts sends the people of every company that is a Lead this run.
func (s *Syncer) leadContacts(ctx context.Context, rep *Report, cos []company, write bool) {
	// The people list was read before the Leads were saved, and saving a Lead makes a Contact of its own, so read
	// it again before looking for matches.
	delete(s.idx, "Contact")
	delete(s.byID, "Contact")
	s.mine = nil
	for _, c := range cos {
		if _, ok := s.leadOf[c.ID]; !ok || len(c.Roles) > 0 {
			continue
		}
		cts, err := s.contacts(ctx, c.ID)
		if err != nil {
			s.add(rep, Action{"contact", c.ID, c.Name, "failed", err.Error()})
			continue
		}
		s.people(ctx, rep, c, cts, "lead", write)
	}
}

// fromOps handles the Customers and Suppliers EmberOps has that carry no Hearth id: each is matched to the Hearth
// company of the same name, which gets the matching type, or becomes a new company. The next run links them.
func (s *Syncer) fromOps(ctx context.Context, rep *Report, maker CompanyMaker, write bool) {
	for _, role := range []string{"customer", "supplier"} {
		rd := roleDoctype[role]
		rows, err := s.all(ctx, rd.dt)
		if err != nil {
			s.add(rep, Action{"union", "", rd.dt, "failed", err.Error()})
			continue
		}
		label, key := "Client", "client"
		if role == "supplier" {
			label, key = "Vendor", "vendor"
		}
		for _, r := range rows {
			if h, _ := r["hearth_id"].(string); h != "" {
				continue
			}
			if d, _ := r["disabled"].(float64); d == 1 {
				continue
			}
			name := strings.TrimSpace(fmt.Sprint(r["name"]))
			if name == "" {
				continue
			}
			ids, err := s.companiesNamed(ctx, name)
			if err != nil {
				s.add(rep, Action{"union", "", name, "failed", err.Error()})
				continue
			}
			switch {
			case len(ids) > 1:
				s.add(rep, Action{"union", "", name, "conflict", fmt.Sprintf("EmberOps has a %s with no Hearth record, and Hearth has %d companies with that name; none was touched", rd.dt, len(ids))})
			case len(ids) == 1:
				has, err := s.hasRole(ctx, ids[0], role)
				if err != nil {
					s.add(rep, Action{"union", ids[0], name, "failed", err.Error()})
					continue
				}
				if has {
					// It already has the type, so the company pass links it by name on its own.
					continue
				}
				if !write {
					s.add(rep, Action{"union", ids[0], name, "would add type", label + " (a " + rd.dt + " exists in EmberOps)"})
					continue
				}
				if err := s.addType(ctx, ids[0], key); err != nil {
					s.add(rep, Action{"union", ids[0], name, "failed", err.Error()})
					continue
				}
				s.add(rep, Action{"union", ids[0], name, "add type", label + " (a " + rd.dt + " exists in EmberOps)"})
			default:
				if !write {
					s.add(rep, Action{"union", "", name, "would create company", label + ", from the " + rd.dt + " in EmberOps"})
					continue
				}
				if maker == nil {
					s.add(rep, Action{"union", "", name, "held", "no way to create a company here"})
					continue
				}
				kind := "customer"
				if role == "supplier" {
					kind = "vendor"
				}
				id, err := maker.MakeCompany(ctx, name, kind)
				if err != nil {
					s.add(rep, Action{"union", "", name, "failed", err.Error()})
					continue
				}
				if err := s.addType(ctx, id, key); err != nil {
					s.add(rep, Action{"union", id, name, "failed", err.Error()})
					continue
				}
				s.add(rep, Action{"union", id, name, "create company", label + ", from the " + rd.dt + " in EmberOps"})
			}
		}
	}
}

func (s *Syncer) companiesNamed(ctx context.Context, name string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text FROM companies WHERE org_id=$1 AND deleted_at IS NULL
		AND lower(btrim(name)) = lower(btrim($2)) ORDER BY created_at`, s.org, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Syncer) hasRole(ctx context.Context, id, role string) (bool, error) {
	var has bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM company_type_links l
		JOIN company_types t ON t.id = l.type_id WHERE l.company_id = $1::uuid AND t.org_id = $2 AND t.erp_role = $3)`,
		id, s.org, role).Scan(&has)
	return has, err
}

func (s *Syncer) addType(ctx context.Context, id, key string) error {
	ct, err := s.db.Exec(ctx, `INSERT INTO company_type_links (company_id, type_id)
		SELECT $1::uuid, t.id FROM company_types t JOIN companies c ON c.id = $1::uuid AND c.org_id = t.org_id
		WHERE t.org_id = $2 AND t.key = $3 AND NOT t.archived ON CONFLICT DO NOTHING`, id, s.org, key)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		if ok, _ := s.hasKey(ctx, id, key); ok {
			return nil
		}
		return fmt.Errorf("the %q type is missing or archived, so company %s could not be typed", key, id)
	}
	return nil
}

func (s *Syncer) hasKey(ctx context.Context, id, key string) (bool, error) {
	var has bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM company_type_links l
		JOIN company_types t ON t.id = l.type_id WHERE l.company_id = $1::uuid AND t.org_id = $2 AND t.key = $3)`,
		id, s.org, key).Scan(&has)
	return has, err
}

// links keeps, per record Hearth sent, the page it has in EmberOps, so Hearth's company, contact and deal pages can
// link to it. It reads each doctype's Hearth-made records once and writes one row each.
func (s *Syncer) links(ctx context.Context, rep *Report) {
	if s.opsURL == "" {
		return
	}
	for _, dt := range []string{"Customer", "Supplier", "Lead", "Contact", "Opportunity"} {
		rows, err := s.ops.ListAll(ctx, emberops.ListQuery{Doctype: dt, Fields: []string{"name", "hearth_id"},
			Filters: []emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"}}}, readAll)
		if err != nil {
			s.add(rep, Action{"link", "", dt, "failed", err.Error()})
			continue
		}
		pg := pageOf[dt]
		for _, r := range rows {
			hid, _ := r["hearth_id"].(string)
			name, _ := r["name"].(string)
			if _, err := uuid.Parse(hid); err != nil || name == "" {
				continue
			}
			u := s.opsURL + "/app/" + pg.slug + "/" + url.PathEscape(name)
			if _, err := s.db.Exec(ctx, `INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id, external_url)
				VALUES ($1,$2,$3,$4,$5::uuid,$6)
				ON CONFLICT (org_id, source, external_id) DO UPDATE SET external_url = EXCLUDED.external_url, updated_at = now()
				WHERE external_refs.external_url IS DISTINCT FROM EXCLUDED.external_url`,
				s.org, linkSource, dt+"/"+hid, pg.entity, hid, u); err != nil {
				s.add(rep, Action{"link", hid, name, "failed", err.Error()})
			}
		}
	}
}
