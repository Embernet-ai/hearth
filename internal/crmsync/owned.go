package crmsync

// One owner per field, pinned in code.
//
// Hearth owns what it sends, and EmberOps owns what only accounting knows: the
// credit limit, payment terms, tax rules, accounts, price lists, and anything
// an invoice hangs off. The sync may only put the fields listed here on an
// EmberOps record. A field that is not on the list is not sent, whatever builds
// the document: a new mapping that reaches for credit_limit is refused here, in
// the run, and shows up as a failed action instead of quietly overwriting
// accounting's number. The test beside this file holds every document the sync
// builds to the same list.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Embernet-ai/hearth/internal/emberops"
)

// ErrNotHearths is returned when a document carries a field the sync does not
// own.
var ErrNotHearths = errors.New("crmsync: that field belongs to EmberOps and is never sent from Hearth")

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// hearthOwned is, per doctype, every field the sync may send. "hearth_id" is
// the key every record is found by, and "_tags" and "disabled" are the sync's
// own controls: tags carry the types, and a disable is how a removed type is
// passed on.
var hearthOwned = map[string]map[string]bool{
	"Customer": set("hearth_id", "customer_name", "customer_type", "hearth_company_kind", "hearth_domain",
		"hearth_notes", "hearth_deleted_at", "disabled", "_tags", "website", "hearth_url", "hearth_owner"),
	"Supplier": set("hearth_id", "supplier_name", "supplier_type", "hearth_company_kind", "hearth_domain",
		"hearth_notes", "hearth_deleted_at", "us_ein", "disabled", "_tags", "website", "hearth_url", "hearth_owner"),
	"Contact": set("hearth_id", "first_name", "last_name", "designation", "hearth_notes", "email_ids", "phone_nos",
		"links", "hearth_deleted_at", "hearth_url", "hearth_owner"),
	"Lead":    set("hearth_id", "lead_name", "company_name", "email_id", "_tags", "website", "hearth_url", "hearth_owner"),
	"Comment": set("hearth_id", "comment_type", "reference_doctype", "reference_name", "content", "comment_by"),
	"Opportunity": set("hearth_id", "opportunity_from", "party_name", "title", "status", "sales_stage",
		"expected_closing", "opportunity_amount", "currency", "hearth_url", "hearth_owner"),
}

// EmberOpsOwned names fields that are accounting's, for the test that proves
// none of them can be sent. Not exhaustive: the list above is the rule, this is
// the set of things most likely to be reached for by mistake.
var EmberOpsOwned = []string{"credit_limit", "credit_limits", "payment_terms", "tax_id", "tax_category",
	"tax_withholding_category", "default_price_list", "default_currency", "accounts", "default_bank_account",
	"customer_group", "territory", "sales_team", "default_sales_partner", "mode_of_payment"}

// OwnedBy reports whether the sync may send this field on this doctype.
func OwnedBy(dt, field string) bool { return hearthOwned[dt][field] }

// guard refuses a document that carries a field the sync does not own.
func guard(dt string, doc map[string]any) error {
	allowed, ok := hearthOwned[dt]
	if !ok {
		return fmt.Errorf("%w: no fields are listed for %s", ErrNotHearths, dt)
	}
	var bad []string
	for k := range doc {
		if k == "doctype" || allowed[k] {
			continue
		}
		bad = append(bad, k)
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("%w: %s on %s", ErrNotHearths, strings.Join(bad, ", "), dt)
	}
	return nil
}

// send is the only way the sync writes a record: the guard first, then the
// idempotent upsert.
func (s *Syncer) send(ctx context.Context, dt, hearthID string, doc map[string]any) (emberops.UpsertResult, error) {
	if err := guard(dt, doc); err != nil {
		return emberops.UpsertResult{}, err
	}
	return s.ops.Upsert(ctx, dt, hearthID, doc)
}

// overwrites says what an update is about to replace. Every field Hearth owns is
// sent each run, so if somebody changed one in EmberOps, or it changed in
// Hearth since the last run, the new value wins; this lists each such field with
// what was there and what goes in, so nobody finds out by surprise. Only plain
// values are compared: tags and the nested email and phone rows are not.
func (s *Syncer) overwrites(rep *Report, kind, id, label string, doc map[string]any, have emberops.Row, write bool) {
	if have == nil {
		return
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	verb := "overwrite"
	if !write {
		verb = "would overwrite"
	}
	for _, k := range keys {
		if k == "_tags" || k == "doctype" || k == "hearth_id" {
			continue
		}
		var want string
		switch v := doc[k].(type) {
		case string:
			want = strings.TrimSpace(v)
		case float64, int, int64:
			want = fmt.Sprint(v)
		default:
			continue
		}
		old, ok := have[k]
		if !ok || blank(old) {
			continue
		}
		var cur string
		switch v := old.(type) {
		case string:
			cur = strings.TrimSpace(v)
		case float64, int, int64:
			cur = fmt.Sprint(v)
		default:
			continue
		}
		if want != "" && cur != want {
			s.add(rep, Action{kind, id, label, verb, fmt.Sprintf("%s: EmberOps has %q, Hearth sends %q", k, cur, want)})
		}
	}
}
