package docs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
)

// Sharing one task with a guest at a chosen level. A viewer may read it and no
// more; an editor may change it. Only the creator, a manager or an admin may
// share, and the levels stop at edit -- no guest gets admin.
func TestAGuestGetsExactlyTheLevelShared(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	carol, gina := uuid.NewString(), uuid.NewString()
	for _, p := range []string{carol, gina} {
		exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','x')`, p, f.org)
	}
	dept := uuid.NewString()
	exec(`INSERT INTO spaces (id, org_id, name, created_by) VALUES ($1,$2,'Ops',$3)`, dept, f.org, f.alice)
	exec(`INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1,$2,$3)`, f.org, dept, carol)
	space := dept
	task, err := f.svc.Create(ctx, f.org, f.subj(t, f.alice), docs.CreateParams{Kind: docs.KindTask, Title: "Client job", SpaceID: &space})
	if err != nil {
		t.Fatal(err)
	}
	// bob can edit the task but is not creator/manager/admin.
	exec(`INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		VALUES (gen_random_uuid(), $1, 'document', $2, $3, 'edit', $4)`,
		f.org, task.ID, core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}.Key(), f.alice)

	// Before sharing, gina cannot even see it.
	if _, err := f.svc.Get(ctx, f.org, f.subj(t, gina), task.ID); err == nil {
		t.Fatal("gina saw the task before it was shared")
	}
	// bob (edit, not manager) cannot share.
	if err := f.svc.ShareWith(ctx, f.org, f.subj(t, f.bob), task.ID, gina, core.AccessView); !errors.Is(err, docs.ErrNotAllowed) {
		t.Fatalf("a plain editor shared the task: %v", err)
	}
	// Admin level is not a guest level.
	if err := f.svc.ShareWith(ctx, f.org, f.subj(t, f.alice), task.ID, gina, core.AccessAdmin); !errors.Is(err, docs.ErrField) {
		t.Fatalf("a guest was given admin: %v", err)
	}

	// The manager shares it read-only. gina can open it, not change it.
	if err := f.svc.ShareWith(ctx, f.org, f.subj(t, carol), task.ID, gina, core.AccessView); err != nil {
		t.Fatalf("the manager could not share: %v", err)
	}
	if _, err := f.svc.Get(ctx, f.org, f.subj(t, gina), task.ID); err != nil {
		t.Fatalf("a viewer cannot read the task: %v", err)
	}
	status := docs.StatusInProgress
	if _, err := f.svc.Update(ctx, f.org, f.subj(t, gina), task.ID, docs.UpdateParams{Task: &docs.TaskPatch{Status: &status}}); err == nil {
		t.Fatal("a viewer changed the task")
	}
	// gina shows as a guest, not an assignee or owner.
	guests, err := f.svc.Guests(ctx, f.org, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ginaLevel string
	for _, g := range guests {
		if g.PersonID == gina {
			ginaLevel = g.Level
		}
		if g.PersonID == f.alice {
			t.Error("the owner showed up as a guest")
		}
	}
	if ginaLevel != "view" {
		t.Fatalf("gina should be a viewer guest, got %q (list %+v)", ginaLevel, guests)
	}

	// Raised to editor, gina can change it.
	if err := f.svc.ShareWith(ctx, f.org, f.subj(t, f.alice), task.ID, gina, core.AccessEdit); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Update(ctx, f.org, f.subj(t, gina), task.ID, docs.UpdateParams{Task: &docs.TaskPatch{Status: &status}}); err != nil {
		t.Fatalf("an editor guest cannot change the task: %v", err)
	}

	// Unsharing takes it away; afterwards gina cannot see it.
	if err := f.svc.UnshareGuest(ctx, f.org, f.subj(t, carol), task.ID, gina); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Get(ctx, f.org, f.subj(t, gina), task.ID); err == nil {
		t.Fatal("gina still saw the task after being unshared")
	}
}

// Unshare will not strip an assignee or the owner, whose access is not a guest
// share and must survive a tidy of the guest list.
func TestUnshareLeavesOwnersAndAssigneesAlone(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$2)`, f.org, f.alice)
	admin := f.subj(t, f.alice)
	task, err := f.svc.Create(ctx, f.org, admin, docs.CreateParams{Kind: docs.KindTask, Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	// bob is an assignee.
	if err := f.svc.Assign(ctx, f.org, admin, task.ID, []string{f.bob}, f.bob); err != nil {
		t.Fatal(err)
	}
	// Trying to unshare the assignee or the owner is refused, and their access stands.
	if err := f.svc.UnshareGuest(ctx, f.org, admin, task.ID, f.bob); !errors.Is(err, docs.ErrField) {
		t.Fatalf("unshare touched an assignee: %v", err)
	}
	if err := f.svc.UnshareGuest(ctx, f.org, admin, task.ID, f.alice); !errors.Is(err, docs.ErrField) {
		t.Fatalf("unshare touched the owner: %v", err)
	}
	if _, err := f.svc.Get(ctx, f.org, f.subj(t, f.bob), task.ID); err != nil {
		t.Fatalf("the assignee lost access: %v", err)
	}
}
