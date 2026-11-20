package crmsync

// Deals to Opportunities, with a Lead first for a prospect that has one.
//
// Hearth keeps the deal board, the owners and the timeline; EmberOps gets an
// Opportunity so quotes and orders can hang off something. The deal stays
// Hearth's: a stage changed in EmberOps is not read back, and the next run says
// so in the overwrite report.
//
//   - A deal on a company that is a Customer in EmberOps becomes an Opportunity
//     from that Customer.
//   - A deal on any other company (a prospect, or one nobody has typed) needs a
//     party EmberOps knows, so the company becomes a Lead first, and only
//     because it has a deal. The thousands of companies with no deal never
//     become Leads.
//   - A deal with no company has no party and waits.
//   - Nothing is deleted. A deal removed in Hearth leaves its Opportunity as it
//     is, and the report says so.
//
// The stage map uses ERPNext's standard sales stages. EmberOps owns that list,
// so the map is checked against the real site before the first write.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/emberops"
)

type deal struct {
	ID, Name, Stage, Currency, CompanyID string
	Owner                                string
	AmountCents                          int64
	Close                                *time.Time
	Deleted                              bool
}

// stageOf maps a Hearth stage to an Opportunity status and, for an open deal,
// its sales stage. A finished deal carries only the status.
func stageOf(stage string) (status, salesStage string) {
	switch stage {
	case "new":
		return "Open", "Prospecting"
	case "screening":
		return "Open", "Qualification"
	case "meeting":
		return "Open", "Needs Analysis"
	case "proposal":
		return "Open", "Proposal/Price Quote"
	case "customer", "won":
		// EmberOps sets Converted itself, from the Opportunity's Quotations, and refuses it from anyone else. A won
		// deal sends no status: a new Opportunity opens Open, and a standing one keeps whatever EmberOps made it.
		return "", ""
	case "lost":
		return "Lost", ""
	case "no_response":
		return "Closed", ""
	}
	return "Open", ""
}

// opsStatuses are the Opportunity statuses EmberOps derives from the Quotations on it. Hearth never sends one, and
// never sends a status over one.
var opsStatuses = map[string]bool{"Converted": true, "Quotation": true, "Replied": true}

// settableStatus is the status Hearth may send for an Opportunity EmberOps already has: none when EmberOps has
// moved it on by its own rules.
func settableStatus(want string, have emberops.Row) string {
	if want == "" || opsStatuses[want] {
		return ""
	}
	if cur, _ := have["status"].(string); opsStatuses[strings.TrimSpace(cur)] {
		return ""
	}
	return want
}

// splitName is how EmberOps cuts a Lead's name into first, middle and last (ERPNext's parse_full_name). A Lead's
// lead_name is rebuilt from those three on every save, so they are what has to be sent for the name to hold.
func splitName(full string) (first, middle, last string) {
	n := strings.Fields(full)
	if len(n) == 0 {
		return "", "", ""
	}
	first = n[0]
	if len(n) > 2 {
		middle = strings.Join(n[1:len(n)-1], " ")
	}
	if len(n) > 1 {
		last = n[len(n)-1]
	}
	return first, middle, last
}

func (s *Syncer) deals(ctx context.Context, companyIDs []string) ([]deal, error) {
	rows, err := s.db.Query(ctx, `
		SELECT d.id::text, d.name, d.stage, d.currency, coalesce(d.company_id::text, ''), d.amount_cents,
		       d.close_date::timestamp, d.deleted_at IS NOT NULL,
		       coalesce((SELECT p.display_name FROM principals p WHERE p.id = d.owner_id), '')
		FROM deals d
		WHERE d.org_id = $1 AND (d.company_id IS NULL OR d.company_id = ANY($2::uuid[]))
		ORDER BY d.created_at, d.id`, s.org, companyIDs)
	if err != nil {
		return nil, fmt.Errorf("crmsync: read deals: %w", err)
	}
	defer rows.Close()
	var out []deal
	for rows.Next() {
		var d deal
		if err := rows.Scan(&d.ID, &d.Name, &d.Stage, &d.Currency, &d.CompanyID, &d.AmountCents, &d.Close, &d.Deleted, &d.Owner); err != nil {
			return nil, fmt.Errorf("crmsync: scan deal: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Syncer) syncDeals(ctx context.Context, rep *Report, cos []company, write bool) {
	byID := map[string]company{}
	ids := make([]string, 0, len(cos))
	for _, c := range cos {
		byID[c.ID] = c
		ids = append(ids, c.ID)
	}
	ds, err := s.deals(ctx, ids)
	if err != nil {
		s.add(rep, Action{"opportunity", "", "deals", "failed", err.Error()})
		return
	}
	leads := s.leadOf // company id -> Lead name made or found this run
	if leads == nil {
		leads = map[string]string{}
		s.leadOf = leads
	}
	for _, d := range ds {
		if d.CompanyID == "" {
			// No company, and in a limited run no company to scope by, is not this run's.
			continue
		}
		c, ok := byID[d.CompanyID]
		if !ok {
			continue
		}
		s.oneDeal(ctx, rep, c, d, leads, write)
	}
}

func (s *Syncer) oneDeal(ctx context.Context, rep *Report, c company, d deal, leads map[string]string, write bool) {
	opp, haveOpp, err := fetch(ctx, s.ops, "Opportunity", d.ID)
	if err != nil {
		s.add(rep, Action{"opportunity", d.ID, d.Name, "failed", err.Error()})
		return
	}
	if d.Deleted {
		if haveOpp {
			s.add(rep, Action{"opportunity", d.ID, d.Name, "keep", "the deal was removed in Hearth; its Opportunity is left as it is"})
		}
		return
	}
	if unnamed(c.Name, c.Domain) {
		s.add(rep, Action{"opportunity", d.ID, d.Name, "skipped", "its company has no real name yet"})
		return
	}

	from, party := "", ""
	if c.Roles["customer"] {
		if row, ok, err := s.known(ctx, "Customer", c.ID); err != nil {
			s.add(rep, Action{"opportunity", d.ID, d.Name, "failed", err.Error()})
			return
		} else if ok {
			from, party = "Customer", fmt.Sprint(row["name"])
		} else if !write {
			from, party = "Customer", c.Name
		} else {
			s.add(rep, Action{"opportunity", d.ID, d.Name, "held", "its company is a client but has no Customer in EmberOps yet"})
			return
		}
	} else {
		name, ok := leads[c.ID]
		if !ok {
			var err error
			if name, err = s.lead(ctx, rep, c, write); err != nil {
				s.add(rep, Action{"lead", c.ID, c.Name, "failed", err.Error()})
				return
			}
			leads[c.ID] = name
		}
		from, party = "Lead", name
		if party == "" {
			// A dry run that would make the Lead: the Opportunity would follow it.
			party = c.Name
		}
	}

	status, stage := stageOf(d.Stage)
	if haveOpp {
		status = settableStatus(status, opp)
	}
	doc := map[string]any{"opportunity_from": from, "party_name": party, "title": d.Name}
	if status != "" {
		doc["status"] = status
	}
	if stage != "" {
		doc["sales_stage"] = stage
	}
	if d.Close != nil {
		doc["expected_closing"] = d.Close.Format("2006-01-02")
	}
	if d.AmountCents > 0 {
		doc["opportunity_amount"] = float64(d.AmountCents) / 100
	}
	if cur := strings.TrimSpace(d.Currency); cur != "" {
		doc["currency"] = cur
	}
	s.stamp(doc, "deals", d.ID, d.Owner)
	verb := "create"
	if haveOpp {
		verb = "update"
		s.overwrites(rep, "opportunity", d.ID, d.Name, doc, opp, write)
		if s.same(ctx, doc, opp) {
			s.add(rep, Action{"opportunity", d.ID, d.Name, "same", "nothing changed since the last run"})
			return
		}
	}
	shown := status
	if shown == "" {
		shown = "status left to EmberOps"
	}
	detail := fmt.Sprintf("%s %s, %s", from, c.Name, shown)
	if !write {
		s.add(rep, Action{"opportunity", d.ID, d.Name, "would " + verb, detail})
		return
	}
	if _, err := s.send(ctx, "Opportunity", d.ID, doc); err != nil {
		s.add(rep, Action{"opportunity", d.ID, d.Name, "failed", err.Error()})
		return
	}
	s.add(rep, Action{"opportunity", d.ID, d.Name, verb, detail})
}

// leadDoc is what Hearth sends for a Lead. EmberOps owns lead_name once the Lead exists: it is rebuilt from the
// three name parts on every save, so sending it again only ever shows as a difference, and the parts are what hold.
func leadDoc(who string, c company, exists bool) map[string]any {
	first, middle, last := splitName(who)
	doc := map[string]any{"first_name": first, "middle_name": middle, "last_name": last, "company_name": c.Name,
		"_tags": tagsFor(c)}
	if !exists {
		doc["lead_name"] = strings.Join(strings.Fields(who), " ")
	}
	return doc
}

// lead finds or makes the Lead for a prospect that has a deal, and returns its
// EmberOps name. A dry run that would make one returns an empty name.
func (s *Syncer) lead(ctx context.Context, rep *Report, c company, write bool) (string, error) {
	var row emberops.Row
	var ok bool
	var err error
	if s.prospects {
		// Thousands of Leads: read once, not once each.
		row, ok, err = s.known(ctx, "Lead", c.ID)
	} else {
		row, ok, err = fetch(ctx, s.ops, "Lead", c.ID)
	}
	if err != nil {
		return "", err
	}
	who, email := c.Name, ""
	if cts, err := s.contacts(ctx, c.ID); err == nil && len(cts) > 0 {
		who, email = cts[0].Name, cts[0].Email
	}
	doc := leadDoc(who, c, ok)
	if strings.TrimSpace(email) != "" {
		doc["email_id"] = strings.TrimSpace(email)
	}
	s.stamp(doc, "companies", c.ID, c.Owner)
	if w := strings.TrimSpace(c.Website); w != "" {
		doc["website"] = w
	}
	why := "a prospect"
	verb := "create"
	if ok {
		verb = "update"
		s.overwrites(rep, "lead", c.ID, c.Name, doc, row, write)
		if s.same(ctx, doc, row) {
			s.add(rep, Action{"lead", c.ID, c.Name, "same", why})
			return fmt.Sprint(row["name"]), nil
		}
	}
	if !write {
		s.add(rep, Action{"lead", c.ID, c.Name, "would " + verb, why})
		if ok {
			return fmt.Sprint(row["name"]), nil
		}
		return "", nil
	}
	res, err := s.send(ctx, "Lead", c.ID, doc)
	if err != nil {
		return "", err
	}
	s.add(rep, Action{"lead", c.ID, c.Name, verb, why})
	if res.Name != "" {
		return res.Name, nil
	}
	return fmt.Sprint(row["name"]), nil
}
