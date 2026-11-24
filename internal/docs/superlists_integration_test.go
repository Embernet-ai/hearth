package docs_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/docs"
)

// A super admin sees every template and every linked document, including the ones other people made and nobody shared
// with them. A person with no grant sees none of them.
func TestASuperAdminSeesTemplatesAndLinksOthersMade(t *testing.T) {
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
	alice, dave, adm := f.subj(t, f.alice), f.subj(t, f.dave), f.subj(t, admin)

	tpl, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindTemplate, Title: "Alice's template"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindDoc, Title: "Alice A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindDoc, Title: "Alice B"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Link(ctx, f.org, alice, a.Ref(), b.Ref(), docs.LinkRelates); err != nil {
		t.Fatal(err)
	}
	has := func(list []docs.Template, id string) bool {
		for _, x := range list {
			if x.ID == id {
				return true
			}
		}
		return false
	}
	if l, err := f.svc.Templates(ctx, f.org, adm); err != nil || !has(l, tpl.ID) {
		t.Errorf("a super admin's templates = %v (%v), want Alice's in them", l, err)
	}
	if l, _ := f.svc.Templates(ctx, f.org, dave); has(l, tpl.ID) {
		t.Error("a person with no grant saw Alice's template")
	}
	if l, err := f.svc.Linked(ctx, f.org, adm, a.ID); err != nil || len(l) != 1 {
		t.Errorf("a super admin's documents linked to A = %d (%v), want 1", len(l), err)
	}
	if l, err := f.svc.LinkedTo(ctx, f.org, adm, b.Ref()); err != nil || len(l) != 1 {
		t.Errorf("a super admin's documents linked to B = %d (%v), want 1", len(l), err)
	}
}
