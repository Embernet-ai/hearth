package api_test

// The sales desk over the API, against a real Postgres: follow-ups, forecast, report, bulk change, and the refusals
// for a token whose person does not work sales and for a token that cannot write.

import (
	"context"
	"net/http"
	"testing"
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
