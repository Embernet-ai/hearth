package docs_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/docs"
)

type reassignFx struct {
	f          *fx
	a, b, c, d string // a: unassigned task, b: on bob, c: work order on bob and dave, d: a plain doc
}

func reassignSetup(t *testing.T) *reassignFx {
	t.Helper()
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	mk := func(kind string) string {
		d, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: kind, Title: kind})
		if err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	r := &reassignFx{f: f, a: mk(docs.KindTask), b: mk(docs.KindTask), c: mk(docs.KindWorkOrder), d: mk(docs.KindDoc)}
	if err := f.svc.Assign(ctx, f.org, alice, r.b, []string{f.bob}, f.bob); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Assign(ctx, f.org, alice, r.c, []string{f.bob, f.dave}, f.bob); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *reassignFx) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := r.f.pools.Web.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (r *reassignFx) assignees(t *testing.T, doc string) string {
	t.Helper()
	ids, err := r.f.svc.Assignees(context.Background(), doc)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(ids, ",")
}

func TestReassignDryRunWritesNothing(t *testing.T) {
	r := reassignSetup(t)
	ctx := context.Background()
	plan := []docs.Reassignment{{DocID: r.a, Assignees: []string{r.f.alice}, Primary: r.f.alice}}
	rep, err := r.f.svc.Reassign(ctx, r.f.org, r.f.alice, plan, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed != 1 || rep.Committed || rep.After[r.f.alice] != 1 {
		t.Fatalf("report: %+v", rep)
	}
	if got := r.assignees(t, r.a); got != "" {
		t.Fatalf("a dry run assigned %q", got)
	}
}

func TestReassignWritesTheSameThingAssignDoesButQuietly(t *testing.T) {
	r := reassignSetup(t)
	ctx := context.Background()
	// Age every document so an updated_at bump would show.
	if _, err := r.f.pools.Web.Exec(ctx, `UPDATE documents SET updated_at = '2025-01-01' WHERE org_id = $1`, r.f.org); err != nil {
		t.Fatal(err)
	}
	events := r.count(t, `SELECT count(*) FROM outbox WHERE org_id = $1`, r.f.org)

	plan := []docs.Reassignment{
		{DocID: r.a, Assignees: []string{r.f.alice}, Primary: r.f.alice},          // none -> alice
		{DocID: r.b, Assignees: []string{r.f.alice, r.f.bob}, Primary: r.f.alice}, // bob -> alice and bob
		{DocID: r.c, Assignees: []string{r.f.bob, r.f.dave}, Primary: r.f.bob},    // already right
	}
	rep, err := r.f.svc.Reassign(ctx, r.f.org, r.f.alice, plan, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Committed || rep.Changed != 2 || len(rep.Reverse) != 2 {
		t.Fatalf("report: changed %d, reverse %d, committed %v", rep.Changed, rep.Changed, rep.Committed)
	}
	if rep.ChecksumBefore != rep.ChecksumAfter {
		t.Fatal("owner_id, created_by and updated_at moved")
	}
	if rep.Before[r.f.bob] != 2 || rep.After[r.f.bob] != 2 || rep.After[r.f.alice] != 2 {
		t.Fatalf("counts before %v after %v", rep.Before, rep.After)
	}
	if got := r.assignees(t, r.a); got != r.f.alice {
		t.Fatalf("a = %q", got)
	}
	// The primary leads, and is mirrored where the board reads it.
	if got := r.assignees(t, r.b); !strings.HasPrefix(got, r.f.alice) || !strings.Contains(got, r.f.bob) {
		t.Fatalf("b = %q", got)
	}
	if n := r.count(t, `SELECT count(*) FROM task_fields WHERE document_id = $1 AND assignee_id = $2`, r.b, r.f.alice); n != 1 {
		t.Fatal("task_fields primary not mirrored")
	}
	// The assignee can edit what they were given.
	if n := r.count(t, `SELECT count(*) FROM grants WHERE entity_id = $1 AND subject_key = $2 AND level IN ('edit', 'admin')`, r.a, "principal:"+r.f.alice); n != 1 {
		t.Fatal("no edit grant for the new assignee")
	}
	// Quiet: nothing touched, nothing announced, and the run is on record.
	if n := r.count(t, `SELECT count(*) FROM documents WHERE org_id = $1 AND updated_at <> '2025-01-01'`, r.f.org); n != 0 {
		t.Fatalf("%d documents had updated_at bumped", n)
	}
	if n := r.count(t, `SELECT count(*) FROM outbox WHERE org_id = $1`, r.f.org); n != events {
		t.Fatalf("outbox grew from %d to %d: events were sent", events, n)
	}
	if n := r.count(t, `SELECT count(*) FROM audit_log WHERE org_id = $1 AND action = 'assign.bulk'`, r.f.org); n != 1 {
		t.Fatal("no audit row")
	}

	// The reverse plan puts it back exactly.
	var back []docs.Reassignment
	back = append(back, rep.Reverse...)
	if _, err := r.f.svc.Reassign(ctx, r.f.org, r.f.alice, back, true, true); err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if got := r.assignees(t, r.a); got != "" {
		t.Fatalf("a after reverse = %q", got)
	}
	if got := r.assignees(t, r.b); got != r.f.bob {
		t.Fatalf("b after reverse = %q", got)
	}
	if n := r.count(t, `SELECT count(*) FROM task_fields WHERE document_id = $1 AND assignee_id IS NULL`, r.a); n != 1 {
		t.Fatal("task_fields primary not cleared by the reverse")
	}
}

func TestReassignRefusesAStaleOrUnsafePlanAndWritesNothing(t *testing.T) {
	r := reassignSetup(t)
	ctx := context.Background()
	before := r.assignees(t, r.b)

	// Stale: the plan thinks b is on Dave.
	_, err := r.f.svc.Reassign(ctx, r.f.org, r.f.alice, []docs.Reassignment{
		{DocID: r.a, Assignees: []string{r.f.alice}, Primary: r.f.alice},
		{DocID: r.b, Assignees: []string{r.f.alice}, Primary: r.f.alice, ExpectCurrent: []string{"Dave"}},
	}, true, false)
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale plan: %v", err)
	}
	if r.assignees(t, r.a) != "" || r.assignees(t, r.b) != before {
		t.Fatal("a refused plan still wrote its first row")
	}
	// A plain doc is not assignable.
	if _, err := r.f.svc.Reassign(ctx, r.f.org, r.f.alice, []docs.Reassignment{
		{DocID: r.d, Assignees: []string{r.f.alice}, Primary: r.f.alice}}, true, false); err == nil {
		t.Fatal("assigned a plain document")
	}
	// A deactivated person cannot be assigned, except by a restore.
	if _, err := r.f.pools.Web.Exec(ctx, `UPDATE principals SET status = 'suspended' WHERE id = $1`, r.f.dave); err != nil {
		t.Fatal(err)
	}
	plan := []docs.Reassignment{{DocID: r.a, Assignees: []string{r.f.dave}, Primary: r.f.dave}}
	if _, err := r.f.svc.Reassign(ctx, r.f.org, r.f.alice, plan, true, false); err == nil {
		t.Fatal("assigned a deactivated person")
	}
	if _, err := r.f.svc.Reassign(ctx, r.f.org, r.f.alice, plan, true, true); err != nil {
		t.Fatalf("a restore may put back a deactivated person: %v", err)
	}
	_ = time.Now
}
