package maint_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/maint"
)

// A super admin sees every schedule and the compliance report over every machine, not only the ones granted to them.
func TestASuperAdminSeesSchedulesAndComplianceForEveryMachine(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	admin := uuid.NewString()
	for _, q := range []string{
		`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Ada Admin')`,
		`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($2,$1,'super_admin',$1)`,
	} {
		if _, err := f.pools.Web.Exec(ctx, q, admin, f.org); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM capability_grants WHERE org_id=$1`, f.org)
	})
	alice, bob, adm := f.subj(t, f.alice), f.subj(t, f.bob), f.subj(t, admin)
	a := f.asset(t, "Press nobody shared")
	days := 30
	if _, err := f.svc.Save(ctx, f.org, alice, maint.Schedule{AssetID: a.ID, Name: "Guard inspection", EveryDays: &days}); err != nil {
		t.Fatal(err)
	}
	if l, err := f.svc.List(ctx, f.org, adm); err != nil || len(l) != 1 {
		t.Errorf("a super admin's schedules = %d (%v), want 1", len(l), err)
	}
	if l, _ := f.svc.List(ctx, f.org, bob); len(l) != 0 {
		t.Errorf("bob saw %d schedules for a machine he has no grant on", len(l))
	}
	if _, err := f.svc.Report(ctx, f.org, adm, time.Now().AddDate(0, -1, 0), time.Now().AddDate(0, 1, 0)); err != nil {
		t.Errorf("a super admin's compliance report: %v", err)
	}
}
