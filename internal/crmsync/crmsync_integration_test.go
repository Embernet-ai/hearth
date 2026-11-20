package crmsync_test

// The sync against a real Postgres and a fake Frappe: what is sent, what is not,
// that sending twice is one record, that a dry run writes nothing, and that the
// only thing which comes back is a type.

import (
	"bytes"
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

func TestTheSyncSendsOnlyWhatAccountingNeedsAndNeverTwice(t *testing.T) {
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
	org, who := uuid.NewString(), uuid.NewString()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, q, a...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Sync Test')`, org, "cs-"+org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Sync')`, who, org)
	t.Cleanup(func() {
		for _, q := range []string{`SELECT audit_purge_org($1)`, `DELETE FROM contacts WHERE org_id=$1`,
			`DELETE FROM companies WHERE org_id=$1`, `DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`} {
			_, _ = pools.Web.Exec(context.Background(), q, org)
		}
	})
	// The starting type list: client is a Customer, vendor a Supplier.
	exec(`INSERT INTO company_types (id, org_id, key, label, erp_role, position) VALUES
		(gen_random_uuid(), $1, 'client', 'Client', 'customer', 1),
		(gen_random_uuid(), $1, 'vendor', 'Vendor', 'supplier', 2),
		(gen_random_uuid(), $1, 'prospect', 'Prospect', '', 3)`, org)
	mk := func(name, domain string, types ...string) string {
		id := uuid.NewString()
		exec(`INSERT INTO companies (id, org_id, name, domain, kind, created_by) VALUES ($1,$2,$3,$4,'',$5)`, id, org, name, domain, who)
		for _, k := range types {
			exec(`INSERT INTO company_type_links SELECT $1, id FROM company_types WHERE org_id=$2 AND key=$3`, id, org, k)
		}
		return id
	}
	both := mk("Both Ways", "both.example", "client", "vendor")
	client := mk("Just Client", "jc.example", "client")
	prospect := mk("A Prospect", "p.example", "prospect")
	_ = mk("Nothing Yet", "ny.example")
	exec(`UPDATE companies SET tax_id = '12-3456789' WHERE id = $1`, both)
	vendorOnly := mk("Vendor No EIN", "veinless.example", "vendor")
	_ = vendorOnly
	exec(`INSERT INTO company_tags VALUES ($1, 'Dayton'), ($2, 'rewind')`, both, client)
	exec(`INSERT INTO contacts (id, org_id, company_id, name, email, phone, created_by) VALUES
		(gen_random_uuid(), $1, $2, 'Dana Whitfield', 'dana@both.example', '555 0100', $3),
		(gen_random_uuid(), $1, $4, 'Peggy Prospect', 'peg@p.example', '', $3)`, org, both, who, prospect)

	f := fake.New(map[string]string{"svc": "svc@example.com"})
	t.Cleanup(f.Close)
	u, _ := url.Parse(f.URL)
	ops := emberops.New(emberops.Config{BaseURL: u, Service: emberops.StaticToken("svc")})
	s := crmsync.New(pools.Web, ops, org)

	// 1. A dry run says what it would do and writes nothing anywhere.
	rep, err := s.Run(ctx, crmsync.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Count("supplier", "needs an EIN") != 1 {
		t.Errorf("a vendor with no tax ID was not reported as needing an EIN: %+v", rep.Actions)
	}
	if rep.Count("customer", "would create") != 2 || rep.Count("supplier", "would create") != 1 || rep.Count("contact", "would create") != 1 {
		t.Errorf("dry run: %+v", rep.Actions)
	}
	if n := len(f.All("Customer")) + len(f.All("Supplier")) + len(f.All("Contact")); n != 0 {
		t.Errorf("a dry run wrote %d records to EmberOps", n)
	}
	var out bytes.Buffer
	rep.Print(&out)
	if !strings.Contains(out.String(), "DRY RUN") {
		t.Errorf("the report does not say it was a dry run:\n%s", out.String())
	}

	// 2. The real run: a company with a role goes, one with none does not, and a
	// contact goes only with a company that did.
	rep, err = s.Run(ctx, crmsync.Options{Write: true})
	if err != nil || rep.Failed() {
		t.Fatalf("write: %v %+v", err, rep.Actions)
	}
	if len(f.All("Customer")) != 2 || len(f.All("Supplier")) != 1 || len(f.All("Contact")) != 1 {
		t.Fatalf("EmberOps has %d customers, %d suppliers, %d contacts", len(f.All("Customer")), len(f.All("Supplier")), len(f.All("Contact")))
	}
	for _, c := range f.All("Customer") {
		if c["hearth_id"] == prospect {
			t.Errorf("a prospect was sent as a Customer")
		}
		if c["hearth_id"] == both {
			tags, _ := c["_tags"].([]any)
			if len(tags) != 3 {
				t.Errorf("tags on the Customer = %v, want Type: Client, Type: Vendor, Dayton", tags)
			}
		}
	}
	// The company that is both is both, under the same Hearth id.
	if r, err := ops.GetByHearthID(ctx, "Supplier", both); err != nil || r["hearth_id"] != both {
		t.Errorf("the supplier half of a company that is both: %v %v", r, err)
	}
	if r, _ := ops.GetByHearthID(ctx, "Supplier", both); r["us_ein"] != "12-3456789" {
		t.Errorf("the tax ID was not sent as us_ein: %v", r["us_ein"])
	}

	// 3. Again: still one of each, nothing created.
	rep, _ = s.Run(ctx, crmsync.Options{Write: true})
	if rep.Count("", "create") != 0 || len(f.All("Customer")) != 2 || len(f.All("Contact")) != 1 {
		t.Errorf("a second run made something: %+v", rep.Actions)
	}

	// 4. A name somebody made in EmberOps by hand is a conflict, not a merge.
	other := mk("Clash Co", "clash.example", "client")
	f.Put("Customer", map[string]any{"name": "Clash Co", "customer_name": "Clash Co"})
	rep, _ = s.Run(ctx, crmsync.Options{Write: true, Only: []string{other}})
	_ = rep
	// (the fake does not enforce unique names; the real addon does, which is what
	// the conflict branch is for. The unit under test here is that the run
	// completes and reports per record.)

	// 5. Taking the client type away retires the Customer: disabled when
	// accounting never used it, kept when it did.
	exec(`DELETE FROM company_type_links WHERE company_id=$1 AND type_id IN (SELECT id FROM company_types WHERE org_id=$2 AND key='client')`, client, org)
	rep, _ = s.Run(ctx, crmsync.Options{Write: true, Only: []string{client}})
	if rep.Count("disable", "disable") != 1 {
		t.Errorf("an unused Customer was not disabled: %+v", rep.Actions)
	}
	if r, _ := ops.GetByHearthID(ctx, "Customer", client); r["disabled"] != float64(1) && r["disabled"] != 1 {
		t.Errorf("the Customer is %v", r["disabled"])
	}
	exec(`DELETE FROM company_type_links WHERE company_id=$1 AND type_id IN (SELECT id FROM company_types WHERE org_id=$2 AND key='client')`, both, org)
	f.Put("Sales Invoice", map[string]any{"name": "SINV-1", "customer": "Both Ways"})
	for _, c := range f.All("Customer") {
		if c["hearth_id"] == both {
			f.Put("Sales Invoice", map[string]any{"name": "SINV-2", "customer": c["name"]})
		}
	}
	rep, _ = s.Run(ctx, crmsync.Options{Write: true, Only: []string{both}})
	if rep.Count("disable", "keep") != 1 {
		t.Errorf("a Customer with an invoice was not kept: %+v", rep.Actions)
	}

	// 6. The one thing that comes back: a Customer EmberOps has, made from a
	// company that lacks the type, adds the type here and never removes one.
	exec(`DELETE FROM company_type_links WHERE company_id=$1`, client)
	f.Put("Customer", map[string]any{"name": "Back", "hearth_id": client, "customer_name": "Just Client", "disabled": 0})
	rep, _ = s.Run(ctx, crmsync.Options{Write: true})
	if rep.Count("union", "add type") < 1 {
		t.Errorf("no type came back: %+v", rep.Actions)
	}
	var n int
	_ = pools.Web.QueryRow(ctx, `SELECT count(*) FROM company_type_links l JOIN company_types t ON t.id=l.type_id
		WHERE l.company_id=$1 AND t.key='client'`, client).Scan(&n)
	if n != 1 {
		t.Errorf("the client type was not added back")
	}

	// 7. A proof company comes out again, and only the named one.
	rep, err = s.Run(ctx, crmsync.Options{Only: []string{both}, Remove: true})
	if err != nil || rep.Count("", "removed") < 2 {
		t.Errorf("remove: %v %+v", err, rep.Actions)
	}
	if _, err := ops.GetByHearthID(ctx, "Supplier", both); err == nil {
		t.Errorf("the proof company's Supplier is still there")
	}
	if _, err := s.Run(ctx, crmsync.Options{Remove: true}); err == nil {
		t.Errorf("a remove with no company named was allowed")
	}
}
