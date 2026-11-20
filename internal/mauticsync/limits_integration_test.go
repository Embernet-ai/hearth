package mauticsync_test

// The second live run still lost 62 records: a contact position past 64 characters (20), a first name and an email past it (1), and
// a company whose name Mautic already held (10). Limits come from Mautic's own field definitions, and a name that collides is
// adopted, not created twice.

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/mautic"
	"github.com/Embernet-ai/hearth/internal/mauticsync"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// limited is a Mautic that publishes field definitions and refuses what its fields refuse: text past the limit, an email past it,
// and a second company with a name that is already taken.
type limited struct {
	*fake
	defs map[string]map[string]mautic.FieldDef
}

func (l limited) FieldDefs(_ context.Context, object string) (map[string]mautic.FieldDef, error) {
	return l.defs[object], nil
}

func tooLong(fl mautic.Fields, defs map[string]mautic.FieldDef) string {
	for k, v := range fl {
		if s, ok := v.(string); ok && defs[k].Limit > 0 && len([]rune(s)) > defs[k].Limit {
			return k
		}
	}
	return ""
}

func (l limited) UpsertContact(ctx context.Context, id int64, fl mautic.Fields) (int64, error) {
	if k := tooLong(fl, l.defs["contact"]); k != "" {
		return 0, &mautic.APIError{Status: 400, Method: "POST", Path: "/contacts/new", Body: `{"errors":[{"code":400,"message":"` + k + `: This value is too long."}]}`}
	}
	return l.fake.UpsertContact(ctx, id, fl)
}

func (l limited) UpsertCompany(ctx context.Context, id int64, fl mautic.Fields) (int64, error) {
	if k := tooLong(fl, l.defs["company"]); k != "" {
		return 0, &mautic.APIError{Status: 400, Method: "POST", Path: "/companies/new", Body: `{"errors":[{"code":400,"message":"` + k + `: This value is too long."}]}`}
	}
	if v, _ := fl["companystate"].(string); v != "" && v != "Ohio" {
		return 0, &mautic.APIError{Status: 400, Method: "POST", Path: "/companies/new", Body: `{"errors":[{"code":400,"message":"companystate: The selected choice is invalid."}]}`}
	}
	if id == 0 {
		if found, _ := l.fake.FindCompany(ctx, fl["companyname"].(string)); found != 0 {
			return 0, &mautic.APIError{Status: 400, Method: "POST", Path: "/companies/new", Body: `{"errors":[{"code":400,"message":"companyname: This field must be unique."}]}`}
		}
	}
	return l.fake.UpsertCompany(ctx, id, fl)
}

func TestFieldLimitsComeFromMauticAndATakenCompanyNameIsAdopted(t *testing.T) {
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
			t.Fatalf("setup: %v\n%s", err, q)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Mautic Limits')`, org, "ml-"+org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Owner')`, who, org)
	t.Cleanup(func() {
		for _, q := range []string{`SELECT audit_purge_org($1)`, `DELETE FROM mautic_sync_state WHERE org_id=$1`, `DELETE FROM contacts WHERE org_id=$1`,
			`DELETE FROM companies WHERE org_id=$1`, `DELETE FROM outbox WHERE org_id=$1`, `DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`} {
			_, _ = pools.Web.Exec(context.Background(), q, org)
		}
	})
	long := strings.Repeat("Senior Controls Engineer ", 5)
	taken, plain := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO companies (id, org_id, name, domain, address_state, created_by) VALUES ($1,$2,'Already In Mautic','aim.test','OH',$3)`, taken, org, who)
	exec(`INSERT INTO companies (id, org_id, name, domain, created_by) VALUES ($1,$2,'Plain Co','plain.test',$3)`, plain, org, who)
	exec(`INSERT INTO contacts (id, org_id, company_id, name, email, title, created_by) VALUES ($1,$2,$3,'Long Title','lt@example.test',$4,$5)`, uuid.NewString(), org, plain, long, who)
	exec(`INSERT INTO contacts (id, org_id, company_id, name, email, created_by) VALUES ($1,$2,$3,$4,'fn@example.test',$5)`, uuid.NewString(), org, plain, strings.Repeat("Bartholomew", 8)+" Smith", who)
	exec(`INSERT INTO contacts (id, org_id, company_id, name, email, created_by) VALUES ($1,$2,$3,'Long Address',$4,$5)`, uuid.NewString(), org, plain, strings.Repeat("a", 60)+"@example.test", who)
	exec(`INSERT INTO contacts (id, org_id, company_id, name, email, created_by) VALUES ($1,$2,$3,'Ada Taken','ada@example.test',$4)`, uuid.NewString(), org, taken, who)

	f := newFake()
	f.companies[7] = mautic.Fields{"companyname": "already in mautic"} // somebody made it in Mautic by hand
	text := func(n int) mautic.FieldDef { return mautic.FieldDef{Type: "text", Limit: n} }
	m := limited{fake: f, defs: map[string]map[string]mautic.FieldDef{
		"contact": {"position": text(64), "firstname": text(64), "lastname": text(64), "company": text(64), "email": {Type: "email", Limit: 64}, "hearth_id": text(64)},
		"company": {"companyname": text(64), "hearth_company_id": text(64), "companystate": {Type: "region"}},
	}}
	rep, err := mauticsync.New(pools.Web, m, org).Run(ctx, mauticsync.Options{Write: true})
	if err != nil || rep.Failed() {
		t.Fatalf("run: %v, failed=%v", err, rep.Failed())
	}
	for _, c := range f.contacts {
		for _, k := range []string{"position", "firstname", "lastname"} {
			if s, _ := c[k].(string); len([]rune(s)) > 64 {
				t.Errorf("%s of %d characters was sent", k, len([]rune(s)))
			}
		}
	}
	if rep.Count("contact", "skip") != 1 {
		t.Errorf("the address longer than Mautic takes should be skipped, once: %+v", rep.Actions)
	}
	if len(f.contacts) != 3 {
		t.Errorf("Mautic got %d contacts, want 3 (the long address is skipped)", len(f.contacts))
	}
	if len(f.companies) != 2 {
		t.Errorf("Mautic has %d companies, want 2: the one it had and Plain Co", len(f.companies))
	}
	if got, _ := f.companies[7]["hearth_company_id"].(string); got != taken {
		t.Errorf("the Mautic company that already had the name did not take the Hearth id: %q", got)
	}
	var mid int64
	if err := pools.Web.QueryRow(ctx, `SELECT mautic_id FROM mautic_company_links WHERE company_id=$1`, taken).Scan(&mid); err != nil || mid != 7 {
		t.Errorf("Hearth's company is linked to Mautic company %d (%v), want 7", mid, err)
	}
}
