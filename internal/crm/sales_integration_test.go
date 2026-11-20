package crm_test

// The sales side against a real Postgres: owners, the pipeline, the timeline,
// and the paged lists.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/migrations"
)

// salesTeam makes the Sales team with both test people in it, which is what
// shares a deal beyond its creator.
func (f *fx) salesTeam(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	id := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx, `INSERT INTO teams (id, org_id, name) VALUES ($1,$2,'Sales')`, id, f.org); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{f.alice, f.bob} {
		if _, err := f.pools.Web.Exec(ctx, `INSERT INTO team_members (team_id, principal_id) VALUES ($1,$2)`, id, p); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM teams WHERE org_id=$1`, f.org) })
}

func (f *fx) salesCleanup(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM crm_activities WHERE org_id=$1`,
			`DELETE FROM deals WHERE org_id=$1`,
		} {
			_, _ = f.pools.Web.Exec(ctx, q, f.org)
		}
	})
}

func TestCompaniesPageFiltersSortsPagesAndFindsOwners(t *testing.T) {
	f := setup(t)
	f.salesCleanup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	var ids []string
	for i := 0; i < 7; i++ {
		c, err := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{
			Name: fmt.Sprintf("Firm %02d", i), Domain: fmt.Sprintf("firm%d.example", i),
			Kind: crm.KindProspect,
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		ids = append(ids, c.ID)
	}
	// Two of them belong to Bob.
	for _, id := range ids[:2] {
		if err := f.svc.SetCompanyDetails(ctx, f.org, alice, id, crm.Details{
			OwnerID: f.bob, Website: "https://x.example", City: "Dayton", State: "OH",
		}); err != nil {
			t.Fatalf("set details: %v", err)
		}
	}

	rows, total, err := f.svc.CompaniesPage(ctx, f.org, alice, crm.ListQuery{Limit: 3})
	if err != nil || total != 7 || len(rows) != 3 {
		t.Fatalf("page 1: total=%d rows=%d err=%v, want 7 total and 3 rows", total, len(rows), err)
	}
	rows2, _, _ := f.svc.CompaniesPage(ctx, f.org, alice, crm.ListQuery{Limit: 3, Offset: 3})
	if len(rows2) != 3 || rows2[0].ID == rows[0].ID {
		t.Errorf("page 2 repeated page 1 or came back short: %d rows", len(rows2))
	}

	mine, total, _ := f.svc.CompaniesPage(ctx, f.org, alice, crm.ListQuery{Owner: f.bob})
	if total != 2 || len(mine) != 2 || mine[0].OwnerName != "Bob" || mine[0].City != "Dayton" {
		t.Errorf("owner filter: total=%d rows=%v", total, mine)
	}
	_, none, _ := f.svc.CompaniesPage(ctx, f.org, alice, crm.ListQuery{Owner: "none"})
	if none != 5 {
		t.Errorf("unowned = %d, want 5", none)
	}

	desc, _, _ := f.svc.CompaniesPage(ctx, f.org, alice, crm.ListQuery{Sort: "name", Desc: true, Limit: 1})
	if desc[0].Name != "Firm 06" {
		t.Errorf("sort name desc first = %q, want Firm 06", desc[0].Name)
	}
	// A sort that is not on the whitelist falls back rather than reaching SQL.
	if _, _, err := f.svc.CompaniesPage(ctx, f.org, alice,
		crm.ListQuery{Sort: "name; DROP TABLE companies"}); err != nil {
		t.Errorf("an unknown sort should fall back, got %v", err)
	}

	// An owner who is not a person here is refused, not stored.
	err = f.svc.SetCompanyDetails(ctx, f.org, alice, ids[2], crm.Details{
		OwnerID: "00000000-0000-0000-0000-000000000001"})
	if !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("a stranger as owner: got %v, want ErrInvalid", err)
	}
	// And the owner can reach what they own even when nobody shared it.
	bob := f.subj(t, f.bob)
	if _, err := f.svc.GetCompany(ctx, f.org, bob, ids[0]); err != nil {
		t.Errorf("owner cannot open their own company: %v", err)
	}
}

func TestContactDetailsKeepOwnerAndExtras(t *testing.T) {
	f := setup(t)
	f.salesCleanup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	c, err := f.svc.CreateContact(ctx, f.org, alice, crm.Contact{Name: "Dana", Email: "dana@x.example"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetContactDetails(ctx, f.org, alice, c.ID, crm.Details{
		OwnerID: f.bob, LinkedIn: "https://linkedin.com/in/dana",
		ExtraEmails: []string{"d@y.example"}, ExtraPhones: []string{"+1 555 0100"},
	}); err != nil {
		t.Fatal(err)
	}
	d, err := f.svc.ContactDetails(ctx, f.org, alice, c.ID)
	if err != nil || d.OwnerName != "Bob" || len(d.ExtraEmails) != 1 || d.ExtraPhones[0] != "+1 555 0100" {
		t.Errorf("details = %+v err=%v", d, err)
	}
	rows, total, _ := f.svc.ContactsPage(ctx, f.org, alice, crm.ListQuery{Owner: f.bob})
	if total != 1 || rows[0].LinkedIn == "" {
		t.Errorf("contacts by owner: total=%d rows=%v", total, rows)
	}
}

func TestAPipelineOfDeals(t *testing.T) {
	f := setup(t)
	f.salesCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)

	co, _ := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "Acme", Domain: "acme.example"})
	closes := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	mk := func(name string, cents int64) *crm.Deal {
		d, err := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{
			Name: name, AmountCents: cents, CompanyID: &co.ID, OwnerID: &f.bob, CloseDate: &closes,
		})
		if err != nil {
			t.Fatalf("create deal: %v", err)
		}
		return d
	}
	a, b, c := mk("A", 100000), mk("B", 250050), mk("C", 0)
	if a.Stage != crm.StageNew {
		t.Fatalf("a new deal starts in %q", a.Stage)
	}

	// Shared with the organisation: Bob sees Alice's deal without being added.
	got, err := f.svc.GetDeal(ctx, f.org, bob, a.ID)
	if err != nil || got.CompanyName != "Acme" || got.OwnerName != "Bob" || got.Amount() != "$1,000" {
		t.Fatalf("bob's view = %+v err=%v", got, err)
	}
	if b.Amount() != "$2,500.50" {
		t.Errorf("amount = %s", b.Amount())
	}

	// Drag C above A, in the same column, then B into Meeting.
	if err := f.svc.MoveDeal(ctx, f.org, alice, c.ID, crm.StageNew, &a.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.MoveDeal(ctx, f.org, alice, b.ID, crm.StageMeeting, nil); err != nil {
		t.Fatal(err)
	}
	col, _, _ := f.svc.Deals(ctx, f.org, alice, crm.ListQuery{Stage: crm.StageNew, Sort: "position"})
	if len(col) != 2 || col[0].ID != c.ID || col[1].ID != a.ID {
		t.Errorf("column order after the drag: %v", names(col))
	}
	meet, _, _ := f.svc.Deals(ctx, f.org, alice, crm.ListQuery{Stage: crm.StageMeeting})
	if len(meet) != 1 || meet[0].ID != b.ID {
		t.Errorf("meeting column: %v", names(meet))
	}
	if err := f.svc.MoveDeal(ctx, f.org, alice, a.ID, "nonsense", nil); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("moving to a stage that is not one: %v", err)
	}

	on, _ := f.svc.DealsFor(ctx, f.org, alice, core.Ref{Type: core.EntityCompany, ID: co.ID})
	if len(on) != 3 {
		t.Errorf("deals on the company = %d, want 3", len(on))
	}
	byAmt, _, _ := f.svc.Deals(ctx, f.org, alice, crm.ListQuery{Sort: "amount", Desc: true})
	if byAmt[0].ID != b.ID {
		t.Errorf("sorted by amount: %v", names(byAmt))
	}
	q, total, _ := f.svc.Deals(ctx, f.org, alice, crm.ListQuery{Search: "acme"})
	if total != 3 || len(q) != 3 {
		t.Errorf("search by company name found %d", total)
	}

	a.Name = "A renamed"
	a.Stage = crm.StageWon
	if err := f.svc.UpdateDeal(ctx, f.org, alice, *a); err != nil {
		t.Fatal(err)
	}
	won, _ := f.svc.GetDeal(ctx, f.org, alice, a.ID)
	if won.Stage != crm.StageWon || won.Name != "A renamed" {
		t.Errorf("after update: %+v", won)
	}

	// Only an admin removes one. Bob has edit through the org grant on a deal
	// he does not own, and an owner is an admin of what they own.
	unowned, _ := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Unowned"})
	if err := f.svc.DeleteDeal(ctx, f.org, bob, unowned.ID); err == nil {
		t.Error("bob deleted a deal he only had edit on")
	}
	if err := f.svc.DeleteDeal(ctx, f.org, alice, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.GetDeal(ctx, f.org, alice, a.ID); !errors.Is(err, crm.ErrNotFound) {
		t.Errorf("a deleted deal is still readable: %v", err)
	}
}

func names(ds []*crm.Deal) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return out
}

func TestTheTimelineShowsAnActivityOnEveryRecordItIsAbout(t *testing.T) {
	f := setup(t)
	f.salesCleanup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	co, _ := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "Acme", Domain: "acme.example"})
	p, _ := f.svc.CreateContact(ctx, f.org, alice, crm.Contact{Name: "Dana", CompanyID: &co.ID})
	d, _ := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Rewind line", CompanyID: &co.ID})

	dur := int64(90000)
	call, err := f.svc.AddActivity(ctx, f.org, alice, crm.Activity{
		Kind: crm.ActCall, Title: "Call with Dana", Body: "left a message",
		Direction: crm.Outbound, Disposition: "Left voicemail", DurationMs: &dur,
		OccurredAt: time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC),
		Targets:    []core.Ref{{Type: core.EntityContact, ID: p.ID}, {Type: core.EntityDeal, ID: d.ID}},
	})
	if err != nil {
		t.Fatalf("add call: %v", err)
	}
	if _, err := f.svc.AddActivity(ctx, f.org, alice, crm.Activity{
		Kind: crm.ActNote, Title: "Needs a quote", Targets: []core.Ref{{Type: core.EntityDeal, ID: d.ID}},
		OccurredAt: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("add note: %v", err)
	}

	onDeal, _ := f.svc.ActivitiesFor(ctx, f.org, alice, d.Ref())
	if len(onDeal) != 2 || onDeal[0].Kind != crm.ActNote || onDeal[1].Direction != crm.Outbound {
		t.Fatalf("deal timeline = %+v", onDeal)
	}
	onPerson, _ := f.svc.ActivitiesFor(ctx, f.org, alice, p.Ref())
	if len(onPerson) != 1 || onPerson[0].ID != call.ID || onPerson[0].ByName != "Alice" ||
		onPerson[0].DurationMs == nil || *onPerson[0].DurationMs != 90000 {
		t.Errorf("contact timeline = %+v", onPerson)
	}
	onCompany, _ := f.svc.ActivitiesFor(ctx, f.org, alice, co.Ref())
	if len(onCompany) != 0 {
		t.Errorf("an activity that does not name the company showed on it")
	}

	if _, err := f.svc.AddActivity(ctx, f.org, alice, crm.Activity{Kind: crm.ActNote, Body: "x"}); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("an activity about nothing: %v", err)
	}
	// Bob has no grant on a private company, so cannot write on it.
	private, _ := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "Private", Domain: "p.example"})
	bob := f.subj(t, f.bob)
	if _, err := f.svc.AddActivity(ctx, f.org, bob, crm.Activity{Kind: crm.ActNote, Body: "x",
		Targets: []core.Ref{private.Ref()}}); err == nil {
		t.Error("bob wrote on a company he cannot see")
	}
	if err := f.svc.DeleteActivity(ctx, f.org, alice, call.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := f.svc.ActivitiesFor(ctx, f.org, alice, p.Ref())
	if len(after) != 0 {
		t.Errorf("a deleted call is still on the timeline")
	}
}

func TestTheUnassignedQueueCanBeClaimedAndHandedOff(t *testing.T) {
	f := setup(t)
	f.salesCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)

	a, _ := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "Orphan A", Domain: "a.example"})
	b, _ := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "Orphan B", Domain: "b.example"})
	d, _ := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Loose deal"})

	counts, err := f.svc.UnassignedCounts(ctx, f.org, alice)
	if err != nil || counts[core.EntityCompany] != 2 || counts[core.EntityDeal] != 1 {
		t.Fatalf("counts = %v err=%v", counts, err)
	}
	// Alice claims A; B is handed to Bob; the deal is claimed by Bob, who can
	// edit it through the shared grant even though he did not make it.
	if err := f.svc.SetOwner(ctx, f.org, alice, a.Ref(), f.alice); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetOwner(ctx, f.org, alice, b.Ref(), f.bob); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetOwner(ctx, f.org, bob, d.Ref(), f.bob); err != nil {
		t.Fatal(err)
	}
	rows, total, _ := f.svc.Unassigned(ctx, f.org, alice, core.EntityCompany, crm.ListQuery{})
	if total != 0 || len(rows) != 0 {
		t.Errorf("claimed companies are still in the queue: %d", total)
	}
	// Ownership is a label: Alice still reads the company Bob owns.
	if _, err := f.svc.GetCompany(ctx, f.org, alice, b.ID); err != nil {
		t.Errorf("an owner other than me hides a record from me: %v", err)
	}
	// Handing back to nobody returns it to the queue.
	if err := f.svc.SetOwner(ctx, f.org, alice, b.Ref(), ""); err != nil {
		t.Fatal(err)
	}
	rows, total, _ = f.svc.Unassigned(ctx, f.org, alice, core.EntityCompany, crm.ListQuery{})
	if total != 1 || rows[0].Name != "Orphan B" {
		t.Errorf("queue after hand back = %v", rows)
	}
	if err := f.svc.SetOwner(ctx, f.org, alice, a.Ref(), "00000000-0000-0000-0000-000000000009"); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("handing a record to a stranger: %v", err)
	}
}

// Without a Sales team nothing is shared: a deal is as private as any other
// Hearth record, so adding the sales side widens nobody's view by itself.
func TestWithNoSalesTeamADealStaysWithItsCreator(t *testing.T) {
	f := setup(t)
	f.salesCleanup(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)
	d, _ := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Mine"})
	if _, err := f.svc.GetDeal(ctx, f.org, bob, d.ID); !errors.Is(err, crm.ErrNotFound) {
		t.Errorf("bob read a deal nobody shared with him: %v", err)
	}
}

func TestACompanyCanBeSeveralThingsAndCarryTags(t *testing.T) {
	f := setup(t)
	f.salesCleanup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	types, err := f.svc.Types(ctx, f.org, false)
	if err != nil || len(types) != 6 || types[0].Key != "client" || types[0].ERPRole != "customer" {
		t.Fatalf("starting list = %+v err=%v", types, err)
	}
	a, _ := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "Both Ways", Domain: "both.example"})
	b, _ := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "Just Vendor", Domain: "jv.example"})
	_, _ = f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "Nothing Yet", Domain: "ny.example"})

	both, vendor := []string{"client", "vendor"}, []string{"vendor"}
	tags := []string{"Dayton", " dayton ", "rewind", ""}
	if err := f.svc.SetCompanyLabels(ctx, f.org, alice, a.ID, &both, &tags); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetCompanyLabels(ctx, f.org, alice, b.ID, &vendor, nil); err != nil {
		t.Fatal(err)
	}
	l, _ := f.svc.CompanyLabels(ctx, f.org, alice, a.ID)
	if len(l.Types) != 2 || l.Keys()[0] != "client" || len(l.Tags) != 2 {
		t.Errorf("labels = %+v, want client and vendor, and Dayton and rewind once each", l)
	}

	count := func(q crm.ListQuery) int {
		_, n, err := f.svc.CompaniesPage(ctx, f.org, alice, q)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(crm.ListQuery{Type: "vendor"}); n != 2 {
		t.Errorf("vendors = %d, want both of the companies that are one", n)
	}
	if n := count(crm.ListQuery{Type: "client"}); n != 1 {
		t.Errorf("clients = %d", n)
	}
	if n := count(crm.ListQuery{Type: "none"}); n != 1 {
		t.Errorf("companies with no type = %d, want the one nobody has said anything about", n)
	}
	if n := count(crm.ListQuery{Tag: "DAYTON"}); n != 1 {
		t.Errorf("tag filter is case sensitive: %d", n)
	}
	rows, _, _ := f.svc.CompaniesPage(ctx, f.org, alice, crm.ListQuery{Type: "client"})
	if len(rows[0].TypeLabels) != 2 || len(rows[0].Tags) != 2 {
		t.Errorf("a list row carries %v and %v", rows[0].TypeLabels, rows[0].Tags)
	}

	bad := []string{"wizard"}
	if err := f.svc.SetCompanyLabels(ctx, f.org, alice, a.ID, &bad, nil); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("a type that is not on the list: %v", err)
	}
	// Admins add and archive. An archived type stays on what has it and cannot be added.
	nt, err := f.svc.SaveType(ctx, f.org, crm.CompanyType{Label: "Distributor", ERPRole: "supplier"})
	if err != nil || nt.Key != "distributor" {
		t.Fatalf("new type = %+v err=%v", nt, err)
	}
	if _, err := f.svc.SaveType(ctx, f.org, crm.CompanyType{Label: "Distributor"}); !errors.Is(err, crm.ErrDuplicate) {
		t.Errorf("the same type twice: %v", err)
	}
	dist := []string{"distributor"}
	if err := f.svc.SetCompanyLabels(ctx, f.org, alice, b.ID, &dist, nil); err != nil {
		t.Fatal(err)
	}
	nt.Archived = true
	if _, err := f.svc.SaveType(ctx, f.org, *nt); err != nil {
		t.Fatal(err)
	}
	if l, _ := f.svc.CompanyLabels(ctx, f.org, alice, b.ID); len(l.Types) != 1 {
		t.Errorf("archiving a type took it off the company that has it")
	}
	if err := f.svc.SetCompanyLabels(ctx, f.org, alice, a.ID, &dist, nil); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("adding an archived type: %v", err)
	}
	if err := f.svc.SetCompanyLabels(ctx, f.org, alice, b.ID, &dist, nil); err != nil {
		t.Errorf("keeping an archived type on a company that has it: %v", err)
	}
	if k := crm.KeyFor("  Tier 1 / OEM!! "); k != "tier_1_oem" {
		t.Errorf("key = %q", k)
	}
	// Bob has no grant on a private company: he cannot label it.
	bob := f.subj(t, f.bob)
	if err := f.svc.SetCompanyLabels(ctx, f.org, bob, a.ID, &vendor, nil); err == nil {
		t.Errorf("somebody with no grant labelled a company")
	}
}

func TestAListGroupsUnderHeadingsAndCountsTheWholeListNotThePage(t *testing.T) {
	f := setup(t)
	f.salesCleanup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	for i, kind := range []string{crm.KindVendor, crm.KindVendor, crm.KindVendor, crm.KindCustomer, crm.KindCustomer, crm.KindProspect} {
		if _, err := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{
			Name: fmt.Sprintf("Grouped %d", i), Kind: kind,
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	q := crm.ListQuery{Search: "Grouped", Group: "kind", Limit: 4}
	rows, total, err := f.svc.CompaniesPage(ctx, f.org, alice, q)
	if err != nil || total != 6 || len(rows) != 4 {
		t.Fatalf("page of a grouped list: %d rows of %d, %v", len(rows), total, err)
	}
	// rows of one heading sit together, headings in the order the group sorts by
	var seen []string
	for _, r := range rows {
		if h := crm.CompanyGroup(r, "kind"); len(seen) == 0 || seen[len(seen)-1] != h {
			seen = append(seen, h)
		}
	}
	if fmt.Sprint(seen) != "[customer prospect vendor]" {
		t.Fatalf("headings in order, each once on the page, the fourth row being the last of four: %v", seen)
	}
	counts, err := f.svc.GroupCounts(ctx, f.org, alice, "companies", q)
	if err != nil || counts["vendor"] != 3 || counts["customer"] != 2 || counts["prospect"] != 1 {
		t.Fatalf("a heading's total is across the whole list, not the page: %v %v", counts, err)
	}
	// the same filter narrows the totals
	q.Kind = crm.KindVendor
	counts, _ = f.svc.GroupCounts(ctx, f.org, alice, "companies", q)
	if len(counts) != 1 || counts["vendor"] != 3 {
		t.Fatalf("totals follow the filter: %v", counts)
	}
	// a company with no kind has a heading that says so, in the rows and in the totals
	if _, err := f.pools.Web.Exec(ctx, `UPDATE companies SET kind = '' WHERE org_id = $1 AND name = 'Grouped 5'`, f.org); err != nil {
		t.Fatal(err)
	}
	q.Kind = ""
	rows, _, _ = f.svc.CompaniesPage(ctx, f.org, alice, crm.ListQuery{Search: "Grouped", Group: "kind", Limit: 10})
	var empties int
	for _, r := range rows {
		if crm.CompanyGroup(r, "kind") == "No kind" {
			empties++
		}
	}
	counts, _ = f.svc.GroupCounts(ctx, f.org, alice, "companies", crm.ListQuery{Search: "Grouped", Group: "kind"})
	if empties != 1 || counts["No kind"] != 1 || counts[""] != 0 {
		t.Fatalf("a company with no kind falls under No kind: rows %d, totals %v", empties, counts)
	}
	// a grouping the list does not have is no grouping
	if counts, err := f.svc.GroupCounts(ctx, f.org, alice, "companies", crm.ListQuery{Group: "nonsense"}); err != nil || counts != nil {
		t.Fatalf("unknown grouping: %v %v", counts, err)
	}
	// owner headings name people, and nobody for no owner
	ownerRows, _, _ := f.svc.CompaniesPage(ctx, f.org, alice, crm.ListQuery{Search: "Grouped", Group: "owner", Limit: 10})
	if len(ownerRows) != 6 || crm.CompanyGroup(ownerRows[0], "owner") != "Nobody" {
		t.Fatalf("an unowned company falls under Nobody")
	}
}

func TestFavoritesAreAPersonsOwnAndFollowAccess(t *testing.T) {
	f := setup(t)
	f.salesCleanup(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)

	mine, err := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "Favourite Firm", Kind: crm.KindVendor})
	if err != nil {
		t.Fatal(err)
	}
	// Bob has no grant on Alice's private company, so he cannot star it and cannot learn it exists
	if err := f.svc.SetFavorite(ctx, f.org, bob, mine.Ref(), true); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("a record somebody cannot see cannot be starred: %v", err)
	}
	if err := f.svc.SetFavorite(ctx, f.org, alice, mine.Ref(), true); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetFavorite(ctx, f.org, alice, mine.Ref(), true); err != nil {
		t.Fatalf("starring twice is the same star: %v", err)
	}
	if on, _ := f.svc.IsFavorite(ctx, f.org, alice, mine.Ref()); !on {
		t.Fatal("starred")
	}
	if on, _ := f.svc.IsFavorite(ctx, f.org, bob, mine.Ref()); on {
		t.Fatal("a star is one person's own")
	}
	list, err := f.svc.Favorites(ctx, f.org, alice)
	if err != nil || len(list) != 1 || list[0].Ref != mine.Ref() {
		t.Fatalf("favorites: %+v %v", list, err)
	}
	if other, _ := f.svc.Favorites(ctx, f.org, bob); len(other) != 0 {
		t.Fatalf("bob sees nothing of alice's: %+v", other)
	}
	if err := f.svc.SetFavorite(ctx, f.org, alice, core.Ref{Type: core.EntityDocument, ID: mine.ID}, true); !errors.Is(err, crm.ErrInvalid) {
		t.Fatalf("only companies, contacts and deals: %v", err)
	}
	// deleting the record takes it out of the list rather than leaving a name behind
	if err := f.svc.DeleteCompany(ctx, f.org, alice, mine.ID); err != nil {
		t.Fatal(err)
	}
	if gone, _ := f.svc.Favorites(ctx, f.org, alice); len(gone) != 0 {
		t.Fatalf("a deleted record is not listed: %+v", gone)
	}
	if err := f.svc.SetFavorite(ctx, f.org, alice, mine.Ref(), false); err != nil {
		t.Fatalf("unstar: %v", err)
	}
}

func TestUnnamedCompaniesAreNamedByTheirWebAddressAndItCanBeUndone(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	mk := func(name, domain string) string {
		c, err := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "tmp " + uuid.NewString()[:8], Domain: domain, Kind: crm.KindProspect})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.pools.Web.Exec(ctx, `UPDATE companies SET name = $2, notes = $3 WHERE id = $1`, c.ID, name, "kept note"); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	nameless := mk("(unnamed 32872919051)", "narmco-"+uuid.NewString()[:6]+".example")
	noDomain := mk("(unnamed 41643296275)", "")
	real := mk("Baldor Electric", "baldor-"+uuid.NewString()[:6]+".example")

	sqlText, err := migrations.FS.ReadFile("00056_name_unnamed_companies.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, _ := strings.Cut(string(sqlText), "-- +goose Down")
	up = up[strings.Index(up, "-- +goose Up")+len("-- +goose Up"):]
	if _, err := f.pools.Web.Exec(ctx, up); err != nil {
		t.Fatalf("up: %v", err)
	}
	get := func(id string) (name, domain, notes string) {
		if err := f.pools.Web.QueryRow(ctx, `SELECT name, domain, notes FROM companies WHERE id = $1`, id).Scan(&name, &domain, &notes); err != nil {
			t.Fatal(err)
		}
		return
	}
	name, domain, notes := get(nameless)
	if name != domain || !strings.HasPrefix(notes, "kept note\n") || !strings.Contains(notes, `as "(unnamed 32872919051)"`) {
		t.Fatalf("a company with no name is named by its web address and keeps the old text in its notes: %q %q %q", name, domain, notes)
	}
	if n, _, _ := get(noDomain); n != "(unnamed 41643296275)" {
		t.Fatalf("with no web address there is nothing to name it by: %q", n)
	}
	if n, _, nt := get(real); !strings.HasPrefix(n, "Baldor") || nt != "kept note" {
		t.Fatalf("a real name is left alone: %q %q", n, nt)
	}
	// twice is the same as once
	if _, err := f.pools.Web.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	if _, _, nt := get(nameless); strings.Count(nt, "Named from its web address") != 1 {
		t.Fatalf("running it again adds nothing: %q", nt)
	}
	// and it can be undone
	if _, err := f.pools.Web.Exec(ctx, down); err != nil {
		t.Fatalf("down: %v", err)
	}
	name, _, notes = get(nameless)
	if name != "(unnamed 32872919051)" || notes != "kept note" {
		t.Fatalf("undone: %q %q", name, notes)
	}
}
