package mauticsync_test

// Contacts sent at once must never make Mautic companies of their own. Mautic builds a company from a contact's company text when it
// holds none of that name, and with contacts in parallel each one built its own: the same name on two to four companies, which then
// refused every edit. The company is linked by id, so a company that could not be made on one run and is made on the next ends as one.

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/mautic"
	"github.com/Embernet-ai/hearth/internal/mauticsync"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// autoCompany is a Mautic that does what the real one does with a contact's company text: look for a company of that name and, when
// there is none, make one. The look and the make are not one step, so contacts at the same moment each make their own.
type autoCompany struct {
	*fake
	refuseCompanies atomic.Bool
}

func (a *autoCompany) UpsertCompany(ctx context.Context, id int64, fl mautic.Fields) (int64, error) {
	if a.refuseCompanies.Load() {
		return 0, &mautic.APIError{Status: 500, Method: "POST", Path: "/companies/new", Body: "try again later"}
	}
	return a.fake.UpsertCompany(ctx, id, fl)
}

func (a *autoCompany) UpsertContact(ctx context.Context, id int64, fl mautic.Fields) (int64, error) {
	if name, _ := fl["company"].(string); name != "" {
		a.fake.mu.Lock()
		have := false
		for _, c := range a.fake.companies {
			if n, _ := c["companyname"].(string); strings.EqualFold(n, name) {
				have = true
			}
		}
		a.fake.mu.Unlock()
		time.Sleep(20 * time.Millisecond) // the gap between looking and making
		if !have {
			a.fake.mu.Lock()
			a.fake.companies[a.fake.id()] = mautic.Fields{"companyname": name}
			a.fake.mu.Unlock()
		}
	}
	return a.fake.UpsertContact(ctx, id, fl)
}

func TestContactsSentAtOnceNeverMakeCompaniesOfTheirOwn(t *testing.T) {
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
	org, who, co := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, q, a...); err != nil {
			t.Fatalf("setup: %v\n%s", err, q)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Mautic Dupes')`, org, "md-"+org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Owner')`, who, org)
	t.Cleanup(func() {
		for _, q := range []string{`SELECT audit_purge_org($1)`, `DELETE FROM mautic_sync_state WHERE org_id=$1`, `DELETE FROM contacts WHERE org_id=$1`,
			`DELETE FROM companies WHERE org_id=$1`, `DELETE FROM outbox WHERE org_id=$1`, `DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`} {
			_, _ = pools.Web.Exec(context.Background(), q, org)
		}
	})
	exec(`INSERT INTO companies (id, org_id, name, domain, created_by) VALUES ($1,$2,'Racey Co','racey.test',$3)`, co, org, who)
	for i := 0; i < 16; i++ {
		exec(`INSERT INTO contacts (id, org_id, company_id, name, email, created_by) VALUES ($1,$2,$3,$4,$5,$6)`,
			uuid.NewString(), org, co, "Racer "+string(rune('A'+i)), "r"+string(rune('a'+i))+"@example.test", who)
	}
	f := newFake()
	m := &autoCompany{fake: f}
	s := mauticsync.New(pools.Web, m, org)

	// The first run cannot make the company (Mautic answers an error), so every contact is sent with no company made.
	m.refuseCompanies.Store(true)
	if _, err := s.Run(ctx, mauticsync.Options{Write: true, Workers: 8}); err != nil {
		t.Fatal(err)
	}
	if len(f.contacts) != 16 {
		t.Fatalf("Mautic got %d contacts, want 16", len(f.contacts))
	}
	if n := len(f.companies); n != 0 {
		t.Fatalf("contacts sent at once made %d Mautic companies from their company text, want none", n)
	}

	// The next run can make it: one company, and every contact linked to it.
	m.refuseCompanies.Store(false)
	rep, err := s.Run(ctx, mauticsync.Options{Write: true, Workers: 8})
	if err != nil || rep.Failed() {
		t.Fatalf("second run: %v failed=%v", err, rep.Failed())
	}
	if n := len(f.companies); n != 1 {
		t.Fatalf("Mautic has %d companies named Racey Co, want exactly 1", n)
	}
	var linked int
	if err := pools.Web.QueryRow(ctx, `SELECT count(*) FROM mautic_links WHERE org_id=$1 AND company_mautic_id IS NOT NULL`, org).Scan(&linked); err != nil || linked != 16 {
		t.Errorf("%d of 16 contacts are linked to the company (%v)", linked, err)
	}
}
