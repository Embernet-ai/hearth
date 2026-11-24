package crmsync_test

// Deals to Opportunities and the overwrite report, against a real Postgres and a
// fake Frappe.

import (
	"context"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/crmsync"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/fake"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

type syncFixture struct {
	ctx   context.Context
	exec  func(q string, a ...any)
	org   string
	who   string
	f     *fake.Frappe
	ops   *emberops.Client
	sync  *crmsync.Syncer
	mkCo  func(name, domain string, types ...string) string
	mkDl  func(name, stage, company string, cents int) string
	pools *pg.Pools
}

func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := pg.Migrate(ctx, pools, log); err != nil {
		t.Fatal(err)
	}
	x := &syncFixture{ctx: ctx, org: uuid.NewString(), who: uuid.NewString(), pools: pools}
	x.exec = func(q string, a ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, q, a...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	x.exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Deals Test')`, x.org, "dl-"+x.org[:8])
	x.exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Sync')`, x.who, x.org)
	t.Cleanup(func() {
		for _, q := range []string{`SELECT audit_purge_org($1)`, `DELETE FROM external_refs WHERE org_id=$1`, `DELETE FROM deals WHERE org_id=$1`,
			`DELETE FROM contacts WHERE org_id=$1`, `DELETE FROM companies WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`, `DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`} {
			_, _ = pools.Web.Exec(context.Background(), q, x.org)
		}
	})
	x.exec(`INSERT INTO company_types (id, org_id, key, label, erp_role, position) VALUES
		(gen_random_uuid(), $1, 'client', 'Client', 'customer', 1),
		(gen_random_uuid(), $1, 'vendor', 'Vendor', 'supplier', 2),
		(gen_random_uuid(), $1, 'prospect', 'Prospect', '', 3)`, x.org)
	x.mkCo = func(name, domain string, types ...string) string {
		id := uuid.NewString()
		x.exec(`INSERT INTO companies (id, org_id, name, domain, kind, created_by) VALUES ($1,$2,$3,$4,'',$5)`, id, x.org, name, domain, x.who)
		for _, k := range types {
			x.exec(`INSERT INTO company_type_links SELECT $1, id FROM company_types WHERE org_id=$2 AND key=$3`, id, x.org, k)
		}
		return id
	}
	x.mkDl = func(name, stage, company string, cents int) string {
		id := uuid.NewString()
		var co any
		if company != "" {
			co = company
		}
		x.exec(`INSERT INTO deals (id, org_id, name, stage, amount_cents, company_id, created_by, close_date)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'2026-12-31')`, id, x.org, name, stage, cents, co, x.who)
		return id
	}
	x.f = fake.New(map[string]string{"svc": "svc@example.com"})
	t.Cleanup(x.f.Close)
	u, _ := url.Parse(x.f.URL)
	x.ops = emberops.New(emberops.Config{BaseURL: u, Service: emberops.StaticToken("svc")})
	x.sync = crmsync.New(pools.Web, x.ops, x.org)
	return x
}

func TestDealsGoAcrossOnlyWhenAskedAndOnlyWithAPartyEmberOpsKnows(t *testing.T) {
	x := newSyncFixture(t)
	client := x.mkCo("Steady Client", "sc.example", "client")
	prospect := x.mkCo("Hopeful Prospect", "hp.example", "prospect")
	nothing := x.mkCo("No Deal Co", "nd.example")
	_ = nothing
	d1 := x.mkDl("Line upgrade", "proposal", client, 1250000)
	d2 := x.mkDl("First pilot", "meeting", prospect, 400000)
	d3 := x.mkDl("Floating idea", "new", "", 0)
	d4 := x.mkDl("Old thing", "lost", prospect, 100)
	x.exec(`UPDATE deals SET deleted_at = now() WHERE id = $1`, d4)
	_ = d3

	// Off by default: nothing about deals is sent, and the report says nothing about them.
	rep, err := x.sync.Run(x.ctx, crmsync.Options{Write: true})
	if err != nil || rep.Failed() {
		t.Fatalf("write: %v %+v", err, rep.Actions)
	}
	if len(x.f.All("Opportunity"))+len(x.f.All("Lead")) != 0 {
		t.Fatalf("deals went across without being asked for: %d opportunities, %d leads",
			len(x.f.All("Opportunity")), len(x.f.All("Lead")))
	}

	// A dry run with deals says what it would do and writes nothing.
	rep, err = x.sync.Run(x.ctx, crmsync.Options{Deals: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Count("opportunity", "would create") != 3 || rep.Count("lead", "would create") != 2 {
		t.Errorf("dry run with deals: %+v", rep.Actions)
	}
	if len(x.f.All("Opportunity"))+len(x.f.All("Lead")) != 0 {
		t.Fatal("a dry run wrote deals")
	}

	// The real run: an Opportunity per live deal that has a company, a Lead only
	// for the prospect that has a deal, never for the company with none.
	rep, err = x.sync.Run(x.ctx, crmsync.Options{Write: true, Deals: true})
	if err != nil || rep.Failed() {
		t.Fatalf("write deals: %v %+v", err, rep.Actions)
	}
	if n := len(x.f.All("Opportunity")); n != 3 {
		t.Fatalf("%d Opportunities, want 3 (every live deal, the one with no company too; a removed deal does not go)", n)
	}
	if n := len(x.f.All("Lead")); n != 2 {
		t.Fatalf("%d Leads, want 2 (the prospect with a deal, and one for the deal with no company)", n)
	}
	o3, err := x.ops.GetByHearthID(x.ctx, "Opportunity", d3)
	if err != nil {
		t.Fatalf("the deal with no company did not reach EmberOps: %v", err)
	}
	own, err := x.ops.GetByHearthID(x.ctx, "Lead", d3)
	if err != nil || o3["opportunity_from"] != "Lead" || o3["party_name"] != own["name"] || own["company_name"] != "Floating idea" {
		t.Errorf("the deal with no company: Opportunity %v, its Lead %v (%v)", o3, own, err)
	}
	o1, err := x.ops.GetByHearthID(x.ctx, "Opportunity", d1)
	if err != nil {
		t.Fatal(err)
	}
	if o1["opportunity_from"] != "Customer" || o1["status"] != "Open" || o1["sales_stage"] != "Proposal/Price Quote" ||
		o1["title"] != "Line upgrade" || o1["expected_closing"] != "2026-12-31" {
		t.Errorf("the client's Opportunity: %v", o1)
	}
	if amt, _ := o1["opportunity_amount"].(float64); amt != 12500 {
		t.Errorf("amount = %v, want 12500 dollars from 1250000 cents", o1["opportunity_amount"])
	}
	o2, err := x.ops.GetByHearthID(x.ctx, "Opportunity", d2)
	if err != nil {
		t.Fatal(err)
	}
	lead, err := x.ops.GetByHearthID(x.ctx, "Lead", prospect)
	if err != nil {
		t.Fatalf("no Lead for the prospect: %v", err)
	}
	if o2["opportunity_from"] != "Lead" || o2["party_name"] != lead["name"] {
		t.Errorf("the prospect's Opportunity points at %v %v, want Lead %v", o2["opportunity_from"], o2["party_name"], lead["name"])
	}
	if lead["company_name"] != "Hopeful Prospect" {
		t.Errorf("the Lead is %v", lead)
	}

	// Again: still one of each. A stage moved in Hearth is the next run's update.
	x.exec(`UPDATE deals SET stage='won' WHERE id=$1`, d1)
	rep, _ = x.sync.Run(x.ctx, crmsync.Options{Write: true, Deals: true})
	if rep.Count("", "create") != 0 || len(x.f.All("Opportunity")) != 3 || len(x.f.All("Lead")) != 2 {
		t.Errorf("a second run made something: %+v", rep.Actions)
	}
	o1, _ = x.ops.GetByHearthID(x.ctx, "Opportunity", d1)
	// EmberOps sets Converted from Quotations and refuses it from anyone else: a won deal changes no status.
	if rep.Failed() {
		t.Errorf("a won deal failed: %+v", rep.Actions)
	}
	if o1["status"] == "Converted" {
		t.Errorf("a won deal was sent Converted: %v", o1["status"])
	}
	for _, a := range rep.Actions {
		if a.Verb == "overwrite" && a.What == "opportunity" && strings.Contains(a.Detail, "status") {
			t.Errorf("a won deal tried to overwrite the status: %+v", a)
		}
	}

	// A deal removed in Hearth leaves its Opportunity alone and says so.
	x.exec(`UPDATE deals SET deleted_at = now() WHERE id=$1`, d2)
	rep, _ = x.sync.Run(x.ctx, crmsync.Options{Write: true, Deals: true})
	if rep.Count("opportunity", "keep") != 1 || len(x.f.All("Opportunity")) != 3 {
		t.Errorf("a removed deal was not kept: %+v", rep.Actions)
	}
}

func TestSomethingChangedInEmberOpsIsReportedBeforeItIsOverwritten(t *testing.T) {
	x := newSyncFixture(t)
	co := x.mkCo("Edited Elsewhere", "orig.example", "client")
	if rep, err := x.sync.Run(x.ctx, crmsync.Options{Write: true}); err != nil || rep.Failed() {
		t.Fatalf("first write: %v %+v", err, rep.Actions)
	}
	// Nothing differs yet, so there is nothing to report.
	rep, _ := x.sync.Run(x.ctx, crmsync.Options{Write: true})
	if rep.Count("", "overwrite") != 0 {
		t.Fatalf("a run with no differences reported overwrites: %+v", rep.Actions)
	}
	// Somebody edits a Hearth-owned field in EmberOps.
	for _, c := range x.f.All("Customer") {
		if c["hearth_id"] == co {
			x.f.Put("Customer", map[string]any{"name": c["name"], "hearth_id": co, "customer_name": c["customer_name"],
				"hearth_domain": "edited.example"})
		}
	}
	rep, _ = x.sync.Run(x.ctx, crmsync.Options{})
	if rep.Count("customer", "would overwrite") != 1 {
		t.Fatalf("the dry run did not say what it would overwrite: %+v", rep.Actions)
	}
	rep, _ = x.sync.Run(x.ctx, crmsync.Options{Write: true})
	var line string
	for _, a := range rep.Actions {
		if a.Verb == "overwrite" {
			line = a.Detail
		}
	}
	if !strings.Contains(line, "hearth_domain") || !strings.Contains(line, "edited.example") || !strings.Contains(line, "orig.example") {
		t.Errorf("the overwrite line is %q", line)
	}
	r, _ := x.ops.GetByHearthID(x.ctx, "Customer", co)
	if r["hearth_domain"] != "orig.example" {
		t.Errorf("Hearth's value did not win: %v", r["hearth_domain"])
	}
}
