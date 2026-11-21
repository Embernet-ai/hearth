package crmsync_test

// What makes the two sides one place for the people who work in them: a link on every record, the people of a
// prospect, a record that has not changed left alone, and a customer or supplier made in EmberOps reaching Hearth.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/crmsync"
)

const upsertCalls = "emberops_hearth.api.upsert"

type testMaker struct{ x *syncFixture }

func (m testMaker) MakeCompany(ctx context.Context, name, kind string) (string, error) {
	return m.x.mkCo(name, ""), nil
}

func (x *syncFixture) person(company, name, email, phone string) string {
	id := uuid.NewString()
	x.exec(`INSERT INTO contacts (id, org_id, company_id, name, email, phone, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		id, x.org, company, name, email, phone, x.who)
	return id
}

func opts() crmsync.Options {
	return crmsync.Options{Write: true, Deals: true, HearthURL: "https://hearth.example/", OpsURL: "https://ops.example/"}
}

func TestEveryRecordCarriesItsHearthLinkAndOwner(t *testing.T) {
	x := newSyncFixture(t)
	owner := uuid.NewString()
	x.exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Pat Seller')`, owner, x.org)
	co := x.mkCo("Linked Client", "lc.example", "client")
	x.exec(`UPDATE companies SET owner_id=$1, website='https://lc.example' WHERE id=$2`, owner, co)
	ct := x.person(co, "Lee Linked", "lee@lc.example", "555 0101")
	x.exec(`UPDATE contacts SET owner_id=$1 WHERE id=$2`, owner, ct)
	pr := x.mkCo("Hopeful Co", "hc.example", "prospect")
	dl := x.mkDl("Pilot", "meeting", pr, 1000)
	x.exec(`UPDATE deals SET owner_id=$1 WHERE id=$2`, owner, dl)

	rep, err := x.sync.Run(x.ctx, opts())
	if err != nil || rep.Failed() {
		t.Fatalf("run: %v %+v", err, rep.Actions)
	}
	c := byHearth(t, x, "Customer", co)
	if c["hearth_url"] != "https://hearth.example/companies/"+co || c["hearth_owner"] != "Pat Seller" || c["website"] != "https://lc.example" {
		t.Errorf("Customer: url %v owner %v website %v", c["hearth_url"], c["hearth_owner"], c["website"])
	}
	if p := byHearth(t, x, "Contact", ct); p["hearth_url"] != "https://hearth.example/contacts/"+ct || p["hearth_owner"] != "Pat Seller" {
		t.Errorf("Contact: url %v owner %v", p["hearth_url"], p["hearth_owner"])
	}
	if l := byHearth(t, x, "Lead", pr); l["hearth_url"] != "https://hearth.example/companies/"+pr {
		t.Errorf("Lead: url %v", l["hearth_url"])
	}
	if o := byHearth(t, x, "Opportunity", dl); o["hearth_url"] != "https://hearth.example/deals/"+dl || o["hearth_owner"] != "Pat Seller" {
		t.Errorf("Opportunity: url %v owner %v", o["hearth_url"], o["hearth_owner"])
	}
}

func byHearth(t *testing.T, x *syncFixture, dt, id string) map[string]any {
	t.Helper()
	for _, d := range x.f.All(dt) {
		if d["hearth_id"] == id {
			return d
		}
	}
	t.Fatalf("EmberOps has no %s for %s", dt, id)
	return nil
}

func TestHearthKeepsTheEmberOpsPageOfEveryRecord(t *testing.T) {
	x := newSyncFixture(t)
	co := x.mkCo("Paged Client", "pc.example", "client")
	ct := x.person(co, "Pat Paged", "pat@pc.example", "")
	if rep, err := x.sync.Run(x.ctx, opts()); err != nil || rep.Failed() {
		t.Fatalf("run: %v %+v", err, rep.Actions)
	}
	cust := byHearth(t, x, "Customer", co)["name"].(string)
	var u, et string
	if err := x.pools.Web.QueryRow(x.ctx, `SELECT external_url, entity_type FROM external_refs WHERE org_id=$1 AND source='emberops-record' AND external_id=$2`,
		x.org, "Customer/"+co).Scan(&u, &et); err != nil || u != "https://ops.example/app/customer/"+cust || et != "company" {
		t.Errorf("the company's EmberOps page = %q (%s), %v", u, et, err)
	}
	if err := x.pools.Web.QueryRow(x.ctx, `SELECT external_url, entity_type FROM external_refs WHERE org_id=$1 AND source='emberops-record' AND external_id=$2`,
		x.org, "Contact/"+ct).Scan(&u, &et); err != nil || !strings.HasPrefix(u, "https://ops.example/app/contact/") || et != "contact" {
		t.Errorf("the contact's EmberOps page = %q (%s), %v", u, et, err)
	}
	// Hearth's own pages read them back.
	links, err := crm.New(x.pools.Web, authz.New(x.pools.Web)).EmberOpsLinks(x.ctx, x.org, core.Ref{Type: core.EntityCompany, ID: co})
	if err != nil || len(links) != 1 || links[0].Label != "Customer" || links[0].URL != "https://ops.example/app/customer/"+cust {
		t.Errorf("the company page's links = %+v, %v", links, err)
	}
	// A dry run records nothing, and a second run changes nothing.
	var before, after int
	_ = x.pools.Web.QueryRow(x.ctx, `SELECT count(*) FROM external_refs WHERE org_id=$1 AND source='emberops-record'`, x.org).Scan(&before)
	o := opts()
	if _, err := x.sync.Run(x.ctx, o); err != nil {
		t.Fatal(err)
	}
	_ = x.pools.Web.QueryRow(x.ctx, `SELECT count(*) FROM external_refs WHERE org_id=$1 AND source='emberops-record'`, x.org).Scan(&after)
	if before != 2 || after != before {
		t.Errorf("links before %d, after a second run %d", before, after)
	}
}

func TestARecordThatHasNotChangedIsNotSentAgain(t *testing.T) {
	x := newSyncFixture(t)
	co := x.mkCo("Quiet Client", "qc.example", "client")
	ct := x.person(co, "Quinn Quiet", "quinn@qc.example", "555 0100")
	pr := x.mkCo("Quiet Prospect", "qp.example", "prospect")
	x.mkDl("Quiet deal", "proposal", pr, 5000)
	if rep, err := x.sync.Run(x.ctx, opts()); err != nil || rep.Failed() {
		t.Fatalf("first run: %v %+v", err, rep.Actions)
	}
	first := x.f.CallCount(upsertCalls)
	rep, err := x.sync.Run(x.ctx, opts())
	if err != nil || rep.Failed() {
		t.Fatalf("second run: %v %+v", err, rep.Actions)
	}
	if n := x.f.CallCount(upsertCalls) - first; n != 0 {
		t.Errorf("an unchanged CRM was sent again: %d upserts, %+v", n, rep.Actions)
	}
	for _, what := range []string{"customer", "contact", "lead", "opportunity"} {
		if rep.Count(what, "same") != 1 {
			t.Errorf("%s was not reported as the same: %+v", what, rep.Actions)
		}
	}
	// One change in Hearth is exactly one record sent.
	x.exec(`UPDATE contacts SET phone='555 0199' WHERE id=$1`, ct)
	before := x.f.CallCount(upsertCalls)
	if rep, err = x.sync.Run(x.ctx, opts()); err != nil || rep.Failed() {
		t.Fatalf("third run: %v %+v", err, rep.Actions)
	}
	if n := x.f.CallCount(upsertCalls) - before; n != 1 || rep.Count("contact", "update") != 1 {
		t.Errorf("one edited phone sent %d upserts: %+v", n, rep.Actions)
	}
	phones, _ := byHearth(t, x, "Contact", ct)["phone_nos"].([]any)
	if len(phones) != 1 || phones[0].(map[string]any)["phone"] != "555 0199" {
		t.Errorf("the edited phone did not arrive: %v", phones)
	}
	// An edit made in EmberOps to a field Hearth owns is put back, and the report says so.
	cust := byHearth(t, x, "Customer", co)
	cust["hearth_notes"] = "typed in EmberOps"
	x.exec(`UPDATE companies SET notes='the real note' WHERE id=$1`, co)
	if rep, err = x.sync.Run(x.ctx, opts()); err != nil || rep.Failed() {
		t.Fatalf("fourth run: %v %+v", err, rep.Actions)
	}
	if byHearth(t, x, "Customer", co)["hearth_notes"] != "the real note" || rep.Count("customer", "overwrite") != 1 {
		t.Errorf("Hearth's note did not win: %+v", rep.Actions)
	}
}

func TestProspectsAndTheirPeopleReachEmberOpsAsLeadsAndContacts(t *testing.T) {
	x := newSyncFixture(t)
	pr := x.mkCo("Plain Prospect", "pp.example")
	x.person(pr, "Pia Prospect", "pia@pp.example", "")
	x.person(pr, "No Email", "", "555 0188")
	deal := x.mkCo("Deal Prospect", "dp.example", "prospect")
	x.mkDl("A deal", "new", deal, 1)
	x.person(deal, "Dee Dealer", "dee@dp.example", "")
	// EmberOps made a Contact of its own when the Lead was saved: same person, made by the sync's user.
	x.f.Put("Contact", map[string]any{"name": "Dee Dealer", "email_id": "dee@dp.example", "owner": "svc@example.com", "first_name": "Dee"})

	// Off: only the prospect with a deal is a Lead, and its person is a Contact linked to the Lead.
	if rep, err := x.sync.Run(x.ctx, opts()); err != nil || rep.Failed() {
		t.Fatalf("run: %v %+v", err, rep.Actions)
	}
	if n := len(x.f.All("Lead")); n != 1 {
		t.Fatalf("%d Leads with prospects off, want 1", n)
	}
	if n := len(x.f.All("Contact")); n != 1 {
		t.Fatalf("%d Contacts: the Lead's own Contact was not taken over, or one was made twice", n)
	}
	dee := x.f.All("Contact")[0]
	lead := byHearth(t, x, "Lead", deal)
	if dee["hearth_id"] == nil || dee["name"] != "Dee Dealer" {
		t.Errorf("the Lead's Contact was not taken over: %v", dee)
	}
	// Second run: now it follows Hearth like any record the sync made, and nothing is duplicated.
	x.exec(`UPDATE contacts SET title='Head Buyer' WHERE id=$1`, strings.ToLower(dee["hearth_id"].(string)))
	if rep, err := x.sync.Run(x.ctx, opts()); err != nil || rep.Failed() {
		t.Fatalf("second run: %v %+v", err, rep.Actions)
	}
	var adopted int
	_ = x.pools.Web.QueryRow(x.ctx, `SELECT count(*) FROM external_refs WHERE org_id=$1 AND source='emberops-linked'`, x.org).Scan(&adopted)
	if adopted != 0 {
		t.Errorf("the Lead's own Contact was recorded as EmberOps' own, so Hearth would never update it")
	}
	if got := x.f.All("Contact")[0]["designation"]; got != "Head Buyer" {
		t.Errorf("a Hearth edit did not reach the taken-over Contact: designation %v", got)
	}
	if len(x.f.All("Contact")) != 1 || len(x.f.All("Lead")) != 1 {
		t.Errorf("a second run duplicated something: %d contacts, %d leads", len(x.f.All("Contact")), len(x.f.All("Lead")))
	}
	if links, _ := x.f.All("Contact")[0]["links"].([]any); len(links) != 1 || links[0].(map[string]any)["link_name"] != lead["name"] {
		t.Errorf("the contact is not linked to its Lead: %v", x.f.All("Contact")[0]["links"])
	}

	// On: the plain prospect is a Lead too, and both its people are Contacts under it.
	o := opts()
	o.Prospects = true
	rep, err := x.sync.Run(x.ctx, o)
	if err != nil || rep.Failed() {
		t.Fatalf("prospects run: %v %+v", err, rep.Actions)
	}
	if len(x.f.All("Lead")) != 2 || len(x.f.All("Contact")) != 3 {
		t.Fatalf("with prospects on: %d Leads, %d Contacts, want 2 and 3: %+v", len(x.f.All("Lead")), len(x.f.All("Contact")), rep.Actions)
	}
	l := byHearth(t, x, "Lead", pr)
	if l["company_name"] != "Plain Prospect" {
		t.Errorf("the prospect's Lead: %v", l)
	}
	// And a third run changes nothing.
	before := x.f.CallCount(upsertCalls)
	mark := len(x.f.Calls)
	if rep, err = x.sync.Run(x.ctx, o); err != nil || rep.Failed() {
		t.Fatalf("third run: %v %+v", err, rep.Actions)
	}
	// The Leads are read in one list, never one call each.
	for _, c := range x.f.Calls[mark:] {
		if strings.Contains(c, "get_by_hearth_id") && strings.Contains(c, "Lead") {
			t.Errorf("a Lead was read on its own: %s", c)
		}
	}
	if n := x.f.CallCount(upsertCalls) - before; n != 0 {
		t.Errorf("a settled CRM was sent again: %d upserts %+v", n, rep.Actions)
	}
	// A prospect nobody has named stays out.
	x.mkCo("unnamed 41643296275", "")
	if rep, _ = x.sync.Run(x.ctx, o); rep.Count("lead", "skipped") != 1 {
		t.Errorf("an unnamed prospect was not held back: %+v", rep.Actions)
	}
}

func TestACustomerOrSupplierMadeInEmberOpsReachesHearth(t *testing.T) {
	x := newSyncFixture(t)
	existing := x.mkCo("Quiet Vendor Inc", "qv.example") // in Hearth already, no type
	x.f.Put("Supplier", map[string]any{"name": "Quiet Vendor Inc", "supplier_name": "Quiet Vendor Inc", "disabled": 0})
	x.f.Put("Customer", map[string]any{"name": "Walk In Co", "customer_name": "Walk In Co", "disabled": 0})
	x.f.Put("Customer", map[string]any{"name": "Off Co", "customer_name": "Off Co", "disabled": 1})
	o := opts()
	o.Companies = testMaker{x}

	rep, err := x.sync.Run(x.ctx, o)
	if err != nil || rep.Failed() {
		t.Fatalf("run: %v %+v", err, rep.Actions)
	}
	var n int
	_ = x.pools.Web.QueryRow(x.ctx, `SELECT count(*) FROM companies WHERE org_id=$1 AND lower(name)='quiet vendor inc'`, x.org).Scan(&n)
	if n != 1 {
		t.Errorf("a Supplier whose name Hearth already has made %d companies", n)
	}
	var vendor bool
	_ = x.pools.Web.QueryRow(x.ctx, `SELECT EXISTS (SELECT 1 FROM company_type_links l JOIN company_types t ON t.id=l.type_id
		WHERE l.company_id=$1 AND t.key='vendor')`, existing).Scan(&vendor)
	if !vendor {
		t.Errorf("the existing company did not get the vendor type")
	}
	_ = x.pools.Web.QueryRow(x.ctx, `SELECT count(*) FROM companies WHERE org_id=$1 AND name='Walk In Co'`, x.org).Scan(&n)
	if n != 1 {
		t.Errorf("a Customer made in EmberOps made %d Hearth companies", n)
	}
	_ = x.pools.Web.QueryRow(x.ctx, `SELECT count(*) FROM companies WHERE org_id=$1 AND name='Off Co'`, x.org).Scan(&n)
	if n != 0 {
		t.Errorf("a disabled Customer became a Hearth company")
	}
	// The next cycle links them by name, and nothing is made again.
	for i := 0; i < 2; i++ {
		if rep, err = x.sync.Run(x.ctx, o); err != nil || rep.Failed() {
			t.Fatalf("run %d: %v %+v", i+2, err, rep.Actions)
		}
	}
	_ = x.pools.Web.QueryRow(x.ctx, `SELECT count(*) FROM companies WHERE org_id=$1 AND lower(name) IN ('quiet vendor inc','walk in co')`, x.org).Scan(&n)
	if n != 2 || len(x.f.All("Customer")) != 2 || len(x.f.All("Supplier")) != 1 {
		t.Errorf("after two more runs: %d companies, %d customers, %d suppliers", n, len(x.f.All("Customer")), len(x.f.All("Supplier")))
	}
	for _, c := range x.f.All("Customer") {
		if c["name"] == "Walk In Co" && c["hearth_id"] == nil {
			t.Errorf("Walk In Co was never linked to its Hearth company")
		}
	}
}

func (x *syncFixture) activity(kind, title, body string, targets ...[2]string) string {
	id := uuid.NewString()
	x.exec(`INSERT INTO crm_activities (id, org_id, kind, title, body, direction, disposition, duration_ms, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,'reached',$7,$8)`, id, x.org, kind, title, body,
		map[bool]any{true: "outbound", false: nil}[kind == "call"], int64(125000), x.who)
	for _, t := range targets {
		x.exec(`INSERT INTO crm_activity_targets (activity_id, target_type, target_id) VALUES ($1,$2,$3::uuid)`, id, t[0], t[1])
	}
	return id
}

func TestNotesAndCallsBecomeCommentsOnTheMatchingRecordOneWay(t *testing.T) {
	x := newSyncFixture(t)
	co := x.mkCo("Talked To Client", "tc.example", "client")
	ct := x.person(co, "Tess Talker", "tess@tc.example", "")
	pr := x.mkCo("Talked To Prospect", "tp.example", "prospect")
	dl := x.mkDl("Talked deal", "meeting", pr, 100)
	untouched := x.mkCo("Nobody Knows", "nk.example") // no record in EmberOps
	note := x.activity("note", "Site visit", "Looked at the press <brake>.\nSecond line.", [2]string{"company", co}, [2]string{"contact", ct})
	call := x.activity("call", "", "Left a message", [2]string{"deal", dl})
	x.activity("note", "Lost in space", "no record", [2]string{"company", untouched})
	gone := x.activity("note", "Removed", "deleted in Hearth", [2]string{"company", co})
	x.exec(`UPDATE crm_activities SET deleted_at = now() WHERE id = $1`, gone)

	o := opts()
	o.Timeline = true
	rep, err := x.sync.Run(x.ctx, o)
	if err != nil || rep.Failed() {
		t.Fatalf("run: %v %+v", err, rep.Actions)
	}
	cs := x.f.All("Comment")
	if len(cs) != 3 || rep.Count("comment", "create") != 3 {
		t.Fatalf("%d Comments, want 3 (a note on a company and on a person, and a call on a deal): %+v", len(cs), rep.Actions)
	}
	byKey := map[string]map[string]any{}
	for _, c := range cs {
		byKey[c["hearth_id"].(string)] = c
	}
	cust := byHearth(t, x, "Customer", co)["name"]
	if c := byKey[note+":"+co]; c == nil || c["reference_doctype"] != "Customer" || c["reference_name"] != cust ||
		!strings.Contains(c["content"].(string), "Hearth note") || !strings.Contains(c["content"].(string), "&lt;brake&gt;") ||
		!strings.Contains(c["content"].(string), "<br>Second line.") {
		t.Errorf("the note on the company: %v", c)
	}
	if c := byKey[note+":"+ct]; c == nil || c["reference_doctype"] != "Contact" {
		t.Errorf("the note on the person: %v", c)
	}
	if c := byKey[call+":"+dl]; c == nil || c["reference_doctype"] != "Opportunity" ||
		!strings.Contains(c["content"].(string), "Hearth call, outbound, 3 min, reached") {
		t.Errorf("the call on the deal: %v", c)
	}

	// Again: nothing is sent. An edit in Hearth is one update. An edit in EmberOps is put back.
	before := x.f.CallCount(upsertCalls)
	if rep, err = x.sync.Run(x.ctx, o); err != nil || rep.Failed() {
		t.Fatalf("second run: %v %+v", err, rep.Actions)
	}
	if n := x.f.CallCount(upsertCalls) - before; n != 0 || len(x.f.All("Comment")) != 3 {
		t.Errorf("a settled timeline was sent again: %d upserts, %d Comments %+v", n, len(x.f.All("Comment")), rep.Actions)
	}
	x.exec(`UPDATE crm_activities SET body='Second thought' WHERE id=$1`, note)
	byKey[call+":"+dl]["content"] = "typed in EmberOps"
	if rep, err = x.sync.Run(x.ctx, o); err != nil || rep.Failed() {
		t.Fatalf("third run: %v %+v", err, rep.Actions)
	}
	if rep.Count("comment", "update") != 3 || len(x.f.All("Comment")) != 3 {
		t.Errorf("an edit in Hearth (on two records) and one in EmberOps should be three updates and no new Comment: %+v", rep.Actions)
	}
	// Off, nothing about the timeline is sent.
	y := newSyncFixture(t)
	c2 := y.mkCo("Quiet Timeline", "qt.example", "client")
	y.activity("note", "x", "y", [2]string{"company", c2})
	if rep, err = y.sync.Run(y.ctx, opts()); err != nil || len(y.f.All("Comment")) != 0 {
		t.Errorf("the timeline was sent without being asked: %v", err)
	}
}

func TestAPhoneCallBecomesACallLogOnTheContactAndCompanyAndOnlyThePhonesDo(t *testing.T) {
	x := newSyncFixture(t)
	co := x.mkCo("Rang Us Client", "ru.example", "client")
	ct := x.person(co, "Rae Caller", "rae@ru.example", "216 555 0142")
	in := x.activity("call", "Inbound call from Rae Caller", "Number: +12165550142\nAnswered by Patrick on extension 1003", [2]string{"company", co}, [2]string{"contact", ct})
	x.exec(`UPDATE crm_activities SET direction='inbound', disposition='answered', duration_ms=94000 WHERE id=$1`, in)
	x.exec(`INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id) VALUES ($1,'phone','call-1','activity',$2)`, x.org, in)
	miss := x.activity("call", "Missed call from Rae Caller", "Number: +12165550142\nRang 2001, nobody answered", [2]string{"contact", ct})
	x.exec(`UPDATE crm_activities SET direction='inbound', disposition='missed', duration_ms=0 WHERE id=$1`, miss)
	x.exec(`INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id) VALUES ($1,'phone','call-2','activity',$2)`, x.org, miss)
	// a call a person logged by hand is a Comment and never a Call Log
	x.activity("call", "", "Talked at the show", [2]string{"company", co})

	o := opts()
	o.Timeline = true
	o.TimeZone = "America/New_York"
	rep, err := x.sync.Run(x.ctx, o)
	if err != nil || rep.Failed() {
		t.Fatalf("run: %v %+v", err, rep.Actions)
	}
	logs := x.f.All("Call Log")
	if len(logs) != 2 || rep.Count("calllog", "create") != 2 {
		t.Fatalf("%d Call Logs, want the two phone calls: %+v", len(logs), rep.Actions)
	}
	if n := len(x.f.All("Comment")); n != 4 {
		t.Errorf("the timeline still sends every call and note as a Comment: %d Comments", n)
	}
	var answered, missed map[string]any
	for _, l := range logs {
		switch l["hearth_id"] {
		case in:
			answered = l
		case miss:
			missed = l
		}
	}
	if answered == nil || answered["type"] != "Incoming" || answered["status"] != "Completed" || answered["from"] != "+12165550142" ||
		fmt.Sprint(answered["duration"]) != "94" || answered["customer"] != byHearth(t, x, "Customer", co)["name"] ||
		answered["id"] != "hearth-"+in || !strings.Contains(fmt.Sprint(answered["summary"]), "Answered by Patrick on extension 1003") {
		t.Errorf("the answered call: %v", answered)
	}
	if links, _ := answered["links"].([]any); len(links) != 2 {
		t.Errorf("the call is linked to the company's Customer and the person's Contact: %v", answered["links"])
	}
	if missed == nil || missed["status"] != "No Answer" || fmt.Sprint(missed["duration"]) != "0" {
		t.Errorf("the missed call: %v", missed)
	}
	// Again: nothing is sent.
	before := x.f.CallCount(upsertCalls)
	if rep, err = x.sync.Run(x.ctx, o); err != nil || rep.Failed() {
		t.Fatalf("second run: %v %+v", err, rep.Actions)
	}
	if n := x.f.CallCount(upsertCalls) - before; n != 0 || len(x.f.All("Call Log")) != 2 {
		t.Errorf("settled Call Logs were sent again: %d upserts %+v", n, rep.Actions)
	}
}

func TestAPersonTheCallerMadeWithNoCompanyStillReachesEmberOps(t *testing.T) {
	x := newSyncFixture(t)
	id := uuid.NewString()
	x.exec(`INSERT INTO contacts (id, org_id, name, phone, created_by) VALUES ($1,$2,'+12165550177','+12165550177',$3)`, id, x.org, x.who)
	x.exec(`INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id) VALUES ($1,'phone','caller:+12165550177','contact',$2)`, x.org, id)
	// a person with no company who did not come from the phone stays in Hearth, as before
	x.exec(`INSERT INTO contacts (id, org_id, name, phone, created_by) VALUES (gen_random_uuid(),$1,'Loose Lou','216 555 0111',$2)`, x.org, x.who)
	rep, err := x.sync.Run(x.ctx, opts())
	if err != nil || rep.Failed() {
		t.Fatalf("run: %v %+v", err, rep.Actions)
	}
	cs := x.f.All("Contact")
	if len(cs) != 1 || cs[0]["hearth_id"] != id {
		t.Fatalf("EmberOps has %d Contacts, want only the one the phone made: %+v", len(cs), rep.Actions)
	}
	if links, _ := cs[0]["links"].([]any); len(links) != 0 {
		t.Errorf("a person with no company was linked to %v", links)
	}
	before := x.f.CallCount(upsertCalls)
	if rep, err = x.sync.Run(x.ctx, opts()); err != nil || rep.Failed() || x.f.CallCount(upsertCalls) != before {
		t.Errorf("a settled contact was sent again: %v %+v", err, rep.Actions)
	}
}
