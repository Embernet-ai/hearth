package hubspot

// The delta import against a real Postgres and a fake HubSpot.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

type fx struct {
	t     *testing.T
	pools *pg.Pools
	svc   *crm.Service
	az    *authz.Service
	org   string
	alice string
	rep   string
	sales string
	actor core.Subjects
	hub   *fakeHub
}

func setupFx(t *testing.T) *fx {
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
	if err := pg.Migrate(ctx, pools, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))); err != nil {
		t.Fatal(err)
	}
	az := authz.New(pools.Web)
	f := &fx{t: t, pools: pools, az: az, svc: crm.New(pools.Web, az), org: uuid.NewString(), alice: uuid.NewString(),
		rep: uuid.NewString(), sales: uuid.NewString(), hub: newFakeHub(t)}
	f.exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'HubSpot Import Test')`, f.org, "hs-"+f.org[:8])
	f.exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Alice')`, f.alice, f.org)
	f.exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Rep')`, f.rep, f.org)
	f.exec(`INSERT INTO teams (id, org_id, name) VALUES ($1,$2,'Sales')`, f.sales, f.org)
	f.exec(`INSERT INTO team_members (team_id, principal_id) VALUES ($1,$2)`, f.sales, f.rep)
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`SELECT audit_purge_org($1)`,
			`DELETE FROM crm_activities WHERE org_id=$1`,
			`DELETE FROM deals WHERE org_id=$1`,
			`DELETE FROM contacts WHERE org_id=$1`,
			`DELETE FROM companies WHERE org_id=$1`,
			`DELETE FROM external_refs WHERE org_id=$1`,
			`DELETE FROM hubspot_import_records WHERE org_id=$1`,
			`DELETE FROM hubspot_import_state WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM teams WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	if f.actor, err = az.SubjectsFor(ctx, f.org, f.alice); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fx) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pools.Web.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

func (f *fx) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pools.Web.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (f *fx) str(sql string, args ...any) string {
	f.t.Helper()
	var s string
	if err := f.pools.Web.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func (f *fx) run(apply bool, opt Options) *Report {
	f.t.Helper()
	opt.Apply = apply
	if opt.Since.IsZero() {
		opt.Since = t0
	}
	opt.OwnerEmails = map[string]string{"77": "rep@example.com"}
	rep, err := New(f.pools.Web, f.svc, f.hub.client(), f.org, f.actor, []Person{{PrincipalID: f.rep, Email: "rep@example.com"}}, opt).
		Run(context.Background())
	if err != nil {
		f.t.Fatalf("run (apply=%v): %v", apply, err)
	}
	return rep
}

func (f *fx) company(name, domain string) string {
	f.t.Helper()
	c, err := f.svc.CreateCompany(context.Background(), f.org, f.actor, crm.Company{Name: name, Domain: domain, Kind: crm.KindProspect})
	if err != nil {
		f.t.Fatal(err)
	}
	return c.ID
}

func (f *fx) legacy(object, hid, etype, id string) {
	f.exec(`INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id) VALUES ($1,$2,$3,$4,$5)`,
		f.org, LegacySource, legacyKey(object, hid), etype, id)
}

func summarize(r *Report) string {
	s := ""
	for _, o := range Order {
		if c, ok := r.Objects[o]; ok {
			s += fmt.Sprintf("%s[c%d l%d u%d s%d k%d x%d f%d] ", o, c.Create, c.Link, c.Update, c.Same, c.Skip, c.Conflicts, c.Failed)
		}
	}
	return s
}

func TestDeltaImportEndToEnd(t *testing.T) {
	f := setupFx(t)
	ctx := context.Background()
	baseline := t0 // the first import finished at t0
	later := t0.Add(24 * time.Hour)

	// What Hearth already holds from the first import.
	acme := f.company("Acme Legacy", "acme.example") // untouched since the first import
	f.exec(`UPDATE companies SET updated_at=$2 WHERE id=$1`, acme, t0.Add(-time.Hour))
	f.legacy(Companies, "1001", "company", acme)
	edited := f.company("Edited Here", "edited.example") // a person has edited it since
	f.legacy(Companies, "1002", "company", edited)
	handmade := f.company("Handmade Co", "handmade.example") // made in Hearth, never imported
	pat, err := f.svc.CreateContact(ctx, f.org, f.actor, crm.Contact{Name: "Pat", Email: "pat@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	f.legacy(Contacts, "2001", "contact", pat.ID)
	f.exec(`UPDATE contacts SET updated_at=$2 WHERE id=$1`, pat.ID, t0.Add(-time.Hour))
	loose, err := f.svc.CreateContact(ctx, f.org, f.actor, crm.Contact{Name: "Loose", Email: "loose@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	deal, err := f.svc.CreateDeal(ctx, f.org, f.actor, crm.Deal{Name: "Old deal", Stage: "new", CompanyID: &acme})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE deals SET updated_at=$2 WHERE id=$1`, deal.ID, t0.Add(-time.Hour))
	f.legacy(Deals, "3001", "deal", deal.ID)
	oldNote := f.company("Note holder", "noteholder.example")
	f.legacy(Notes, "4002", "activity", oldNote) // any entity id: it only has to be known

	h := f.hub
	h.stages = map[string]string{"s1": "Proposal Prepared", "s2": "Closed - Won", "s3": "Brand New Stage"}
	h.add(Companies, "1001", later, map[string]string{"name": "Acme Legacy Inc", "domain": "acme.example", "city": "Dayton"})
	h.add(Companies, "1002", later, map[string]string{"name": "Someone Else's Name", "domain": "edited.example", "city": "Toledo"})
	h.add(Companies, "1003", later, map[string]string{"name": "New Co", "domain": "new.example", "state": "OH"})
	h.add(Companies, "1004", later, map[string]string{"name": "Handmade Company", "domain": "handmade.example", "city": "Akron"})
	h.add(Companies, "1005", later, map[string]string{"name": "New Co East", "domain": "new.example"})
	h.add(Companies, "1099", t0.Add(-48*time.Hour), map[string]string{"name": "Quiet Co", "domain": "quiet.example"})
	h.add(Contacts, "2001", later, map[string]string{"firstname": "Pat", "email": "pat@example.com", "jobtitle": "Engineer"})
	h.add(Contacts, "2002", later, map[string]string{"firstname": "Newly", "lastname": "Made", "email": "new@example.com", "hubspot_owner_id": "77"})
	h.add(Contacts, "2003", later, map[string]string{"firstname": "Loose", "email": "loose@example.com", "phone": "216-555-0142"})
	h.add(Contacts, "2004", later, map[string]string{"firstname": "Quiet", "email": "quiet@example.com"})
	h.linkPrimary(Contacts, Companies, "2002", "1003", "1099") // 1099 is also associated, but 1003 is its company
	h.link(Contacts, Companies, "2004", "1099")
	h.add(Deals, "3001", later, map[string]string{"dealname": "Old deal", "dealstage": "s1", "amount": "2500.5"})
	h.add(Deals, "3002", later, map[string]string{"dealname": "Won deal", "dealstage": "s2", "amount": "1500", "closedate": "2026-10-01T00:00:00.000Z"})
	h.add(Deals, "3003", later, map[string]string{"dealname": "Odd stage", "dealstage": "s3"})
	h.link(Deals, Companies, "3002", "1003")
	h.link(Deals, Contacts, "3002", "2002")
	h.add(Notes, "4001", later, map[string]string{"hs_note_body": "<p>Met on site.</p>", "hs_timestamp": "2026-10-02T15:00:00.000Z"})
	h.add(Notes, "4002", later, map[string]string{"hs_note_body": "already here"})
	h.add(Notes, "4003", later, map[string]string{"hs_note_body": "about nothing"})
	h.link(Notes, Companies, "4001", "1003")
	h.add(Calls, "5001", later, map[string]string{"hs_call_title": "Intro", "hs_call_direction": "OUTBOUND", "hs_call_duration": "60000"})
	h.link(Calls, Contacts, "5001", "2002")

	opt := Options{Baseline: baseline}
	companiesBefore := f.count(`SELECT count(*) FROM companies WHERE org_id=$1`, f.org)

	// A dry run says what would happen and changes nothing.
	dry := f.run(false, opt)
	if n := f.count(`SELECT count(*) FROM companies WHERE org_id=$1`, f.org); n != companiesBefore {
		t.Fatalf("a dry run changed the company count from %d to %d", companiesBefore, n)
	}
	if n := f.count(`SELECT count(*) FROM hubspot_import_state WHERE org_id=$1`, f.org); n != 0 {
		t.Fatalf("a dry run moved a high-water mark")
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE org_id=$1 AND action LIKE 'crm.hubspot_import.%'`, f.org); n != 0 {
		t.Fatalf("a dry run wrote %d audit rows", n)
	}

	applied := f.run(true, opt)
	if summarize(dry) != summarize(applied) {
		t.Errorf("the dry run did not predict the apply:\n dry:     %s\n applied: %s", summarize(dry), summarize(applied))
	}
	if applied.Failed() {
		t.Fatalf("failures: %v", applied.Problems)
	}

	// Companies: 1001 updated, 1002 conflict, 1003 and 1005 and the pulled 1099 created, 1004 linked.
	c := applied.Objects[Companies]
	if c.Create != 3 || c.Link != 1 || c.Update != 2 || c.Conflicts != 2 || c.Pulled != 1 {
		t.Errorf("companies: %+v", *c)
	}
	if got := f.str(`SELECT name FROM companies WHERE id=$1`, acme); got != "Acme Legacy Inc" {
		t.Errorf("the untouched legacy company was not brought up to date: %q", got)
	}
	if got := f.str(`SELECT address_city FROM companies WHERE id=$1`, acme); got != "Dayton" {
		t.Errorf("city = %q", got)
	}
	if got := f.str(`SELECT name FROM companies WHERE id=$1`, edited); got != "Edited Here" {
		t.Errorf("a person's edit was overwritten: %q", got)
	}
	if got := f.str(`SELECT address_city FROM companies WHERE id=$1`, edited); got != "Toledo" {
		t.Errorf("an empty field was not filled: %q", got)
	}
	// Both conflicts are a name: one a person edited, one on a company made here that the import only linked.
	if applied.ConflictTotal != 2 || applied.Conflicts[0].Field != "name" || applied.Conflicts[0].Hearth != "Edited Here" ||
		applied.Conflicts[1].Hearth != "Handmade Co" {
		t.Errorf("conflicts = %+v", applied.Conflicts)
	}
	if got := f.str(`SELECT address_city FROM companies WHERE id=$1`, handmade); got != "Akron" {
		t.Errorf("the hand made company was not linked and filled: %q", got)
	}
	if got := f.str(`SELECT name FROM companies WHERE id=$1`, handmade); got != "Handmade Co" {
		t.Errorf("the hand made company's name was overwritten: %q", got)
	}
	if n := f.count(`SELECT count(*) FROM companies WHERE org_id=$1 AND lower(domain)='new.example'`, f.org); n != 1 {
		t.Errorf("%d companies hold new.example, want exactly one", n)
	}
	if n := f.count(`SELECT count(*) FROM companies WHERE org_id=$1 AND name='New Co East' AND domain='' AND website='new.example'`, f.org); n != 1 {
		t.Errorf("the second company sharing a web address should keep it as its website only")
	}
	if n := f.count(`SELECT count(*) FROM companies WHERE org_id=$1`, f.org); n != companiesBefore+3 {
		t.Errorf("company count %d, want %d", n, companiesBefore+3)
	}
	// New records are shared with the sales team and audited.
	if n := f.count(`SELECT count(*) FROM grants g JOIN companies c ON c.id=g.entity_id WHERE g.org_id=$1 AND g.entity_type='company' AND g.subject_key LIKE '%'||$2::text||'%' AND c.name='New Co'`, f.org, f.sales); n != 1 {
		t.Errorf("the new company is not shared with Sales")
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE org_id=$1 AND action='crm.hubspot_import.create'`, f.org); n < 8 {
		t.Errorf("only %d create audit rows", n)
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE org_id=$1 AND action='crm.hubspot_import.update' AND entity_id=$2`, f.org, acme); n != 1 {
		t.Errorf("the update of the legacy company left %d audit rows", n)
	}

	if fc := applied.Fields[Companies]["name"]; fc == nil || fc.Replace != 1 || fc.Fill != 0 {
		t.Errorf("name writes = %+v, want one replacement (the untouched legacy company)", fc)
	}
	if fc := applied.Fields[Companies]["city"]; fc == nil || fc.Fill != 3 || fc.Replace != 0 {
		t.Errorf("city writes = %+v, want three fills (two companies and the linked one)", fc)
	}

	// Contacts: 1 updated (title), 2 created (one pulled company's), 1 linked by email.
	k := applied.Objects[Contacts]
	if k.Create != 2 || k.Link != 1 || k.Update != 1 {
		t.Errorf("contacts: %+v", *k)
	}
	if got := f.str(`SELECT title FROM contacts WHERE id=$1`, pat.ID); got != "Engineer" {
		t.Errorf("title = %q", got)
	}
	if got := f.str(`SELECT phone FROM contacts WHERE id=$1`, loose.ID); got != "216-555-0142" {
		t.Errorf("a linked contact's empty phone was not filled: %q", got)
	}
	if n := f.count(`SELECT count(*) FROM contacts WHERE org_id=$1 AND lower(email)='loose@example.com'`, f.org); n != 1 {
		t.Errorf("the contact with an existing email was duplicated")
	}
	if got := f.str(`SELECT coalesce(owner_id::text,'') FROM contacts WHERE org_id=$1 AND email='new@example.com'`, f.org); got != f.rep {
		t.Errorf("the owner mapped from the owner file is %q, want %q", got, f.rep)
	}
	if n := f.count(`SELECT count(*) FROM contacts k JOIN companies c ON c.id=k.company_id WHERE k.org_id=$1 AND k.email='new@example.com' AND c.name='New Co'`, f.org); n != 1 {
		t.Errorf("the new contact is not on its company")
	}

	// Deals.
	d := applied.Objects[Deals]
	if d.Create != 2 || d.Update != 1 {
		t.Errorf("deals: %+v", *d)
	}
	if got := f.str(`SELECT stage FROM deals WHERE id=$1`, deal.ID); got != "proposal" {
		t.Errorf("stage = %q", got)
	}
	if n := f.count(`SELECT count(*) FROM deals WHERE org_id=$1 AND name='Won deal' AND stage='won' AND amount_cents=150000 AND close_date='2026-10-01'`, f.org); n != 1 {
		t.Errorf("the new deal was not made with its stage, amount and close date")
	}
	if applied.UnknownStages["Brand New Stage"] != 1 {
		t.Errorf("unknown stages = %v", applied.UnknownStages)
	}
	if n := f.count(`SELECT count(*) FROM company_type_links l JOIN company_types t ON t.id=l.type_id JOIN companies c ON c.id=l.company_id WHERE c.org_id=$1 AND c.name='New Co' AND t.key='client'`, f.org); n != 1 {
		t.Errorf("a company this run made, with a won deal, is not a client")
	}

	// Notes and calls.
	n := applied.Objects[Notes]
	if n.Create != 1 || n.Same != 1 || n.Skip != 1 {
		t.Errorf("notes: %+v", *n)
	}
	if got := f.count(`SELECT count(*) FROM crm_activities WHERE org_id=$1 AND kind='note' AND body='Met on site.'`, f.org); got != 1 {
		t.Errorf("the note was not made from the HubSpot text")
	}
	cl := applied.Objects[Calls]
	if cl.Create != 1 {
		t.Errorf("calls: %+v", *cl)
	}
	if got := f.count(`SELECT count(*) FROM crm_activities WHERE org_id=$1 AND kind='call' AND direction='outbound' AND duration_ms=60000`, f.org); got != 1 {
		t.Errorf("the call was not made")
	}
	if _, ok := applied.NotCarried["meetings"]; !ok {
		t.Errorf("meetings and tasks changed in HubSpot are not accounted for")
	}

	// High-water marks are remembered, and a second run reads only what changed after them and does nothing.
	for _, o := range Order {
		var hw time.Time
		if err := f.pools.Web.QueryRow(ctx, `SELECT high_water FROM hubspot_import_state WHERE org_id=$1 AND object=$2`, f.org, o).Scan(&hw); err != nil {
			t.Fatalf("no high-water mark for %s: %v", o, err)
		}
		if !hw.Equal(later) {
			t.Errorf("%s high-water %s, want %s", o, hw, later)
		}
	}
	again := f.run(true, Options{Baseline: baseline}) // no -since: the marks decide
	for _, o := range Order {
		c := again.Objects[o]
		if c.Create+c.Link+c.Update != 0 {
			t.Errorf("a second run changed %s: %+v", o, *c)
		}
	}
	if s := f.hub.searches(Companies); !s[len(s)-1].Equal(later) {
		t.Errorf("the second run asked HubSpot for changes since %s, want its high-water mark %s", s[len(s)-1], later)
	}
	// The mark is inclusive, so a record changed at exactly that instant is read again and its standing conflicts show again,
	// but a company that shares a web address with another is not a conflict.
	for _, c := range again.Conflicts {
		if c.Field == "domain" {
			t.Errorf("a shared web address was reported as a conflict: %+v", c)
		}
	}

	// Somebody edits a company here; HubSpot then changes both that field and another one.
	var newCo string
	if err := f.pools.Web.QueryRow(ctx, `SELECT id::text FROM companies WHERE org_id=$1 AND name='New Co'`, f.org).Scan(&newCo); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.UpdateCompany(ctx, f.org, f.actor, crm.Company{ID: newCo, Name: "Renamed By Sales", Domain: "new.example"}); err != nil {
		t.Fatal(err)
	}
	h.add(Companies, "1003", later.Add(time.Hour), map[string]string{"name": "New Co Again", "domain": "new.example", "state": "MI"})
	third := f.run(true, Options{Baseline: baseline})
	if got := f.str(`SELECT name FROM companies WHERE id=$1`, newCo); got != "Renamed By Sales" {
		t.Errorf("the import overwrote a rename made here: %q", got)
	}
	if got := f.str(`SELECT address_state FROM companies WHERE id=$1`, newCo); got != "MI" {
		t.Errorf("a field nobody edited here was not brought up to date: %q", got)
	}
	if third.ConflictTotal < 1 {
		t.Errorf("the rename was not reported as a conflict")
	}

	// A record deleted here stays deleted.
	if err := f.svc.DeleteCompany(ctx, f.org, f.actor, acme); err != nil {
		t.Fatal(err)
	}
	h.add(Companies, "1001", later.Add(2*time.Hour), map[string]string{"name": "Acme Back Again", "domain": "acme.example"})
	fourth := f.run(true, Options{Baseline: baseline})
	if got := f.count(`SELECT count(*) FROM companies WHERE id=$1 AND deleted_at IS NOT NULL AND name='Acme Legacy Inc'`, acme); got != 1 {
		t.Errorf("a company deleted in Hearth was revived or changed")
	}
	if fourth.Objects[Companies].SkipWhy["deleted in Hearth, left deleted"] != 1 {
		t.Errorf("skips = %v", fourth.Objects[Companies].SkipWhy)
	}
	_ = oldNote

	// -force-since reads from the date given even where a mark exists, and never moves the mark back.
	f.run(false, Options{Baseline: baseline, Since: t0, ForceSince: true, Only: []string{Companies}})
	if s := f.hub.searches(Companies); !s[len(s)-1].Equal(t0) {
		t.Errorf("a forced run asked for changes since %s, want %s", s[len(s)-1], t0)
	}
}

func TestFirstRunNeedsToKnowWhereToStart(t *testing.T) {
	f := setupFx(t)
	_, err := New(f.pools.Web, f.svc, f.hub.client(), f.org, f.actor, nil, Options{}).Run(context.Background())
	if err == nil {
		t.Fatal("a first run with no -since and no high-water mark must refuse, not guess where the last import ended")
	}
}

func TestApplyRefusesWhileAnotherImportRuns(t *testing.T) {
	f := setupFx(t)
	ctx := context.Background()
	h := fnvLock(f.org)
	conn, err := f.pools.Web.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, h); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, h) }()
	if _, err := New(f.pools.Web, f.svc, f.hub.client(), f.org, f.actor, nil, Options{Since: t0}).Run(ctx); err == nil {
		t.Fatal("two imports at once would each decide a record is new")
	}
}
