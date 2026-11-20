package crmsync_test

// An unsubscribe in Hearth reaches EmberOps, one way: the Contact and the Lead carry unsubscribed, and a person who is not
// opted out is never marked, so nothing here can take an unsubscribe back.

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

func TestAnUnsubscribeReachesEmberOpsOnTheContactAndTheLead(t *testing.T) {
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
	if err := pg.Migrate(ctx, pools, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))); err != nil {
		t.Fatal(err)
	}
	org, who := uuid.NewString(), uuid.NewString()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, q, a...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Unsub Test')`, org, "un-"+org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Sync')`, who, org)
	t.Cleanup(func() {
		for _, q := range []string{`SELECT audit_purge_org($1)`, `DELETE FROM contacts WHERE org_id=$1`,
			`DELETE FROM companies WHERE org_id=$1`, `DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`} {
			_, _ = pools.Web.Exec(context.Background(), q, org)
		}
	})
	exec(`INSERT INTO company_types (id, org_id, key, label, erp_role, position) VALUES
		(gen_random_uuid(), $1, 'client', 'Client', 'customer', 1), (gen_random_uuid(), $1, 'prospect', 'Prospect', '', 2)`, org)
	mk := func(name, domain, kind string) string {
		id := uuid.NewString()
		exec(`INSERT INTO companies (id, org_id, name, domain, kind, created_by) VALUES ($1,$2,$3,$4,'',$5)`, id, org, name, domain, who)
		exec(`INSERT INTO company_type_links SELECT $1, id FROM company_types WHERE org_id=$2 AND key=$3`, id, org, kind)
		return id
	}
	client, prospect := mk("Unsub Client", "uc.example", "client"), mk("Unsub Prospect", "up.example", "prospect")
	cOut, cIn, pOut := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO contacts (id, org_id, company_id, name, email, created_by, marketing_opt_out_at, marketing_opt_out_reason) VALUES
		($1,$2,$3,'Out Client','out@uc.example',$4, now(), 'unsubscribed')`, cOut, org, client, who)
	exec(`INSERT INTO contacts (id, org_id, company_id, name, email, created_by) VALUES
		($1,$2,$3,'In Client','in@uc.example',$4)`, cIn, org, client, who)
	exec(`INSERT INTO contacts (id, org_id, company_id, name, email, created_by, marketing_opt_out_at, marketing_opt_out_reason) VALUES
		($1,$2,$3,'Out Prospect','out@up.example',$4, now(), 'bounced')`, pOut, org, prospect, who)

	f := fake.New(map[string]string{"svc": "svc@example.com"})
	t.Cleanup(f.Close)
	u, _ := url.Parse(f.URL)
	ops := emberops.New(emberops.Config{BaseURL: u, Service: emberops.StaticToken("svc")})
	s := crmsync.New(pools.Web, ops, org)

	rep, err := s.Run(ctx, crmsync.Options{Write: true, Prospects: true})
	if err != nil || rep.Failed() {
		t.Fatalf("write: %v %+v", err, rep.Actions)
	}
	un := func(dt, id string) any {
		r, err := ops.GetByHearthID(ctx, dt, id)
		if err != nil {
			t.Fatalf("%s %s: %v", dt, id, err)
		}
		return r["unsubscribed"]
	}
	is := func(v any) bool { return v == float64(1) || v == 1 || v == true }
	if !is(un("Contact", cOut)) {
		t.Errorf("an unsubscribed person's Contact is %v in EmberOps, want unsubscribed", un("Contact", cOut))
	}
	if is(un("Contact", cIn)) {
		t.Errorf("a person who never unsubscribed was marked unsubscribed in EmberOps")
	}
	if !is(un("Contact", pOut)) {
		t.Errorf("a bounced prospect's Contact is %v in EmberOps, want unsubscribed", un("Contact", pOut))
	}
	if !is(un("Lead", prospect)) {
		t.Errorf("the Lead of an opted-out prospect is %v in EmberOps, want unsubscribed", un("Lead", prospect))
	}
	// One way: somebody ticking it off in EmberOps does not stick while Hearth holds the opt-out, and the sync never
	// ticks it on for a person who is not opted out.
	rep, _ = s.Run(ctx, crmsync.Options{Write: true, Prospects: true})
	if rep.Count("", "failed") != 0 {
		t.Errorf("second run: %+v", rep.Actions)
	}
	if !is(un("Contact", cOut)) || is(un("Contact", cIn)) {
		t.Errorf("a second run changed the unsubscribed flags")
	}

	// -show reads unsubscribed back for the Contact and the Lead, and -remove takes the Lead away with the rest.
	rep, _ = s.Run(ctx, crmsync.Options{Only: []string{prospect}, Show: true})
	var shown string
	for _, a := range rep.Actions {
		shown += a.What + " " + a.Detail + "\n"
	}
	if strings.Count(shown, `"unsubscribed":1`) < 2 {
		t.Errorf("-show did not read unsubscribed back on the Lead and its Contact:\n%s", shown)
	}
	if _, err := s.Run(ctx, crmsync.Options{Only: []string{prospect}, Remove: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.GetByHearthID(ctx, "Lead", prospect); err == nil {
		t.Errorf("-remove left the proof company's Lead in EmberOps")
	}
}
