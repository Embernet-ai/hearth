package crm_test

// Who works sales: a member of the Sales team and a super admin do; a person outside both does not, and a team that
// does not exist gives nobody sales through membership.

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestHasSalesAccessIsTheSalesTeamOrASuperAdmin(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	outsider, admin := uuid.NewString(), uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$3,'user','Outside'), ($2,$3,'user','Ada Admin')`, outsider, admin, f.org)
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$2)`, f.org, admin)
	t.Cleanup(func() { _, _ = f.pools.Web.Exec(ctx, `DELETE FROM capability_grants WHERE org_id=$1`, f.org) })

	// No Sales team yet: only the super admin works sales.
	for who, want := range map[string]bool{f.alice: false, outsider: false, admin: true} {
		got, err := f.svc.HasSalesAccess(ctx, f.org, f.subj(t, who))
		if err != nil || got != want {
			t.Errorf("before the Sales team exists, %s: %v %v, want %v", who, got, err, want)
		}
	}

	f.salesTeam(t) // alice and bob join it
	for who, want := range map[string]bool{f.alice: true, f.bob: true, outsider: false, admin: true} {
		got, err := f.svc.HasSalesAccess(ctx, f.org, f.subj(t, who))
		if err != nil || got != want {
			t.Errorf("with the Sales team, %s: %v %v, want %v", who, got, err, want)
		}
	}
}
