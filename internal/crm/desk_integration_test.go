package crm_test

// The sales desk against a real Postgres: follow-ups, the forecast, the report, bulk change, the new timeline kinds,
// and who may see any of it.

import (
	"context"
	"errors"
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
