package mauticsync_test

// Mautic refuses a whole company over a state or country its select lists do not have, and a name past 64 characters. The first
// live run lost 289 records to that. The company has to go anyway, without the part Mautic will not take.

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

// strict answers like the real Mautic: a 400 for a state or country that is not in its lists, and for a name over 64 characters.
type strict struct{ *fake }

func (s strict) FieldDefs(_ context.Context, object string) (map[string]mautic.FieldDef, error) {
	if object == "company" {
		return map[string]mautic.FieldDef{"companyname": {Type: "text", Limit: 64}}, nil
	}
	return map[string]mautic.FieldDef{"company": {Type: "text", Limit: 64}}, nil
}

func (s strict) UpsertCompany(ctx context.Context, id int64, fl mautic.Fields) (int64, error) {
	if v, _ := fl["companystate"].(string); v != "" && v != "Ohio" {
		return 0, &mautic.APIError{Status: 400, Method: "POST", Path: "/companies/new", Body: `{"errors":[{"code":400,"message":"companystate: The selected choice is invalid."}]}`}
	}
	if v, _ := fl["companycountry"].(string); v != "" && v != "United States" {
		return 0, &mautic.APIError{Status: 400, Method: "POST", Path: "/companies/new", Body: `{"errors":[{"code":400,"message":"companycountry: The selected choice is invalid."}]}`}
	}
	if n, _ := fl["companyname"].(string); len([]rune(n)) > 64 {
		return 0, &mautic.APIError{Status: 400, Method: "POST", Path: "/companies/new", Body: `{"errors":[{"code":400,"message":"companyname: This value is too long."}]}`}
	}
	return s.fake.UpsertCompany(ctx, id, fl)
}

func (s strict) UpsertContact(ctx context.Context, id int64, fl mautic.Fields) (int64, error) {
	if n, _ := fl["company"].(string); len([]rune(n)) > 64 {
		return 0, &mautic.APIError{Status: 400, Method: "POST", Path: "/contacts/new", Body: `{"errors":[{"code":400,"message":"company: This value is too long."}]}`}
	}
	return s.fake.UpsertContact(ctx, id, fl)
}

func TestACompanyMauticRefusesStillGoesWithoutTheRefusedPart(t *testing.T) {
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
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Mautic Refused')`, org, "mr-"+org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Owner')`, who, org)
	t.Cleanup(func() {
		for _, q := range []string{`SELECT audit_purge_org($1)`, `DELETE FROM mautic_sync_state WHERE org_id=$1`, `DELETE FROM contacts WHERE org_id=$1`,
			`DELETE FROM companies WHERE org_id=$1`, `DELETE FROM outbox WHERE org_id=$1`, `DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`} {
			_, _ = pools.Web.Exec(context.Background(), q, org)
		}
	})
	long := strings.Repeat("Maripak USA ", 8)
	odd, fine, lng := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO companies (id, org_id, name, domain, address_state, address_country, created_by) VALUES ($1,$2,'Odd State Co','odd.test','OH','Atlantis',$3)`, odd, org, who)
	exec(`INSERT INTO companies (id, org_id, name, domain, address_state, address_country, created_by) VALUES ($1,$2,'Fine Co','fine.test','Ohio','United States',$3)`, fine, org, who)
	exec(`INSERT INTO companies (id, org_id, name, domain, created_by) VALUES ($1,$2,$3,'long.test',$4)`, lng, org, long, who)
	for i, co := range []string{odd, fine, lng} {
		exec(`INSERT INTO contacts (id, org_id, company_id, name, email, created_by) VALUES ($1,$2,$3,$4,$5,$6)`,
			uuid.NewString(), org, co, "Person "+string(rune('A'+i)), "p"+string(rune('a'+i))+"@example.test", who)
	}
	f := newFake()
	s := mauticsync.New(pools.Web, strict{f}, org)
	rep, err := s.Run(ctx, mauticsync.Options{Write: true})
	if err != nil || rep.Failed() {
		t.Fatalf("run: %v, failed=%v", err, rep.Failed())
	}
	if len(f.companies) != 3 || len(f.contacts) != 3 {
		t.Fatalf("Mautic got %d companies and %d contacts, want 3 and 3", len(f.companies), len(f.contacts))
	}
	for _, c := range f.companies {
		if c["companyname"] == "Odd State Co" {
			if _, has := c["companystate"]; has {
				t.Errorf("the refused state was kept: %v", c["companystate"])
			}
		}
		if n, _ := c["companyname"].(string); len([]rune(n)) > 64 {
			t.Errorf("a name of %d characters was sent", len([]rune(n)))
		}
	}
	// A second run asks Mautic nothing about companies that already went.
	before := f.calls
	if rep, err = s.Run(ctx, mauticsync.Options{Write: true}); err != nil || rep.Failed() {
		t.Fatalf("second run: %v", err)
	}
	if rep.Count("company", "create")+rep.Count("company", "update") != 0 {
		t.Errorf("a second run resent companies: %+v", rep)
	}
	_ = before
}
