package docs_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// Every status a parent can be given reaches its open subtasks, at any depth.
// The tree is closeTree's: A (todo) has A1 (in progress) and A2 (done); B is
// done; C is cancelled with an open C1 under it.
//
//	to done or cancelled  A, A1 and C1 take it, A2, B and C keep theirs
//	to todo, in progress  A and C1 take it; A1 takes todo, or stays when it is
//	or blocked            already there; A2, B and C keep theirs
func TestAParentsStatusReachesItsOpenSubtasks(t *testing.T) {
	cases := []struct {
		to      string
		moved   []string // by title
		stayed  []string
		noEvent []string // already in the status, so no row
	}{
		{docs.StatusDone, []string{"A", "A1", "C1"}, []string{"A2", "B", "C"}, nil},
		{docs.StatusCancelled, []string{"A", "A1", "C1"}, []string{"A2", "B", "C"}, nil},
		{docs.StatusInProgress, []string{"A", "C1"}, []string{"A2", "B", "C"}, []string{"A1"}},
		{docs.StatusBlocked, []string{"A", "A1", "C1"}, []string{"A2", "B", "C"}, nil},
		{docs.StatusTodo, []string{"A1"}, []string{"A2", "B", "C"}, []string{"A", "C1"}},
	}
	for _, tc := range cases {
		t.Run(tc.to, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			alice := f.subj(t, f.alice)
			c := f.closeTree(t, alice)
			id := map[string]string{"A": c.a, "A1": c.a1, "A2": c.a2, "B": c.bDone, "C": c.c, "C1": c.c1}
			before := f.before(t, alice, c)
			if tc.to == docs.StatusTodo {
				// The parent starts in todo; give it somewhere to move from.
				// Set directly: going through Update would move the subtasks too.
				if _, err := f.pools.Web.Exec(ctx, `UPDATE task_fields SET status='in_progress' WHERE document_id=$1`, c.parent); err != nil {
					t.Fatal(err)
				}
				before = f.before(t, alice, c)
			}

			sctx, sink := docs.WithClosedSink(ctx)
			to := tc.to
			if _, err := f.svc.Update(sctx, f.org, alice, c.parent, docs.UpdateParams{Task: &docs.TaskPatch{Status: &to}}); err != nil {
				t.Fatal(err)
			}
			if got := f.snap(t, alice, c.parent).status; got != tc.to {
				t.Fatalf("the parent is %s, want %s", got, tc.to)
			}
			if len(sink.IDs) != len(tc.moved) || sink.Status != tc.to {
				t.Errorf("the sink should name %d subtasks moved to %s, got %v to %q", len(tc.moved), tc.to, sink.IDs, sink.Status)
			}
			for _, name := range tc.moved {
				s := f.snap(t, alice, id[name])
				if s.status != tc.to || s.version != before[id[name]].version+1 {
					t.Errorf("%s should be %s with one new version: before %+v after %+v", name, tc.to, before[id[name]], s)
				}
				if (tc.to == docs.StatusDone) != !s.done.IsZero() {
					t.Errorf("%s: completed date %v does not fit status %s", name, s.done, tc.to)
				}
				f.wantAudit(t, id[name], c.parent, tc.to, 1)
				var ev int
				if err := f.pools.Web.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE entity_id=$1 AND event='status_changed'
					AND payload->>'to'=$2 AND payload->>'with_parent'=$3`, id[name], tc.to, c.parent).Scan(&ev); err != nil || ev != 1 {
					t.Errorf("%s needs one status event that names its parent: %v %d", name, err, ev)
				}
			}
			for _, name := range append(append([]string{}, tc.stayed...), tc.noEvent...) {
				if s := f.snap(t, alice, id[name]); s != before[id[name]] {
					t.Errorf("%s must not change: before %+v after %+v", name, before[id[name]], s)
				}
				f.wantAudit(t, id[name], c.parent, tc.to, 0)
			}

			// Saving the parent again in the status it is in moves nothing.
			sctx2, sink2 := docs.WithClosedSink(ctx)
			if _, err := f.svc.Update(sctx2, f.org, alice, c.parent, docs.UpdateParams{Task: &docs.TaskPatch{Status: &to}}); err != nil || len(sink2.IDs) != 0 {
				t.Fatalf("a save that changes no status moved subtasks: %v %v", err, sink2.IDs)
			}
		})
	}
}

// wantAudit counts the cascade audit rows on id that name parent and status.
func (f *fx) wantAudit(t *testing.T, id, parent, status string, want int) {
	t.Helper()
	rows, err := f.pools.Web.Query(context.Background(), `SELECT after FROM audit_log WHERE entity_id=$1 AND action='task.close_with_parent'`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var after map[string]any
		if err := json.Unmarshal(raw, &after); err != nil {
			t.Fatal(err)
		}
		if after["with_parent"] == parent && after["status"] == status {
			n++
		}
		_, hasClosed := after["closed_with_parent"]
		if hasClosed != docs.IsClosedStatus(status) {
			t.Errorf("closed_with_parent should be present only for a closed status: %v", after)
		}
	}
	if n != want {
		t.Errorf("%s: want %d audit rows naming parent and %s, got %d", id, want, status, n)
	}
}

// A subtask's change never reaches its parent or its siblings, at any status,
// through each way a status is set. Its own subtasks do follow it.
func TestASubtasksChangeNeverTouchesItsParentOrSiblings(t *testing.T) {
	for _, to := range docs.Statuses {
		for name, set := range map[string]func(f *fx, ctx context.Context, a, id string) error{
			"update": func(f *fx, ctx context.Context, a, id string) error {
				s := to
				_, err := f.svc.Update(ctx, f.org, f.subj(t, a), id, docs.UpdateParams{Task: &docs.TaskPatch{Status: &s}})
				return err
			},
			"setfield": func(f *fx, ctx context.Context, a, id string) error {
				return f.svc.SetField(ctx, f.org, f.subj(t, a), id, docs.ParseField("status"), to)
			},
		} {
			t.Run(to+" by "+name, func(t *testing.T) {
				f := setup(t)
				alice := f.subj(t, f.alice)
				c := f.closeTree(t, alice)
				// An extra open sibling of A under the parent.
				sib, err := f.svc.Create(context.Background(), f.org, alice, docs.CreateParams{Kind: docs.KindTask, Title: "Sibling", ParentID: &c.parent})
				if err != nil {
					t.Fatal(err)
				}
				untouched := []string{c.parent, c.bDone, c.c, c.c1, sib.ID}
				was := map[string]snap{}
				for _, id := range untouched {
					was[id] = f.snap(t, alice, id)
				}
				// A is todo, so every status but todo is a change.
				if err := set(f, context.Background(), f.alice, c.a); err != nil {
					t.Fatal(err)
				}
				if got := f.snap(t, alice, c.a).status; got != to {
					t.Fatalf("A is %s, want %s", got, to)
				}
				for _, id := range untouched {
					if s := f.snap(t, alice, id); s != was[id] {
						t.Errorf("a change on a subtask moved %s: before %+v after %+v", id, was[id], s)
					}
					var n int
					_ = f.pools.Web.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE entity_id=$1 AND action='task.close_with_parent'`, id).Scan(&n)
					if n != 0 {
						t.Errorf("%s got a cascade audit row from a subtask's change", id)
					}
				}
				// A2 is under A and done: it keeps its status; A1 under A follows A.
				if s := f.snap(t, alice, c.a2).status; s != docs.StatusDone {
					t.Errorf("a finished subtask of A changed to %s", s)
				}
				if to != docs.StatusTodo && f.snap(t, alice, c.a1).status != to {
					t.Errorf("A1 should follow A to %s", to)
				}
			})
		}
	}
}

// A leaf has nothing beneath it: changing it writes no cascade row anywhere.
func TestALeafChangeCascadesNothing(t *testing.T) {
	f := setup(t)
	alice := f.subj(t, f.alice)
	c := f.closeTree(t, alice)
	ctx, sink := docs.WithClosedSink(context.Background())
	s := docs.StatusCancelled
	if _, err := f.svc.Update(ctx, f.org, alice, c.c1, docs.UpdateParams{Task: &docs.TaskPatch{Status: &s}}); err != nil {
		t.Fatal(err)
	}
	if len(sink.IDs) != 0 {
		t.Fatalf("a leaf moved subtasks: %v", sink.IDs)
	}
	if got := f.snap(t, alice, c.c).status; got != docs.StatusCancelled {
		t.Fatalf("C changed to %s", got)
	}
}

// A rule and an edit copied in from EmberOps cascade every status, not only done.
func TestRulesAndExternalEditsCascadeAnyStatus(t *testing.T) {
	for _, to := range []string{docs.StatusCancelled, docs.StatusInProgress, docs.StatusBlocked} {
		t.Run("rule to "+to, func(t *testing.T) {
			f := setup(t)
			alice := f.subj(t, f.alice)
			c := f.closeTree(t, alice)
			ctx, sink := docs.WithClosedSink(context.Background())
			err := f.svc.SetFieldAsRule(ctx, f.org, docs.RuleActor{RuleID: uuid.NewString(), PrincipalID: f.alice},
				c.parent, docs.ParseField("status"), to)
			if err != nil {
				t.Fatal(err)
			}
			if got := f.snap(t, alice, c.c1).status; got != to {
				t.Errorf("C1 under a cancelled task is %s, want %s", got, to)
			}
			if got := f.snap(t, alice, c.a2).status; got != docs.StatusDone {
				t.Errorf("A2 was done and is now %s", got)
			}
			if len(sink.IDs) == 0 || sink.Status != to {
				t.Errorf("the sink missed the cascade: %v %q", sink.IDs, sink.Status)
			}
			f.wantAudit(t, c.c1, c.parent, to, 1)
		})
		t.Run("external to "+to, func(t *testing.T) {
			f := setup(t)
			alice := f.subj(t, f.alice)
			c := f.closeTree(t, alice)
			p, err := f.svc.Get(context.Background(), f.org, alice, c.parent)
			if err != nil {
				t.Fatal(err)
			}
			ctx, sink := docs.WithClosedSink(context.Background())
			if err := f.svc.ApplyExternal(ctx, f.org, c.parent, docs.ExternalChange{
				Title: p.Title, Body: p.Body, Modified: "2026-11-20 10:00:00", By: f.alice,
				Task: &docs.ExternalTask{Status: to, Priority: p.Task.Priority},
			}); err != nil {
				t.Fatal(err)
			}
			if got := f.snap(t, alice, c.c1).status; got != to {
				t.Errorf("C1 is %s, want %s", got, to)
			}
			if got := f.snap(t, alice, c.bDone).status; got != docs.StatusDone {
				t.Errorf("B was done and is now %s", got)
			}
			if len(sink.IDs) == 0 || sink.Status != to {
				t.Errorf("the sink missed the cascade: %v %q", sink.IDs, sink.Status)
			}
			f.wantAudit(t, c.c1, c.parent, to, 1)
			// The same copy again, as when EmberOps's own hook moved the
			// subtasks first and Hearth reads them on the next page: no second
			// move, no second audit row.
			ctx2, sink2 := docs.WithClosedSink(context.Background())
			if err := f.svc.ApplyExternal(ctx2, f.org, c.parent, docs.ExternalChange{
				Title: p.Title, Body: p.Body, Modified: "2026-11-20 10:01:00", By: f.alice,
				Task: &docs.ExternalTask{Status: to, Priority: p.Task.Priority},
			}); err != nil || len(sink2.IDs) != 0 {
				t.Fatalf("a repeat copy moved subtasks again: %v %v", err, sink2.IDs)
			}
			f.wantAudit(t, c.c1, c.parent, to, 1)
		})
	}
}

// A running timer on a subtask is not touched by a cascade, the same as when a
// person changes the task by hand: timers stop only when their owner stops them.
func TestACascadeLeavesRunningTimersRunning(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	c := f.closeTree(t, alice)
	tl := timelog.New(f.pools.Web, f.az)
	if _, err := tl.Start(ctx, f.org, alice, c.a1, "working"); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{docs.StatusBlocked, docs.StatusCancelled} {
		s := to
		if _, err := f.svc.Update(ctx, f.org, alice, c.parent, docs.UpdateParams{Task: &docs.TaskPatch{Status: &s}}); err != nil {
			t.Fatal(err)
		}
		running, err := tl.Running(ctx, f.org, f.alice)
		if err != nil || running == nil || running.DocumentID != c.a1 {
			t.Fatalf("after the parent went %s the timer on A1 is %+v %v", to, running, err)
		}
	}
	if got := f.snap(t, alice, c.a1).status; got != docs.StatusCancelled {
		t.Fatalf("A1 is %s", got)
	}
}
