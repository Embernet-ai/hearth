package docs_test

import (
	"context"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/docs"
)

// A due date is entered as a date and stored as midnight UTC. Read back on a server
// whose own zone is west of UTC it came out as the evening before, so the page said
// the 4th for a job due the 5th. It must come back as the day it was entered.
func TestADueDateComesBackAsTheDayItWasEntered(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no zone database")
	}
	was := time.Local
	time.Local = ny
	defer func() { time.Local = was }()

	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	due := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	d, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Replace the seal",
		Task: &docs.TaskFields{Status: docs.StatusTodo, DueAt: &due},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	rows, err := f.svc.Search(ctx, f.org, alice, docs.Query{Kind: docs.KindWorkOrder})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var got *docs.Row
	for _, r := range rows {
		if r.ID == d.ID {
			got = r
		}
	}
	if got == nil || got.Task == nil || got.Task.DueAt == nil {
		t.Fatalf("the work order or its due date did not come back: %+v", got)
	}
	if day := got.Task.DueAt.Format("2006-01-02"); day != "2026-11-05" {
		t.Errorf("due 5 Nov read back as %s", day)
	}
}
