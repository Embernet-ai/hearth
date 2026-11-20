package plant_test

import (
	"context"
	"testing"
)

// A space marked open_to_all shows in everybody's tree, so a person with no
// grant and no department can still pick it when filing a daily report, and
// the private space beside it stays hidden from them.
func TestAnOpenSpaceIsInEveryonesTree(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)

	if _, err := f.svc.CreateSpace(ctx, f.org, alice, "Private area", "area", nil); err != nil {
		t.Fatal(err)
	}
	var open string
	if err := f.pools.Web.QueryRow(ctx, `
		INSERT INTO spaces (id, org_id, name, kind, created_by, open_to_all)
		VALUES (gen_random_uuid(), $1, 'Daily reports', 'area', $2, true) RETURNING id::text`,
		f.org, f.alice).Scan(&open); err != nil {
		t.Fatal(err)
	}
	tree, err := f.svc.SpaceTree(ctx, f.org, bob)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 1 || tree[0].ID != open {
		t.Fatalf("bob's tree = %d spaces, want exactly the open one: %+v", len(tree), tree)
	}
}
