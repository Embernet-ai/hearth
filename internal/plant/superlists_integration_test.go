package plant_test

import (
	"context"
	"testing"

	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/plant"
)

// The asset tree shows a super admin a machine that somebody else made and shared with nobody, and its ancestors.
func TestTheAssetTreeShowsASuperAdminWhatOthersMade(t *testing.T) {
	f := setup(t) // alice is the super admin here, bob is not
	ctx := context.Background()
	bob := f.subj(t, f.bob)
	site, err := f.docs.Create(ctx, f.org, bob, docs.CreateParams{Kind: docs.KindAsset, Title: "Bob's site"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetFields(ctx, f.org, bob, site.ID, plant.Fields{}); err != nil {
		t.Fatal(err)
	}
	press, err := f.docs.Create(ctx, f.org, bob, docs.CreateParams{Kind: docs.KindAsset, Title: "Bob's press"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetFields(ctx, f.org, bob, press.ID, plant.Fields{ParentAssetID: &site.ID}); err != nil {
		t.Fatal(err)
	}
	alice := f.subj(t, f.alice)
	tree, err := f.svc.Tree(ctx, f.org, alice)
	if err != nil {
		t.Fatal(err)
	}
	var gotSite, gotPress *plant.Asset
	for _, a := range tree {
		switch a.ID {
		case site.ID:
			gotSite = a
		case press.ID:
			gotPress = a
		}
	}
	if gotSite == nil || gotPress == nil || gotPress.Depth != 1 {
		t.Fatalf("a super admin's tree lost bob's machines: site %v press %v", gotSite, gotPress)
	}
}
