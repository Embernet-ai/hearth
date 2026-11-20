package docs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
)

// Who may set a task's assignees is narrower than who may edit it: the creator,
// a manager of its department, or an admin -- and nobody else, even with edit.
// Assigning grants each person the task, and the primary mirrors into the field
// the board reads.
func TestOnlyTheRightPeopleAssign(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// alice creates the task (its owner); carol manages the department; sam is a
	// super admin; bob merely has edit on the task; ed and fay get assigned.
	carol, sam, ed, fay := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, p := range []string{carol, sam, ed, fay} {
		exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','x')`, p, f.org)
	}
	dept := uuid.NewString()
	exec(`INSERT INTO spaces (id, org_id, name, created_by) VALUES ($1,$2,'Operations',$3)`, dept, f.org, f.alice)
	exec(`INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1,$2,$3)`, f.org, dept, carol)
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$2)`, f.org, sam)

	spaceID := dept
	task, err := f.svc.Create(ctx, f.org, f.subj(t, f.alice), docs.CreateParams{Kind: docs.KindTask, Title: "Safety training", SpaceID: &spaceID})
	if err != nil {
		t.Fatal(err)
	}
	// bob can edit the task but is not creator/manager/admin.
	exec(`INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		VALUES (gen_random_uuid(), $1, 'document', $2, $3, 'edit', $4)`,
		f.org, task.ID, core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}.Key(), f.alice)

	may := func(who string) bool {
		t.Helper()
		ok, err := f.svc.MayAssign(ctx, f.org, f.subj(t, who), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !may(f.alice) || !may(carol) || !may(sam) {
		t.Fatal("creator, manager and admin should all be allowed to assign")
	}
	if may(f.bob) {
		t.Fatal("a plain editor was allowed to assign")
	}
	// bob's attempt is refused.
	if err := f.svc.Assign(ctx, f.org, f.subj(t, f.bob), task.ID, []string{ed}, ed); !errors.Is(err, docs.ErrNotAllowed) {
		t.Fatalf("a plain editor assigned the task: %v", err)
	}

	// The manager assigns two people, fay primary.
	if err := f.svc.Assign(ctx, f.org, f.subj(t, carol), task.ID, []string{ed, fay}, fay); err != nil {
		t.Fatalf("the manager could not assign: %v", err)
	}
	ids, primary, err := f.svc.AssignedTo(ctx, task.ID)
	if err != nil || len(ids) != 2 || primary != fay {
		t.Fatalf("assigned set wrong: %v %v primary=%s", ids, err, primary)
	}
	// Both can now open the task they were given.
	for _, who := range []string{ed, fay} {
		if _, err := f.svc.Get(ctx, f.org, f.subj(t, who), task.ID); err != nil {
			t.Fatalf("an assignee cannot open the task: %v", err)
		}
	}
	// The primary mirrors into the field the board groups by.
	got, err := f.svc.Get(ctx, f.org, f.subj(t, f.alice), task.ID)
	if err != nil || got.Task == nil || got.Task.AssigneeID == nil || *got.Task.AssigneeID != fay {
		t.Fatalf("primary did not mirror into task_fields: %+v", got.Task)
	}
	// Clearing removes everyone.
	if err := f.svc.Assign(ctx, f.org, f.subj(t, carol), task.ID, nil, ""); err != nil {
		t.Fatal(err)
	}
	if ids, _, _ := f.svc.AssignedTo(ctx, task.ID); len(ids) != 0 {
		t.Fatalf("clearing left assignees: %v", ids)
	}
}

// "Assigned to me" matches whether you are the primary or just one of several,
// and still matches a task assigned the old single-field way with no extra
// row. Grouping, which uses the single field, is unaffected.
func TestAssignedToMeMatchesEveryAssignee(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	p1, p2 := uuid.NewString(), uuid.NewString()
	for _, p := range []string{p1, p2} {
		exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','x')`, p, f.org)
	}
	admin := f.subj(t, f.alice)
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$2)`, f.org, f.alice)
	admin = f.subj(t, f.alice)

	// A: assigned to p1 (primary) and p2, through Assign.
	a, _ := f.svc.Create(ctx, f.org, admin, docs.CreateParams{Kind: docs.KindTask, Title: "A"})
	if err := f.svc.Assign(ctx, f.org, admin, a.ID, []string{p1, p2}, p1); err != nil {
		t.Fatal(err)
	}
	// B: assigned to p2 the old way -- the field only, no document_assignees row.
	b, _ := f.svc.Create(ctx, f.org, admin, docs.CreateParams{Kind: docs.KindTask, Title: "B",
		Task: &docs.TaskFields{Status: docs.StatusTodo, AssigneeID: &p2}})

	matches := func(who string) map[string]bool {
		t.Helper()
		rows, err := f.svc.Search(ctx, f.org, admin, docs.Query{
			Filters: []docs.Filter{{Field: docs.Col("assignee"), Op: docs.OpIs, Values: []string{who}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, r := range rows {
			out[r.ID] = true
		}
		return out
	}
	if m := matches(p1); !m[a.ID] || m[b.ID] {
		t.Fatalf("p1 should match only A: %v", m)
	}
	if m := matches(p2); !m[a.ID] || !m[b.ID] {
		t.Fatalf("p2 is on both (A as extra, B the old way) and should match both: %v", m)
	}
}
