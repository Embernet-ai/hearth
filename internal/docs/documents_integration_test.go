package docs_test

// History, checklists, and templates, against a real Postgres.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
)

// An old version can be viewed and restored, and restoring writes a new one
// rather than rewinding the counter.
func TestAnOldVersionCanBeViewedAndRestored(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindDoc, Title: "Restart procedure",
		Body: "Isolate the drive.\nFit the bearing.",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	second := "Isolate the drive.\nLock off at the panel.\nFit the bearing."
	if _, err := f.svc.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Body: &second, ExpectedVersion: 1,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	history, err := f.svc.History(ctx, f.org, alice, d.ID, 10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("%d versions, want 2", len(history))
	}
	// Newest first, and the current one says so.
	if history[0].Version != 2 || !history[0].Current {
		t.Errorf("the newest version is %d current=%v", history[0].Version, history[0].Current)
	}
	if history[1].Current {
		t.Error("an old version claims to be current")
	}
	if history[0].AuthorName != "Alice" {
		t.Errorf("author rendered as %q", history[0].AuthorName)
	}

	// Viewing an old one returns what it actually said.
	old, err := f.svc.VersionAt(ctx, f.org, alice, d.ID, 1)
	if err != nil {
		t.Fatalf("version 1: %v", err)
	}
	if strings.Contains(old.Body, "Lock off") {
		t.Errorf("version 1 has the later text in it: %q", old.Body)
	}

	// Restoring produces version 3 holding version 1's text. Rewinding the
	// counter would make version 2 mean two different things and would lose
	// the record that anybody restored anything.
	restored, err := f.svc.Restore(ctx, f.org, alice, d.ID, 1)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Version != 3 {
		t.Errorf("restore produced version %d, want 3", restored.Version)
	}
	if restored.Body != old.Body {
		t.Errorf("the restored body is not version 1's:\n%q", restored.Body)
	}
	history, _ = f.svc.History(ctx, f.org, alice, d.ID, 10)
	if len(history) != 3 {
		t.Errorf("%d versions after a restore, want 3", len(history))
	}

	// Somebody with no grant sees no history at all.
	if _, err := f.svc.History(ctx, f.org, f.subj(t, f.dave), d.ID, 10); err == nil {
		t.Error("a stranger read the history")
	}
}

// The diff is line based, because a procedure is a list of steps.
func TestDiffReportsTheLinesThatMoved(t *testing.T) {
	ops := docs.Diff(
		"Isolate the drive.\nFit the bearing.",
		"Isolate the drive.\nLock off at the panel.\nFit the bearing.")

	var added, removed, same int
	for _, op := range ops {
		switch op.Kind {
		case "+":
			added++
			if op.Text != "Lock off at the panel." {
				t.Errorf("added the wrong line: %q", op.Text)
			}
		case "-":
			removed++
		default:
			same++
		}
	}
	if added != 1 || removed != 0 || same != 2 {
		t.Errorf("diff reported +%d -%d =%d, want +1 -0 =2", added, removed, same)
	}
	if !docs.Changed(ops) {
		t.Error("a diff with an addition reports no change")
	}
	if docs.Changed(docs.Diff("same", "same")) {
		t.Error("identical text reports a change")
	}
}

// A checklist is not a task, and ticking one is doing the work rather than
// changing what the work is.
func TestChecklistsNeedCommentToTickAndEditToChange(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Quarterly service",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, step := range []string{"Lock off at the panel", "Verify dead", "Fit the bearing"} {
		if _, err := f.svc.AddChecklistItem(ctx, f.org, alice, d.ID, step, ""); err != nil {
			t.Fatalf("add %q: %v", step, err)
		}
	}

	items, err := f.svc.Checklist(ctx, f.org, alice, d.ID)
	if err != nil {
		t.Fatalf("checklist: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("%d items, want 3", len(items))
	}
	// In the order they were added, which is the order somebody works through.
	if items[0].Text != "Lock off at the panel" || items[2].Text != "Fit the bearing" {
		t.Errorf("items came back out of order: %+v", items)
	}

	// Bob can comment, so Bob can tick, and ticking records who.
	if err := f.az.Grant(ctx, f.org, d.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessComment, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	bob := f.subj(t, f.bob)
	if err := f.svc.SetChecklistItemDone(ctx, f.org, bob, items[0].ID, true); err != nil {
		t.Fatalf("tick: %v", err)
	}
	items, _ = f.svc.Checklist(ctx, f.org, alice, d.ID)
	if !items[0].Done() {
		t.Fatal("the item did not tick")
	}
	if items[0].DoneBy == nil || *items[0].DoneBy != f.bob {
		t.Error("the tick does not record who did it, so the record is a lie")
	}
	if p := docs.Progress(items); p.Done != 1 || p.Total != 3 || p.Complete() {
		t.Errorf("progress = %+v", p)
	}

	// But he cannot rewrite the procedure.
	if err := f.svc.RemoveChecklistItem(ctx, f.org, bob, items[1].ID); err == nil {
		t.Error("comment access was enough to delete a step from the procedure")
	}
	if err := f.svc.RemoveChecklistItem(ctx, f.org, alice, items[1].ID); err != nil {
		t.Fatalf("the owner could not remove a step: %v", err)
	}

	// Unticking clears the name with the time, or the history says the work
	// happened and nobody did it.
	if err := f.svc.SetChecklistItemDone(ctx, f.org, alice, items[0].ID, false); err != nil {
		t.Fatalf("untick: %v", err)
	}
	items, _ = f.svc.Checklist(ctx, f.org, alice, d.ID)
	if items[0].Done() || items[0].DoneBy != nil {
		t.Errorf("unticking left %+v", items[0])
	}
}

// A daily report is filed under a space like any other document, and the
// per-space list (Search with Kind+SpaceID) finds it there and nowhere else
// -- the whole point of a dedicated section instead of a filter nobody knows
// to type.
func TestADailyReportIsFoundByItsSpace(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	var spaceID string
	if err := f.pools.Web.QueryRow(ctx, `
		INSERT INTO spaces (id, org_id, name, kind, created_by)
		VALUES (gen_random_uuid(), $1, 'Website Redesign', 'project', $2)
		RETURNING id::text`, f.org, f.alice).Scan(&spaceID); err != nil {
		t.Fatalf("create space: %v", err)
	}

	inSpace, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindDailyReport, Title: "Daily report — 2026-09-24",
		Body: "Fixed the homepage hero.", SpaceID: &spaceID,
	})
	if err != nil {
		t.Fatalf("create in space: %v", err)
	}
	// A different day: one report per person per day is a rule now
	// (migration 00044), and this test is about where a report is filed.
	yesterday := time.Now().AddDate(0, 0, -1)
	if _, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindDailyReport, Title: "Daily report — elsewhere",
		ReportDate: &yesterday,
	}); err != nil {
		t.Fatalf("create elsewhere: %v", err)
	}

	found, err := f.svc.Search(ctx, f.org, alice, docs.Query{
		Kind: docs.KindDailyReport, SpaceID: &spaceID, Limit: 10,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 1 || found[0].ID != inSpace.ID {
		t.Fatalf("search returned %d rows, want exactly the one filed in this space: %+v", len(found), found)
	}
	if !docs.Writable(docs.KindDailyReport) {
		t.Error("a daily report should be a writable kind, same as a plain doc")
	}
}

// A step's description is optional and independent of its text -- a step
// added with nothing but a title should not grow one from thin air, and one
// added with a description should get back exactly what was typed.
func TestChecklistItemDescriptionIsOptional(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindTask, Title: "Deploy",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	plain, err := f.svc.AddChecklistItem(ctx, f.org, alice, d.ID, "Ship it", "")
	if err != nil {
		t.Fatalf("add plain: %v", err)
	}
	detailed, err := f.svc.AddChecklistItem(ctx, f.org, alice, d.ID, "Cut the release",
		"Use the staging account, not prod, and check both the email and Google paths.")
	if err != nil {
		t.Fatalf("add detailed: %v", err)
	}

	items, err := f.svc.Checklist(ctx, f.org, alice, d.ID)
	if err != nil {
		t.Fatalf("checklist: %v", err)
	}
	byID := map[string]docs.ChecklistItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	if got := byID[plain.ID].Description; got != "" {
		t.Errorf("plain step got a description from nowhere: %q", got)
	}
	if got, want := byID[detailed.ID].Description, "Use the staging account, not prod, and check both the email and Google paths."; got != want {
		t.Errorf("description = %q, want %q", got, want)
	}
}

// A template produces a document with its properties set. That is the one
// people notice is missing: a procedure whose asset tag has to be retyped is a
// template nobody uses twice.
func TestATemplateProducesADocumentWithItsPropertiesSet(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	if err := f.svc.EnsureTemplateProperties(ctx, f.org); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tag := f.seedProperty(t, "asset_tag", "Asset tag", docs.PropText, nil)
	crit := f.seedProperty(t, "criticality", "Criticality", docs.PropSelect,
		[]string{"low", "high", "safety"})

	tmpl, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindTemplate, Title: "Quarterly service",
		Body: "# Quarterly service\n\nIsolate, inspect, regrease.",
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	f.setProperty(t, tmpl.ID, tag, `"CV-3"`)
	f.setProperty(t, tmpl.ID, crit, `"safety"`)
	for _, step := range []string{"Lock off", "Inspect the belt", "Regrease"} {
		if _, err := f.svc.AddChecklistItem(ctx, f.org, alice, tmpl.ID, step, ""); err != nil {
			t.Fatalf("add step: %v", err)
		}
	}
	// One step already ticked on the template, which must NOT copy across.
	items, _ := f.svc.Checklist(ctx, f.org, alice, tmpl.ID)
	if err := f.svc.SetChecklistItemDone(ctx, f.org, alice, items[0].ID, true); err != nil {
		t.Fatalf("tick: %v", err)
	}

	made, err := f.svc.Instantiate(ctx, f.org, alice, docs.InstantiateParams{
		TemplateID: tmpl.ID, Title: "Quarterly service: Conveyor 3",
		Kind: docs.KindWorkOrder,
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if made.Kind != docs.KindWorkOrder {
		t.Errorf("produced a %s", made.Kind)
	}
	if made.Title != "Quarterly service: Conveyor 3" {
		t.Errorf("title = %q", made.Title)
	}
	if made.Body != tmpl.Body {
		t.Errorf("the body did not copy:\n%q", made.Body)
	}
	if made.Task == nil || made.Task.Status != docs.StatusTodo {
		t.Errorf("a work order came out with no lifecycle: %+v", made.Task)
	}

	props, err := f.svc.PropertiesFor(ctx, f.org, docs.KindWorkOrder, made.ID)
	if err != nil {
		t.Fatalf("properties: %v", err)
	}
	got := map[string]string{}
	for _, p := range props {
		got[p.Key] = p.Value
	}
	if got["asset_tag"] != "CV-3" {
		t.Errorf("asset tag came out %q", got["asset_tag"])
	}
	if got["criticality"] != "safety" {
		t.Errorf("criticality came out %q", got["criticality"])
	}
	// The field that says what the template makes describes the template, not
	// the thing, so it must not travel.
	if got[docs.TemplateKindKey] != "" {
		t.Errorf("the template's own Creates field was copied onto the work order")
	}

	steps, err := f.svc.Checklist(ctx, f.org, alice, made.ID)
	if err != nil {
		t.Fatalf("checklist: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("%d steps copied, want 3", len(steps))
	}
	for _, s := range steps {
		if s.Done() {
			t.Errorf("a step arrived already ticked: %q", s.Text)
		}
	}

	// And it remembers where it came from, so somebody looking at this in six
	// months can find the procedure and fix it there.
	linked, err := f.svc.Linked(ctx, f.org, alice, made.ID)
	if err != nil {
		t.Fatalf("linked: %v", err)
	}
	found := false
	for _, l := range linked {
		if l.ID == tmpl.ID {
			found = true
		}
	}
	if !found {
		t.Error("the document does not link back to the template it came from")
	}

	// A template cannot make a template.
	loop, err := f.svc.Instantiate(ctx, f.org, alice, docs.InstantiateParams{
		TemplateID: tmpl.ID, Kind: docs.KindTemplate,
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if loop.Kind == docs.KindTemplate {
		t.Error("a template produced another template")
	}

	// Instantiating something that is not a template is refused.
	if _, err := f.svc.Instantiate(ctx, f.org, alice, docs.InstantiateParams{
		TemplateID: made.ID,
	}); err == nil {
		t.Error("an ordinary work order was used as a template")
	}
}

// Templates are permission filtered like everything else.
func TestTemplatesAreListedOnlyToPeopleWhoCanSeeThem(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	if _, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindTemplate, Title: "Hers alone",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	mine, err := f.svc.Templates(ctx, f.org, alice)
	if err != nil {
		t.Fatalf("templates: %v", err)
	}
	if len(mine) != 1 {
		t.Fatalf("%d templates for the owner", len(mine))
	}
	theirs, err := f.svc.Templates(ctx, f.org, f.subj(t, f.dave))
	if err != nil {
		t.Fatalf("templates as dave: %v", err)
	}
	if len(theirs) != 0 {
		t.Fatalf("a stranger sees %d templates", len(theirs))
	}
}
