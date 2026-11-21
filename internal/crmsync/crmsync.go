// Package crmsync keeps Hearth's CRM and EmberOps in step, with one owner per
// field.
//
// Hearth is the CRM: people work in it, and it holds the deals, timeline,
// owners, types and tags. EmberOps holds what accounting needs. So the flow is
// one way for everything Hearth owns, and the only thing that comes back is the
// fact that a Customer or Supplier exists there, which adds a type here.
//
//   - A company with a type whose accounting role is customer becomes a
//     Customer, and one with a supplier role becomes a Supplier. A company can
//     be both. A company with neither (a prospect, a partner, nothing yet) is
//     not sent, so Customer is not filled with names accounting never needs.
//   - Its contacts become Contacts, linked to the Customer or Supplier.
//   - Its types and tags become Frappe tags on the record ("Type: Client",
//     and the free tags as typed), which is what makes them filters and list
//     columns in EmberOps.
//
// Nothing here deletes. Taking a type away disables the Customer or Supplier,
// and only when it has no transactions; otherwise it is left and reported.
//
// Every write is an emberops_hearth upsert keyed on the Hearth id, so sending a
// record twice is one record, and a run that stops can simply be run again.
// A name already held by a record Hearth did not make is a conflict, listed and
// skipped, never merged by guess.
//
// Dry run is the default. It reads both sides and writes neither.
package crmsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
)

// Ops is the slice of the EmberOps client the sync uses. *emberops.Client is
// one; the tests use it against the fake Frappe.
type Ops interface {
	Upsert(ctx context.Context, doctype, hearthID string, values map[string]any) (emberops.UpsertResult, error)
	GetByHearthID(ctx context.Context, doctype, hearthID string) (emberops.Row, error)
	DeleteByHearthID(ctx context.Context, doctype, hearthID string) error
	ListAll(ctx context.Context, lq emberops.ListQuery, max int) ([]emberops.Row, error)
	Count(ctx context.Context, doctype string, filters []emberops.Filter) (int, error)
	Update(ctx context.Context, doctype, name string, values map[string]any) (emberops.Row, error)
	Get(ctx context.Context, doctype, name string) (emberops.Row, error)
}

// readAll is the most records the sync reads of one doctype. The client's own default for a list is 5000, and Fireball
// holds more Leads than that, so the sync named a limit it cannot reach: every Lead past the 5000th looked missing, was
// "created" again on every run, and its notes and calls had no record to attach to.
const readAll = 1_000_000

// Syncer runs one organisation's sync.
type Syncer struct {
	db  *pgxpool.Pool
	ops Ops
	org string
	// idx is every EmberOps record that carries a hearth_id, per doctype, read
	// once per run. Asking about thousands of untyped companies one at a time
	// would be thousands of calls to answer a question one list answers.
	idx map[string][]emberops.Row
	// adopted is the records EmberOps had before Hearth knew them, which the
	// sync linked and does not own: doctype/hearth id. It lives in external_refs,
	// so it survives every rerun.
	adopted map[string]bool
	// hearthURL and opsURL are the two public addresses the links are built from: Hearth's, which goes
	// on every EmberOps record so it points back, and EmberOps', which is what Hearth's pages link to.
	hearthURL, opsURL string
	// mine is the EmberOps users that the sync's own records belong to, learned from the records it
	// made. A Contact that one of them owns and that carries no Hearth id was made by EmberOps itself
	// when a Lead was saved, so it is the sync's to take over and not somebody else's to leave alone.
	mine map[string]bool
	// zone is the time zone EmberOps' Datetime fields are written in (a Frappe Datetime has none of its own).
	zone      *time.Location
	prospects bool
	// byID is the same records as idx, by Hearth id.
	byID map[string]map[string]emberops.Row
	// leadOf is the Lead made or found for each company this run.
	leadOf map[string]string
}

// all reads every record of a doctype once per run. Asking about thousands of
// untyped companies one at a time would be thousands of calls to answer a
// question one list answers, and the same list is what name and email matches
// are made against.
func (s *Syncer) all(ctx context.Context, dt string) ([]emberops.Row, error) {
	if s.idx == nil {
		s.idx = map[string][]emberops.Row{}
	}
	if rows, ok := s.idx[dt]; ok {
		return rows, nil
	}
	// Contact has no disabled field, and asking for one is a 417 on the real site.
	fields := []string{"name", "hearth_id", "disabled", "owner"}
	switch dt {
	case "Contact":
		fields = []string{"name", "hearth_id", "email_id", "owner"}
	case "Opportunity":
		fields = []string{"name", "hearth_id", "owner"}
	case "Lead":
		// Everything the sync compares, so thousands of Leads are one read and not one read each.
		fields = []string{"name", "hearth_id", "owner", "lead_name", "first_name", "middle_name", "last_name", "company_name", "email_id", "website", "hearth_url",
			"hearth_owner", "_user_tags", "unsubscribed"}
	}
	rows, err := s.ops.ListAll(ctx, emberops.ListQuery{Doctype: dt, Fields: fields}, readAll)
	if err != nil {
		return nil, err
	}
	s.idx[dt] = rows
	if s.byID == nil {
		s.byID = map[string]map[string]emberops.Row{}
	}
	m := make(map[string]emberops.Row, len(rows))
	for _, r := range rows {
		if hid, _ := r["hearth_id"].(string); hid != "" {
			m[hid] = r
		}
	}
	s.byID[dt] = m
	return rows, nil
}

// known says whether EmberOps has a record of this doctype for the company.
func (s *Syncer) known(ctx context.Context, dt, id string) (emberops.Row, bool, error) {
	if _, err := s.all(ctx, dt); err != nil {
		return nil, false, err
	}
	r, ok := s.byID[dt][id]
	return r, ok, nil
}

// match finds a record EmberOps already had before Hearth knew about it: the
// same name for a Customer or Supplier, the same address for a Contact. It
// returns every match, because two is a conflict and not a choice.
func (s *Syncer) match(ctx context.Context, dt, name, email string) ([]emberops.Row, error) {
	rows, err := s.all(ctx, dt)
	if err != nil {
		return nil, err
	}
	var out []emberops.Row
	for _, r := range rows {
		if dt == "Contact" {
			if e, _ := r["email_id"].(string); email != "" && strings.EqualFold(strings.TrimSpace(e), strings.TrimSpace(email)) {
				out = append(out, r)
			}
			continue
		}
		if n, _ := r["name"].(string); strings.EqualFold(strings.TrimSpace(n), strings.TrimSpace(name)) {
			out = append(out, r)
		}
	}
	if dt == "Contact" && len(out) == 0 && strings.TrimSpace(name) != "" {
		// EmberOps makes a Contact of its own when a Lead is saved, named for the person, with no
		// email when Hearth had none. That one is the sync's, so it is found by name, but only among
		// the records the sync's own user made and that no Hearth record has claimed.
		for _, r := range rows {
			if h, _ := r["hearth_id"].(string); h != "" {
				continue
			}
			if n, _ := r["name"].(string); strings.EqualFold(strings.TrimSpace(n), strings.TrimSpace(name)) && s.syncMade(ctx, r) {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// syncMade says whether EmberOps made this record on the sync's behalf: its owner is a user that owns
// records the sync created (a Lead, a Customer or a Supplier with a Hearth id that no one adopted).
func (s *Syncer) syncMade(ctx context.Context, r emberops.Row) bool {
	if s.mine == nil {
		s.mine = map[string]bool{}
		for _, dt := range []string{"Lead", "Customer", "Supplier"} {
			rows, err := s.all(ctx, dt)
			if err != nil {
				continue
			}
			for _, x := range rows {
				hid, _ := x["hearth_id"].(string)
				owner, _ := x["owner"].(string)
				if hid == "" || owner == "" {
					continue
				}
				if own, err := s.isAdopted(ctx, dt, hid); err == nil && !own {
					s.mine[owner] = true
				}
			}
		}
	}
	owner, _ := r["owner"].(string)
	return owner != "" && s.mine[owner]
}

// link claims a record EmberOps already had by setting its hearth_id and
// nothing else. Nothing EmberOps owns is overwritten and nothing is duplicated.
// It returns true when the record was handled, which is every case but "no match".
func (s *Syncer) link(ctx context.Context, rep *Report, kind, dt, id, label, name, email string, write bool) bool {
	ms, err := s.match(ctx, dt, name, email)
	if err != nil {
		s.add(rep, Action{kind, id, label, "failed", err.Error()})
		return true
	}
	if len(ms) == 0 {
		return false
	}
	if len(ms) > 1 {
		s.add(rep, Action{kind, id, label, "conflict", fmt.Sprintf("EmberOps has %d %s records that match; none was touched", len(ms), dt)})
		return true
	}
	have, _ := ms[0]["hearth_id"].(string)
	rec, _ := ms[0]["name"].(string)
	if have != "" && have != id {
		s.add(rep, Action{kind, id, label, "conflict", dt + " " + rec + " already belongs to a different Hearth record; not touched"})
		return true
	}
	if dt == "Contact" && s.syncMade(ctx, ms[0]) {
		// The sync's own record, made by EmberOps when a Lead was saved: it takes it over, and from
		// the next run it follows Hearth like every record the sync made.
		if !write {
			s.add(rep, Action{kind, id, label, "would link", dt + " " + rec + " was made by EmberOps for the Lead; its hearth_id would be set and it would follow Hearth"})
			return true
		}
		if _, err := s.ops.Update(ctx, dt, rec, map[string]any{"hearth_id": id}); err != nil {
			s.add(rep, Action{kind, id, label, "failed", err.Error()})
			return true
		}
		s.add(rep, Action{kind, id, label, "link", dt + " " + rec + " was made by EmberOps for the Lead; the sync took it over"})
		return true
	}
	if !write {
		s.add(rep, Action{kind, id, label, "would link", dt + " " + rec + " already exists in EmberOps; only its hearth_id would be set, and it would stay EmberOps' own"})
		return true
	}
	if _, err := s.ops.Update(ctx, dt, rec, map[string]any{"hearth_id": id}); err != nil {
		s.add(rep, Action{kind, id, label, "failed", err.Error()})
		return true
	}
	et := "company"
	if dt == "Contact" {
		et = "contact"
	}
	if err := s.markAdopted(ctx, et, dt, id, rec); err != nil {
		s.add(rep, Action{kind, id, label, "failed", "linked, but the link could not be recorded: " + err.Error()})
		return true
	}
	s.add(rep, Action{kind, id, label, "link", dt + " " + rec + " linked; no other field was changed, and it stays EmberOps' own"})
	return true
}

const adoptSource = "emberops-linked"

func (s *Syncer) isAdopted(ctx context.Context, dt, id string) (bool, error) {
	if s.adopted == nil {
		rows, err := s.db.Query(ctx, `SELECT external_id FROM external_refs WHERE org_id=$1 AND source=$2`, s.org, adoptSource)
		if err != nil {
			return false, fmt.Errorf("crmsync: read links: %w", err)
		}
		defer rows.Close()
		s.adopted = map[string]bool{}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return false, err
			}
			s.adopted[k] = true
		}
		if err := rows.Err(); err != nil {
			return false, err
		}
	}
	return s.adopted[dt+"/"+id], nil
}

func (s *Syncer) markAdopted(ctx context.Context, entityType, dt, id, emberopsName string) error {
	if _, err := s.db.Exec(ctx, `INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id, external_url)
		VALUES ($1,$2,$3,$4,$5::uuid,$6) ON CONFLICT DO NOTHING`, s.org, adoptSource, dt+"/"+id, entityType, id, emberopsName); err != nil {
		return err
	}
	if s.adopted != nil {
		s.adopted[dt+"/"+id] = true
	}
	return nil
}

func blank(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	}
	return false
}

// tagsOf reads the tags EmberOps holds on a record: Frappe keeps them as a
// comma separated _user_tags string, and the fake keeps the list that was sent.
func tagsOf(r emberops.Row) []string {
	var out []string
	switch x := r["_tags"].(type) {
	case []any:
		for _, t := range x {
			if v := strings.TrimSpace(fmt.Sprint(t)); v != "" {
				out = append(out, v)
			}
		}
	}
	if u, ok := r["_user_tags"].(string); ok {
		for _, t := range strings.Split(u, ",") {
			if t = strings.TrimSpace(t); t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

// fillOnly is what the sync does with a record it did not create. Only fields
// EmberOps has empty are filled, tags are merged as a union, and a non-empty
// value that differs from Hearth's is reported and left exactly as it is.
func (s *Syncer) fillOnly(ctx context.Context, rep *Report, kind, dt, id, label string, doc map[string]any, write bool) {
	row, err := s.ops.GetByHearthID(ctx, dt, id)
	if err != nil {
		s.add(rep, Action{kind, id, label, "failed", err.Error()})
		return
	}
	out := map[string]any{}
	var diffs, fills []string
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := doc[k]
		if k == "_tags" || k == "doctype" || k == "hearth_id" || blank(v) {
			continue
		}
		have := row[k]
		switch {
		case blank(have):
			out[k] = v
			fills = append(fills, k)
		case fmt.Sprint(have) != fmt.Sprint(v):
			diffs = append(diffs, fmt.Sprintf("%s: EmberOps keeps %q, Hearth has %q", k, fmt.Sprint(have), fmt.Sprint(v)))
		}
	}
	existing := tagsOf(row)
	seen := map[string]bool{}
	for _, t := range existing {
		seen[strings.ToLower(t)] = true
	}
	merged := append([]string{}, existing...)
	added := 0
	if want, ok := doc["_tags"].([]string); ok {
		for _, t := range want {
			if !seen[strings.ToLower(t)] {
				seen[strings.ToLower(t)] = true
				merged = append(merged, t)
				added++
			}
		}
	}
	if added > 0 {
		out["_tags"] = merged
		fills = append(fills, fmt.Sprintf("%d tag(s) added to the %d EmberOps has", added, len(existing)))
	}
	for _, d := range diffs {
		s.add(rep, Action{kind, id, label, "difference", d})
	}
	if len(out) == 0 {
		s.add(rep, Action{kind, id, label, "unchanged", "EmberOps' own record; nothing empty to fill"})
		return
	}
	if !write {
		s.add(rep, Action{kind, id, label, "would fill", strings.Join(fills, ", ")})
		return
	}
	if _, err := s.send(ctx, dt, id, out); err != nil {
		s.add(rep, Action{kind, id, label, "failed", err.Error()})
		return
	}
	s.add(rep, Action{kind, id, label, "fill", strings.Join(fills, ", ")})
}

// unnamed is a company with no real name: none at all, the "(unnamed 4164...)" a HubSpot company with no name was brought
// in as, or only its web address, which is what Hearth calls such a company since migration 00056. It stays out of the sync
// until somebody names it, because an invoice cannot go to "unnamed 4164..." or to "narmco.com.tw".
func unnamed(name, domain string) bool {
	n := strings.ToLower(strings.Trim(strings.TrimSpace(name), "()[]"))
	if n == "" || strings.HasPrefix(n, "unnamed") {
		return true
	}
	d := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), "www.")
	return d != "" && strings.TrimPrefix(n, "www.") == d
}

// New makes a Syncer for one organisation.
func New(db *pgxpool.Pool, ops Ops, orgID string) *Syncer {
	return &Syncer{db: db, ops: ops, org: orgID}
}

// Options steers a run.
type Options struct {
	// Write turns writes on. Without it nothing is written anywhere.
	Write bool
	// Only limits the run to these Hearth company ids (and their contacts).
	Only []string
	// Remove deletes the EmberOps records for the Only companies and their
	// contacts, which is how a proof company is taken out again. It never
	// touches a company that is not named, and needs Only.
	Remove bool
	// Deals also sends Hearth's deals as Opportunities, with a Lead first for a
	// prospect that has one. Off by default: it needs EmberOps to accept Lead and
	// Opportunity from this service, which is EmberOps' own change.
	Deals bool
	// Show reads the EmberOps records of the Only companies back and reports
	// what is stored, which is how a proof is checked against the real site.
	Show bool
	// HearthURL is where this Hearth is served (https://hearth.example). Every record sent carries a
	// link back to its Hearth page when it is set.
	HearthURL string
	// OpsURL is where EmberOps is served. It is what the links on Hearth's pages are built from.
	OpsURL string
	// Prospects also sends every company that has no accounting role as a Lead, and its people as
	// Contacts, so the whole CRM is reachable from EmberOps. Off by default: Customer stays what
	// accounting needs, and a Lead is the one place a prospect can live without making it a Customer.
	Prospects bool
	// Timeline also sends Hearth's notes and calls as Comments on the matching EmberOps record, one way.
	Timeline bool
	// TimeZone is the zone the EmberOps site is set to, for the start and end of a Call Log. Empty is America/New_York, Fireball's.
	TimeZone string
	// Companies makes a company in Hearth when EmberOps has a Customer or Supplier no Hearth company matches.
	// Without it such a party is reported and left.
	Companies CompanyMaker
	// Contacts makes a person in Hearth when EmberOps has a Contact (with an email or a number) that Hearth does not. Without it such a person is reported and left.
	Contacts ContactMaker
}

// Action is one thing the run did or would do.
type Action struct {
	What   string // customer, supplier, contact, tags, disable, union
	ID     string // Hearth id
	Name   string
	Verb   string // create, update, same, would create, would update, disable, keep, add type, conflict, failed, removed
	Detail string
}

// Report is what a run found.
type Report struct {
	Write   bool
	Actions []Action
}

// Count is how many actions had this verb.
func (r *Report) Count(what, verb string) int {
	n := 0
	for _, a := range r.Actions {
		if (what == "" || a.What == what) && a.Verb == verb {
			n++
		}
	}
	return n
}

// Failed reports whether anything could not be written.
func (r *Report) Failed() bool { return r.Count("", "failed") > 0 }

type company struct {
	ID, Name, Domain, Notes, TaxID string
	Website, Owner                 string
	Roles                          map[string]bool // customer, supplier
	Types                          []string        // labels
	Tags                           []string
}

type contact struct {
	ID, Name, Email, Phone, Title, Notes, CompanyID string
	Owner                                           string
	// OptOut is true once the person has stopped being emailed (marketing_opt_out_at is set). It goes to EmberOps one way,
	// as the Contact and Lead unsubscribed flag, and is never sent as false, so nothing here can clear it there.
	OptOut bool
}

func (s *Syncer) companies(ctx context.Context, only []string) ([]company, error) {
	if only == nil {
		// An empty array, not NULL: cardinality(NULL) is NULL and would match nothing.
		only = []string{}
	}
	rows, err := s.db.Query(ctx, `
		SELECT c.id::text, c.name, c.domain, c.notes,
		       coalesce((SELECT array_agg(DISTINCT t.erp_role) FROM company_type_links l
		                  JOIN company_types t ON t.id = l.type_id
		                 WHERE l.company_id = c.id AND t.erp_role <> ''), '{}'),
		       coalesce((SELECT array_agg(t.label ORDER BY t.position) FROM company_type_links l
		                  JOIN company_types t ON t.id = l.type_id WHERE l.company_id = c.id), '{}'),
		       coalesce((SELECT array_agg(g.tag ORDER BY lower(g.tag)) FROM company_tags g WHERE g.company_id = c.id), '{}'),
		       c.tax_id, c.website,
		       coalesce((SELECT p.display_name FROM principals p WHERE p.id = c.owner_id), '')
		FROM companies c
		WHERE c.org_id = $1 AND c.deleted_at IS NULL
		  AND (cardinality($2::uuid[]) = 0 OR c.id = ANY($2::uuid[]))
		ORDER BY c.name, c.id`, s.org, only)
	if err != nil {
		return nil, fmt.Errorf("crmsync: read companies: %w", err)
	}
	defer rows.Close()
	var out []company
	for rows.Next() {
		var c company
		var roles []string
		if err := rows.Scan(&c.ID, &c.Name, &c.Domain, &c.Notes, &roles, &c.Types, &c.Tags, &c.TaxID, &c.Website, &c.Owner); err != nil {
			return nil, fmt.Errorf("crmsync: scan company: %w", err)
		}
		c.Roles = map[string]bool{}
		for _, r := range roles {
			c.Roles[r] = true
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Syncer) contacts(ctx context.Context, companyID string) ([]contact, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id::text, c.name, c.email, c.phone, c.title, c.notes,
		       coalesce((SELECT p.display_name FROM principals p WHERE p.id = c.owner_id), ''), c.marketing_opt_out_at IS NOT NULL
		FROM contacts c WHERE c.org_id=$1 AND c.company_id=$2 AND c.deleted_at IS NULL ORDER BY c.name, c.id`, s.org, companyID)
	if err != nil {
		return nil, fmt.Errorf("crmsync: read contacts: %w", err)
	}
	defer rows.Close()
	var out []contact
	for rows.Next() {
		c := contact{CompanyID: companyID}
		if err := rows.Scan(&c.ID, &c.Name, &c.Email, &c.Phone, &c.Title, &c.Notes, &c.Owner, &c.OptOut); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// tagsFor is what goes on the EmberOps record: the types as "Type: Label" and
// the free tags as typed.
func tagsFor(c company) []string {
	var out []string
	for _, t := range c.Types {
		out = append(out, "Type: "+t)
	}
	out = append(out, c.Tags...)
	return out
}

func exists(ctx context.Context, ops Ops, dt, id string) (bool, error) {
	_, ok, err := fetch(ctx, ops, dt, id)
	return ok, err
}

// fetch reads the EmberOps record for a Hearth id, and says whether there is one.
func fetch(ctx context.Context, ops Ops, dt, id string) (emberops.Row, bool, error) {
	row, err := ops.GetByHearthID(ctx, dt, id)
	switch {
	case err == nil:
		return row, true, nil
	case errors.Is(err, emberops.ErrNotFound):
		return nil, false, nil
	}
	return nil, false, err
}

// Run reads Hearth and EmberOps and, when Options.Write is set, writes the
// difference. Without it, nothing is written anywhere.
func (s *Syncer) Run(ctx context.Context, o Options) (*Report, error) {
	rep := &Report{Write: o.Write}
	s.idx, s.byID, s.adopted, s.mine, s.leadOf = nil, nil, nil, nil, map[string]string{}
	s.prospects = o.Prospects
	s.zone = nil
	if tz := strings.TrimSpace(o.TimeZone); tz != "" {
		z, err := time.LoadLocation(tz)
		if err != nil {
			return rep, fmt.Errorf("crmsync: the EmberOps time zone %q: %w", tz, err)
		}
		s.zone = z
	} else if z, err := time.LoadLocation("America/New_York"); err == nil {
		s.zone = z
	}
	s.hearthURL, s.opsURL = strings.TrimRight(strings.TrimSpace(o.HearthURL), "/"), strings.TrimRight(strings.TrimSpace(o.OpsURL), "/")
	if (o.Remove || o.Show) && len(o.Only) == 0 {
		return rep, errors.New("crmsync: removing or showing needs the company ids to act on, and nothing else")
	}
	cos, err := s.companies(ctx, o.Only)
	if err != nil {
		return rep, err
	}
	for _, c := range cos {
		if o.Show {
			s.show(ctx, rep, c)
			continue
		}
		if o.Remove {
			s.remove(ctx, rep, c)
			continue
		}
		s.company(ctx, rep, c, o.Write)
	}
	if o.Deals && !o.Show && !o.Remove {
		s.syncDeals(ctx, rep, cos, o.Write)
	}
	if !o.Show && !o.Remove {
		s.leadContacts(ctx, rep, cos, o.Write)
	}
	if !o.Show && !o.Remove && len(o.Only) == 0 {
		s.phoneContacts(ctx, rep, o.Write)
	}
	if !o.Remove && len(o.Only) == 0 {
		s.union(ctx, rep, o.Write)
		s.fromOps(ctx, rep, o.Companies, o.Write)
		s.contactsFromOps(ctx, rep, o.Contacts, o.Write)
	}
	if o.Timeline && !o.Show && !o.Remove && len(o.Only) == 0 {
		s.timeline(ctx, rep, o.Write)
		s.callLogs(ctx, rep, o.Write)
	}
	if o.Write && !o.Show && !o.Remove {
		s.links(ctx, rep)
	}
	return rep, nil
}

func (s *Syncer) add(rep *Report, a Action) { rep.Actions = append(rep.Actions, a) }

var roleDoctype = map[string]struct{ dt, kind string }{
	"customer": {"Customer", "customer"},
	"supplier": {"Supplier", "vendor"},
}

func (s *Syncer) company(ctx context.Context, rep *Report, c company, write bool) {
	if len(c.Roles) > 0 && unnamed(c.Name, c.Domain) {
		s.add(rep, Action{"company", c.ID, c.Name, "skipped", "no real name yet; it and its contacts stay out of EmberOps until it has one"})
		return
	}
	roles := make([]string, 0, 2)
	for r := range c.Roles {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	have := map[string]bool{}
	placed := false
	for _, role := range roles {
		rd := roleDoctype[role]
		have[rd.dt] = true
		doc := mapping.TZ{}.CompanyDoc(mapping.Rec{"id": c.ID, "name": c.Name, "kind": rd.kind, "domain": c.Domain, "notes": c.Notes})
		delete(doc, "doctype")
		delete(doc, "hearth_id")
		doc["_tags"] = tagsFor(c)
		s.stamp(doc, "companies", c.ID, c.Owner)
		if w := strings.TrimSpace(c.Website); w != "" {
			doc["website"] = w
		}
		if rd.dt == "Supplier" && strings.TrimSpace(c.TaxID) != "" {
			doc["us_ein"] = strings.TrimSpace(c.TaxID)
		}
		row, ok, err := fetch(ctx, s.ops, rd.dt, c.ID)
		if err != nil {
			s.add(rep, Action{rd.kind, c.ID, c.Name, "failed", err.Error()})
			continue
		}
		if !ok && s.link(ctx, rep, strings.ToLower(rd.dt), rd.dt, c.ID, c.Name, c.Name, "", write) {
			placed = true
			continue
		}
		if !ok && rd.dt == "Supplier" && strings.TrimSpace(c.TaxID) == "" {
			s.add(rep, Action{"supplier", c.ID, c.Name, "needs an EIN", "EmberOps will not make a Supplier without a tax ID; add one to the company in Hearth"})
			continue
		}
		if ok {
			if own, err := s.isAdopted(ctx, rd.dt, c.ID); err != nil {
				s.add(rep, Action{strings.ToLower(rd.dt), c.ID, c.Name, "failed", err.Error()})
				continue
			} else if own {
				s.fillOnly(ctx, rep, strings.ToLower(rd.dt), rd.dt, c.ID, c.Name, doc, write)
				placed = true
				continue
			}
		}
		verb := "create"
		if ok {
			verb = "update"
			s.overwrites(rep, strings.ToLower(rd.dt), c.ID, c.Name, doc, row, write)
			if s.same(ctx, doc, row) {
				s.add(rep, Action{strings.ToLower(rd.dt), c.ID, c.Name, "same", "nothing changed since the last run"})
				placed = true
				continue
			}
		}
		if !write {
			s.add(rep, Action{strings.ToLower(rd.dt), c.ID, c.Name, "would " + verb, strings.Join(tagsFor(c), ", ")})
			placed = true
			continue
		}
		if _, err := s.send(ctx, rd.dt, c.ID, doc); err != nil {
			if errors.Is(err, emberops.ErrDuplicate) {
				s.add(rep, Action{strings.ToLower(rd.dt), c.ID, c.Name, "conflict",
					"EmberOps already has a " + rd.dt + " with this name that Hearth did not make; not merged"})
				continue
			}
			if strings.Contains(err.Error(), "requires the following fields") {
				// The US compliance pack asks a Supplier for us_ein and for us_w9_on_file,
				// which is an attestation Hearth does not make. That is a reason to wait for
				// staff, not a failed run.
				s.add(rep, Action{strings.ToLower(rd.dt), c.ID, c.Name, "needs fields", "EmberOps wants more before it will save this: " + err.Error()})
				continue
			}
			s.add(rep, Action{strings.ToLower(rd.dt), c.ID, c.Name, "failed", err.Error()})
			continue
		}
		s.add(rep, Action{strings.ToLower(rd.dt), c.ID, c.Name, verb, strings.Join(tagsFor(c), ", ")})
		placed = true
	}
	// A record EmberOps has that this company no longer has the role for.
	for _, role := range []string{"customer", "supplier"} {
		rd := roleDoctype[role]
		if have[rd.dt] {
			continue
		}
		row, ok, err := s.known(ctx, rd.dt, c.ID)
		if err != nil {
			s.add(rep, Action{"disable", c.ID, c.Name, "failed", err.Error()})
			continue
		}
		if !ok {
			continue
		}
		s.retire(ctx, rep, c, rd.dt, row, write)
	}
	if len(roles) == 0 {
		if s.prospects {
			s.prospect(ctx, rep, c, write)
		}
		return
	}
	// Contacts hang off a Customer or Supplier, so none can go until one exists.
	if !placed {
		s.add(rep, Action{"contact", c.ID, c.Name, "held", "its contacts wait until the company has a Customer or Supplier in EmberOps"})
		return
	}
	// Contacts follow the company's first accounting record.
	contacts, err := s.contacts(ctx, c.ID)
	if err != nil {
		s.add(rep, Action{"contact", c.ID, c.Name, "failed", err.Error()})
		return
	}
	linkKind := roleDoctype[roles[0]].kind
	if c.Roles["customer"] {
		linkKind = "customer"
	}
	s.people(ctx, rep, c, contacts, linkKind, write)
}

// people sends a company's contacts, each linked to the company's record in EmberOps: its Customer or Supplier by
// default, or its Lead when linkKind is "lead".
func (s *Syncer) people(ctx context.Context, rep *Report, c company, contacts []contact, linkKind string, write bool) {
	for _, ct := range contacts {
		doc := mapping.TZ{}.ContactDoc(mapping.Rec{"id": ct.ID, "name": ct.Name, "email": ct.Email, "phone": ct.Phone,
			"title": ct.Title, "notes": ct.Notes, "company_id": c.ID, "company_kind": linkKind})
		delete(doc, "doctype")
		delete(doc, "hearth_id")
		if linkKind == "lead" {
			// A Lead is not a Customer or Supplier, which is all ContactDoc knows how to link to.
			doc["links"] = []any{map[string]any{"link_doctype": "Lead", "link_name": mapping.Ref(c.ID)}}
		}
		s.stamp(doc, "contacts", ct.ID, ct.Owner)
		unsubscribe(doc, ct.OptOut)
		crow, ok, err := fetch(ctx, s.ops, "Contact", ct.ID)
		if err != nil {
			s.add(rep, Action{"contact", ct.ID, ct.Name, "failed", err.Error()})
			continue
		}
		if !ok && s.link(ctx, rep, "contact", "Contact", ct.ID, ct.Name, ct.Name, ct.Email, write) {
			continue
		}
		if ok {
			if own, err := s.isAdopted(ctx, "Contact", ct.ID); err != nil {
				s.add(rep, Action{"contact", ct.ID, ct.Name, "failed", err.Error()})
				continue
			} else if own {
				s.fillOnly(ctx, rep, "contact", "Contact", ct.ID, ct.Name, doc, write)
				continue
			}
		}
		verb := "create"
		if ok {
			verb = "update"
			s.overwrites(rep, "contact", ct.ID, ct.Name, doc, crow, write)
			if s.same(ctx, doc, crow) {
				s.add(rep, Action{"contact", ct.ID, ct.Name, "same", "nothing changed since the last run"})
				continue
			}
		}
		if !write {
			s.add(rep, Action{"contact", ct.ID, ct.Name, "would " + verb, c.Name})
			continue
		}
		if _, err := s.send(ctx, "Contact", ct.ID, doc); err != nil {
			s.add(rep, Action{"contact", ct.ID, ct.Name, "failed", err.Error()})
			continue
		}
		s.add(rep, Action{"contact", ct.ID, ct.Name, verb, c.Name})
	}
}

// transactionDoctypes are the documents that mean accounting has used a Customer
// or Supplier, by the field that names it.
var transactionDoctypes = map[string][]struct{ dt, field string }{
	"Customer": {{"Sales Invoice", "customer"}, {"Sales Order", "customer"}, {"Quotation", "party_name"},
		{"Delivery Note", "customer"}, {"Payment Entry", "party"}},
	"Supplier": {{"Purchase Invoice", "supplier"}, {"Purchase Order", "supplier"}, {"Payment Entry", "party"},
		{"Purchase Receipt", "supplier"}},
}

// retire handles a company that has an EmberOps Customer or Supplier it no longer
// has the type for: disable it when accounting never used it, keep it and say so
// when it did. It never deletes.
func (s *Syncer) retire(ctx context.Context, rep *Report, c company, dt string, row emberops.Row, write bool) {
	name, _ := row["name"].(string)
	if disabled, _ := row["disabled"].(float64); disabled == 1 {
		return
	}
	if own, err := s.isAdopted(ctx, dt, c.ID); err == nil && own {
		s.add(rep, Action{"disable", c.ID, c.Name, "kept", fmt.Sprintf("the %s %s was EmberOps' own before the link; it is never disabled from here", dt, name)})
		return
	}
	used := 0
	for _, t := range transactionDoctypes[dt] {
		n, err := s.ops.Count(ctx, t.dt, []emberops.Filter{emberops.F(t.field, name)})
		if err != nil {
			// Not being able to see whether accounting used it is a reason to leave it
			// alone, never a reason to disable it and never a failed run.
			s.add(rep, Action{"disable", c.ID, c.Name, "kept", fmt.Sprintf("could not check whether %s %s was used (%s); left as it is", dt, name, err.Error())})
			return
		}
		used += n
	}
	if used > 0 {
		s.add(rep, Action{"disable", c.ID, c.Name, "keep", fmt.Sprintf("the %s %s has %d transactions, so it stays", dt, name, used)})
		return
	}
	if !write {
		s.add(rep, Action{"disable", c.ID, c.Name, "would disable", fmt.Sprintf("the %s %s has no transactions", dt, name)})
		return
	}
	if _, err := s.send(ctx, dt, c.ID, map[string]any{"disabled": 1}); err != nil {
		s.add(rep, Action{"disable", c.ID, c.Name, "failed", err.Error()})
		return
	}
	s.add(rep, Action{"disable", c.ID, c.Name, "disable", fmt.Sprintf("the %s %s has no transactions", dt, name)})
}

// remove takes the EmberOps records of one named company away: its contacts, and
// its Customer and Supplier. It is for proof companies.
func (s *Syncer) remove(ctx context.Context, rep *Report, c company) {
	contacts, _ := s.contacts(ctx, c.ID)
	for _, ct := range contacts {
		s.del(ctx, rep, "Contact", ct.ID, ct.Name, "contact")
	}
	s.del(ctx, rep, "Customer", c.ID, c.Name, "customer")
	s.del(ctx, rep, "Supplier", c.ID, c.Name, "supplier")
	s.del(ctx, rep, "Lead", c.ID, c.Name, "lead")
}

func (s *Syncer) del(ctx context.Context, rep *Report, dt, id, name, what string) {
	ok, err := exists(ctx, s.ops, dt, id)
	switch {
	case err != nil:
		s.add(rep, Action{what, id, name, "failed", err.Error()})
	case !ok:
	default:
		if own, _ := s.isAdopted(ctx, dt, id); own {
			s.add(rep, Action{what, id, name, "kept", dt + " was EmberOps' own before the link; never deleted from here"})
			return
		}
		if err := s.ops.DeleteByHearthID(ctx, dt, id); err != nil {
			s.add(rep, Action{what, id, name, "failed", err.Error()})
			return
		}
		s.add(rep, Action{what, id, name, "removed", dt})
	}
}

// union is the one thing that comes back: a Customer or Supplier EmberOps has,
// made from a Hearth company, whose type Hearth does not carry. It adds the type
// and never removes one.
func (s *Syncer) union(ctx context.Context, rep *Report, write bool) {
	for _, role := range []string{"customer", "supplier"} {
		rd := roleDoctype[role]
		rows, err := s.ops.ListAll(ctx, emberops.ListQuery{Doctype: rd.dt, Fields: []string{"name", "hearth_id", "disabled"},
			Filters: []emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"}}}, readAll)
		if err != nil {
			s.add(rep, Action{"union", "", rd.dt, "failed", err.Error()})
			continue
		}
		for _, r := range rows {
			hid, _ := r["hearth_id"].(string)
			if hid == "" {
				continue
			}
			var has bool
			if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM company_type_links l
				JOIN company_types t ON t.id = l.type_id WHERE l.company_id = $1::uuid AND t.org_id = $2 AND t.erp_role = $3)`,
				hid, s.org, role).Scan(&has); err != nil {
				s.add(rep, Action{"union", hid, rd.dt, "failed", err.Error()})
				continue
			}
			if has {
				continue
			}
			label, key := "Client", "client"
			if role == "supplier" {
				label, key = "Vendor", "vendor"
			}
			if !write {
				s.add(rep, Action{"union", hid, fmt.Sprint(r["name"]), "would add type", label})
				continue
			}
			ct, err := s.db.Exec(ctx, `INSERT INTO company_type_links (company_id, type_id)
				SELECT $1::uuid, t.id FROM company_types t JOIN companies c ON c.id = $1::uuid AND c.org_id = t.org_id
				WHERE t.org_id = $2 AND t.key = $3 AND NOT t.archived ON CONFLICT DO NOTHING`, hid, s.org, key)
			if err != nil {
				s.add(rep, Action{"union", hid, fmt.Sprint(r["name"]), "failed", err.Error()})
				continue
			}
			if ct.RowsAffected() > 0 {
				s.add(rep, Action{"union", hid, fmt.Sprint(r["name"]), "add type", label})
			}
		}
	}
}

// Print writes the report as text.
func (r *Report) Print(w io.Writer) {
	mode := "DRY RUN, nothing was written to either side"
	if r.Write {
		mode = "WRITE"
	}
	fmt.Fprintf(w, "%s\n\n", mode)
	for _, a := range r.Actions {
		fmt.Fprintf(w, "%-13s %-9s %-36s %s", a.Verb, a.What, a.ID, a.Name)
		if a.Detail != "" {
			fmt.Fprintf(w, "   (%s)", a.Detail)
		}
		fmt.Fprintln(w)
	}
	verbs := map[string]int{}
	for _, a := range r.Actions {
		verbs[a.What+" "+a.Verb]++
	}
	keys := make([]string, 0, len(verbs))
	for k := range verbs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintln(w, "\ntotals:")
	for _, k := range keys {
		fmt.Fprintf(w, "  %-28s %d\n", k, verbs[k])
	}
}

// show reads back what EmberOps holds for one company and its contacts.
func (s *Syncer) show(ctx context.Context, rep *Report, c company) {
	keep := []string{"name", "customer_name", "supplier_name", "hearth_id", "hearth_domain", "hearth_notes",
		"hearth_company_kind", "us_ein", "disabled", "_user_tags", "_tags", "email_id", "first_name", "last_name", "links", "unsubscribed"}
	one := func(kind, dt, id, label string) {
		row, err := s.ops.GetByHearthID(ctx, dt, id)
		if errors.Is(err, emberops.ErrNotFound) {
			s.add(rep, Action{kind, id, label, "absent", dt + " has no record for this id"})
			return
		}
		if err != nil {
			s.add(rep, Action{kind, id, label, "failed", err.Error()})
			return
		}
		seen := map[string]any{}
		for _, k := range keep {
			if v, ok := row[k]; ok {
				seen[k] = v
			}
		}
		b, _ := json.Marshal(seen)
		s.add(rep, Action{kind, id, label, "read", dt + " " + string(b)})
	}
	one("customer", "Customer", c.ID, c.Name)
	one("supplier", "Supplier", c.ID, c.Name)
	one("lead", "Lead", c.ID, c.Name)
	cts, _ := s.contacts(ctx, c.ID)
	for _, ct := range cts {
		one("contact", "Contact", ct.ID, ct.Name)
	}
}
