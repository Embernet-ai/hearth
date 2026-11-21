package api_test

// The CRM over the API, against a real Postgres.
//
// Three claims. The API refuses what the pages refuse for the same caller. A
// create sent twice, the way a retrying webhook sends it, makes one record. And
// the find-then-create an automation does works end to end: look a person up by
// email, get told who already has the address, carry on with that id.

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/crm"
)

type crmFx struct {
	*fx
	carol string
	team  string
}

func crmSetup(t *testing.T) *crmFx {
	t.Helper()
	f := setup(t)
	ctx := context.Background()
	c := &crmFx{fx: f, carol: uuid.NewString(), team: uuid.NewString()}
	crmSvc := crm.New(f.pools.Web, f.az)
	f.svc.WithCRM(crmSvc, crmSvc)

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Carol')`, c.carol, f.org)
	// Alice and Bob work sales. Carol does not.
	exec(`INSERT INTO teams (id, org_id, name) VALUES ($1,$2,'Sales')`, c.team, f.org)
	exec(`INSERT INTO team_members (team_id, principal_id) VALUES ($1,$2),($1,$3)`, c.team, f.alice, f.bob)
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM crm_activities WHERE org_id=$1`,
			`DELETE FROM deals WHERE org_id=$1`,
			`DELETE FROM contacts WHERE org_id=$1`,
			`DELETE FROM companies WHERE org_id=$1`,
			`DELETE FROM teams WHERE org_id=$1`,
		} {
			_, _ = f.pools.Web.Exec(ctx, q, f.org)
		}
	})
	return c
}

func TestTheCRMAPIRefusesWhatThePagesRefuse(t *testing.T) {
	f := crmSetup(t)
	alice, carol := f.token(t, f.alice, "write"), f.token(t, f.carol, "write")

	code, co, _ := f.call(t, alice, "POST", "/v1/companies", map[string]any{
		"name": "Acme", "domain": "acme.example", "kind": "prospect", "website": "https://acme.example",
		"address": map[string]any{"city": "Dayton", "state": "OH"}, "annual_revenue_cents": 250000000,
	})
	if code != http.StatusCreated || co["name"] != "Acme" || co["website"] != "https://acme.example" {
		t.Fatalf("create company = %d %v", code, co)
	}
	id := co["id"].(string)
	if a := co["address"].(map[string]any); a["city"] != "Dayton" {
		t.Errorf("address = %v", a)
	}

	// Alice is on the Sales team, so Bob sees it too.
	if code, _, _ := f.call(t, f.token(t, f.bob), "GET", "/v1/companies/"+id, nil); code != http.StatusOK {
		t.Errorf("a sales team member could not read a sales company: %d", code)
	}
	// Carol is not, and is told the same thing the page tells her: nothing.
	if code, _, _ := f.call(t, carol, "GET", "/v1/companies/"+id, nil); code != http.StatusNotFound {
		t.Errorf("somebody outside the sales team read a company: %d", code)
	}
	_, list, _ := f.call(t, carol, "GET", "/v1/companies", nil)
	if list["total"].(float64) != 0 {
		t.Errorf("carol's company list: %v", list)
	}
	if code, _, _ := f.call(t, carol, "PATCH", "/v1/companies/"+id, map[string]any{"name": "Mine now"}); code != http.StatusNotFound {
		t.Errorf("carol changed a company: %d", code)
	}
	// A read token cannot write, whoever it belongs to.
	if code, _, _ := f.call(t, f.token(t, f.alice, "read"), "POST", "/v1/companies", map[string]any{"name": "X"}); code != http.StatusForbidden {
		t.Errorf("a read token created a company: %d", code)
	}
	// An unknown field is refused, as on the documents endpoints.
	if code, _, _ := f.call(t, alice, "POST", "/v1/companies", map[string]any{"nmae": "typo"}); code != http.StatusBadRequest {
		t.Errorf("a misspelled field: %d", code)
	}

	code, patched, _ := f.call(t, alice, "PATCH", "/v1/companies/"+id, map[string]any{
		"name": "Acme Rewinds", "address": map[string]any{"city": "Columbus"}, "owner_id": f.bob,
	})
	if code != http.StatusOK || patched["name"] != "Acme Rewinds" || patched["owner_id"] != f.bob ||
		patched["address"].(map[string]any)["city"] != "Columbus" ||
		patched["address"].(map[string]any)["state"] != "OH" {
		t.Errorf("patch kept or lost the wrong fields: %d %v", code, patched)
	}
	// Owner never gates: Alice, who is not the owner, still reads it.
	if code, _, _ := f.call(t, alice, "GET", "/v1/companies/"+id, nil); code != http.StatusOK {
		t.Errorf("a non-owner on the sales team lost sight of a record: %d", code)
	}
	// Filter by owner and by nobody.
	_, mine, _ := f.call(t, alice, "GET", "/v1/companies?owner="+f.bob, nil)
	_, none, _ := f.call(t, alice, "GET", "/v1/companies?owner=none", nil)
	if mine["total"].(float64) != 1 || none["total"].(float64) != 0 {
		t.Errorf("owner filters: %v %v", mine["total"], none["total"])
	}
}

func TestACreateSentTwiceMakesOneRecord(t *testing.T) {
	f := crmSetup(t)
	alice := f.token(t, f.alice, "write")

	body := map[string]any{"name": "Dana Whitfield", "email": "Dana@Acme.example", "external_id": "chatwoot:conv-9",
		"source": "n8n-chatwoot"}
	code, first, _ := f.call(t, alice, "POST", "/v1/contacts", body)
	if code != http.StatusCreated || first["email"] != "dana@acme.example" {
		t.Fatalf("first create = %d %v", code, first)
	}
	// The retry: same pair, even with a different payload, answers with the
	// original and changes nothing.
	body["name"] = "Somebody Else"
	code, again, _ := f.call(t, alice, "POST", "/v1/contacts", body)
	if code != http.StatusOK || again["id"] != first["id"] || again["name"] != "Dana Whitfield" {
		t.Errorf("replay = %d %v, want 200 and the original", code, again)
	}
	_, all, _ := f.call(t, alice, "GET", "/v1/contacts", nil)
	if all["total"].(float64) != 1 {
		t.Errorf("%v contacts after a retry, want 1", all["total"])
	}
	// The same external id under another source is a different thing.
	body["source"] = "n8n-formbricks"
	body["email"] = "other@acme.example"
	if code, _, _ := f.call(t, alice, "POST", "/v1/contacts", body); code != http.StatusCreated {
		t.Errorf("another source with the same external id: %d", code)
	}

	// Two at once: still one.
	done := make(chan int, 2)
	race := map[string]any{"name": "Race", "email": "race@acme.example", "external_id": "m-1", "source": "mautic"}
	for i := 0; i < 2; i++ {
		go func() {
			code, _, _ := f.call(t, alice, "POST", "/v1/contacts", race)
			done <- code
		}()
	}
	a, b := <-done, <-done
	if !(a == http.StatusCreated && b == http.StatusOK) && !(a == http.StatusOK && b == http.StatusCreated) {
		t.Errorf("two simultaneous creates answered %d and %d, want one 201 and one 200", a, b)
	}
	_, found, _ := f.call(t, alice, "GET", "/v1/contacts?email=race@acme.example", nil)
	if found["total"].(float64) != 1 {
		t.Errorf("the race left %v contacts with that address", found["total"])
	}
}

func TestFindThenCreateByEmail(t *testing.T) {
	f := crmSetup(t)
	alice := f.token(t, f.alice, "write")

	if _, found, _ := f.call(t, alice, "GET", "/v1/contacts?email=who@acme.example", nil); found["total"].(float64) != 0 {
		t.Fatalf("found somebody who is not there: %v", found)
	}
	_, made, _ := f.call(t, alice, "POST", "/v1/contacts", map[string]any{"name": "Who", "email": "who@acme.example"})
	// Exact and case-insensitive.
	_, found, _ := f.call(t, alice, "GET", "/v1/contacts?email=WHO@acme.example", nil)
	if found["total"].(float64) != 1 {
		t.Fatalf("lookup by email: %v", found)
	}
	// A second create with the address says who has it.
	code, dup, _ := f.call(t, alice, "POST", "/v1/contacts", map[string]any{"name": "Who Again", "email": "who@acme.example"})
	if code != http.StatusConflict || dup["existing_id"] != made["id"] {
		t.Errorf("duplicate = %d %v, want 409 naming %v", code, dup, made["id"])
	}
	// Somebody who cannot see the person is not told the id.
	code, dup, _ = f.call(t, f.token(t, f.carol, "write"), "POST", "/v1/contacts", map[string]any{"name": "X", "email": "who@acme.example"})
	if _, leaked := dup["existing_id"]; leaked {
		t.Errorf("a caller who cannot see a contact was handed their id (%d)", code)
	}
}

func TestDealsMoveThroughStagesAndTheTimelineCollects(t *testing.T) {
	f := crmSetup(t)
	alice := f.token(t, f.alice, "write")

	_, co, _ := f.call(t, alice, "POST", "/v1/companies", map[string]any{"name": "Acme", "domain": "acme.example"})
	_, ct, _ := f.call(t, alice, "POST", "/v1/contacts", map[string]any{"name": "Dana", "email": "d@acme.example", "company_id": co["id"]})
	code, deal, _ := f.call(t, alice, "POST", "/v1/deals", map[string]any{
		"name": "Rewind line", "amount_cents": 4800000, "close_date": "2026-12-01",
		"company_id": co["id"], "contact_id": ct["id"], "external_id": "mautic:camp-4", "source": "n8n-mautic",
	})
	if code != http.StatusCreated || deal["stage"] != "new" || deal["close_date"] != "2026-12-01" || deal["company_name"] != "Acme" {
		t.Fatalf("create deal = %d %v", code, deal)
	}
	id := deal["id"].(string)

	code, moved, _ := f.call(t, alice, "PATCH", "/v1/deals/"+id, map[string]any{"stage": "meeting"})
	if code != http.StatusOK || moved["stage"] != "meeting" || moved["amount_cents"].(float64) != 4800000 {
		t.Errorf("advance = %d %v", code, moved)
	}
	if code, _, _ := f.call(t, alice, "PATCH", "/v1/deals/"+id, map[string]any{"stage": "nonsense"}); code != http.StatusBadRequest {
		t.Errorf("a stage that is not one: %d", code)
	}
	if code, _, _ := f.call(t, alice, "PATCH", "/v1/deals/"+id, map[string]any{"close_date": "soon"}); code != http.StatusBadRequest {
		t.Errorf("a bad date: %d", code)
	}
	// The deals a person is the point of contact on: how a flow finds the deal to advance.
	_, byContact, _ := f.call(t, alice, "GET", "/v1/deals?contact_id="+ct["id"].(string), nil)
	_, byOther, _ := f.call(t, alice, "GET", "/v1/deals?contact_id="+uuid.NewString(), nil)
	if byContact["total"].(float64) != 1 || byOther["total"].(float64) != 0 {
		t.Errorf("deals by contact: %v and %v, want 1 and 0", byContact["total"], byOther["total"])
	}
	_, byStage, _ := f.call(t, alice, "GET", "/v1/deals?stage=meeting", nil)
	if byStage["total"].(float64) != 1 {
		t.Errorf("deals in meeting: %v", byStage["total"])
	}

	// A note and a call about the deal and the person, sent twice.
	note := map[string]any{"kind": "note", "title": "NPS 9", "body": "loved it", "external_id": "fb-77", "source": "n8n-formbricks",
		"targets": []map[string]any{{"type": "deal", "id": id}, {"type": "contact", "id": ct["id"]}}}
	code, _, _ = f.call(t, alice, "POST", "/v1/activities", note)
	if code != http.StatusCreated {
		t.Fatalf("add note = %d", code)
	}
	if code, _, _ = f.call(t, alice, "POST", "/v1/activities", note); code != http.StatusOK {
		t.Errorf("a repeated note answered %d, want 200", code)
	}
	f.call(t, alice, "POST", "/v1/activities", map[string]any{"kind": "call", "title": "Call", "direction": "outbound",
		"disposition": "Left voicemail", "duration_ms": 90000, "occurred_at": "2026-10-01T15:00:00Z",
		"targets": []map[string]any{{"type": "contact", "id": ct["id"]}}})
	_, tl, _ := f.call(t, alice, "GET", "/v1/contacts/"+ct["id"].(string)+"/activities", nil)
	if n := len(tl["activities"].([]any)); n != 2 {
		t.Errorf("the contact's timeline has %d entries, want the note and the call once each", n)
	}
	_, dtl, _ := f.call(t, alice, "GET", "/v1/deals/"+id+"/activities", nil)
	if n := len(dtl["activities"].([]any)); n != 1 {
		t.Errorf("the deal's timeline has %d entries, want 1", n)
	}
	if code, _, _ := f.call(t, alice, "POST", "/v1/activities", map[string]any{"kind": "note", "body": "x"}); code != http.StatusBadRequest {
		t.Errorf("an activity about nothing: %d", code)
	}
	// Carol cannot write on it or read it.
	if code, _, _ := f.call(t, f.token(t, f.carol, "write"), "POST", "/v1/activities", map[string]any{"kind": "note", "body": "x",
		"targets": []map[string]any{{"type": "deal", "id": id}}}); code == http.StatusCreated {
		t.Errorf("carol wrote on a deal she cannot see")
	}
	if code, _, _ := f.call(t, f.token(t, f.carol, "read"), "GET", "/v1/deals/"+id+"/activities", nil); code != http.StatusNotFound {
		t.Errorf("carol read a timeline: %d", code)
	}

	// Stats over what the caller can see.
	f.call(t, alice, "POST", "/v1/deals", map[string]any{"name": "Lost one", "stage": "lost", "amount_cents": 100})
	_, st, _ := f.call(t, alice, "GET", "/v1/crm/stats", nil)
	if st["companies"].(float64) != 1 || st["contacts"].(float64) != 1 || st["deals"].(float64) != 2 ||
		st["open_deals"].(float64) != 1 || st["open_value_cents"].(float64) != 4800000 {
		t.Errorf("stats = %v", st)
	}
	_, cst, _ := f.call(t, f.token(t, f.carol, "read"), "GET", "/v1/crm/stats", nil)
	if cst["deals"].(float64) != 0 || cst["companies"].(float64) != 0 {
		t.Errorf("carol's stats count what she cannot see: %v", cst)
	}
}

func TestTheCRMRoutesAre404WhenTheCRMIsOff(t *testing.T) {
	f := setup(t) // no WithCRM
	if code, _, _ := f.call(t, f.token(t, f.alice, "write"), "GET", "/v1/companies", nil); code != http.StatusNotFound {
		t.Errorf("a deployment with no CRM answered %d", code)
	}
}

func TestCompanyTypesAndTagsOverTheAPI(t *testing.T) {
	f := crmSetup(t)
	alice := f.token(t, f.alice, "write")

	_, types, _ := f.call(t, alice, "GET", "/v1/company-types", nil)
	if n := len(types["types"].([]any)); n != 6 {
		t.Fatalf("%d types, want the starting six", n)
	}
	// A bad key is refused before anything is made.
	if code, _, _ := f.call(t, alice, "POST", "/v1/companies", map[string]any{"name": "Ghost", "domain": "ghost.example",
		"types": []string{"wizard"}}); code != http.StatusBadRequest {
		t.Errorf("a type that is not on the list: %d", code)
	}
	_, none, _ := f.call(t, alice, "GET", "/v1/companies?q=Ghost", nil)
	if none["total"].(float64) != 0 {
		t.Errorf("a refused create left a company behind")
	}
	code, co, _ := f.call(t, alice, "POST", "/v1/companies", map[string]any{"name": "Both Ways", "domain": "both.example",
		"types": []string{"client", "vendor"}, "tags": []string{"Dayton"}, "external_id": "x-1"})
	if code != http.StatusCreated || len(co["types"].([]any)) != 2 || co["tags"].([]any)[0] != "Dayton" {
		t.Fatalf("create = %d %v", code, co)
	}
	f.call(t, alice, "POST", "/v1/companies", map[string]any{"name": "Plain", "domain": "plain.example"})
	_, vendors, _ := f.call(t, alice, "GET", "/v1/companies?type=vendor", nil)
	_, untyped, _ := f.call(t, alice, "GET", "/v1/companies?type=none", nil)
	_, tagged, _ := f.call(t, alice, "GET", "/v1/companies?tag=dayton", nil)
	if vendors["total"].(float64) != 1 || untyped["total"].(float64) != 1 || tagged["total"].(float64) != 1 {
		t.Errorf("filters: vendor %v, untyped %v, tagged %v", vendors["total"], untyped["total"], tagged["total"])
	}
	// PATCH replaces what it names and leaves the rest.
	id := co["id"].(string)
	code, p, _ := f.call(t, alice, "PATCH", "/v1/companies/"+id, map[string]any{"types": []string{"partner"}})
	if code != http.StatusOK || len(p["types"].([]any)) != 1 || p["types"].([]any)[0] != "partner" || len(p["tags"].([]any)) != 1 {
		t.Errorf("patch types = %d %v", code, p)
	}
}

func TestWhoIsCallingFindsTheContactByAnySpellingOfTheNumber(t *testing.T) {
	f := crmSetup(t)
	alice, carol := f.token(t, f.alice, "read"), f.token(t, f.carol, "read")
	w := f.token(t, f.alice, "write")

	_, co, _ := f.call(t, w, "POST", "/v1/companies", map[string]any{"name": "Acme Rewinds"})
	_, ct, _ := f.call(t, w, "POST", "/v1/contacts", map[string]any{
		"name": "Ada Lovelace", "phone": "(216) 555-0142", "company_id": co["id"], "extra_phones": []string{"+52 492 555 0100"},
	})
	_, _, _ = f.call(t, w, "POST", "/v1/contacts", map[string]any{"name": "No Number", "phone": "555-0142"})

	for _, n := range []string{"+12165550142", "216-555-0142", "1 216 555 0142", "+52 492 555 0100"} {
		code, got, _ := f.call(t, alice, "GET", "/v1/crm/lookup?phone="+url.QueryEscape(n), nil)
		c, _ := got["contact"].(map[string]any)
		if code != http.StatusOK || c["id"] != ct["id"] || c["name"] != "Ada Lovelace" {
			t.Errorf("lookup %q = %d %v", n, code, got)
		}
		if comp, _ := got["company"].(map[string]any); comp["name"] != "Acme Rewinds" {
			t.Errorf("lookup %q company = %v", n, got["company"])
		}
	}
	if code, _, _ := f.call(t, alice, "GET", "/v1/crm/lookup?phone=%2B12165550199", nil); code != http.StatusNotFound {
		t.Errorf("an unknown number: %d", code)
	}
	if code, _, _ := f.call(t, alice, "GET", "/v1/crm/lookup?phone=555-0142", nil); code != http.StatusBadRequest {
		t.Errorf("a number with no country and too few digits: %d", code)
	}
	// Somebody outside the sales team sees nothing, the same as every list.
	if code, _, _ := f.call(t, carol, "GET", "/v1/crm/lookup?phone=%2B12165550142", nil); code != http.StatusNotFound {
		t.Errorf("a person outside sales looked up a sales contact: %d", code)
	}
	// The Go normalizer and the SQL function agree, so the index finds what the API asks for.
	for _, n := range []string{"(216) 555-0142", "+1 216 555 0142", "0052 492 555 0100", "555-0142", "ext 5"} {
		var sqlv string
		if err := f.pools.Web.QueryRow(context.Background(), `SELECT phone_e164($1)`, n).Scan(&sqlv); err != nil {
			t.Fatal(err)
		}
		if sqlv != crm.PhoneE164(n) {
			t.Errorf("phone_e164(%q) = %q in SQL, %q in Go", n, sqlv, crm.PhoneE164(n))
		}
	}
}

func TestThePhoneLogsACallOnceAndKeepsAndRemovesAContact(t *testing.T) {
	f := crmSetup(t)
	w, r := f.token(t, f.alice, "write"), f.token(t, f.alice, "read")
	_, co, _ := f.call(t, w, "POST", "/v1/companies", map[string]any{"name": "Rang Co"})
	_, ct, _ := f.call(t, w, "POST", "/v1/contacts", map[string]any{"name": "Rae Caller", "phone": "216 555 0142", "company_id": co["id"], "source": "phone", "external_id": "caller:+12165550142"})
	_, again, _ := f.call(t, w, "POST", "/v1/contacts", map[string]any{"name": "Rae Caller", "phone": "216 555 0142", "source": "phone", "external_id": "caller:+12165550142"})
	if again["id"] != ct["id"] {
		t.Errorf("the same caller made a second contact: %v vs %v", again["id"], ct["id"])
	}
	body := map[string]any{"kind": "call", "title": "Inbound call from Rae Caller", "body": "Number: +12165550142\nAnswered by Patrick", "direction": "inbound",
		"disposition": "answered", "duration_ms": 94000, "source": "phone", "external_id": "call-1",
		"targets": []map[string]any{{"type": "contact", "id": ct["id"]}, {"type": "company", "id": co["id"]}}}
	code, a1, _ := f.call(t, w, "POST", "/v1/activities", body)
	code2, a2, _ := f.call(t, w, "POST", "/v1/activities", body)
	if code != http.StatusCreated || code2 != http.StatusOK || a1["id"] != a2["id"] {
		t.Errorf("logging a call twice: %d then %d %v %v", code, code2, a1["id"], a2["id"])
	}
	_, tl, _ := f.call(t, r, "GET", "/v1/contacts/"+ct["id"].(string)+"/activities", nil)
	if n := len(tl["activities"].([]any)); n != 1 {
		t.Errorf("the timeline has %d calls, want 1", n)
	}
	// the phone removes a person it made
	if code, _, _ := f.call(t, r, "DELETE", "/v1/contacts/"+ct["id"].(string), nil); code != http.StatusForbidden {
		t.Errorf("a read token deleted a contact: %d", code)
	}
	if code, _, _ := f.call(t, w, "DELETE", "/v1/contacts/"+ct["id"].(string), nil); code != http.StatusNoContent {
		t.Errorf("delete = %d", code)
	}
	if code, _, _ := f.call(t, r, "GET", "/v1/contacts/"+ct["id"].(string), nil); code != http.StatusNotFound {
		t.Errorf("a deleted contact still answers: %d", code)
	}
}
