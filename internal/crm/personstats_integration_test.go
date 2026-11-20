package crm_test

import (
	"context"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/crm"
)

// One person's pipeline: open value by stage, past due, and wins and losses
// counted by the day the deal closed, never by the last time it was edited.
func TestPersonStatsCountOnlyTheirDealsAndDateClosesExactly(t *testing.T) {
	f := setup(t)
	f.salesCleanup(t)
	f.salesTeam(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	past := time.Now().AddDate(0, 0, -3)
	soon := time.Now().AddDate(0, 0, 10)
	mk := func(name string, owner string, cents int64, stage string, closes *time.Time) *crm.Deal {
		d, err := f.svc.CreateDeal(ctx, f.org, alice, crm.Deal{Name: name, AmountCents: cents, OwnerID: &owner, CloseDate: closes})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if stage != crm.StageNew {
			if err := f.svc.MoveDeal(ctx, f.org, alice, d.ID, stage, nil); err != nil {
				t.Fatalf("move %s: %v", name, err)
			}
		}
		return d
	}
	mk("open late", f.alice, 100000, crm.StageProposal, &past)
	mk("open soon", f.alice, 50000, crm.StageMeeting, &soon)
	mk("open no date", f.alice, 25000, crm.StageNew, nil)
	won := mk("won", f.alice, 400000, crm.StageWon, nil)
	mk("lost", f.alice, 70000, crm.StageLost, nil)
	mk("bob's open", f.bob, 999, crm.StageNew, nil)
	mk("bob's won", f.bob, 888, crm.StageWon, nil)

	today := time.Now().Truncate(24 * time.Hour)
	from := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.Local)
	st, err := f.svc.PersonStats(ctx, f.org, alice, f.alice, today, from, from.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.OpenCount != 3 || len(st.Open) != 1 || st.Open[0].Cents != 175000 {
		t.Errorf("open = %d deals %+v, want 3 deals worth 175000 (bob's are not hers)", st.OpenCount, st.Open)
	}
	if st.Overdue != 1 {
		t.Errorf("past expected close = %d, want 1", st.Overdue)
	}
	if st.WonCount != 1 || st.Won[0].Cents != 400000 || st.LostCount != 1 {
		t.Errorf("won %d %+v lost %d, want one win of 400000 and one loss", st.WonCount, st.Won, st.LostCount)
	}
	stageCount := map[string]int{}
	for _, r := range st.Stages {
		stageCount[r.Stage] = r.Count
	}
	if stageCount[crm.StageProposal] != 1 || stageCount[crm.StageMeeting] != 1 || stageCount[crm.StageNew] != 1 {
		t.Errorf("stages = %v", stageCount)
	}

	// The list the tile links to holds exactly the rows the tile counts.
	rows, total, _ := f.svc.Deals(ctx, f.org, alice, crm.ListQuery{Owner: f.alice, Open: true})
	if total != st.OpenCount || len(rows) != st.OpenCount {
		t.Errorf("open list has %d rows, the tile says %d", total, st.OpenCount)
	}
	rows, total, _ = f.svc.Deals(ctx, f.org, alice, crm.ListQuery{Owner: f.alice, Overdue: true})
	if total != st.Overdue {
		t.Errorf("overdue list has %d rows, the tile says %d", total, st.Overdue)
	}
	cf, ct := from, from.AddDate(0, 1, -1)
	rows, total, _ = f.svc.Deals(ctx, f.org, alice, crm.ListQuery{Owner: f.alice, Stage: crm.StageWon, ClosedFrom: &cf, ClosedTo: &ct})
	if total != 1 || rows[0].Name != "won" {
		t.Errorf("won this month list = %d rows, want the one win", total)
	}

	// Editing a won deal later does not move its close date; a win from last
	// month is not this month's win.
	last := time.Now().AddDate(0, -2, 0)
	if _, err := f.pools.Web.Exec(ctx, `UPDATE deals SET closed_at = $2 WHERE id = $1`, won.ID, last); err != nil {
		t.Fatal(err)
	}
	won.Name = "won, renamed"
	won.Stage = crm.StageWon
	if err := f.svc.UpdateDeal(ctx, f.org, alice, *won); err != nil {
		t.Fatal(err)
	}
	st, _ = f.svc.PersonStats(ctx, f.org, alice, f.alice, today, from, from.AddDate(0, 1, 0))
	if st.WonCount != 0 {
		t.Errorf("a win closed two months ago counted as this month's after an edit: %d", st.WonCount)
	}
	// Reopening clears it, closing again stamps it.
	if err := f.svc.MoveDeal(ctx, f.org, alice, won.ID, crm.StageProposal, nil); err != nil {
		t.Fatal(err)
	}
	var closed *time.Time
	_ = f.pools.Web.QueryRow(ctx, `SELECT closed_at FROM deals WHERE id=$1`, won.ID).Scan(&closed)
	if closed != nil {
		t.Errorf("a reopened deal still has a close date: %v", closed)
	}
	if err := f.svc.MoveDeal(ctx, f.org, alice, won.ID, crm.StageWon, nil); err != nil {
		t.Fatal(err)
	}
	st, _ = f.svc.PersonStats(ctx, f.org, alice, f.alice, today, from, from.AddDate(0, 1, 0))
	if st.WonCount != 1 {
		t.Errorf("closing it again today: won = %d, want 1", st.WonCount)
	}

	// Bob cannot see a deal alice kept private, and so it is not in his count of her.
	if _, err := f.pools.Web.Exec(ctx, `DELETE FROM grants WHERE org_id=$1 AND entity_type='deal'`, f.org); err != nil {
		t.Fatal(err)
	}
	st, _ = f.svc.PersonStats(ctx, f.org, f.subj(t, f.bob), f.alice, today, from, from.AddDate(0, 1, 0))
	if st.OpenCount != 0 || st.WonCount != 0 {
		t.Errorf("with no grants bob counts %d open, %d won of alice's; he may see none of them", st.OpenCount, st.WonCount)
	}
}
