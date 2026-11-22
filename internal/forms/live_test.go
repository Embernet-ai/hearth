package forms

// Live check of the Forms area's calls against the real forms app. Skipped unless HEARTH_LIVE_FORMS_URL and _API_KEY are
// set (secrets/access/hearth-apps-live.py puts them there). It makes a form with every question type, changes it,
// schedules and publishes it, reads its results, and deletes it.

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveForms(t *testing.T) {
	base := os.Getenv("HEARTH_LIVE_FORMS_URL")
	if base == "" {
		t.Skip("HEARTH_LIVE_FORMS_URL not set: this talks to the real forms app")
	}
	ctx := context.Background()
	c := New(base, os.Getenv("HEARTH_LIVE_FORMS_API_KEY"))
	in := Input{Name: "Hearth live proof " + time.Now().UTC().Format("150405"), Questions: []Question{
		{Headline: "What did you think?", Type: "openText", Required: true},
		{Headline: "Pick one", Type: "multipleChoiceSingle", Choices: []string{"Yes", "No"}},
		{Headline: "Pick some", Type: "multipleChoiceMulti", Choices: []string{"A", "B", "C"}},
		{Headline: "How was it?", Type: "rating"},
		{Headline: "Would you recommend us?", Type: "nps"},
	}}
	id, err := c.Create(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(ctx, id) })
	f, err := c.Get(ctx, id)
	if err != nil || len(f.Questions) != 5 || f.Status != Draft || f.Questions[1].Choices[0] != "Yes" {
		t.Fatalf("read back %+v %v", f, err)
	}
	in.Name += " edited"
	in.Questions = in.Questions[:3]
	if err := c.Update(ctx, id, in); err != nil {
		t.Fatalf("update: %v", err)
	}
	f, err = c.Get(ctx, id)
	if err != nil || len(f.Questions) != 3 || f.Name != in.Name {
		t.Fatalf("after update %+v %v", f, err)
	}
	start, end := time.Now().UTC().Add(72*time.Hour).Format("2006-01-02"), time.Now().UTC().Add(120*time.Hour).Format("2006-01-02")
	if err := c.Schedule(ctx, id, start, end); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	f, _ = c.Get(ctx, id)
	if Date(f.PublishOn) != start || Date(f.CloseOn) != end || !f.IsScheduled(time.Now()) || f.Status != Draft {
		t.Fatalf("scheduled form holds %q to %q, want %s to %s", f.PublishOn, f.CloseOn, start, end)
	}
	if err := c.Schedule(ctx, id, "", ""); err != nil {
		t.Fatalf("unschedule: %v", err)
	}
	for _, s := range []Status{InProgress, Paused, Completed, Draft} {
		if err := c.SetStatus(ctx, id, s); err != nil {
			t.Fatalf("set %s: %v", s, err)
		}
		if f, _ = c.Get(ctx, id); f.Status != s {
			t.Fatalf("status is %s, want %s", f.Status, s)
		}
	}
	res, err := c.Results(ctx, id)
	if err != nil || res.Total != 0 {
		t.Fatalf("results %+v %v", res, err)
	}
	all, err := c.Forms(ctx)
	if err != nil || len(all) < 1 {
		t.Fatalf("list %v %v", all, err)
	}
	t.Logf("forms: made with five question types, read back, edited, scheduled, published, paused, closed, listed (%d forms), results read", len(all))
}
