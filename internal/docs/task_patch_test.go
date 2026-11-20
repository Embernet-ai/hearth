package docs_test

import (
	"context"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/docs"
)

// ptr is the "I mean this value, not absent" helper that TaskPatch requires.
func ptr[T any](v T) *T { return &v }

// A status change must not erase the rest of the task.
//
// This is a regression test for a real bug. The update wrote status, priority,
// assignee, and due_at from a single struct, so a caller who filled in only the
// status silently blanked the other three. The status dropdown on the document
// page did exactly that, which meant assigning a work order to somebody and then
// moving it to in progress quietly un-assigned it and dropped its due date.
func TestStatusChangeKeepsTheRestOfTheTask(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	due := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	d, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Replace the seal",
		Task: &docs.TaskFields{
			Status: docs.StatusTodo, Priority: 2,
			AssigneeID: &f.bob, DueAt: &due,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Exactly what the status form sends: a status, and nothing else.
	got, err := f.svc.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Task:            &docs.TaskPatch{Status: ptr(docs.StatusInProgress)},
		ExpectedVersion: d.Version,
	})
	if err != nil {
		t.Fatalf("update status: %v", err)
	}

	if got.Task.Status != docs.StatusInProgress {
		t.Errorf("status = %q, want in_progress", got.Task.Status)
	}
	if got.Task.AssigneeID == nil || *got.Task.AssigneeID != f.bob {
		t.Errorf("assignee = %v, want it left alone as bob", got.Task.AssigneeID)
	}
	if got.Task.Priority != 2 {
		t.Errorf("priority = %d, want it left alone as 2", got.Task.Priority)
	}
	if got.Task.DueAt == nil {
		t.Error("due date was cleared by a status change")
	}
}

// Clearing still has to be possible, or "unassign this" becomes impossible once
// absence stops meaning "erase it".
func TestClearingAssigneeAndDueIsExplicit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	due := time.Now().Add(24 * time.Hour)
	d, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindTask, Title: "Temporary",
		Task: &docs.TaskFields{Status: docs.StatusTodo, AssigneeID: &f.bob, DueAt: &due},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := f.svc.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Task:            &docs.TaskPatch{ClearAssignee: true, ClearDue: true},
		ExpectedVersion: d.Version,
	})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got.Task.AssigneeID != nil {
		t.Errorf("assignee = %v, want cleared", *got.Task.AssigneeID)
	}
	if got.Task.DueAt != nil {
		t.Errorf("due = %v, want cleared", *got.Task.DueAt)
	}
}
