package docs_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
)

// A task as a folder: subtasks land where their parent is, whoever reaches the
// parent reaches them, and nothing flows up -- holding a subtask of your own
// shows you nothing of the parent or of anybody else's subtask beside it.
func TestASubtaskLivesInsideItsParent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, dave := f.subj(t, f.alice), f.subj(t, f.dave)

	space := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx, `INSERT INTO spaces (id, org_id, name, created_by) VALUES ($1,$2,'Training',$3)`,
		space, f.org, f.alice); err != nil {
		t.Fatal(err)
	}
	parent, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindTask, Title: "Ignition", SpaceID: &space})
	if err != nil {
		t.Fatal(err)
	}
	mine, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindTask, Title: "Alice", ParentID: &parent.ID})
	if err != nil {
		t.Fatalf("could not add a subtask: %v", err)
	}
	if mine.ParentID == nil || *mine.ParentID != parent.ID {
		t.Fatalf("the subtask does not know its parent: %+v", mine.ParentID)
	}
	if mine.SpaceID == nil || *mine.SpaceID != space {
		t.Fatalf("a subtask should land in its parent's space, got %v", mine.SpaceID)
	}
	// Read back through the ordinary path, which carries parent_id too.
	got, err := f.svc.Get(ctx, f.org, alice, mine.ID)
	if err != nil || got.ParentID == nil || *got.ParentID != parent.ID {
		t.Fatalf("Get lost the parent: %v %+v", err, got)
	}

	// Dave owns a subtask of his own under the same parent (filed by Alice,
	// then handed to him by a grant -- the member case).
	daves, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindTask, Title: "Dave", ParentID: &parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pools.Web.Exec(ctx, `INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		VALUES (gen_random_uuid(), $1, 'document', $2, $3, 'edit', $4)`,
		f.org, daves.ID, core.Subject{Kind: core.SubjectPrincipal, ID: f.dave}.Key(), f.alice); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Get(ctx, f.org, dave, daves.ID); err != nil {
		t.Fatalf("Dave cannot open his own subtask: %v", err)
	}
	if _, err := f.svc.Get(ctx, f.org, dave, parent.ID); err == nil {
		t.Fatal("holding a subtask let Dave open its parent")
	}
	kids, err := f.svc.Children(ctx, f.org, dave, parent.ID)
	if err != nil || len(kids) != 1 || kids[0].ID != daves.ID {
		t.Fatalf("Dave should see only his own subtask: %v %d", err, len(kids))
	}
	all, err := f.svc.Children(ctx, f.org, alice, parent.ID)
	if err != nil || len(all) != 2 || all[0].Title != "Alice" {
		t.Fatalf("Alice should see both, in the order they were made: %v %d", err, len(all))
	}

	// Dave cannot hang new work under a parent he cannot see.
	if _, err := f.svc.Create(ctx, f.org, dave, docs.CreateParams{Kind: docs.KindTask, Title: "sneak", ParentID: &parent.ID}); !errors.Is(err, docs.ErrNotFound) {
		t.Fatalf("Dave filed under a parent he cannot see: %v", err)
	}
}

// Moving a task under another refuses a loop at any depth, and an empty parent
// takes it back out.
func TestSetParentRefusesALoop(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	mk := func(title string, parent *string) string {
		t.Helper()
		d, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindTask, Title: title, ParentID: parent})
		if err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	a := mk("A", nil)
	b := mk("B", &a)
	c := mk("C", &b)

	for name, move := range map[string][2]string{
		"itself":       {a, a},
		"its child":    {a, b},
		"a grandchild": {a, c},
	} {
		if err := f.svc.SetParent(ctx, f.org, alice, move[0], move[1]); !errors.Is(err, docs.ErrParentLoop) {
			t.Errorf("putting a task inside %s was not refused: %v", name, err)
		}
	}
	if err := f.svc.SetParent(ctx, f.org, alice, c, a); err != nil {
		t.Fatalf("a legal move was refused: %v", err)
	}
	if err := f.svc.SetParent(ctx, f.org, alice, c, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(ctx, f.org, alice, c)
	if got.ParentID != nil {
		t.Fatalf("an empty parent should take the task out, got %v", *got.ParentID)
	}
}

// Migration 00041 turns the ClickUp import's parent links into parents, and
// only those: a link made by hand, a link between tasks that did not come from
// ClickUp, and a child with two candidate parents are all left alone. The
// backfill statement is taken from the migration itself and run again over
// data seeded the way an import leaves it.
func TestImportedParentLinksBecomeParents(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	raw, err := os.ReadFile("../../migrations/00041_subtasks.sql")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, "WITH candidates AS")
	end := strings.Index(src, "-- +goose Down")
	if start < 0 || end < start {
		t.Fatal("could not find the backfill in the migration")
	}
	backfill := src[start:end]

	mk := func(title string) string {
		t.Helper()
		d, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindTask, Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	imported := func(id string, at time.Time) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, `INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id, imported_at)
			VALUES ($1, 'clickup', $2, 'document', $3, $4)`, f.org, "cu-"+id[:8], id, at); err != nil {
			t.Fatal(err)
		}
	}
	link := func(from, to string, at time.Time) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, `INSERT INTO entity_links (id, org_id, from_type, from_id, to_type, to_id, kind, created_by, created_at)
			VALUES (gen_random_uuid(), $1, 'document', $2, 'document', $3, 'concerns', $4, $5)`, f.org, from, to, f.alice, at); err != nil {
			t.Fatal(err)
		}
	}
	importRun := time.Now().Add(-48 * time.Hour)

	course, person := mk("Ignition"), mk("Luis") // the import's own link
	imported(course, importRun)
	imported(person, importRun)
	link(course, person, importRun.Add(20*time.Minute))

	related, other := mk("Codesys"), mk("Notes") // linked by hand, a day later
	imported(related, importRun)
	imported(other, importRun)
	link(related, other, importRun.Add(24*time.Hour))

	local, localKid := mk("Local"), mk("Local child") // never came from ClickUp
	link(local, localKid, time.Now())

	p1, p2, twice := mk("P1"), mk("P2"), mk("Two parents") // ambiguous
	for _, id := range []string{p1, p2, twice} {
		imported(id, importRun)
	}
	link(p1, twice, importRun.Add(time.Minute))
	link(p2, twice, importRun.Add(time.Minute))

	if _, err := f.pools.Web.Exec(ctx, backfill); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	parentOf := func(id string) string {
		t.Helper()
		d, err := f.svc.Get(ctx, f.org, alice, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.ParentID == nil {
			return ""
		}
		return *d.ParentID
	}
	if parentOf(person) != course {
		t.Error("the import's own parent link did not become a parent")
	}
	if parentOf(other) != "" {
		t.Error("a link made by hand a day after the import became a parent")
	}
	if parentOf(localKid) != "" {
		t.Error("a link between tasks that never came from ClickUp became a parent")
	}
	if parentOf(twice) != "" {
		t.Error("a child with two candidate parents was given one")
	}
}

// closeTree is the folder from the request: Parent has two open subtasks, A and
// B-done; A has an open child A1 and a finished child A2; C was cancelled and
// has an open child C1 of its own. Closing Parent must close A, A1 and C1, and
// leave B-done, A2 and C exactly as they were.
type closeTree struct {
	parent, a, a1, a2, bDone, c, c1 string
}

func (f *fx) closeTree(t *testing.T, actor core.Subjects) closeTree {
	t.Helper()
	ctx := context.Background()
	mk := func(title string, parent *string) string {
		t.Helper()
		d, err := f.svc.Create(ctx, f.org, actor, docs.CreateParams{Kind: docs.KindTask, Title: title, ParentID: parent})
		if err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	set := func(id, status string) {
		t.Helper()
		if _, err := f.svc.Update(ctx, f.org, actor, id, docs.UpdateParams{Task: &docs.TaskPatch{Status: &status}}); err != nil {
			t.Fatal(err)
		}
	}
	var c closeTree
	c.parent = mk("Ignition", nil)
	c.a = mk("A", &c.parent)
	c.a1 = mk("A1", &c.a)
	c.a2 = mk("A2", &c.a)
	c.bDone = mk("B", &c.parent)
	c.c = mk("C", &c.parent)
	set(c.a2, docs.StatusDone)
	set(c.bDone, docs.StatusDone)
	set(c.c, docs.StatusCancelled)
	// C1 is filed under C after C was cancelled: cancelling a task now takes
	// its open subtasks with it, so an open task beneath a cancelled one has to
	// arrive afterwards (a move or a late add), which is the case the walk must
	// still reach.
	c.c1 = mk("C1", &c.c)
	set(c.a1, docs.StatusInProgress)
	d, err := f.svc.Get(ctx, f.org, actor, c.bDone)
	if err != nil {
		t.Fatal(err)
	}
	_ = d
	return c
}

type snap struct {
	status  string
	version int64
	done    time.Time
}

func (f *fx) snap(t *testing.T, actor core.Subjects, id string) snap {
	t.Helper()
	d, err := f.svc.Get(context.Background(), f.org, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	var at time.Time
	if d.Task.CompletedAt != nil {
		at = *d.Task.CompletedAt
	}
	return snap{d.Task.Status, d.Version, at}
}

// checkClosed asserts the whole outcome of closing tree.parent as who.
func (f *fx) checkClosed(t *testing.T, actor core.Subjects, who string, c closeTree, before map[string]snap, closed []string) {
	t.Helper()
	ctx := context.Background()
	for name, id := range map[string]string{"A": c.a, "A1": c.a1, "C1": c.c1} {
		s := f.snap(t, actor, id)
		if s.status != docs.StatusDone || s.done.IsZero() {
			t.Errorf("%s should be done with a completed date, got %+v", name, s)
		}
		if s.version != before[id].version+1 {
			t.Errorf("%s should have its own new version, was %d now %d", name, before[id].version, s.version)
		}
		var versions int
		if err := f.pools.Web.QueryRow(ctx, `SELECT count(*) FROM document_versions WHERE document_id=$1 AND version=$2`,
			id, s.version).Scan(&versions); err != nil || versions != 1 {
			t.Errorf("%s has no history row for version %d: %v %d", name, s.version, err, versions)
		}
		var n int
		var actorID string
		if err := f.pools.Web.QueryRow(ctx, `SELECT count(*), COALESCE(max(actor_id::text),'') FROM audit_log
			WHERE entity_id=$1 AND action='task.close_with_parent'`, id).Scan(&n, &actorID); err != nil || n != 1 || actorID != who {
			t.Errorf("%s needs one audit entry naming %s: %v %d %q", name, who, err, n, actorID)
		}
		var ev int
		if err := f.pools.Web.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE entity_id=$1 AND event='status_changed'
			AND payload->>'closed_with'=$2`, id, c.parent).Scan(&ev); err != nil || ev != 1 {
			t.Errorf("%s needs one status event that says it closed with its parent: %v %d", name, err, ev)
		}
	}
	// What was already finished or dropped is untouched: same status, same
	// version, same completed date, no audit entry.
	for name, id := range map[string]string{"A2": c.a2, "B": c.bDone, "C": c.c} {
		if s := f.snap(t, actor, id); s != before[id] {
			t.Errorf("%s was already closed and must not change: before %+v after %+v", name, before[id], s)
		}
		var n int
		_ = f.pools.Web.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE entity_id=$1 AND action='task.close_with_parent'`, id).Scan(&n)
		if n != 0 {
			t.Errorf("%s got an audit entry it should not have", name)
		}
	}
	if len(closed) != 3 {
		t.Errorf("the sink should name the three subtasks closed, got %v", closed)
	}
}

func (f *fx) before(t *testing.T, actor core.Subjects, c closeTree) map[string]snap {
	m := map[string]snap{}
	for _, id := range []string{c.a, c.a1, c.a2, c.bDone, c.c, c.c1} {
		m[id] = f.snap(t, actor, id)
	}
	return m
}

// Done on the parent closes every open subtask under it, at any depth, through
// each way a task can be closed, and leaves done and cancelled ones alone.
func TestClosingATaskClosesItsOpenSubtasks(t *testing.T) {
	done := docs.StatusDone
	paths := map[string]func(f *fx, ctx context.Context, actor core.Subjects, id string) error{
		"update (the task page, the API, the connector tool)": func(f *fx, ctx context.Context, a core.Subjects, id string) error {
			_, err := f.svc.Update(ctx, f.org, a, id, docs.UpdateParams{Task: &docs.TaskPatch{Status: &done}})
			return err
		},
		"board drag and bulk status (SetField)": func(f *fx, ctx context.Context, a core.Subjects, id string) error {
			return f.svc.SetField(ctx, f.org, a, id, docs.ParseField("status"), done)
		},
	}
	for name, closeIt := range paths {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			alice := f.subj(t, f.alice)
			c := f.closeTree(t, alice)
			before := f.before(t, alice, c)
			ctx, sink := docs.WithClosedSink(context.Background())
			if err := closeIt(f, ctx, alice, c.parent); err != nil {
				t.Fatal(err)
			}
			f.checkClosed(t, alice, f.alice, c, before, sink.IDs)
			if s := f.snap(t, alice, c.parent); s.status != docs.StatusDone {
				t.Fatalf("the parent is not done: %+v", s)
			}

			// Reopening the parent does not reopen anything.
			todo := docs.StatusTodo
			if _, err := f.svc.Update(context.Background(), f.org, alice, c.parent, docs.UpdateParams{Task: &docs.TaskPatch{Status: &todo}}); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{c.a, c.a1, c.c1} {
				if s := f.snap(t, alice, id); s.status != docs.StatusDone {
					t.Errorf("reopening the parent reopened %s: %+v", id, s)
				}
			}
			// Closing it again closes nothing new: the subtasks are done.
			ctx2, sink2 := docs.WithClosedSink(context.Background())
			if err := closeIt(f, ctx2, alice, c.parent); err != nil || len(sink2.IDs) != 0 {
				t.Fatalf("a second close should close no subtask: %v %v", err, sink2.IDs)
			}
		})
	}
}

// A rule that marks a task done closes what is inside it, in the rule's name.
func TestARuleClosingATaskClosesItsSubtasks(t *testing.T) {
	f := setup(t)
	alice := f.subj(t, f.alice)
	c := f.closeTree(t, alice)
	before := f.before(t, alice, c)
	ctx, sink := docs.WithClosedSink(context.Background())
	err := f.svc.SetFieldAsRule(ctx, f.org, docs.RuleActor{RuleID: uuid.NewString(), PrincipalID: f.alice},
		c.parent, docs.ParseField("status"), docs.StatusDone)
	if err != nil {
		t.Fatal(err)
	}
	f.checkClosed(t, alice, f.alice, c, before, sink.IDs)
}

// A task closed in EmberOPS and copied in closes its subtasks in the spine, and
// the sink hands their ids to the caller to send on.
func TestAnExternalCloseClosesSubtasks(t *testing.T) {
	f := setup(t)
	alice := f.subj(t, f.alice)
	c := f.closeTree(t, alice)
	before := f.before(t, alice, c)
	p, err := f.svc.Get(context.Background(), f.org, alice, c.parent)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ctx, sink := docs.WithClosedSink(context.Background())
	if err := f.svc.ApplyExternal(ctx, f.org, c.parent, docs.ExternalChange{
		Title: p.Title, Body: p.Body, Modified: "2026-11-20 10:00:00", By: f.alice,
		Task: &docs.ExternalTask{Status: docs.StatusDone, Priority: p.Task.Priority, CompletedAt: &now},
	}); err != nil {
		t.Fatal(err)
	}
	f.checkClosed(t, alice, f.alice, c, before, sink.IDs)
}

// An import says what was finished: it never closes the rest.
func TestAnImportedDoneDoesNotCloseSubtasks(t *testing.T) {
	f := setup(t)
	alice := f.subj(t, f.alice)
	c := f.closeTree(t, alice)
	done := docs.StatusDone
	ctx, sink := docs.WithClosedSink(context.Background())
	if _, err := f.svc.Update(ctx, f.org, alice, c.parent, docs.UpdateParams{
		Task: &docs.TaskPatch{Status: &done, KeepSubtasks: true}}); err != nil {
		t.Fatal(err)
	}
	if len(sink.IDs) != 0 || f.snap(t, alice, c.a).status == docs.StatusDone {
		t.Fatalf("an import closed subtasks: %v", sink.IDs)
	}
}

// Whoever may close the parent closes the subtasks, including ones they hold no
// grant on: the permission is the parent's. Somebody who cannot edit the parent
// closes nothing.
func TestClosePermissionIsTheParents(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, dave := f.subj(t, f.alice), f.subj(t, f.dave)
	c := f.closeTree(t, alice)
	done := docs.StatusDone
	if _, err := f.svc.Update(ctx, f.org, dave, c.parent, docs.UpdateParams{Task: &docs.TaskPatch{Status: &done}}); err == nil {
		t.Fatal("Dave closed a task he cannot even see")
	}
	if f.snap(t, alice, c.a).status == docs.StatusDone {
		t.Fatal("a refused close still closed a subtask")
	}
	if _, err := f.pools.Web.Exec(ctx, `INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		VALUES (gen_random_uuid(), $1, 'document', $2, $3, 'edit', $4)`,
		f.org, c.parent, core.Subject{Kind: core.SubjectPrincipal, ID: f.dave}.Key(), f.alice); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Update(ctx, f.org, dave, c.parent, docs.UpdateParams{Task: &docs.TaskPatch{Status: &done}}); err != nil {
		t.Fatalf("Dave holds edit on the parent: %v", err)
	}
	if s := f.snap(t, alice, c.a1); s.status != docs.StatusDone {
		t.Fatalf("Dave's close left a subtask open: %+v", s)
	}
}
