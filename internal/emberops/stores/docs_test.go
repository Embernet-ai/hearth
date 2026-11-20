package stores_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
)

// spine stands in for the Postgres docs service: it keeps the spine rows the
// store reads back and records what was asked of it. Methods it does not
// override panic on the nil embedded interface, which is the point: the store
// must not call anything these tests do not account for.
type spine struct {
	docs.Store
	rows    map[string]*docs.Document
	deleted []string
	n       int
	parents map[string]string

	marks     map[string]string
	props     map[string]map[string]string
	ensured   int
	applied   []docs.ExternalChange
	appliedID []string
	items     map[string]string // checklist item -> document
	ticked    map[string]bool
	removed   []string
}

func (p *spine) PropertyValuesFor(_ context.Context, _ string, ids []string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for _, id := range ids {
		if v, ok := p.props[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

func (p *spine) SetProperties(_ context.Context, _ string, _ core.Subjects, id string, v map[string]string) error {
	p.props[id] = v
	return nil
}

func (p *spine) PropertyDefsFor(_ context.Context, _, kind string) ([]docs.PropertyDef, error) {
	all := []docs.PropertyDef{{ID: "d1", Key: "criticality", Label: "Criticality", DataType: "select", Options: []string{"A", "B"}},
		{ID: "d2", Key: "vendor-tag", Label: "Vendor tag", DataType: "text"}}
	if kind == docs.KindAsset || kind == "" {
		return all, nil
	}
	if kind == docs.KindTask {
		return all[1:], nil
	}
	return nil, nil
}

func (p *spine) EnsureAssetProperties(context.Context, string) error { p.ensured++; return nil }
func (p *spine) Link(context.Context, string, core.Subjects, core.Ref, core.Ref, string) error {
	return nil
}
func (p *spine) Unlink(context.Context, string, core.Subjects, string, string) error { return nil }

func newSpine() *spine {
	return &spine{rows: map[string]*docs.Document{}, marks: map[string]string{}, props: map[string]map[string]string{}, items: map[string]string{}, ticked: map[string]bool{}}
}

func (p *spine) ExternalMarks(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if m, ok := p.marks[id]; ok {
			out[id] = m
		}
	}
	return out, nil
}

func (p *spine) SetExternalMarks(_ context.Context, m map[string]string) error {
	for k, v := range m {
		p.marks[k] = v
	}
	return nil
}

func (p *spine) ApplyExternal(_ context.Context, _ string, id string, c docs.ExternalChange) error {
	p.applied, p.appliedID = append(p.applied, c), append(p.appliedID, id)
	p.marks[id] = c.Modified
	return nil
}

func (p *spine) ChecklistItemDoc(_ context.Context, _, item string) (string, error) {
	if d, ok := p.items[item]; ok {
		return d, nil
	}
	return "", docs.ErrNotFound
}

func (p *spine) SetChecklistItemDone(_ context.Context, _ string, _ core.Subjects, item string, done bool) error {
	p.ticked[item] = done
	return nil
}

func (p *spine) RemoveChecklistItem(_ context.Context, _ string, _ core.Subjects, item string) error {
	p.removed = append(p.removed, item)
	return nil
}

func (p *spine) Create(_ context.Context, _ string, _ core.Subjects, cp docs.CreateParams) (*docs.Document, error) {
	p.n++
	id := fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012d", p.n)
	d := &docs.Document{ID: id, Kind: cp.Kind, Title: cp.Title, Body: cp.Body, BodyFormat: "markdown", SpaceID: cp.SpaceID, Version: 1}
	if docs.HasTaskFields(cp.Kind) {
		t := docs.TaskFields{Status: docs.StatusTodo}
		if cp.Task != nil {
			t = *cp.Task
		}
		d.Task = &t
	}
	p.rows[id] = d
	return d, nil
}

func (p *spine) Get(_ context.Context, _ string, _ core.Subjects, id string) (*docs.Document, error) {
	d, ok := p.rows[id]
	if !ok {
		return nil, docs.ErrNotFound
	}
	cp := *d
	if d.Task != nil {
		t := *d.Task
		cp.Task = &t
	}
	return &cp, nil
}

func (p *spine) GetAsSystem(ctx context.Context, _ string, id string) (*docs.Document, error) {
	return p.Get(ctx, "", core.Subjects{}, id)
}

func (p *spine) Update(ctx context.Context, _ string, _ core.Subjects, id string, up docs.UpdateParams) (*docs.Document, error) {
	d := p.rows[id]
	if up.Task != nil && up.Task.Status != nil {
		// What the real service does inside its transaction: open subtasks
		// follow the new status, finished and cancelled ones stay.
		to := *up.Task.Status
		var ids []string
		for kid, parent := range p.parents {
			if st := p.rows[kid].Task.Status; parent == id && !docs.IsClosedStatus(st) && st != to {
				p.rows[kid].Task.Status = to
				ids = append(ids, kid)
			}
		}
		docs.NoteCascaded(ctx, to, ids)
	}
	if up.Title != nil {
		d.Title = *up.Title
	}
	if up.Task != nil && up.Task.Status != nil {
		d.Task.Status = *up.Task.Status
	}
	d.Version++
	return p.Get(nil, "", core.Subjects{}, id)
}

func (p *spine) Delete(_ context.Context, _ string, _ core.Subjects, id string) error {
	p.deleted = append(p.deleted, id)
	delete(p.rows, id)
	return nil
}

func (p *spine) Assignees(context.Context, string) ([]string, error) { return []string{ann}, nil }
func (p *spine) Checklist(context.Context, string, core.Subjects, string) ([]docs.ChecklistItem, error) {
	return []docs.ChecklistItem{{ID: "c1", Position: 1, Text: "lock out"}}, nil
}

func (p *spine) Search(_ context.Context, _ string, _ core.Subjects, _ docs.Query) ([]*docs.Row, error) {
	var out []*docs.Row
	for _, d := range p.rows {
		cp := *d
		t := *d.Task
		cp.Task = &t
		out = append(out, &docs.Row{Document: &cp})
	}
	return out, nil
}

func actor(id string) core.Subjects {
	return core.Subjects{{Kind: core.SubjectPrincipal, ID: id}}
}

func TestDocsCreateWritesTheSpineThenEmberOPS(t *testing.T) {
	e := newEnv(t)
	pg := newSpine()
	s := &stores.Docs{Store: pg, Base: e.base, Dir: e.dir}

	d, err := s.Create(e.as("ann-tok"), org, actor(ann), docs.CreateParams{Kind: docs.KindTask, Title: "Fix belt", Task: &docs.TaskFields{Status: docs.StatusInProgress, Priority: 2}})
	if err != nil {
		t.Fatal(err)
	}
	tasks := e.f.All("Task")
	if len(tasks) != 1 {
		t.Fatalf("tasks in EmberOPS: %v", tasks)
	}
	got := tasks[0]
	if got["hearth_id"] != d.ID || got["subject"] != "Fix belt" || got["status"] != "Working" || got["priority"] != "High" {
		t.Fatalf("task = %v", got)
	}
	// The assignee goes as an email for _assign, and the checklist rides along.
	if as, _ := got["_assign"].([]any); len(as) != 1 || as[0] != "ann@example.com" {
		t.Fatalf("_assign = %v", got["_assign"])
	}
	if cl, _ := got["hearth_checklist"].([]any); len(cl) != 1 {
		t.Fatalf("checklist = %v", got["hearth_checklist"])
	}
	// Written as the person, not the service.
	for i, c := range e.f.Calls {
		if e.f.Seen[i] != "ann-tok" && e.f.Users[i] != "ann@example.com" && !strings.Contains(c, "/api/resource/User") && !strings.Contains(c, "/api/resource/Employee") {
			t.Fatalf("call %q acted as %q", c, e.f.Users[i])
		}
	}
}

func TestDocsCreateIsUndoneWhenEmberOPSRefuses(t *testing.T) {
	e := newEnv(t)
	pg := newSpine()
	s := &stores.Docs{Store: pg, Base: e.base, Dir: e.dir}

	_, err := s.Create(e.as("bad-token"), org, actor(ann), docs.CreateParams{Kind: docs.KindTask, Title: "x"})
	if err == nil {
		t.Fatal("expected EmberOPS to refuse")
	}
	if len(pg.deleted) != 1 || len(pg.rows) != 0 {
		t.Fatalf("spine row left behind: deleted=%v rows=%v", pg.deleted, pg.rows)
	}
}

func TestDocsUpdateAndDeleteReachEmberOPS(t *testing.T) {
	e := newEnv(t)
	pg := newSpine()
	s := &stores.Docs{Store: pg, Base: e.base, Dir: e.dir}
	ctx := e.as("ann-tok")

	d, _ := s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindWorkOrder, Title: "PM belt"})
	done := docs.StatusDone
	if _, err := s.Update(ctx, org, actor(ann), d.ID, docs.UpdateParams{Task: &docs.TaskPatch{Status: &done}}); err != nil {
		t.Fatal(err)
	}
	wo := e.f.All("Maintenance Work Order")
	if len(wo) != 1 || wo[0]["status"] != "Completed" || wo[0]["title"] != "PM belt" {
		t.Fatalf("work order = %v", wo)
	}
	if err := s.Delete(ctx, org, actor(ann), d.ID); err != nil {
		t.Fatal(err)
	}
	if wo = e.f.All("Maintenance Work Order"); wo[0]["hearth_deleted_at"] == nil {
		t.Fatalf("not soft-deleted in EmberOPS: %v", wo[0])
	}
}

func TestDocsSearchShowsEmberOPSStateInTwoCalls(t *testing.T) {
	e := newEnv(t)
	pg := newSpine()
	s := &stores.Docs{Store: pg, Base: e.base, Dir: e.dir}
	ctx := e.as("ann-tok")
	var ids []string
	for _, title := range []string{"a", "b", "c", "d"} {
		d, _ := s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindTask, Title: title})
		ids = append(ids, d.ID)
	}
	// Someone finishes one in EmberOPS itself.
	for _, tk := range e.f.All("Task") {
		if tk["hearth_id"] == ids[1] {
			tk["status"] = "Completed"
			tk["subject"] = "b (renamed in EmberOPS)"
		}
	}

	before := len(e.f.Calls)
	rows, err := s.Search(ctx, org, actor(ann), docs.Query{})
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, r := range rows {
		if r.ID == ids[1] {
			seen++
			if r.Task.Status != docs.StatusDone || r.Title != "b (renamed in EmberOPS)" {
				t.Fatalf("EmberOPS state not shown: %+v %+v", r.Document, r.Task)
			}
		} else if r.Task.Status == docs.StatusDone {
			t.Fatalf("%s wrongly done", r.Title)
		}
	}
	if seen != 1 {
		t.Fatal("renamed task missing")
	}
	if calls := len(e.f.Calls) - before; calls > 2 {
		t.Fatalf("a page of 4 tasks took %d calls (want the Task list and the user directory): %v", calls, e.f.Calls[before:])
	}
}

func TestDocsGetOfAMissingDocumentIsNotFound(t *testing.T) {
	e := newEnv(t)
	s := &stores.Docs{Store: newSpine(), Base: e.base, Dir: e.dir}
	if _, err := s.Get(e.ctx, org, actor(ann), "nope"); !errors.Is(err, docs.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestDocsEditsMadeInEmberOPSReachTheSpineOnRead(t *testing.T) {
	e := newEnv(t)
	pg := newSpine()
	s := &stores.Docs{Store: pg, Base: e.base, Dir: e.dir}
	ctx := e.as("ann-tok")
	a, _ := s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindTask, Title: "a"})
	b, _ := s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindTask, Title: "b"})
	for _, d := range []*docs.Document{a, b} {
		if pg.marks[d.ID] == "" {
			t.Fatalf("Hearth's own write left no mark for %s", d.ID)
		}
	}
	// Reading with nothing changed copies nothing.
	if _, err := s.Search(ctx, org, actor(ann), docs.Query{}); err != nil {
		t.Fatal(err)
	}
	if len(pg.applied) != 0 {
		t.Fatalf("an unchanged read wrote to the spine: %+v", pg.applied)
	}

	// Bob edits task a in EmberOPS: new title, body, status, assignee.
	for _, tk := range e.f.All("Task") {
		if tk["hearth_id"] == a.ID {
			tk["subject"], tk["hearth_body"], tk["status"] = "a, reworded", "new body", "Completed"
			tk["hearth_primary_assignee"], tk["_assign"] = "bob@example.com", `["bob@example.com"]`
			tk["modified"], tk["modified_by"] = "2030-01-01 00:00:00", "bob@example.com"
		}
	}
	before := len(e.f.Calls)
	rows, err := s.Search(ctx, org, actor(ann), docs.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pg.applied) != 1 || pg.appliedID[0] != a.ID {
		t.Fatalf("copied into the spine: %v", pg.appliedID)
	}
	c := pg.applied[0]
	if c.Title != "a, reworded" || c.Body != "new body" || c.By != bob || c.Modified != "2030-01-01 00:00:00" ||
		c.Task.Status != docs.StatusDone || c.Task.AssigneeID == nil || *c.Task.AssigneeID != bob ||
		len(c.AssigneeIDs) != 1 || c.AssigneeIDs[0] != bob {
		t.Fatalf("change = %+v task=%+v", c, c.Task)
	}
	for _, r := range rows {
		if r.ID == a.ID && r.Title != "a, reworded" {
			t.Fatalf("page shows %q", r.Title)
		}
	}
	// Batched: the Task list, the directory, and one fetch of the changed bodies.
	if n := len(e.f.Calls) - before; n > 3 {
		t.Fatalf("%d calls for a page with one changed task: %v", n, e.f.Calls[before:])
	}
	// The same state is not copied twice.
	if _, err := s.Search(ctx, org, actor(ann), docs.Query{}); err != nil || len(pg.applied) != 1 {
		t.Fatalf("copied again: %d %v", len(pg.applied), err)
	}
}

func TestDocsSyncIsBoundedPerRead(t *testing.T) {
	e := newEnv(t)
	pg := newSpine()
	s := &stores.Docs{Store: pg, Base: e.base, Dir: e.dir}
	ctx := e.as("ann-tok")
	for i := 0; i < 40; i++ {
		_, _ = s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindTask, Title: "t"})
	}
	for _, tk := range e.f.All("Task") {
		tk["subject"], tk["modified"] = "edited", "2031-01-01 00:00:00"
	}
	if _, err := s.Search(ctx, org, actor(ann), docs.Query{}); err != nil {
		t.Fatal(err)
	}
	if len(pg.applied) != 25 {
		t.Fatalf("copied %d, want the bound of 25", len(pg.applied))
	}
	if _, err := s.Search(ctx, org, actor(ann), docs.Query{}); err != nil || len(pg.applied) != 40 {
		t.Fatalf("the rest were not copied on the next read: %d %v", len(pg.applied), err)
	}
}

func TestDocsGetReadsTheBodyFromEmberOPS(t *testing.T) {
	e := newEnv(t)
	pg := newSpine()
	s := &stores.Docs{Store: pg, Base: e.base, Dir: e.dir}
	ctx := e.as("ann-tok")
	d, _ := s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindDoc, Title: "SOP", Body: "old"})
	for _, h := range e.f.All("Hearth Document") {
		h["body"], h["title"] = "edited in EmberOPS", "SOP v2"
	}
	got, err := s.Get(ctx, org, actor(ann), d.ID)
	if err != nil || got.Body != "edited in EmberOPS" || got.Title != "SOP v2" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestChecklistTickAndRemovePushAtOnce(t *testing.T) {
	e := newEnv(t)
	pg := newSpine()
	s := &stores.Docs{Store: pg, Base: e.base, Dir: e.dir}
	ctx := e.as("ann-tok")
	d, _ := s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindTask, Title: "x"})
	pg.items["c1"] = d.ID
	before := len(e.f.Calls)
	if err := s.SetChecklistItemDone(ctx, org, actor(ann), "c1", true); err != nil {
		t.Fatal(err)
	}
	if !pg.ticked["c1"] {
		t.Fatal("spine not ticked")
	}
	if n := e.f.CallCount("emberops_hearth.api.upsert"); n != 2 {
		t.Fatalf("upserts after the tick = %d (create + tick): %v", n, e.f.Calls[before:])
	}
	if err := s.RemoveChecklistItem(ctx, org, actor(ann), "c1"); err != nil || len(pg.removed) != 1 {
		t.Fatalf("%v %v", err, pg.removed)
	}
	if n := e.f.CallCount("emberops_hearth.api.upsert"); n != 3 {
		t.Fatalf("upserts after the remove = %d", n)
	}
	if err := s.SetChecklistItemDone(ctx, org, actor(ann), "nope", true); !errors.Is(err, docs.ErrNotFound) {
		t.Fatalf("unknown item: %v", err)
	}
}

func TestPropertyValuesTravelWithTheDocumentAndDefinitionsAreCopiedOnce(t *testing.T) {
	e := newEnv(t)
	pg := newSpine()
	s := &stores.Docs{Store: pg, Base: e.base, Dir: e.dir}
	ctx := e.as("ann-tok")
	d, _ := s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindAsset, Title: "Press"})
	if err := s.SetProperties(ctx, org, actor(ann), d.ID, map[string]string{"criticality": "A"}); err != nil {
		t.Fatal(err)
	}
	a := e.f.All("Asset")[0]
	if a["hearth_properties"] != `{"criticality":"A"}` {
		t.Fatalf("asset properties = %v", a["hearth_properties"])
	}

	for i := 0; i < 3; i++ { // a page load each
		if err := s.EnsureAssetProperties(ctx, org); err != nil {
			t.Fatal(err)
		}
	}
	defs := e.f.All("Hearth Property Def")
	if len(defs) != 2 || e.f.CallCount("bulk_upsert") != 1 {
		t.Fatalf("defs = %v, bulk calls %d", defs, e.f.CallCount("bulk_upsert"))
	}
	for _, def := range defs {
		if def["prop_key"] == "criticality" && (def["applies_to"] != "asset" || def["options_json"] != `["A","B"]`) {
			t.Fatalf("def = %v", def)
		}
		if def["prop_key"] == "vendor-tag" && def["applies_to"] != "task,asset" && def["applies_to"] != "asset,task" {
			t.Fatalf("def = %v", def)
		}
	}
}

func TestLinksAreCopiedWithADerivedIdAndRemoved(t *testing.T) {
	e := newEnv(t)
	pg := newSpine()
	s := &stores.Docs{Store: pg, Base: e.base, Dir: e.dir}
	ctx := e.as("ann-tok")
	wo, _ := s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindWorkOrder, Title: "PM"})
	as, _ := s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindAsset, Title: "Press"})
	from, to := core.Ref{Type: core.EntityDocument, ID: wo.ID}, core.Ref{Type: core.EntityDocument, ID: as.ID}
	for i := 0; i < 2; i++ { // linking twice is one link
		if err := s.Link(ctx, org, actor(ann), from, to, docs.LinkConcerns); err != nil {
			t.Fatal(err)
		}
	}
	links := e.f.All("Hearth Entity Link")
	if len(links) != 1 || links[0]["from_doctype"] != "Maintenance Work Order" || links[0]["to_doctype"] != "Asset" ||
		links[0]["link_kind"] != "concerns" || links[0]["from_name"] != wo.ID && links[0]["from_name"] == "" {
		t.Fatalf("links = %v", links)
	}
	// A link to a file has no EmberOPS equivalent and is not copied.
	if err := s.Link(ctx, org, actor(ann), from, core.Ref{Type: core.EntityFile, ID: "f1"}, docs.LinkMention); err != nil {
		t.Fatal(err)
	}
	if len(e.f.All("Hearth Entity Link")) != 1 {
		t.Fatal("a file link was copied")
	}
	if err := s.Unlink(ctx, org, actor(ann), as.ID, wo.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(e.f.All("Hearth Entity Link")); n != 0 {
		t.Fatalf("%d links left after unlink", n)
	}
}

// A close that took subtasks with it reaches EmberOPS for each of them, as the
// service client, or the next read would show them open and copy that back.
func TestDocsClosingATaskSendsItsClosedSubtasksToEmberOPS(t *testing.T) {
	e := newEnv(t)
	pg := newSpine()
	pg.parents = map[string]string{}
	s := &stores.Docs{Store: pg, Base: e.base, Dir: e.dir}
	ctx := e.as("ann-tok")

	parent, _ := s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindTask, Title: "Ignition"})
	kid, _ := s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindTask, Title: "Alice"})
	pg.parents[kid.ID] = parent.ID

	cctx, sink := docs.WithClosedSink(ctx)
	done := docs.StatusDone
	if _, err := s.Update(cctx, org, actor(ann), parent.ID, docs.UpdateParams{Task: &docs.TaskPatch{Status: &done}}); err != nil {
		t.Fatal(err)
	}
	if len(sink.IDs) != 1 || sink.IDs[0] != kid.ID {
		t.Fatalf("the caller's sink should name the subtask: %v", sink.IDs)
	}
	got := map[string]any{}
	for _, tk := range e.f.All("Task") {
		got[tk["hearth_id"].(string)] = tk["status"]
	}
	if got[parent.ID] != "Completed" || got[kid.ID] != "Completed" {
		t.Fatalf("EmberOPS statuses: %v", got)
	}
}

// Every status a parent is given reaches EmberOPS for its open subtasks, a
// finished one is not sent again, and a subtask's own change sends nothing for
// its parent.
func TestDocsAnyStatusOnAParentSendsItsOpenSubtasksToEmberOPS(t *testing.T) {
	for _, tc := range []struct{ to, ops string }{
		{docs.StatusCancelled, "Cancelled"}, {docs.StatusInProgress, "Working"}, {docs.StatusBlocked, "Blocked"},
	} {
		t.Run(tc.to, func(t *testing.T) {
			e := newEnv(t)
			pg := newSpine()
			pg.parents = map[string]string{}
			s := &stores.Docs{Store: pg, Base: e.base, Dir: e.dir}
			ctx := e.as("ann-tok")

			parent, _ := s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindTask, Title: "Ignition"})
			open, _ := s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindTask, Title: "Alice"})
			finished, _ := s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindTask, Title: "Dave"})
			pg.parents[open.ID], pg.parents[finished.ID] = parent.ID, parent.ID
			done := docs.StatusDone
			if _, err := s.Update(ctx, org, actor(ann), finished.ID, docs.UpdateParams{Task: &docs.TaskPatch{Status: &done}}); err != nil {
				t.Fatal(err)
			}

			cctx, sink := docs.WithClosedSink(ctx)
			to := tc.to
			if _, err := s.Update(cctx, org, actor(ann), parent.ID, docs.UpdateParams{Task: &docs.TaskPatch{Status: &to}}); err != nil {
				t.Fatal(err)
			}
			if len(sink.IDs) != 1 || sink.IDs[0] != open.ID || sink.Status != tc.to {
				t.Fatalf("the sink should name the open subtask and %s: %v %q", tc.to, sink.IDs, sink.Status)
			}
			got := map[string]any{}
			for _, tk := range e.f.All("Task") {
				got[tk["hearth_id"].(string)] = tk["status"]
			}
			if got[parent.ID] != tc.ops || got[open.ID] != tc.ops || got[finished.ID] != "Completed" {
				t.Fatalf("EmberOPS statuses: %v", got)
			}

			// A subtask's own change: its parent is not sent, so EmberOPS keeps
			// the parent's status as it was.
			back := docs.StatusTodo
			if _, err := s.Update(ctx, org, actor(ann), open.ID, docs.UpdateParams{Task: &docs.TaskPatch{Status: &back}}); err != nil {
				t.Fatal(err)
			}
			got = map[string]any{}
			for _, tk := range e.f.All("Task") {
				got[tk["hearth_id"].(string)] = tk["status"]
			}
			if got[open.ID] != "Open" || got[parent.ID] != tc.ops || got[finished.ID] != "Completed" {
				t.Fatalf("a subtask's change moved something else: %v", got)
			}
		})
	}
}

// A read made by a member of staff can come back without anyone's hearth_id (staff hold only Select on User). Copying such
// a task into the spine cleared its assignees, so tasks vanished from "assigned to me". An edit that names someone this read
// cannot identify is shown, and copied on a read that can.
func TestDocsEditNamingSomeoneUnknownIsNotCopiedIntoTheSpine(t *testing.T) {
	e := newEnv(t)
	pg := newSpine()
	s := &stores.Docs{Store: pg, Base: e.base, Dir: e.dir}
	ctx := e.as("ann-tok")
	a, _ := s.Create(ctx, org, actor(ann), docs.CreateParams{Kind: docs.KindTask, Title: "a"})
	for _, tk := range e.f.All("Task") {
		if tk["hearth_id"] == a.ID {
			tk["subject"] = "a, reworded"
			tk["hearth_primary_assignee"], tk["_assign"] = "ghost@example.com", `["ghost@example.com"]`
			tk["modified"] = "2030-01-01 00:00:00"
		}
	}
	mark := pg.marks[a.ID]
	rows, err := s.Search(ctx, org, actor(ann), docs.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pg.applied) != 0 || pg.marks[a.ID] != mark {
		t.Fatalf("an edit naming an unknown person was copied (it would clear the assignees): %+v", pg.applied)
	}
	if len(rows) != 1 || rows[0].Title != "a, reworded" {
		t.Fatalf("the page must still show EmberOps's state: %+v", rows)
	}
}
