package api_test

// The sales desk over the API, against a real Postgres: follow-ups, forecast, report, bulk change, and the refusals
// for a token whose person does not work sales and for a token that cannot write.

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestTheSalesDeskOverTheAPI(t *testing.T) {
	f := crmSetup(t)
	t.Cleanup(func() {
		_, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM crm_followups WHERE org_id=$1`, f.org)
	})
	alice, bob := f.token(t, f.alice, "write"), f.token(t, f.bob, "write")
	carol, aliceRead := f.token(t, f.carol, "write"), f.token(t, f.alice, "read")

	code, deal, _ := f.call(t, alice, "POST", "/v1/deals", map[string]any{"name": "Desk deal", "stage": "proposal", "amount_cents": 100000, "close_date": "2099-12-10"})
	if code != http.StatusCreated {
		t.Fatalf("create deal = %d %v", code, deal)
	}
	id := deal["id"].(string)
	if deal["probability_percent"].(float64) != 60 || deal["weighted_cents"].(float64) != 60000 || deal["lost_reason"] != "" {
		t.Errorf("deal fields = %v", deal)
	}

	// A follow-up, seen by a colleague who did not make it.
	code, fu, _ := f.call(t, alice, "POST", "/v1/followups", map[string]any{"title": "Send the quote", "due": "2000-01-01",
		"assignee_id": f.bob, "target": map[string]any{"type": "deal", "id": id}})
	if code != http.StatusCreated || fu["overdue"] != true || fu["assignee_name"] != "Bob" {
		t.Fatalf("create follow-up = %d %v", code, fu)
	}
	code, list, _ := f.call(t, bob, "GET", "/v1/followups?due=overdue", nil)
	if code != http.StatusOK || list["total"].(float64) != 1 {
		t.Fatalf("bob's overdue = %d %v", code, list)
	}
	if _, mine, _ := f.call(t, alice, "GET", "/v1/followups?assignee=me", nil); mine["total"].(float64) != 0 {
		t.Errorf("alice's own = %v, want 0 (it is bob's)", mine["total"])
	}
	fid := fu["id"].(string)
	code, done, _ := f.call(t, bob, "PATCH", "/v1/followups/"+fid, map[string]any{"done": true})
	if code != http.StatusOK || done["status"] != "done" || done["due"] != "2000-01-01" {
		t.Errorf("complete = %d %v (the date must survive)", code, done)
	}
	if code, _, _ := f.call(t, alice, "POST", "/v1/followups", map[string]any{"title": "x", "target": map[string]any{"type": "document", "id": id}}); code != http.StatusBadRequest {
		t.Errorf("a follow-up on a document = %d, want 400", code)
	}
	if code, _, _ := f.call(t, alice, "POST", "/v1/followups", map[string]any{"title": "x", "due": "tomorrow", "target": map[string]any{"type": "deal", "id": id}}); code != http.StatusBadRequest {
		t.Errorf("a bad date = %d, want 400", code)
	}

	// Lost reason through the deal.
	f.call(t, alice, "PATCH", "/v1/deals/"+id, map[string]any{"stage": "lost", "lost_reason": "price"})
	if _, got, _ := f.call(t, bob, "GET", "/v1/deals/"+id, nil); got["lost_reason"] != "price" || got["closed_at"] == nil {
		t.Errorf("lost deal = %v", got)
	}
	f.call(t, alice, "PATCH", "/v1/deals/"+id, map[string]any{"stage": "proposal"})
	if _, got, _ := f.call(t, bob, "GET", "/v1/deals/"+id, nil); got["lost_reason"] != "price" {
		t.Errorf("a stage change dropped the lost reason: %v", got)
	}

	// Forecast and report, everybody's by default.
	code, fc, _ := f.call(t, bob, "GET", "/v1/crm/forecast", nil)
	if code != http.StatusOK || fc["count"].(float64) != 1 || fc["weighted"].([]any)[0].(map[string]any)["cents"].(float64) != 60000 {
		t.Errorf("forecast = %d %v", code, fc)
	}
	code, rep, _ := f.call(t, bob, "GET", "/v1/crm/reports?from=2000-01-01&to=2100-01-01", nil)
	if code != http.StatusOK || rep["open"].(map[string]any)["count"].(float64) != 1 || rep["win_rate_percent"] != nil {
		t.Errorf("report = %d %v", code, rep)
	}
	if reasons, ok := rep["lost_reasons"].([]any); !ok || len(reasons) != 0 {
		t.Errorf("lost reasons = %v, want an empty list (the deal was moved back to proposal)", rep["lost_reasons"])
	}
	if ages, ok := rep["stage_age"].([]any); !ok || len(ages) != 1 || ages[0].(map[string]any)["stage"] != "proposal" {
		t.Errorf("stage age = %v, want the open deal in proposal", rep["stage_age"])
	}

	// Bulk.
	code, res, _ := f.call(t, bob, "POST", "/v1/crm/bulk", map[string]any{"type": "deal", "ids": []string{id}, "stage": "meeting", "owner_id": f.bob})
	if code != http.StatusOK || res["changed"].(float64) != 1 {
		t.Errorf("bulk = %d %v", code, res)
	}
	if code, _, _ := f.call(t, bob, "POST", "/v1/crm/bulk", map[string]any{"type": "deal", "ids": []string{id}}); code != http.StatusBadRequest {
		t.Errorf("bulk with no change = %d, want 400", code)
	}

	// Carol does not work sales: all of it is not there. A read token cannot write.
	for _, c := range []struct{ m, p string }{{"GET", "/v1/followups"}, {"GET", "/v1/crm/forecast"}, {"GET", "/v1/crm/reports"},
		{"POST", "/v1/followups"}, {"PATCH", "/v1/followups/" + fid}, {"DELETE", "/v1/followups/" + fid}, {"POST", "/v1/crm/bulk"}} {
		if code, _, _ := f.call(t, carol, c.m, c.p, map[string]any{}); code != http.StatusNotFound {
			t.Errorf("%s %s as a person outside sales = %d, want 404", c.m, c.p, code)
		}
	}
	if code, _, _ := f.call(t, aliceRead, "POST", "/v1/followups", map[string]any{"title": "x", "target": map[string]any{"type": "deal", "id": id}}); code != http.StatusForbidden {
		t.Errorf("a read token creating = %d, want 403", code)
	}
	if code, _, _ := f.call(t, aliceRead, "GET", "/v1/followups", nil); code != http.StatusOK {
		t.Errorf("a read token listing = %d, want 200", code)
	}
	if code, _, _ := f.call(t, bob, "DELETE", "/v1/followups/"+fid, nil); code != http.StatusOK {
		t.Errorf("delete = %d", code)
	}
}

func TestDuplicatesAndMergeOverTheAPI(t *testing.T) {
	f := crmSetup(t)
	t.Cleanup(func() { _, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM crm_merges WHERE org_id=$1`, f.org) })
	alice, bob, carol := f.token(t, f.alice, "write"), f.token(t, f.bob, "write"), f.token(t, f.carol, "write")
	aliceRead := f.token(t, f.alice, "read")
	_, a, _ := f.call(t, alice, "POST", "/v1/contacts", map[string]any{"name": "Dana API", "email": "dana@api.example"})
	_, b, _ := f.call(t, alice, "POST", "/v1/contacts", map[string]any{"name": "dana api", "email": "d.api@api.example", "phone": "(216) 555-0100"})
	code, dups, _ := f.call(t, bob, "GET", "/v1/crm/duplicates?type=contact", nil)
	if code != http.StatusOK || len(dups["groups"].([]any)) != 1 {
		t.Fatalf("duplicates = %d %v", code, dups)
	}
	if code, _, _ := f.call(t, carol, "GET", "/v1/crm/duplicates", nil); code != http.StatusNotFound {
		t.Errorf("duplicates for a person outside sales = %d, want 404", code)
	}
	body := map[string]any{"type": "contact", "survivor_id": a["id"], "merge_ids": []any{b["id"]}}
	if code, _, _ := f.call(t, carol, "POST", "/v1/crm/merge", body); code != http.StatusNotFound {
		t.Errorf("merge by a person outside sales = %d, want 404", code)
	}
	if code, _, _ := f.call(t, aliceRead, "POST", "/v1/crm/merge", body); code != http.StatusForbidden {
		t.Errorf("merge with a read token = %d, want 403", code)
	}
	if code, _, _ := f.call(t, bob, "POST", "/v1/crm/merge", map[string]any{"type": "contact", "survivor_id": a["id"]}); code != http.StatusBadRequest {
		t.Errorf("merge with nothing to merge = %d, want 400", code)
	}
	if code, res, _ := f.call(t, bob, "POST", "/v1/crm/merge", body); code != http.StatusOK || res["merged"].(float64) != 1 {
		t.Fatalf("merge = %d %v", code, res)
	}
	if code, _, _ := f.call(t, bob, "GET", "/v1/contacts/"+b["id"].(string), nil); code != http.StatusNotFound {
		t.Errorf("the merged contact = %d, want 404", code)
	}
	if _, got, _ := f.call(t, bob, "GET", "/v1/contacts/"+a["id"].(string), nil); got["phone"] != "(216) 555-0100" {
		t.Errorf("survivor = %v, want the duplicate's phone", got)
	}
}

func TestEditingATimelineEntryOverTheAPI(t *testing.T) {
	f := crmSetup(t)
	alice, bob, carol := f.token(t, f.alice, "write"), f.token(t, f.bob, "write"), f.token(t, f.carol, "write")
	aliceRead := f.token(t, f.alice, "read")
	_, deal, _ := f.call(t, alice, "POST", "/v1/deals", map[string]any{"name": "Edits API"})
	_, act, _ := f.call(t, alice, "POST", "/v1/activities", map[string]any{"kind": "call", "title": "Rang", "direction": "outbound", "targets": []map[string]any{{"type": "deal", "id": deal["id"]}}})
	path := "/v1/activities/" + act["id"].(string)
	code, got, _ := f.call(t, bob, "PATCH", path, map[string]any{"body": "left a voicemail", "disposition": "voicemail", "occurred_at": "2026-11-20T14:30:00Z"})
	if code != http.StatusOK || got["body"] != "left a voicemail" || got["disposition"] != "voicemail" || got["kind"] != "call" {
		t.Fatalf("edit = %d %v", code, got)
	}
	if code, _, _ := f.call(t, carol, "PATCH", path, map[string]any{"body": "x"}); code != http.StatusNotFound {
		t.Errorf("an edit by a person outside sales = %d, want 404", code)
	}
	if code, _, _ := f.call(t, aliceRead, "PATCH", path, map[string]any{"body": "x"}); code != http.StatusForbidden {
		t.Errorf("an edit with a read token = %d, want 403", code)
	}
	if code, _, _ := f.call(t, alice, "PATCH", path, map[string]any{"title": "", "body": ""}); code != http.StatusBadRequest {
		t.Errorf("emptying it = %d, want 400", code)
	}
	if code, _, _ := f.call(t, alice, "PATCH", "/v1/activities/00000000-0000-0000-0000-000000000001", map[string]any{"body": "x"}); code != http.StatusNotFound {
		t.Errorf("an entry that is not there = %d, want 404", code)
	}
	if code, _, _ := f.call(t, alice, "PATCH", path, map[string]any{"kind": "note"}); code != http.StatusBadRequest {
		t.Errorf("changing the kind = %d, want 400 (unknown field)", code)
	}
}

func TestCustomPropertiesOverTheAPI(t *testing.T) {
	f := crmSetup(t)
	ctx := context.Background()
	admin := uuid.NewString()
	for _, q := range []string{
		`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Ada Admin')`,
		`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($2,$1,'super_admin',$1)`,
	} {
		if _, err := f.pools.Web.Exec(ctx, q, admin, f.org); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.pools.Web.Exec(c, `DELETE FROM property_values WHERE def_id IN (SELECT id FROM property_defs WHERE org_id=$1)`, f.org)
		_, _ = f.pools.Web.Exec(c, `DELETE FROM property_defs WHERE org_id=$1`, f.org)
		_, _ = f.pools.Web.Exec(c, `DELETE FROM capability_grants WHERE org_id=$1`, f.org)
	})
	adm, alice, bob, carol := f.token(t, admin, "write"), f.token(t, f.alice, "write"), f.token(t, f.bob, "write"), f.token(t, f.carol, "write")
	aliceRead := f.token(t, f.alice, "read")

	// Only an admin defines a field; sales lists them; others are told nothing is there.
	body := map[string]any{"type": "company", "label": "Industry", "data_type": "select", "options": []string{"Plating", "Rewinds"}}
	if code, _, _ := f.call(t, alice, "POST", "/v1/crm/properties", body); code != http.StatusForbidden {
		t.Errorf("a salesperson defining a field = %d, want 403", code)
	}
	if code, _, _ := f.call(t, carol, "POST", "/v1/crm/properties", body); code != http.StatusNotFound {
		t.Errorf("a person outside sales defining a field = %d, want 404", code)
	}
	code, def, _ := f.call(t, adm, "POST", "/v1/crm/properties", body)
	if code != http.StatusCreated || def["key"] != "industry" || def["data_type"] != "select" {
		t.Fatalf("define = %d %v", code, def)
	}
	_, _, _ = f.call(t, adm, "POST", "/v1/crm/properties", map[string]any{"type": "company", "label": "Employees", "data_type": "number"})
	if code, _, _ := f.call(t, adm, "POST", "/v1/crm/properties", map[string]any{"type": "company", "label": "industry", "data_type": "text"}); code != http.StatusConflict {
		t.Errorf("the same key twice = %d, want 409", code)
	}
	if code, _, _ := f.call(t, adm, "POST", "/v1/crm/properties", map[string]any{"type": "company", "label": "X", "data_type": "colour"}); code != http.StatusBadRequest {
		t.Errorf("an unknown data type = %d, want 400", code)
	}
	code, list, _ := f.call(t, bob, "GET", "/v1/crm/properties?type=company", nil)
	if code != http.StatusOK || len(list["properties"].([]any)) != 2 {
		t.Errorf("list = %d %v, want the two company fields", code, list)
	}
	if code, _, _ := f.call(t, carol, "GET", "/v1/crm/properties?type=company", nil); code != http.StatusNotFound {
		t.Errorf("list by a person outside sales = %d, want 404", code)
	}

	// Values ride on the record: set on create, change on patch, read back, bad ones refused before anything changes.
	code, co, _ := f.call(t, alice, "POST", "/v1/companies", map[string]any{"name": "Props Co", "properties": map[string]any{"industry": "rewinds", "employees": "1,200"}})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, co)
	}
	pr := co["properties"].(map[string]any)
	if pr["industry"] != "Rewinds" || pr["employees"].(float64) != 1200 {
		t.Errorf("properties on create = %v, want Rewinds and 1200", pr)
	}
	if code, _, _ := f.call(t, alice, "POST", "/v1/companies", map[string]any{"name": "Bad Co", "properties": map[string]any{"employees": "lots"}}); code != http.StatusBadRequest {
		t.Errorf("a bad value on create = %d, want 400", code)
	}
	if _, l, _ := f.call(t, alice, "GET", "/v1/companies?q=Bad+Co", nil); l["total"].(float64) != 0 {
		t.Error("a request with a bad custom value still made the company")
	}
	path := "/v1/companies/" + co["id"].(string)
	code, got, _ := f.call(t, bob, "PATCH", path, map[string]any{"notes": "n", "properties": map[string]any{"employees": nil, "industry": "Plating"}})
	gp := got["properties"].(map[string]any)
	if code != http.StatusOK || gp["industry"] != "Plating" || gp["employees"] != nil {
		t.Errorf("patch = %d %v, want Plating and employees cleared", code, gp)
	}
	if code, _, _ := f.call(t, bob, "PATCH", path, map[string]any{"properties": map[string]any{"nope": "x"}}); code != http.StatusBadRequest {
		t.Errorf("a field that is not there = %d, want 400", code)
	}
	if code, _, _ := f.call(t, aliceRead, "PATCH", path, map[string]any{"properties": map[string]any{"industry": "Plating"}}); code != http.StatusForbidden {
		t.Errorf("a read token setting a value = %d, want 403", code)
	}
	if _, g, _ := f.call(t, bob, "GET", path, nil); g["properties"].(map[string]any)["industry"] != "Plating" {
		t.Errorf("read back = %v", g["properties"])
	}

	// Change a field, then delete it with its values; the delete is for an admin.
	if code, ch, _ := f.call(t, adm, "PATCH", "/v1/crm/properties/"+def["id"].(string), map[string]any{"type": "company", "label": "Sector", "options": []string{"Plating", "Rewinds", "Foundry"}}); code != http.StatusOK || ch["label"] != "Sector" || ch["key"] != "industry" {
		t.Errorf("change = %d %v", code, ch)
	}
	if code, _, _ := f.call(t, alice, "DELETE", "/v1/crm/properties/"+def["id"].(string)+"?type=company", nil); code != http.StatusForbidden {
		t.Errorf("a salesperson deleting a field = %d, want 403", code)
	}
	if code, _, _ := f.call(t, adm, "DELETE", "/v1/crm/properties/"+def["id"].(string)+"?type=company", nil); code != http.StatusOK {
		t.Errorf("delete = %d, want 200", code)
	}
	if _, g, _ := f.call(t, bob, "GET", path, nil); g["properties"].(map[string]any)["industry"] != nil {
		if _, has := g["properties"].(map[string]any)["industry"]; has {
			t.Errorf("a deleted field still shows: %v", g["properties"])
		}
	}
	// Deals and contacts carry them too.
	f.call(t, adm, "POST", "/v1/crm/properties", map[string]any{"type": "deal", "label": "Contract end", "data_type": "date"})
	_, dl, _ := f.call(t, alice, "POST", "/v1/deals", map[string]any{"name": "Props Deal", "properties": map[string]any{"contract_end": "2027-03-01"}})
	if dl["properties"].(map[string]any)["contract_end"] != "2027-03-01" {
		t.Errorf("a deal's custom field = %v", dl["properties"])
	}
	if code, _, _ := f.call(t, alice, "PATCH", "/v1/deals/"+dl["id"].(string), map[string]any{"properties": map[string]any{"contract_end": "March"}}); code != http.StatusBadRequest {
		t.Errorf("a bad date on a deal = %d, want 400", code)
	}
}
