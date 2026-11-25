package crm_test

// The sales desk against a real Postgres: follow-ups, the forecast, the report, bulk change, the new timeline kinds,
// and who may see any of it.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/docs"
)

func (f *fx) outsider(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := f.pools.Web.Exec(context.Background(),
		`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Outside')`, id, f.org); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fx) superAdmin(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	id := uuid.NewString()
	for _, q := range []string{
		`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Ada Admin')`,
		`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($2,$1,'super_admin',$1)`,
	} {
		if _, err := f.pools.Web.Exec(ctx, q, id, f.org); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM capability_grants WHERE org_id=$1`, f.org)
	})
	return id
}

func (f *fx) deskCleanup(t *testing.T) {
	t.Helper()
	f.salesCleanup(t)
	t.Cleanup(func() {
		_, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM crm_followups WHERE org_id=$1`, f.org)
	})
}

func TestSuperAdminSeesEveryDealAndAnOutsiderSeesNone(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	if _, err := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Shared"}); err != nil {
		t.Fatal(err)
	}
	admin, out := f.subj(t, f.superAdmin(t)), f.subj(t, f.outsider(t))
	if _, total, err := f.svc.Deals(ctx, f.org, admin, crm.ListQuery{}); err != nil || total != 1 {
		t.Errorf("a super admin outside the Sales team sees %d deals (%v), want 1", total, err)
	}
	if st, _ := f.svc.Stats(ctx, f.org, admin); st == nil || st.Deals != 1 {
		t.Errorf("a super admin's stats count %+v, want 1 deal", st)
	}
	if _, total, _ := f.svc.Deals(ctx, f.org, out, crm.ListQuery{}); total != 0 {
		t.Errorf("a person outside sales sees %d deals, want 0", total)
	}
	if has, _ := f.svc.HasSalesAccess(ctx, f.org, out); has {
		t.Error("a person outside sales has sales access")
	}
	if has, _ := f.svc.HasSalesAccess(ctx, f.org, admin); !has {
		t.Error("a super admin has no sales access")
	}
}

func TestTimelineTakesMeetingsAndEmails(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	d, err := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Mailed"})
	if err != nil {
		t.Fatal(err)
	}
	ms := int64(1800000)
	for _, a := range []crm.Activity{
		{Kind: crm.ActMeeting, Title: "Kickoff", DurationMs: &ms, Targets: []core.Ref{d.Ref()}},
		{Kind: crm.ActEmail, Title: "Quote", Body: "attached", Direction: crm.Outbound, Targets: []core.Ref{d.Ref()}},
	} {
		if _, err := f.svc.AddActivity(ctx, f.org, alice, a); err != nil {
			t.Fatalf("%s: %v", a.Kind, err)
		}
	}
	got, _ := f.svc.ActivitiesFor(ctx, f.org, alice, d.Ref())
	if len(got) != 2 {
		t.Fatalf("timeline has %d rows, want 2", len(got))
	}
	if _, err := f.svc.AddActivity(ctx, f.org, alice, crm.Activity{Kind: "carrier pigeon", Body: "x", Targets: []core.Ref{d.Ref()}}); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("an unknown kind: got %v, want ErrInvalid", err)
	}
	if _, err := f.svc.AddActivity(ctx, f.org, alice, crm.Activity{Kind: crm.ActEmail, Body: "x", Direction: "sideways", Targets: []core.Ref{d.Ref()}}); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("an email going sideways: got %v, want ErrInvalid", err)
	}
}

func TestFollowupsAreEveryonesAndCheckedLikeTheRecord(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	alice, bob, out := f.subj(t, f.alice), f.subj(t, f.bob), f.subj(t, f.outsider(t))
	d, err := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Chase"})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().AddDate(0, 0, -2)
	fu, err := f.svc.AddFollowup(ctx, f.org, alice, crm.FollowupParams{Title: "Call back", DueAt: &past, Assignee: f.bob, Target: d.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	// Bob did not make it, and sees it: sales sees all.
	rows, total, err := f.svc.Followups(ctx, f.org, bob, crm.FollowupQuery{})
	if err != nil || total != 1 || rows[0].ID != fu.ID || !rows[0].Overdue || rows[0].AssigneeName != "Bob" || rows[0].TargetName != "Chase" {
		t.Fatalf("bob sees %+v total=%d err=%v", rows, total, err)
	}
	if _, n, _ := f.svc.Followups(ctx, f.org, bob, crm.FollowupQuery{Due: "overdue"}); n != 1 {
		t.Errorf("overdue = %d, want 1", n)
	}
	if _, n, _ := f.svc.Followups(ctx, f.org, bob, crm.FollowupQuery{Assignee: f.alice}); n != 0 {
		t.Errorf("alice's = %d, want 0", n)
	}
	if _, n, _ := f.svc.Followups(ctx, f.org, bob, crm.FollowupQuery{Target: &core.Ref{Type: core.EntityDeal, ID: d.ID}}); n != 1 {
		t.Errorf("about the deal = %d, want 1", n)
	}
	// Outside sales: sees none, cannot add, cannot complete, and is told it is not there.
	if _, n, _ := f.svc.Followups(ctx, f.org, out, crm.FollowupQuery{}); n != 0 {
		t.Errorf("an outsider sees %d follow-ups, want 0", n)
	}
	if _, err := f.svc.AddFollowup(ctx, f.org, out, crm.FollowupParams{Title: "x", Target: d.Ref()}); err == nil {
		t.Error("an outsider added a follow-up")
	}
	if err := f.svc.SetFollowupDone(ctx, f.org, out, fu.ID, true); !errors.Is(err, crm.ErrNotFound) {
		t.Errorf("an outsider completing it: got %v, want ErrNotFound", err)
	}
	// Bad input.
	if _, err := f.svc.AddFollowup(ctx, f.org, alice, crm.FollowupParams{Title: "  ", Target: d.Ref()}); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("an empty title: got %v", err)
	}
	if _, err := f.svc.AddFollowup(ctx, f.org, alice, crm.FollowupParams{Title: "x", Target: core.Ref{Type: core.EntityDocument, ID: d.ID}}); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("a document target: got %v", err)
	}
	if _, err := f.svc.AddFollowup(ctx, f.org, alice, crm.FollowupParams{Title: "x", Assignee: uuid.NewString(), Target: d.Ref()}); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("a stranger assignee: got %v", err)
	}
	// Done, reopened, rescheduled, removed.
	if err := f.svc.SetFollowupDone(ctx, f.org, bob, fu.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, n, _ := f.svc.Followups(ctx, f.org, bob, crm.FollowupQuery{Status: "open"}); n != 0 {
		t.Errorf("open after done = %d, want 0", n)
	}
	if _, n, _ := f.svc.Followups(ctx, f.org, bob, crm.FollowupQuery{Status: "done"}); n != 1 {
		t.Errorf("done = %d, want 1", n)
	}
	next := time.Now().AddDate(0, 0, 3)
	if err := f.svc.SetFollowupDone(ctx, f.org, bob, fu.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RescheduleFollowup(ctx, f.org, bob, fu.ID, &next, f.alice); err != nil {
		t.Fatal(err)
	}
	if _, n, _ := f.svc.Followups(ctx, f.org, bob, crm.FollowupQuery{Due: "week", Assignee: f.alice}); n != 1 {
		t.Errorf("due this week and alice's = %d, want 1", n)
	}
	if err := f.svc.DeleteFollowup(ctx, f.org, out, fu.ID); !errors.Is(err, crm.ErrNotFound) {
		t.Errorf("an outsider deleting it: got %v", err)
	}
	if err := f.svc.DeleteFollowup(ctx, f.org, bob, fu.ID); err != nil {
		t.Fatal(err)
	}
	if _, n, _ := f.svc.Followups(ctx, f.org, bob, crm.FollowupQuery{}); n != 0 {
		t.Errorf("after delete = %d, want 0", n)
	}
}

func TestForecastWeighsEveryOpenDealAndDefaultsToEveryone(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)
	dec := time.Date(2026, 12, 10, 0, 0, 0, 0, time.UTC)
	past := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	mk := func(who core.Subjects, d crm.Deal) *crm.Deal {
		got, err := f.svc.CreateDeal(ctx, f.org, who, d)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	ao, bo := f.alice, f.bob
	mk(alice, crm.Deal{Name: "A proposal", Stage: crm.StageProposal, AmountCents: 100000, CloseDate: &dec, OwnerID: &ao})
	mk(bob, crm.Deal{Name: "B meeting", Stage: crm.StageMeeting, AmountCents: 50000, CloseDate: &dec, OwnerID: &bo})
	mk(bob, crm.Deal{Name: "Late", Stage: crm.StageNew, AmountCents: 10000, CloseDate: &past})
	mk(bob, crm.Deal{Name: "Undated", Stage: crm.StageScreening, AmountCents: 20000})
	won := mk(bob, crm.Deal{Name: "Won one", Stage: crm.StageNew, AmountCents: 70000})
	if err := f.svc.MoveDeal(ctx, f.org, bob, won.ID, crm.StageWon, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	fc, err := f.svc.Forecast(ctx, f.org, bob, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if fc.Count != 4 {
		t.Errorf("open deals = %d, want 4 (everyone's, not just bob's)", fc.Count)
	}
	// 100000*60% + 50000*40% + 10000*10% + 20000*20% = 60000+20000+1000+4000
	if len(fc.Weighted) != 1 || fc.Weighted[0].Cents != 85000 {
		t.Errorf("weighted = %+v, want 85000", fc.Weighted)
	}
	keys := []string{}
	for _, b := range fc.Buckets {
		keys = append(keys, b.Key)
	}
	if len(keys) != 3 || keys[0] != "overdue" || keys[1] != "2026-12" || keys[2] != "none" {
		t.Errorf("buckets = %v, want overdue, 2026-12, none", keys)
	}
	if fc.WonCount != 1 || fc.Won[0].Cents != 70000 {
		t.Errorf("won this month = %d %+v", fc.WonCount, fc.Won)
	}
	mine, _ := f.svc.Forecast(ctx, f.org, bob, f.alice, now)
	if mine.Count != 1 {
		t.Errorf("filtered to alice = %d, want 1", mine.Count)
	}
	if o, _ := f.svc.Forecast(ctx, f.org, f.subj(t, f.outsider(t)), "", now); o.Count != 0 || len(o.Buckets) != 0 {
		t.Errorf("an outsider's forecast has %d deals", o.Count)
	}
}

func TestSalesReportAndLostReason(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)
	ao := f.alice
	open, _ := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Open", Stage: crm.StageMeeting, AmountCents: 40000, OwnerID: &ao})
	w, _ := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Win", AmountCents: 90000, OwnerID: &ao})
	l, _ := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Lose", AmountCents: 30000, OwnerID: &ao})
	_ = open
	if err := f.svc.MoveDeal(ctx, f.org, alice, w.ID, crm.StageWon, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.MoveDeal(ctx, f.org, alice, l.ID, crm.StageLost, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetLostReason(ctx, f.org, bob, l.ID, " went with a competitor "); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.GetDeal(ctx, f.org, bob, l.ID)
	if got.LostReason != "went with a competitor" || got.ClosedAt == nil || got.StageChangedAt.IsZero() {
		t.Errorf("lost deal = %+v", got)
	}
	if err := f.svc.SetLostReason(ctx, f.org, f.subj(t, f.outsider(t)), l.ID, "x"); err == nil {
		t.Error("an outsider set a lost reason")
	}
	if err := f.svc.SetLostReason(ctx, f.org, bob, l.ID, string(make([]byte, 501))); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("a 501 character reason: got %v", err)
	}
	if _, err := f.svc.AddActivity(ctx, f.org, bob, crm.Activity{Kind: crm.ActCall, Body: "rang", Targets: []core.Ref{open.Ref()}}); err != nil {
		t.Fatal(err)
	}
	day := time.Now()
	rep, err := f.svc.SalesReport(ctx, f.org, bob, day.AddDate(0, 0, -1), day, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.OpenCount != 1 || rep.WonCount != 1 || rep.LostCount != 1 || rep.WinRate != 50 {
		t.Errorf("report = open %d won %d lost %d rate %d", rep.OpenCount, rep.WonCount, rep.LostCount, rep.WinRate)
	}
	if len(rep.Won) != 1 || rep.Won[0].Cents != 90000 || len(rep.AvgWon) != 1 || rep.AvgWon[0].Cents != 90000 {
		t.Errorf("won = %+v avg %+v", rep.Won, rep.AvgWon)
	}
	if len(rep.Weighted) != 1 || rep.Weighted[0].Cents != 16000 {
		t.Errorf("weighted = %+v, want 16000", rep.Weighted)
	}
	if len(rep.Owners) != 1 || rep.Owners[0].Name != "Alice" || rep.Owners[0].WonCount != 1 {
		t.Errorf("owners = %+v", rep.Owners)
	}
	if len(rep.Activity) != 1 || rep.Activity[0].Name != "Bob" || rep.Activity[0].Calls != 1 {
		t.Errorf("activity = %+v", rep.Activity)
	}
	if len(rep.Created) != 1 || rep.Created[0].Count != 3 {
		t.Errorf("created = %+v", rep.Created)
	}
	empty, _ := f.svc.SalesReport(ctx, f.org, f.subj(t, f.outsider(t)), day.AddDate(0, 0, -1), day, "")
	if empty.OpenCount != 0 || empty.WonCount != 0 || empty.WinRate != -1 || len(empty.Activity) != 0 {
		t.Errorf("an outsider's report is not empty: %+v", empty)
	}
}

func TestBulkApplyChangesManyAndRefusesTheRest(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	alice, bob, out := f.subj(t, f.alice), f.subj(t, f.bob), f.subj(t, f.outsider(t))
	var ids []string
	for _, n := range []string{"One", "Two", "Three"} {
		d, err := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: n})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
	}
	res, err := f.svc.BulkApply(ctx, f.org, bob, crm.BulkSpec{Type: core.EntityDeal, IDs: append(ids, ids[0], uuid.NewString()), Owner: &f.bob, Stage: crm.StageProposal})
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 3 || len(res.Failed) != 1 {
		t.Errorf("done %d failed %v, want 3 and one stranger", res.Done, res.Failed)
	}
	for _, id := range ids {
		d, _ := f.svc.GetDeal(ctx, f.org, bob, id)
		if d.Stage != crm.StageProposal || d.OwnerID == nil || *d.OwnerID != f.bob {
			t.Errorf("%s = %s owner %v", d.Name, d.Stage, d.OwnerID)
		}
	}
	none := ""
	if res, _ := f.svc.BulkApply(ctx, f.org, bob, crm.BulkSpec{Type: core.EntityDeal, IDs: ids[:1], Owner: &none}); res.Done != 1 {
		t.Errorf("clearing an owner: %+v", res)
	}
	// An outsider changes nothing.
	res, _ = f.svc.BulkApply(ctx, f.org, out, crm.BulkSpec{Type: core.EntityDeal, IDs: ids, Stage: crm.StageLost})
	if res.Done != 0 || len(res.Failed) != 3 {
		t.Errorf("an outsider changed %d, failed %d", res.Done, len(res.Failed))
	}
	// Tags and types go on companies, once.
	c, _ := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "Tagged Co", Kind: crm.KindProspect})
	if err := f.svc.Share(ctx, f.org, alice, core.Ref{Type: core.EntityCompany, ID: c.ID}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if r, err := f.svc.BulkApply(ctx, f.org, bob, crm.BulkSpec{Type: core.EntityCompany, IDs: []string{c.ID}, AddTag: "hot"}); err != nil || r.Done != 1 {
			t.Fatalf("tag: %+v %v", r, err)
		}
	}
	if l, _ := f.svc.CompanyLabels(ctx, f.org, bob, c.ID); len(l.Tags) != 1 || l.Tags[0] != "hot" {
		t.Errorf("tags = %v, want one hot", l.Tags)
	}
	// Bad requests.
	for name, spec := range map[string]crm.BulkSpec{
		"no ids":             {Type: core.EntityDeal, Owner: &f.bob},
		"no change":          {Type: core.EntityDeal, IDs: ids},
		"stage on a company": {Type: core.EntityCompany, IDs: ids, Stage: crm.StageWon},
		"tag on a deal":      {Type: core.EntityDeal, IDs: ids, AddTag: "x"},
		"bad stage":          {Type: core.EntityDeal, IDs: ids, Stage: "nope"},
		"a document":         {Type: core.EntityDocument, IDs: ids, Owner: &f.bob},
	} {
		if _, err := f.svc.BulkApply(ctx, f.org, bob, spec); !errors.Is(err, crm.ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
}

// A super admin outside the Sales team reads every deal, company and contact, in every list and count that a page,
// the API or the dashboard asks. Bead 4688ed: on 2.25.0 they read $0 and nothing.
func TestSuperAdminOutsideSalesReadsDealsCompaniesAndContacts(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	ao := f.alice
	co, err := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "Seen Co", Kind: crm.KindProspect})
	if err != nil {
		t.Fatal(err)
	}
	ct, err := f.svc.CreateContact(ctx, f.org, alice, crm.Contact{Name: "Seen Person", Email: "seen@example.com", CompanyID: &co.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Seen Deal", AmountCents: 12345, OwnerID: &ao, CompanyID: &co.ID}); err != nil {
		t.Fatal(err)
	}
	admin := f.subj(t, f.superAdmin(t))
	out := f.subj(t, f.outsider(t))
	if has, _ := f.svc.HasSalesAccess(ctx, f.org, admin); !has {
		t.Fatal("the super admin has no sales access")
	}
	if _, n, err := f.svc.Deals(ctx, f.org, admin, crm.ListQuery{}); err != nil || n != 1 {
		t.Errorf("deals as a super admin = %d (%v), want 1", n, err)
	}
	if _, n, err := f.svc.CompaniesPage(ctx, f.org, admin, crm.ListQuery{}); err != nil || n != 1 {
		t.Errorf("companies page as a super admin = %d (%v), want 1", n, err)
	}
	if _, n, err := f.svc.ContactsPage(ctx, f.org, admin, crm.ListQuery{}); err != nil || n != 1 {
		t.Errorf("contacts page as a super admin = %d (%v), want 1", n, err)
	}
	if rows, err := f.svc.Companies(ctx, f.org, admin, "", ""); err != nil || len(rows) != 1 {
		t.Errorf("companies as a super admin = %d (%v), want 1", len(rows), err)
	}
	if rows, err := f.svc.Contacts(ctx, f.org, admin, "", ""); err != nil || len(rows) != 1 {
		t.Errorf("contacts as a super admin = %d (%v), want 1", len(rows), err)
	}
	st, err := f.svc.Stats(ctx, f.org, admin)
	if err != nil || st.Companies != 1 || st.Contacts != 1 || st.Deals != 1 || st.OpenValueCents != 12345 {
		t.Errorf("stats as a super admin = %+v (%v), want one of each and 12345 open", st, err)
	}
	if _, err := f.svc.GetContact(ctx, f.org, admin, ct.ID); err != nil {
		t.Errorf("opening a contact as a super admin: %v", err)
	}
	// And nobody else gains: a person outside sales still reads none of it.
	if _, n, _ := f.svc.CompaniesPage(ctx, f.org, out, crm.ListQuery{}); n != 0 {
		t.Errorf("an outsider's companies = %d, want 0", n)
	}
	if _, n, _ := f.svc.ContactsPage(ctx, f.org, out, crm.ListQuery{}); n != 0 {
		t.Errorf("an outsider's contacts = %d, want 0", n)
	}
	if rows, _ := f.svc.Companies(ctx, f.org, out, "", ""); len(rows) != 0 {
		t.Errorf("an outsider's company list = %d, want 0", len(rows))
	}
}

// The people and firms named in a document are listed for a super admin too.
func TestSuperAdminSeesThePartiesOfADocument(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	co, _ := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "Doc Co", Kind: crm.KindProspect})
	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindTask, Title: "Call them"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.docs.Link(ctx, f.org, alice, d.Ref(), core.Ref{Type: core.EntityCompany, ID: co.ID}, docs.LinkRelates); err != nil {
		t.Fatal(err)
	}
	admin := f.subj(t, f.superAdmin(t))
	parties, err := f.svc.PartiesFor(ctx, f.org, admin, d.ID)
	if err != nil || len(parties) != 1 {
		t.Errorf("parties for a super admin = %d (%v), want 1", len(parties), err)
	}
}

func TestDuplicateContactsAreFoundAndMergedWithoutLosingAnything(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	f.salesTeam(t)
	t.Cleanup(func() { _, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM crm_merges WHERE org_id=$1`, f.org) })
	ctx := context.Background()
	alice, bob, out := f.subj(t, f.alice), f.subj(t, f.bob), f.subj(t, f.outsider(t))
	co, _ := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "Dup Co", Kind: crm.KindProspect})
	f.svc.Share(ctx, f.org, alice, core.Ref{Type: core.EntityCompany, ID: co.ID})
	keep, err := f.svc.CreateContact(ctx, f.org, alice, crm.Contact{Name: "Dana Whitfield", Email: "dana@dup.example", CompanyID: &co.ID})
	if err != nil {
		t.Fatal(err)
	}
	dupe, err := f.svc.CreateContact(ctx, f.org, alice, crm.Contact{Name: "dana whitfield", Email: "dana.w@dup.example", Phone: "(216) 555-0142", Title: "Buyer", Notes: "met at the expo", CompanyID: &co.ID})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := f.svc.CreateContact(ctx, f.org, alice, crm.Contact{Name: "Someone Else", Email: "else@dup.example"})
	for _, c := range []*crm.Contact{keep, dupe, other} {
		f.svc.Share(ctx, f.org, alice, c.Ref())
	}
	// The duplicate was unsubscribed; the survivor was not.
	if _, err := f.svc.SetContactOptOut(ctx, f.org, alice, dupe.ID, "unsubscribed"); err != nil {
		t.Fatal(err)
	}
	deal, _ := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Dana's deal", ContactID: &dupe.ID})
	f.svc.AddActivity(ctx, f.org, alice, crm.Activity{Kind: crm.ActNote, Body: "about the duplicate", Targets: []core.Ref{dupe.Ref()}})
	fu, _ := f.svc.AddFollowup(ctx, f.org, alice, crm.FollowupParams{Title: "ring Dana", Target: dupe.Ref()})

	groups, err := f.svc.Duplicates(ctx, f.org, bob, core.EntityContact)
	if err != nil || len(groups) != 1 || len(groups[0].Records) != 2 {
		t.Fatalf("duplicate groups = %+v (%v), want one group of two", groups, err)
	}
	if g, _ := f.svc.Duplicates(ctx, f.org, out, core.EntityContact); len(g) != 0 {
		t.Errorf("an outsider found %d duplicate groups", len(g))
	}
	// An outsider cannot merge anything, and a bad request is refused before anything moves.
	if err := f.svc.Merge(ctx, f.org, out, core.EntityContact, keep.ID, []string{dupe.ID}); !errors.Is(err, crm.ErrNotFound) {
		t.Errorf("an outsider merging: got %v, want ErrNotFound", err)
	}
	for name, e := range map[string]error{
		"no losers":     f.svc.Merge(ctx, f.org, bob, core.EntityContact, keep.ID, nil),
		"into itself":   f.svc.Merge(ctx, f.org, bob, core.EntityContact, keep.ID, []string{keep.ID}),
		"a deal":        f.svc.Merge(ctx, f.org, bob, core.EntityDeal, keep.ID, []string{dupe.ID}),
		"a stranger id": f.svc.Merge(ctx, f.org, bob, core.EntityContact, keep.ID, []string{uuid.NewString()}),
	} {
		if e == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if err := f.svc.Merge(ctx, f.org, bob, core.EntityContact, keep.ID, []string{dupe.ID}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	got, err := f.svc.GetContact(ctx, f.org, bob, keep.ID)
	if err != nil || got.Phone == "" || got.Title != "Buyer" {
		t.Fatalf("survivor = %+v (%v), want the duplicate's phone and title filled in", got, err)
	}
	if !strings.Contains(got.Notes, "met at the expo") {
		t.Errorf("notes = %q, want the duplicate's carried over", got.Notes)
	}
	d, _ := f.svc.ContactDetails(ctx, f.org, bob, keep.ID)
	if len(d.ExtraEmails) != 1 || d.ExtraEmails[0] != "dana.w@dup.example" {
		t.Errorf("extra emails = %v, want the duplicate's address kept", d.ExtraEmails)
	}
	if d.MarketingOptOutAt == nil {
		t.Error("the unsubscribe did not carry over: a merge must never re-subscribe somebody")
	}
	if _, err := f.svc.GetContact(ctx, f.org, bob, dupe.ID); err == nil {
		t.Error("the merged contact can still be opened")
	}
	if dd, _ := f.svc.GetDeal(ctx, f.org, bob, deal.ID); dd.ContactID == nil || *dd.ContactID != keep.ID {
		t.Errorf("the deal still points at %v", dd.ContactID)
	}
	if acts, _ := f.svc.ActivitiesFor(ctx, f.org, bob, keep.Ref()); len(acts) != 1 {
		t.Errorf("survivor's timeline has %d notes, want the duplicate's 1", len(acts))
	}
	if rows, _, _ := f.svc.Followups(ctx, f.org, bob, crm.FollowupQuery{Target: &core.Ref{Type: core.EntityContact, ID: keep.ID}}); len(rows) != 1 || rows[0].ID != fu.ID {
		t.Errorf("the follow-up did not move: %+v", rows)
	}
	var n int
	f.pools.Web.QueryRow(ctx, `SELECT count(*) FROM crm_merges WHERE org_id=$1 AND survivor_id=$2 AND loser_id=$3`, f.org, keep.ID, dupe.ID).Scan(&n)
	if n != 1 {
		t.Errorf("merge records = %d, want 1", n)
	}
	if g, _ := f.svc.Duplicates(ctx, f.org, bob, core.EntityContact); len(g) != 0 {
		t.Errorf("after the merge there are still %d groups", len(g))
	}
}

func TestDuplicateCompaniesAreMergedWithTheirPeopleAndDeals(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	f.salesTeam(t)
	t.Cleanup(func() { _, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM crm_merges WHERE org_id=$1`, f.org) })
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)
	keep, err := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "Acme Rewinds", Kind: crm.KindProspect})
	if err != nil {
		t.Fatal(err)
	}
	dupe, err := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "ACME  Rewinds", Domain: "acme.example", Kind: crm.KindProspect, Notes: "the old record"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*crm.Company{keep, dupe} {
		f.svc.Share(ctx, f.org, alice, c.Ref())
	}
	f.svc.SetCompanyLabels(ctx, f.org, alice, dupe.ID, nil, &[]string{"expo"})
	person, _ := f.svc.CreateContact(ctx, f.org, alice, crm.Contact{Name: "At Dupe", Email: "p@acme.example", CompanyID: &dupe.ID})
	f.svc.Share(ctx, f.org, alice, person.Ref())
	deal, _ := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Rewind order", CompanyID: &dupe.ID})
	groups, err := f.svc.Duplicates(ctx, f.org, bob, core.EntityCompany)
	if err != nil || len(groups) != 1 {
		t.Fatalf("company groups = %+v (%v), want one", groups, err)
	}
	if err := f.svc.Merge(ctx, f.org, bob, core.EntityCompany, keep.ID, []string{dupe.ID}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if c, _ := f.svc.GetCompany(ctx, f.org, bob, keep.ID); c.Domain != "acme.example" || !strings.Contains(c.Notes, "the old record") {
		t.Errorf("survivor = %+v, want the duplicate's domain and notes", c)
	}
	if ct, _ := f.svc.GetContact(ctx, f.org, bob, person.ID); ct.CompanyID == nil || *ct.CompanyID != keep.ID {
		t.Errorf("the person is still at %v", ct.CompanyID)
	}
	if dd, _ := f.svc.GetDeal(ctx, f.org, bob, deal.ID); dd.CompanyID == nil || *dd.CompanyID != keep.ID {
		t.Errorf("the deal is still at %v", dd.CompanyID)
	}
	if l, _ := f.svc.CompanyLabels(ctx, f.org, bob, keep.ID); len(l.Tags) != 1 || l.Tags[0] != "expo" {
		t.Errorf("tags = %v, want the duplicate's expo", l.Tags)
	}
	if _, err := f.svc.GetCompany(ctx, f.org, bob, dupe.ID); err == nil {
		t.Error("the merged company can still be opened")
	}
}

// The external id of an EmberOps link holds the Hearth id; the document's own name is the end of its page address.
func TestAnEmberOpsLinkCarriesTheDocumentsOwnName(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	t.Cleanup(func() {
		_, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM external_refs WHERE org_id=$1`, f.org)
	})
	ctx := context.Background()
	id := uuid.NewString()
	for _, r := range [][3]string{{"Opportunity/" + id, "https://ops.example/app/opportunity/CRM-OPP-2026-00001", "Opportunity"}, {"Contact/" + id, "https://ops.example/app/contact/Opshome%20Mailproof", "Contact"}} {
		if _, err := f.pools.Web.Exec(ctx, `INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id, external_url) VALUES ($1,'emberops-record',$2,'deal',$3,$4)`, f.org, r[0], id, r[1]); err != nil {
			t.Fatal(err)
		}
	}
	links, err := f.svc.EmberOpsLinks(ctx, f.org, core.Ref{Type: core.EntityDeal, ID: id})
	if err != nil || len(links) != 2 {
		t.Fatalf("links = %+v (%v)", links, err)
	}
	got := map[string]string{}
	for _, l := range links {
		got[l.Label] = l.Name
	}
	if got["Opportunity"] != "CRM-OPP-2026-00001" || got["Contact"] != "Opshome Mailproof" {
		t.Errorf("names = %v, want the document names from the addresses, not the Hearth id", got)
	}
}

func TestSalesReportExplainsLossesAndStageAge(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	alice, bob, out := f.subj(t, f.alice), f.subj(t, f.bob), f.subj(t, f.outsider(t))
	lose := func(name, why string, cents int64) {
		d, err := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: name, AmountCents: cents})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.svc.MoveDeal(ctx, f.org, alice, d.ID, crm.StageLost, nil); err != nil {
			t.Fatal(err)
		}
		if why != "" {
			if err := f.svc.SetLostReason(ctx, f.org, alice, d.ID, why); err != nil {
				t.Fatal(err)
			}
		}
	}
	lose("L1", "Price", 1000)
	lose("L2", " price ", 2000)
	lose("L3", "Went with a competitor", 500)
	lose("L4", "", 100)
	// Two open deals, one of which has sat in its stage for ten days.
	old, _ := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Stale", Stage: crm.StageMeeting})
	f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Fresh", Stage: crm.StageMeeting})
	if _, err := f.pools.Web.Exec(ctx, `UPDATE deals SET stage_changed_at = now() - interval '10 days' WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	day := time.Now()
	rep, err := f.svc.SalesReport(ctx, f.org, bob, day.AddDate(0, 0, -1), day, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.LostReasons) != 3 || rep.LostReasons[0].Count != 2 || rep.LostReasons[0].Reason != "Price" && rep.LostReasons[0].Reason != "price" || rep.LostReasons[0].Value[0].Cents != 3000 {
		t.Fatalf("lost reasons = %+v, want Price twice ($30 together, case folded) first", rep.LostReasons)
	}
	last := rep.LostReasons[len(rep.LostReasons)-1]
	if last.Reason != "No reason given" && rep.LostReasons[1].Reason != "No reason given" && rep.LostReasons[2].Reason != "No reason given" {
		t.Errorf("a loss with no reason must say so: %+v", rep.LostReasons)
	}
	if len(rep.StageAge) != 1 || rep.StageAge[0].Stage != crm.StageMeeting || rep.StageAge[0].Count != 2 || rep.StageAge[0].OldestDays != 10 || rep.StageAge[0].AvgDays != 5 {
		t.Errorf("stage age = %+v, want two in Meeting, oldest 10 days, average 5", rep.StageAge)
	}
	empty, _ := f.svc.SalesReport(ctx, f.org, out, day.AddDate(0, 0, -1), day, "")
	if len(empty.LostReasons) != 0 || len(empty.StageAge) != 0 {
		t.Errorf("a person outside sales saw reasons %v and ages %v", empty.LostReasons, empty.StageAge)
	}
}

func TestATimelineEntryCanBeEditedByItsAuthorAndByTheTeam(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	alice, bob, out := f.subj(t, f.alice), f.subj(t, f.bob), f.subj(t, f.outsider(t))
	d, err := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Edits"})
	if err != nil {
		t.Fatal(err)
	}
	ms := int64(60000)
	call, err := f.svc.AddActivity(ctx, f.org, alice, crm.Activity{Kind: crm.ActCall, Title: "Rang", Body: "no answer", Direction: crm.Outbound, DurationMs: &ms, Targets: []core.Ref{d.Ref()}})
	if err != nil {
		t.Fatal(err)
	}
	note, _ := f.svc.AddActivity(ctx, f.org, alice, crm.Activity{Kind: crm.ActNote, Body: "first", Targets: []core.Ref{d.Ref()}})
	s := func(v string) *string { return &v }
	// The author edits.
	when := time.Date(2026, 11, 20, 14, 30, 0, 0, time.UTC)
	got, err := f.svc.UpdateActivity(ctx, f.org, alice, call.ID, crm.ActivityEdit{Body: s(" left a voicemail "), Disposition: s("voicemail"), Direction: s(crm.Inbound), OccurredAt: &when})
	if err != nil || got.Body != "left a voicemail" || got.Disposition != "voicemail" || got.Direction != crm.Inbound || got.Kind != crm.ActCall {
		t.Fatalf("edit = %+v (%v)", got, err)
	}
	// A colleague on the team edits one they did not write: sales sees and fixes everything.
	if _, err := f.svc.UpdateActivity(ctx, f.org, bob, note.ID, crm.ActivityEdit{Body: s("corrected")}); err != nil {
		t.Fatalf("a team member editing: %v", err)
	}
	acts, _ := f.svc.ActivitiesFor(ctx, f.org, bob, d.Ref())
	byID := map[string]*crm.Activity{}
	for _, a := range acts {
		byID[a.ID] = a
	}
	if byID[note.ID].Body != "corrected" || byID[call.ID].Body != "left a voicemail" || !byID[call.ID].OccurredAt.Equal(when) || *byID[call.ID].DurationMs != 60000 {
		t.Errorf("timeline after edits = %+v %+v", byID[note.ID], byID[call.ID])
	}
	// A note carries no direction or outcome, whatever is sent; the kind never changes.
	if n, err := f.svc.UpdateActivity(ctx, f.org, alice, note.ID, crm.ActivityEdit{Direction: s(crm.Inbound), Disposition: s("x")}); err != nil || n.Direction != "" || n.Disposition != "" || n.Kind != crm.ActNote {
		t.Errorf("a note given a direction: %+v (%v)", n, err)
	}
	// Refusals.
	if _, err := f.svc.UpdateActivity(ctx, f.org, out, call.ID, crm.ActivityEdit{Body: s("x")}); !errors.Is(err, crm.ErrNotFound) {
		t.Errorf("an outsider editing: got %v, want ErrNotFound", err)
	}
	if _, err := f.svc.UpdateActivity(ctx, f.org, alice, note.ID, crm.ActivityEdit{Body: s(" "), Title: s("")}); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("emptying an entry: got %v, want ErrInvalid", err)
	}
	if _, err := f.svc.UpdateActivity(ctx, f.org, alice, call.ID, crm.ActivityEdit{Direction: s("sideways")}); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("a direction that is neither: got %v, want ErrInvalid", err)
	}
	neg := int64(-1)
	if _, err := f.svc.UpdateActivity(ctx, f.org, alice, call.ID, crm.ActivityEdit{DurationMs: &neg}); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("a negative duration: got %v, want ErrInvalid", err)
	}
	if _, err := f.svc.UpdateActivity(ctx, f.org, alice, uuid.NewString(), crm.ActivityEdit{Body: s("x")}); !errors.Is(err, crm.ErrNotFound) {
		t.Errorf("an entry that is not there: got %v, want ErrNotFound", err)
	}
	if err := f.svc.DeleteActivity(ctx, f.org, alice, note.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpdateActivity(ctx, f.org, alice, note.ID, crm.ActivityEdit{Body: s("x")}); !errors.Is(err, crm.ErrNotFound) {
		t.Errorf("a removed entry: got %v, want ErrNotFound", err)
	}
}

func TestCustomPropertiesOnCompaniesContactsAndDeals(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM property_values WHERE def_id IN (SELECT id FROM property_defs WHERE org_id=$1)`, f.org)
		_, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM property_defs WHERE org_id=$1`, f.org)
	})
	alice, bob, out := f.subj(t, f.alice), f.subj(t, f.bob), f.subj(t, f.outsider(t))
	admin := f.subj(t, f.superAdmin(t))
	d, err := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Props"})
	if err != nil {
		t.Fatal(err)
	}
	// Only a super admin defines fields.
	if _, err := f.svc.SaveProperty(ctx, f.org, bob, crm.PropertyInput{Kind: core.EntityDeal, Label: "Line speed", DataType: crm.PropNumber}); !errors.Is(err, crm.ErrForbidden) {
		t.Errorf("a salesperson defining a field: got %v, want ErrForbidden", err)
	}
	speed, err := f.svc.SaveProperty(ctx, f.org, admin, crm.PropertyInput{Kind: core.EntityDeal, Label: "Line speed", DataType: crm.PropNumber})
	if err != nil || speed.Key != "line_speed" {
		t.Fatalf("define = %+v (%v), want the key made from the label", speed, err)
	}
	stage, _ := f.svc.SaveProperty(ctx, f.org, admin, crm.PropertyInput{Kind: core.EntityDeal, Label: "Source", DataType: crm.PropSelect, Options: []string{"Expo", " Referral ", "Expo"}})
	if len(stage.Options) != 2 {
		t.Errorf("options = %v, want two with the duplicate and the spaces gone", stage.Options)
	}
	end, _ := f.svc.SaveProperty(ctx, f.org, admin, crm.PropertyInput{Kind: core.EntityDeal, Label: "Contract end", DataType: crm.PropDate})
	f.svc.SaveProperty(ctx, f.org, admin, crm.PropertyInput{Kind: core.EntityDeal, Label: "Reorder", DataType: crm.PropBoolean})
	// The same label on another kind is its own field.
	if _, err := f.svc.SaveProperty(ctx, f.org, admin, crm.PropertyInput{Kind: core.EntityCompany, Label: "Line speed", DataType: crm.PropText}); err != nil {
		t.Errorf("the same label on a company: %v", err)
	}
	if _, err := f.svc.SaveProperty(ctx, f.org, admin, crm.PropertyInput{Kind: core.EntityDeal, Label: "Line Speed", DataType: crm.PropText}); !errors.Is(err, crm.ErrDuplicate) {
		t.Errorf("a second field with the same key: got %v, want ErrDuplicate", err)
	}
	for name, in := range map[string]crm.PropertyInput{
		"no label":         {Kind: core.EntityDeal, DataType: crm.PropText},
		"unknown type":     {Kind: core.EntityDeal, Label: "X", DataType: "colour"},
		"choice with none": {Kind: core.EntityDeal, Label: "Y", DataType: crm.PropSelect},
		"a document":       {Kind: core.EntityDocument, Label: "Z", DataType: crm.PropText},
		"a bad key":        {Kind: core.EntityDeal, Label: "K", Key: "Bad Key", DataType: crm.PropText},
	} {
		if _, err := f.svc.SaveProperty(ctx, f.org, admin, in); !errors.Is(err, crm.ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	// Set values: typed, validated, all or nothing.
	ref := d.Ref()
	if err := f.svc.SetProperties(ctx, f.org, bob, ref, map[string]any{"line_speed": "1,250.5", "source": "referral", "contract_end": "2027-03-01", "reorder": "yes"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	vals, err := f.svc.PropertiesFor(ctx, f.org, bob, ref)
	if err != nil || len(vals) != 4 {
		t.Fatalf("values = %+v (%v), want the four deal fields", vals, err)
	}
	got := map[string]crm.PropertyValue{}
	for _, v := range vals {
		got[v.Key] = v
	}
	if got["line_speed"].Value != 1250.5 || got["source"].Value != "Referral" || got["contract_end"].Text != "2027-03-01" || got["reorder"].Value != true {
		t.Errorf("values = %+v", got)
	}
	for name, v := range map[string]map[string]any{
		"text in a number":          {"line_speed": "fast"},
		"a bad date":                {"contract_end": "March"},
		"not a choice":              {"source": "Billboard"},
		"not yes or no":             {"reorder": "maybe"},
		"a field that is not there": {"nope": "x"},
	} {
		if err := f.svc.SetProperties(ctx, f.org, bob, ref, v); !errors.Is(err, crm.ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	// One bad value in a request changes nothing in it.
	if err := f.svc.SetProperties(ctx, f.org, bob, ref, map[string]any{"line_speed": "99", "contract_end": "soon"}); err == nil {
		t.Fatal("a request with a bad value was accepted")
	}
	if v, _ := f.svc.PropertiesFor(ctx, f.org, bob, ref); func() bool {
		for _, x := range v {
			if x.Key == "line_speed" && x.Value != 1250.5 {
				return true
			}
		}
		return false
	}() {
		t.Error("a request with a bad value changed another field")
	}
	// Clear.
	if err := f.svc.SetProperties(ctx, f.org, bob, ref, map[string]any{"line_speed": "", "source": nil}); err != nil {
		t.Fatal(err)
	}
	vals, _ = f.svc.PropertiesFor(ctx, f.org, bob, ref)
	for _, v := range vals {
		if (v.Key == "line_speed" || v.Key == "source") && v.Value != nil {
			t.Errorf("%s was not cleared: %v", v.Key, v.Value)
		}
	}
	// An outsider sees none of it and sets none of it.
	if _, err := f.svc.PropertiesFor(ctx, f.org, out, ref); !errors.Is(err, crm.ErrNotFound) {
		t.Errorf("an outsider reading values: got %v, want ErrNotFound", err)
	}
	if err := f.svc.SetProperties(ctx, f.org, out, ref, map[string]any{"line_speed": "1"}); err == nil {
		t.Error("an outsider set a value")
	}
	// Change the label and the choices; the type and key stay.
	chg, err := f.svc.SaveProperty(ctx, f.org, admin, crm.PropertyInput{ID: stage.ID, Kind: core.EntityDeal, Label: "Where from", DataType: crm.PropText, Options: []string{"Expo", "Referral", "Web"}})
	if err != nil || chg.Label != "Where from" || chg.DataType != crm.PropSelect || chg.Key != "source" || len(chg.Options) != 3 {
		t.Errorf("change = %+v (%v), want a new label and choices but the same type and key", chg, err)
	}
	// Delete takes the values with it.
	if err := f.svc.SetProperties(ctx, f.org, bob, ref, map[string]any{"contract_end": "2027-03-01"}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteProperty(ctx, f.org, bob, core.EntityDeal, end.ID); !errors.Is(err, crm.ErrForbidden) {
		t.Errorf("a salesperson deleting a field: got %v, want ErrForbidden", err)
	}
	if err := f.svc.DeleteProperty(ctx, f.org, admin, core.EntityDeal, end.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	f.pools.Web.QueryRow(ctx, `SELECT count(*) FROM property_values WHERE def_id=$1`, end.ID).Scan(&n)
	if n != 0 {
		t.Errorf("%d values left after the field was deleted", n)
	}
	if err := f.svc.DeleteProperty(ctx, f.org, admin, core.EntityDeal, end.ID); !errors.Is(err, crm.ErrNotFound) {
		t.Errorf("deleting twice: got %v, want ErrNotFound", err)
	}
	if defs, _ := f.svc.PropertyDefs(ctx, f.org, core.EntityCompany); len(defs) != 1 {
		t.Errorf("company fields = %d, want the one made for companies", len(defs))
	}
}

func TestFollowupKindsSnoozeAndBulkComplete(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	alice, bob, out := f.subj(t, f.alice), f.subj(t, f.bob), f.subj(t, f.outsider(t))
	d, err := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Kinds"})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().AddDate(0, 0, -5)
	soon := time.Now().AddDate(0, 0, 2)
	mk := func(kind, title string, due *time.Time) *crm.Followup {
		fu, err := f.svc.AddFollowup(ctx, f.org, alice, crm.FollowupParams{Kind: kind, Title: title, DueAt: due, Target: d.Ref()})
		if err != nil {
			t.Fatalf("%s: %v", title, err)
		}
		return fu
	}
	call, mail, todo, nodate := mk("call", "Ring", &past), mk("email", "Write", &soon), mk("", "Plain", nil), mk("meeting", "Demo", nil)
	if todo.Kind != "todo" || call.Kind != "call" {
		t.Errorf("kinds = %q and %q, want todo by default and call", todo.Kind, call.Kind)
	}
	if _, err := f.svc.AddFollowup(ctx, f.org, alice, crm.FollowupParams{Kind: "carrier pigeon", Title: "x", Target: d.Ref()}); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("an unknown kind: got %v, want ErrInvalid", err)
	}
	if rows, n, _ := f.svc.Followups(ctx, f.org, bob, crm.FollowupQuery{Kind: "call"}); n != 1 || rows[0].ID != call.ID {
		t.Errorf("the calls = %d, want only Ring", n)
	}
	// Snooze: a late one lands in the future, from today; one still ahead moves from its own day; none starts from today.
	if err := f.svc.SnoozeFollowup(ctx, f.org, bob, call.ID, 3); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SnoozeFollowup(ctx, f.org, bob, mail.ID, 7); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SnoozeFollowup(ctx, f.org, bob, nodate.ID, 1); err != nil {
		t.Fatal(err)
	}
	due := map[string]time.Time{}
	rows, _, _ := f.svc.Followups(ctx, f.org, bob, crm.FollowupQuery{})
	for _, r := range rows {
		if r.DueAt != nil {
			due[r.Title] = *r.DueAt
		}
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if got := due["Ring"]; !got.Equal(today.AddDate(0, 0, 3)) {
		t.Errorf("a late follow-up snoozed 3 days is due %v, want %v (from today)", got, today.AddDate(0, 0, 3))
	}
	if got := due["Write"]; got.Sub(soon).Round(time.Second) != 7*24*time.Hour {
		t.Errorf("a follow-up ahead snoozed 7 days is due %v, want 7 days after %v", got, soon)
	}
	if got := due["Demo"]; !got.Equal(today.AddDate(0, 0, 1)) {
		t.Errorf("one with no date snoozed a day is due %v, want tomorrow", got)
	}
	for _, days := range []int{0, -1, 366} {
		if err := f.svc.SnoozeFollowup(ctx, f.org, bob, call.ID, days); !errors.Is(err, crm.ErrInvalid) {
			t.Errorf("snooze by %d: got %v, want ErrInvalid", days, err)
		}
	}
	if err := f.svc.SnoozeFollowup(ctx, f.org, out, call.ID, 1); !errors.Is(err, crm.ErrNotFound) {
		t.Errorf("an outsider snoozing: got %v, want ErrNotFound", err)
	}
	// Bulk complete.
	res, err := f.svc.CompleteFollowups(ctx, f.org, bob, []string{call.ID, mail.ID, todo.ID, call.ID, uuid.NewString()}, true)
	if err != nil || res.Done != 3 || len(res.Failed) != 1 {
		t.Fatalf("bulk done = %+v (%v), want 3 done and the stranger named", res, err)
	}
	if _, n, _ := f.svc.Followups(ctx, f.org, bob, crm.FollowupQuery{Status: "open"}); n != 1 {
		t.Errorf("open after the bulk = %d, want only the meeting", n)
	}
	if res, _ := f.svc.CompleteFollowups(ctx, f.org, out, []string{nodate.ID}, true); res.Done != 0 || len(res.Failed) != 1 {
		t.Errorf("an outsider completed %d", res.Done)
	}
	for name, e := range map[string]error{"none": func() error { _, err := f.svc.CompleteFollowups(ctx, f.org, bob, nil, true); return err }()} {
		if !errors.Is(e, crm.ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, e)
		}
	}
}

func TestListsFilterOnACustomField(t *testing.T) {
	f := setup(t)
	f.deskCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM property_values WHERE def_id IN (SELECT id FROM property_defs WHERE org_id=$1)`, f.org)
		_, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM property_defs WHERE org_id=$1`, f.org)
	})
	alice, bob, out := f.subj(t, f.alice), f.subj(t, f.bob), f.subj(t, f.outsider(t))
	admin := f.subj(t, f.superAdmin(t))
	for _, d := range []crm.PropertyInput{
		{Kind: core.EntityCompany, Label: "Industry", DataType: crm.PropSelect, Options: []string{"Plating", "Rewinds"}},
		{Kind: core.EntityCompany, Label: "Notes on fit", DataType: crm.PropText},
		{Kind: core.EntityContact, Label: "Buyer", DataType: crm.PropBoolean},
		{Kind: core.EntityDeal, Label: "Line speed", DataType: crm.PropNumber},
	} {
		if _, err := f.svc.SaveProperty(ctx, f.org, admin, d); err != nil {
			t.Fatal(err)
		}
	}
	mkCo := func(name string, v map[string]any) *crm.Company {
		c, err := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: name, Kind: crm.KindProspect})
		if err != nil {
			t.Fatal(err)
		}
		f.svc.Share(ctx, f.org, alice, c.Ref())
		if v != nil {
			if err := f.svc.SetProperties(ctx, f.org, alice, c.Ref(), v); err != nil {
				t.Fatal(err)
			}
		}
		return c
	}
	a := mkCo("Alpha", map[string]any{"industry": "Rewinds", "notes_on_fit": "great fit, large motors"})
	mkCo("Beta", map[string]any{"industry": "Plating"})
	mkCo("Gamma", nil)
	count := func(q crm.ListQuery) int {
		_, n, err := f.svc.CompaniesPage(ctx, f.org, bob, q)
		if err != nil {
			t.Fatalf("filter %+v: %v", q, err)
		}
		return n
	}
	if n := count(crm.ListQuery{PropKey: "industry", PropValue: "rewinds"}); n != 1 {
		t.Errorf("a choice, any case = %d, want 1", n)
	}
	if n := count(crm.ListQuery{PropKey: "industry", PropValue: "Rew"}); n != 0 {
		t.Errorf("part of a choice = %d, want 0 (a choice matches whole)", n)
	}
	if n := count(crm.ListQuery{PropKey: "notes_on_fit", PropValue: "LARGE"}); n != 1 {
		t.Errorf("inside a text field = %d, want 1", n)
	}
	if n := count(crm.ListQuery{PropKey: "industry", PropValue: "*"}); n != 2 {
		t.Errorf("any value = %d, want the two that have one", n)
	}
	if n := count(crm.ListQuery{PropKey: "industry"}); n != 2 {
		t.Errorf("a field with no value named = %d, want the two that have one", n)
	}
	for _, key := range []string{"nope", "Bad Key", "industry' OR 1=1 --", "x\"; DROP TABLE companies; --"} {
		if n := count(crm.ListQuery{PropKey: key, PropValue: "x"}); n != 0 {
			t.Errorf("the key %q matched %d, want none", key, n)
		}
	}
	if n := count(crm.ListQuery{PropKey: "industry", PropValue: "Rewinds", Owner: "none"}); n != 1 {
		t.Errorf("a custom filter beside another = %d, want 1", n)
	}
	if _, n, _ := f.svc.CompaniesPage(ctx, f.org, out, crm.ListQuery{PropKey: "industry", PropValue: "*"}); n != 0 {
		t.Errorf("an outsider's filtered list = %d, want 0", n)
	}
	// Contacts and deals filter the same way, each on their own kind's fields.
	ct, _ := f.svc.CreateContact(ctx, f.org, alice, crm.Contact{Name: "Buyer One", Email: "b1@x.example"})
	f.svc.CreateContact(ctx, f.org, alice, crm.Contact{Name: "Not Buyer", Email: "b2@x.example"})
	for _, id := range []core.Ref{ct.Ref()} {
		f.svc.Share(ctx, f.org, alice, id)
	}
	if err := f.svc.SetProperties(ctx, f.org, alice, ct.Ref(), map[string]any{"buyer": "yes"}); err != nil {
		t.Fatal(err)
	}
	if _, n, _ := f.svc.ContactsPage(ctx, f.org, bob, crm.ListQuery{PropKey: "buyer", PropValue: "true"}); n != 1 {
		t.Errorf("contacts that are buyers = %d, want 1", n)
	}
	d1, _ := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Fast line"})
	f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: "Slow line"})
	if err := f.svc.SetProperties(ctx, f.org, alice, d1.Ref(), map[string]any{"line_speed": "120"}); err != nil {
		t.Fatal(err)
	}
	if _, n, _ := f.svc.Deals(ctx, f.org, bob, crm.ListQuery{PropKey: "line_speed", PropValue: "120"}); n != 1 {
		t.Errorf("deals at 120 = %d, want 1", n)
	}
	if _, n, _ := f.svc.Deals(ctx, f.org, bob, crm.ListQuery{PropKey: "industry", PropValue: "*"}); n != 0 {
		t.Errorf("a company field used on deals = %d, want 0 (fields belong to their kind)", n)
	}
	// Values for a page at once.
	texts, err := f.svc.PropertyTexts(ctx, f.org, core.EntityCompany, []string{a.ID})
	if err != nil || texts[a.ID]["industry"] != "Rewinds" || texts[a.ID]["notes_on_fit"] == "" {
		t.Errorf("texts = %v (%v)", texts, err)
	}
	if none, _ := f.svc.PropertyTexts(ctx, f.org, core.EntityCompany, nil); len(none) != 0 {
		t.Error("no ids gave values")
	}
}
