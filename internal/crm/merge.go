package crm

// Finding duplicate contacts and companies, and merging them.
//
// The unique indexes already stop two contacts holding the same address and two companies the same domain, so the
// duplicates left are the ones those cannot see: the same person under two addresses, the same name at the same firm,
// the same phone number, the same firm under two spellings of its website. A merge keeps one record (the survivor),
// moves everything that pointed at the others onto it, fills the survivor's blanks from them, and removes them. Nothing
// is lost: the removed record's own values are kept in crm_merges, its email addresses and numbers become the survivor's
// extra ones, and a person who unsubscribed stays unsubscribed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// DupRecord is one record in a group of probable duplicates.
type DupRecord struct {
	ID        string
	Name      string
	Detail    string
	Owner     string
	Deals     int
	UpdatedAt time.Time
}

// DupGroup is records that look like one.
type DupGroup struct {
	Reason  string
	Records []DupRecord
}

func normWebsite(w string) string {
	w = strings.ToLower(strings.TrimSpace(w))
	if w == "" {
		return ""
	}
	if !strings.Contains(w, "://") {
		w = "http://" + w
	}
	u, err := url.Parse(w)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

func groupsFrom(buckets map[string][]DupRecord, reasonOf func(key string) string) []DupGroup {
	seen := map[string]bool{}
	var out []DupGroup
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		recs := buckets[k]
		if len(recs) < 2 {
			continue
		}
		ids := make([]string, len(recs))
		for i, r := range recs {
			ids[i] = r.ID
		}
		sort.Strings(ids)
		sig := strings.Join(ids, ",")
		if seen[sig] {
			continue
		}
		seen[sig] = true
		sort.Slice(recs, func(i, j int) bool { return recs[i].UpdatedAt.After(recs[j].UpdatedAt) })
		out = append(out, DupGroup{Reason: reasonOf(k), Records: recs})
		if len(out) >= 200 {
			break
		}
	}
	return out
}

// Duplicates finds probable duplicates among the contacts or the companies the caller may see.
func (s *Service) Duplicates(ctx context.Context, orgID string, actor core.Subjects, t core.EntityType) ([]DupGroup, error) {
	keys := keysOf(actor)
	buckets := map[string][]DupRecord{}
	add := func(k string, r DupRecord) {
		for _, x := range buckets[k] {
			if x.ID == r.ID {
				return
			}
		}
		buckets[k] = append(buckets[k], r)
	}
	switch t {
	case core.EntityContact:
		rows, err := s.db.Query(ctx, `
			SELECT c.id::text, c.name, c.email, c.extra_emails, coalesce(phone_e164(c.phone), ''), c.extra_phones,
			       coalesce(c.company_id::text, ''), coalesce(co.name, ''), coalesce(op.display_name, ''),
			       (SELECT count(*) FROM deals d WHERE d.contact_id = c.id AND d.deleted_at IS NULL)::int, c.updated_at
			FROM contacts c
			LEFT JOIN companies co ON co.id = c.company_id AND co.deleted_at IS NULL
			LEFT JOIN principals op ON op.id = c.owner_id
			WHERE c.org_id = $1 AND c.deleted_at IS NULL AND `+visible("contact", "c", 2), orgID, keys)
		if err != nil {
			return nil, fmt.Errorf("crm: duplicate contacts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, name, email, p164, comp, compName, owner string
			var extraE, extraP []string
			var deals int
			var upd time.Time
			if err := rows.Scan(&id, &name, &email, &extraE, &p164, &extraP, &comp, &compName, &owner, &deals, &upd); err != nil {
				return nil, err
			}
			rec := DupRecord{ID: id, Name: name, Detail: strings.Trim(strings.Join([]string{email, compName}, " · "), " ·"), Owner: owner, Deals: deals, UpdatedAt: upd}
			if n := strings.ToLower(strings.TrimSpace(name)); n != "" {
				add("n:"+n+"|"+comp, rec)
			}
			if p164 != "" {
				add("p:"+p164, rec)
			}
			for _, e := range append([]string{email}, extraE...) {
				if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
					add("e:"+e, rec)
				}
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return groupsFrom(buckets, func(k string) string {
			switch k[0] {
			case 'n':
				return "Same name at the same company"
			case 'p':
				return "Same phone number"
			}
			return "One person's address is another's other address"
		}), nil
	case core.EntityCompany:
		rows, err := s.db.Query(ctx, `
			SELECT c.id::text, c.name, c.domain, c.website, coalesce(op.display_name, ''),
			       (SELECT count(*) FROM deals d WHERE d.company_id = c.id AND d.deleted_at IS NULL)::int, c.updated_at
			FROM companies c LEFT JOIN principals op ON op.id = c.owner_id
			WHERE c.org_id = $1 AND c.deleted_at IS NULL AND `+visible("company", "c", 2), orgID, keys)
		if err != nil {
			return nil, fmt.Errorf("crm: duplicate companies: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, name, domain, website, owner string
			var deals int
			var upd time.Time
			if err := rows.Scan(&id, &name, &domain, &website, &owner, &deals, &upd); err != nil {
				return nil, err
			}
			rec := DupRecord{ID: id, Name: name, Detail: strings.Trim(strings.Join([]string{domain, website}, " · "), " ·"), Owner: owner, Deals: deals, UpdatedAt: upd}
			if n := strings.ToLower(strings.Join(strings.Fields(name), " ")); n != "" {
				add("n:"+n, rec)
			}
			if h := normWebsite(website); h != "" {
				add("w:"+h, rec)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return groupsFrom(buckets, func(k string) string {
			if k[0] == 'n' {
				return "Same company name"
			}
			return "Same website"
		}), nil
	}
	return nil, fmt.Errorf("%w: duplicates are found among contacts and companies", ErrInvalid)
}

func unionFold(first []string, more ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range append([][]string{first}, more...) {
		for _, v := range list {
			v = strings.TrimSpace(v)
			if v == "" || seen[strings.ToLower(v)] {
				continue
			}
			seen[strings.ToLower(v)] = true
			out = append(out, v)
		}
	}
	return out
}

func without(list []string, drop string) []string {
	var out []string
	for _, v := range list {
		if !strings.EqualFold(v, drop) {
			out = append(out, v)
		}
	}
	return out
}

// Merge folds the loser records into the survivor, all of one kind. The caller needs edit on all of them: nothing is
// destroyed, because what each removed record held is kept in crm_merges, so this is not the admin act a delete is.
func (s *Service) Merge(ctx context.Context, orgID string, actor core.Subjects, t core.EntityType, survivor string, losers []string) error {
	if t != core.EntityContact && t != core.EntityCompany {
		return fmt.Errorf("%w: only contacts and companies are merged", ErrInvalid)
	}
	survivor = strings.TrimSpace(survivor)
	var ls []string
	for _, l := range losers {
		l = strings.TrimSpace(l)
		if l != "" && l != survivor {
			ls = append(ls, l)
		}
	}
	if _, err := uuid.Parse(survivor); err != nil {
		return fmt.Errorf("%w: that is not a record id", ErrInvalid)
	}
	for _, l := range ls {
		if _, err := uuid.Parse(l); err != nil {
			return fmt.Errorf("%w: that is not a record id", ErrInvalid)
		}
	}
	if survivor == "" || len(ls) == 0 {
		return fmt.Errorf("%w: pick the record to keep and at least one to fold into it", ErrInvalid)
	}
	if len(ls) > 20 {
		return fmt.Errorf("%w: at most 20 at a time", ErrInvalid)
	}
	if err := s.authz.Require(ctx, actor, core.Ref{Type: t, ID: survivor}, core.AccessEdit); err != nil {
		return ErrNotFound
	}
	for _, l := range ls {
		if err := s.authz.Require(ctx, actor, core.Ref{Type: t, ID: l}, core.AccessEdit); err != nil {
			return ErrNotFound
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, l := range ls {
		if t == core.EntityContact {
			err = mergeContact(ctx, tx, orgID, actor.Principal(), survivor, l)
		} else {
			err = mergeCompany(ctx, tx, orgID, actor.Principal(), survivor, l)
		}
		if err != nil {
			return err
		}
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{OrgID: orgID, EntityType: string(t), EntityID: survivor,
		Event: jobs.EventUpdated, ActorID: actor.Principal(), Payload: map[string]any{"merged": len(ls)}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func recordMerge(ctx context.Context, tx pgx.Tx, orgID, by string, t core.EntityType, survivor, loser string, snapshot any) error {
	b, _ := json.Marshal(snapshot)
	_, err := tx.Exec(ctx, `INSERT INTO crm_merges (id, org_id, entity_type, survivor_id, loser_id, merged_by, loser)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, uuid.NewString(), orgID, string(t), survivor, loser, by, b)
	if err != nil {
		return fmt.Errorf("crm: record merge: %w", err)
	}
	return nil
}

// repoint moves everything that names the loser onto the survivor, in one table at a time.
func repoint(ctx context.Context, tx pgx.Tx, t core.EntityType, survivor, loser string) error {
	typ := string(t)
	stmts := []string{
		`UPDATE crm_followups SET target_id = $2 WHERE target_type = $1 AND target_id = $3`,
		`INSERT INTO crm_activity_targets (activity_id, target_type, target_id)
		   SELECT activity_id, target_type, $2::uuid FROM crm_activity_targets WHERE target_type = $1 AND target_id = $3::uuid
		   ON CONFLICT DO NOTHING`,
		`DELETE FROM crm_activity_targets WHERE target_type = $1 AND target_id = $3::uuid`,
		`DELETE FROM crm_favorites f WHERE f.entity_type = $1 AND f.entity_id = $3::uuid
		   AND EXISTS (SELECT 1 FROM crm_favorites g WHERE g.entity_type = $1 AND g.entity_id = $2::uuid AND g.principal_id = f.principal_id)`,
		`UPDATE crm_favorites SET entity_id = $2::uuid WHERE entity_type = $1 AND entity_id = $3::uuid`,
		`UPDATE entity_links SET from_id = $2::uuid WHERE from_type = $1 AND from_id = $3::uuid
		   AND NOT EXISTS (SELECT 1 FROM entity_links x WHERE x.from_type = entity_links.from_type AND x.from_id = $2::uuid
		       AND x.to_type = entity_links.to_type AND x.to_id = entity_links.to_id AND x.kind = entity_links.kind)`,
		`UPDATE entity_links SET to_id = $2::uuid WHERE to_type = $1 AND to_id = $3::uuid
		   AND NOT EXISTS (SELECT 1 FROM entity_links x WHERE x.to_type = entity_links.to_type AND x.to_id = $2::uuid
		       AND x.from_type = entity_links.from_type AND x.from_id = entity_links.from_id AND x.kind = entity_links.kind)`,
	}
	switch t {
	case core.EntityContact:
		stmts = append(stmts,
			`UPDATE deals SET contact_id = $2::uuid, updated_at = now() WHERE $1 = 'contact' AND contact_id = $3::uuid`,
			`UPDATE calls SET contact_id = $2::uuid WHERE $1 = 'contact' AND contact_id = $3::uuid`,
			`UPDATE connector_mail SET contact_id = $2::uuid WHERE $1 = 'contact' AND contact_id = $3::uuid`)
	case core.EntityCompany:
		stmts = append(stmts,
			`UPDATE deals SET company_id = $2::uuid, updated_at = now() WHERE $1 = 'company' AND company_id = $3::uuid`,
			`UPDATE contacts SET company_id = $2::uuid, updated_at = now() WHERE $1 = 'company' AND company_id = $3::uuid`,
			`UPDATE parts SET supplier_id = $2::uuid WHERE $1 = 'company' AND supplier_id = $3::uuid`,
			`INSERT INTO company_tags (company_id, tag) SELECT $2::uuid, tag FROM company_tags WHERE $1 = 'company' AND company_id = $3::uuid ON CONFLICT DO NOTHING`,
			`INSERT INTO company_type_links (company_id, type_id) SELECT $2::uuid, type_id FROM company_type_links WHERE $1 = 'company' AND company_id = $3::uuid ON CONFLICT DO NOTHING`)
	}
	// The three values are a fixed type name and two ids that Merge has already parsed as UUIDs, so they are written into
	// the statements; that way a statement need not mention every one of them.
	lit := strings.NewReplacer("$1", "'"+typ+"'", "$2", "'"+survivor+"'", "$3", "'"+loser+"'")
	for _, q := range stmts {
		if _, err := tx.Exec(ctx, lit.Replace(q)); err != nil {
			return fmt.Errorf("crm: move records to the survivor: %w", err)
		}
	}
	return nil
}

func mergeContact(ctx context.Context, tx pgx.Tx, orgID, by, survivor, loser string) error {
	type row struct {
		Name, Email, Phone, Title, Notes, LinkedIn string
		Company, Owner                             *string
		ExtraE, ExtraP                             []string
		OptOutAt                                   *time.Time
		OptOutReason                               string
	}
	read := func(id string) (row, error) {
		var r row
		err := tx.QueryRow(ctx, `SELECT name, email, phone, title, notes, linkedin_url, company_id::text, owner_id::text, extra_emails, extra_phones,
			marketing_opt_out_at, marketing_opt_out_reason FROM contacts WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL FOR UPDATE`, id, orgID).
			Scan(&r.Name, &r.Email, &r.Phone, &r.Title, &r.Notes, &r.LinkedIn, &r.Company, &r.Owner, &r.ExtraE, &r.ExtraP, &r.OptOutAt, &r.OptOutReason)
		if errors.Is(err, pgx.ErrNoRows) {
			return r, ErrNotFound
		}
		return r, err
	}
	sv, err := read(survivor)
	if err != nil {
		return err
	}
	lo, err := read(loser)
	if err != nil {
		return err
	}
	if err := recordMerge(ctx, tx, orgID, by, core.EntityContact, survivor, loser, lo); err != nil {
		return err
	}
	// Remove the loser first: it frees its address for the unique index before the survivor takes it.
	if _, err := tx.Exec(ctx, `UPDATE contacts SET deleted_at=now(), updated_at=now() WHERE id=$1`, loser); err != nil {
		return fmt.Errorf("crm: remove the merged contact: %w", err)
	}
	if err := repoint(ctx, tx, core.EntityContact, survivor, loser); err != nil {
		return err
	}
	email := sv.Email
	if email == "" {
		email = lo.Email
	}
	emails := without(unionFold(sv.ExtraE, lo.ExtraE, []string{sv.Email, lo.Email}), email)
	phone := sv.Phone
	if phone == "" {
		phone = lo.Phone
	}
	phones := unionFold(sv.ExtraP, lo.ExtraP, []string{sv.Phone, lo.Phone})
	phones = without(phones, phone)
	notes := sv.Notes
	if n := strings.TrimSpace(lo.Notes); n != "" {
		notes = strings.TrimSpace(notes + "\n\nFrom " + lo.Name + " (merged): " + n)
	}
	pick := func(a, b string) string {
		if a != "" {
			return a
		}
		return b
	}
	company, owner := sv.Company, sv.Owner
	if company == nil {
		company = lo.Company
	}
	if owner == nil {
		owner = lo.Owner
	}
	// An unsubscribe is the person's and is never undone: if either record had one, the survivor has it.
	optAt, optWhy := sv.OptOutAt, sv.OptOutReason
	if optAt == nil && lo.OptOutAt != nil {
		optAt, optWhy = lo.OptOutAt, lo.OptOutReason
	}
	if _, err := tx.Exec(ctx, `UPDATE contacts SET email=$3, extra_emails=$4, phone=$5, extra_phones=$6, title=$7, linkedin_url=$8,
		notes=$9, company_id=$10::uuid, owner_id=$11::uuid, marketing_opt_out_at=$12, marketing_opt_out_reason=$13, updated_at=now()
		WHERE id=$1 AND org_id=$2`, survivor, orgID, email, emptyIfNil(emails), phone, emptyIfNil(phones), pick(sv.Title, lo.Title),
		pick(sv.LinkedIn, lo.LinkedIn), notes, company, owner, optAt, optWhy); err != nil {
		return fmt.Errorf("crm: fill the surviving contact: %w", err)
	}
	return jobs.Publish(ctx, tx, jobs.EventSpec{OrgID: orgID, EntityType: string(core.EntityContact), EntityID: loser,
		Event: jobs.EventDeleted, ActorID: by, Payload: map[string]any{"merged_into": survivor}})
}

func emptyIfNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func mergeCompany(ctx context.Context, tx pgx.Tx, orgID, by, survivor, loser string) error {
	type row struct {
		Name, Domain, Kind, Notes, Website, LinkedIn, Street, City, State, Postcode, Country, Currency, TaxID string
		Owner                                                                                                 *string
		Revenue                                                                                               *int64
	}
	read := func(id string) (row, error) {
		var r row
		err := tx.QueryRow(ctx, `SELECT name, domain, coalesce(kind,''), notes, website, linkedin_url, address_street, address_city, address_state,
			address_postcode, address_country, revenue_currency, coalesce(tax_id,''), owner_id::text, annual_revenue_cents
			FROM companies WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL FOR UPDATE`, id, orgID).
			Scan(&r.Name, &r.Domain, &r.Kind, &r.Notes, &r.Website, &r.LinkedIn, &r.Street, &r.City, &r.State, &r.Postcode, &r.Country, &r.Currency, &r.TaxID, &r.Owner, &r.Revenue)
		if errors.Is(err, pgx.ErrNoRows) {
			return r, ErrNotFound
		}
		return r, err
	}
	sv, err := read(survivor)
	if err != nil {
		return err
	}
	lo, err := read(loser)
	if err != nil {
		return err
	}
	if err := recordMerge(ctx, tx, orgID, by, core.EntityCompany, survivor, loser, lo); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE companies SET deleted_at=now(), updated_at=now() WHERE id=$1`, loser); err != nil {
		return fmt.Errorf("crm: remove the merged company: %w", err)
	}
	if err := repoint(ctx, tx, core.EntityCompany, survivor, loser); err != nil {
		return err
	}
	pick := func(a, b string) string {
		if a != "" {
			return a
		}
		return b
	}
	notes := sv.Notes
	if n := strings.TrimSpace(lo.Notes); n != "" {
		notes = strings.TrimSpace(notes + "\n\nFrom " + lo.Name + " (merged): " + n)
	}
	owner, revenue := sv.Owner, sv.Revenue
	if owner == nil {
		owner = lo.Owner
	}
	if revenue == nil {
		revenue = lo.Revenue
	}
	if _, err := tx.Exec(ctx, `UPDATE companies SET domain=$3, kind=NULLIF($4,''), notes=$5, website=$6, linkedin_url=$7, address_street=$8, address_city=$9,
		address_state=$10, address_postcode=$11, address_country=$12, tax_id=$13, owner_id=$14::uuid, annual_revenue_cents=$15, updated_at=now()
		WHERE id=$1 AND org_id=$2`, survivor, orgID, pick(sv.Domain, lo.Domain), pick(sv.Kind, lo.Kind), notes, pick(sv.Website, lo.Website),
		pick(sv.LinkedIn, lo.LinkedIn), pick(sv.Street, lo.Street), pick(sv.City, lo.City), pick(sv.State, lo.State), pick(sv.Postcode, lo.Postcode),
		pick(sv.Country, lo.Country), pick(sv.TaxID, lo.TaxID), owner, revenue); err != nil {
		return fmt.Errorf("crm: fill the surviving company: %w", err)
	}
	return jobs.Publish(ctx, tx, jobs.EventSpec{OrgID: orgID, EntityType: string(core.EntityCompany), EntityID: loser,
		Event: jobs.EventDeleted, ActorID: by, Payload: map[string]any{"merged_into": survivor}})
}
