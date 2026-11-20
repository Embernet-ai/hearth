package rules_test

// Automations, against a real Postgres.
//
// The three checks the plan names are all about restraint, again: once per
// event, one cycle when two rules chase each other, and a dry run that changes
// nothing.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/jobs"
	"github.com/Embernet-ai/hearth/internal/notify"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
	"github.com/Embernet-ai/hearth/internal/rules"
)

type fx struct {
	svc    *rules.Service
	engine *rules.Engine
	docs   *docs.Service
	az     *authz.Service
	queue  *jobs.Queue
	relay  *jobs.Relay
	worker *jobs.Worker
	pools  *pg.Pools
	org    string
	alice  string
	bob    string
}

func setup(t *testing.T) *fx {
	t.Helper()
	// A database of this package's own. Sharing one with every other
	// integration package meant this fixture's drain claimed jobs belonging
	// to another one, and the failure surfaced over there as work that
	// never happened. See internal/platform/pg/pgtest.
	dsn := pgtest.Database(t, "hearth_test_rules")
	ctx := context.Background()

	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pools.Close)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := pg.Migrate(ctx, pools, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	az := authz.New(pools.Web)
	d := docs.New(pools.Web, az)
	c := channels.New(pools.Web, az)
	n := notify.New(pools.Web)
	svc := rules.New(pools.Web)

	f := &fx{
		svc: svc, docs: d, az: az, pools: pools,
		queue: jobs.NewQueue(pools.Jobs, time.Minute),
		relay: jobs.NewRelay(pools.Jobs, log),
		org:   uuid.NewString(), alice: uuid.NewString(), bob: uuid.NewString(),
	}
	f.engine = rules.NewEngine(pools.Web, svc, d, c, n, f.queue, log)
	f.worker = jobs.NewWorker(f.queue, f.relay, log, jobs.WorkerOptions{ID: "rules-test"})
	f.engine.Register(f.worker)
	f.relay.Route(rules.Routes()...)

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Rules Test')`, f.org, "rt-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Alice')`, f.alice, f.org)
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Bob')`, f.bob, f.org)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM rule_runs WHERE org_id=$1`,
			`DELETE FROM rules WHERE org_id=$1`,
			`DELETE FROM notifications WHERE org_id=$1`,
			`DELETE FROM watches WHERE principal_id IN (SELECT id FROM principals WHERE org_id=$1)`,
			`DELETE FROM jobs WHERE org_id=$1`,
			`DELETE FROM jobs_dead WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM entity_links WHERE org_id=$1`,
			`DELETE FROM messages WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM checklist_items WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

func (f *fx) subj(t *testing.T, p string) core.Subjects {
	t.Helper()
	s, err := f.az.SubjectsFor(context.Background(), f.org, p)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	return s
}

// drain runs the outbox and every rules job it produces.
func (f *fx) drain(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for round := 0; round < 100; round++ {
		if _, err := f.relay.Drain(ctx); err != nil {
			t.Fatalf("drain: %v", err)
		}
		job, err := f.queue.Claim(ctx, "test", f.worker.Kinds())
		if errors.Is(err, jobs.ErrNoJob) {
			n, err := f.relay.Drain(ctx)
			if err != nil {
				t.Fatalf("drain: %v", err)
			}
			if n == 0 {
				return
			}
			continue
		}
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := f.worker.RunOne(ctx, job); err != nil {
			t.Fatalf("run: %v", err)
		}
		if err := f.queue.Complete(ctx, job.ID); err != nil {
			t.Fatalf("complete: %v", err)
		}
	}
	t.Fatal("the rules never settled, which is what a loop looks like")
}

func (f *fx) rule(t *testing.T, r rules.Rule) *rules.Rule {
	t.Helper()
	saved, err := f.svc.Save(context.Background(), f.org, f.alice, r)
	if err != nil {
		t.Fatalf("save rule: %v", err)
	}
	if err := f.svc.SetEnabled(context.Background(), f.org, saved.ID, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	return saved
}

// A rule fires once per matching event, not twice. Delivery is at-least-once,
// so this is enforced by a unique index rather than by care.
func TestARuleFiresOncePerEvent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	f.rule(t, rules.Rule{
		Name:    "Comment when a work order is done",
		Trigger: rules.Trigger{Event: jobs.EventStatus, Kind: docs.KindWorkOrder},
		Conditions: []rules.Condition{
			{Field: docs.Col("status"), Op: docs.OpIs, Values: []string{docs.StatusDone}},
		},
		Actions: []rules.Action{{Kind: rules.ActionComment, Text: "Closed automatically."}},
	})

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Bearing",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.docs.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Task: &docs.TaskPatch{Status: ptr(docs.StatusDone)},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	f.drain(t)

	var comments int
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT count(*) FROM messages WHERE entity_id=$1 AND deleted_at IS NULL`,
		d.ID).Scan(&comments); err != nil {
		t.Fatalf("count: %v", err)
	}
	if comments != 1 {
		t.Fatalf("the rule left %d comments, want 1", comments)
	}

	// Replay the whole outbox, which is what an at-least-once redelivery looks
	// like. The unique index has to make the second pass a no-op.
	if _, err := f.pools.Web.Exec(ctx,
		`UPDATE outbox SET published_at = NULL WHERE org_id=$1`, f.org); err != nil {
		t.Fatalf("replay: %v", err)
	}
	f.drain(t)

	if err := f.pools.Web.QueryRow(ctx,
		`SELECT count(*) FROM messages WHERE entity_id=$1 AND deleted_at IS NULL`,
		d.ID).Scan(&comments); err != nil {
		t.Fatalf("count: %v", err)
	}
	if comments != 1 {
		t.Fatalf("a redelivered event fired the rule again: %d comments", comments)
	}
}

// Two rules that trigger each other stop after one cycle. This is the failure
// mode of every rules engine ever written.
func TestTwoRulesThatTriggerEachOtherStopAfterOneCycle(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	// A sets blocked when it sees in_progress. B sets in_progress when it sees
	// blocked. Left alone, that is forever.
	f.rule(t, rules.Rule{
		Name:    "A: in progress becomes blocked",
		Trigger: rules.Trigger{Event: jobs.EventStatus, Kind: docs.KindWorkOrder},
		Conditions: []rules.Condition{
			{Field: docs.Col("status"), Op: docs.OpIs, Values: []string{docs.StatusInProgress}},
		},
		Actions: []rules.Action{{
			Kind: rules.ActionSetField, Field: docs.Col("status"), Value: docs.StatusBlocked,
		}},
	})
	f.rule(t, rules.Rule{
		Name:    "B: blocked becomes in progress",
		Trigger: rules.Trigger{Event: jobs.EventStatus, Kind: docs.KindWorkOrder},
		Conditions: []rules.Condition{
			{Field: docs.Col("status"), Op: docs.OpIs, Values: []string{docs.StatusBlocked}},
		},
		Actions: []rules.Action{{
			Kind: rules.ActionSetField, Field: docs.Col("status"), Value: docs.StatusInProgress,
		}},
	})

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Ping pong",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.docs.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Task: &docs.TaskPatch{Status: ptr(docs.StatusInProgress)},
	}); err != nil {
		t.Fatalf("start: %v", err)
	}

	// drain fails the test if the queue never settles, which is precisely what
	// an unguarded loop looks like from the outside.
	f.drain(t)

	got, err := f.docs.Get(ctx, f.org, alice, d.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// A ran once and B did not, because B only ever saw an event a rule caused.
	if got.Task.Status != docs.StatusBlocked {
		t.Errorf("status settled at %q, want blocked after exactly one cycle", got.Task.Status)
	}

	runs, err := f.svc.Runs(ctx, f.org, "", 50)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	fired := 0
	for _, r := range runs {
		if r.Matched && !r.DryRun {
			fired++
		}
	}
	if fired != 1 {
		t.Errorf("%d rules fired, want exactly 1", fired)
	}
}

// A dry run changes nothing and reports what it would have done.
func TestADryRunChangesNothingAndSaysWhatItWouldDo(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	// Real history first, with the rule switched off so it cannot act on it.
	var made []string
	for _, title := range []string{"One", "Two", "Three"} {
		d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
			Kind: docs.KindWorkOrder, Title: title,
			Task: &docs.TaskFields{Status: docs.StatusTodo},
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		made = append(made, d.ID)
		if _, err := f.docs.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
			Task: &docs.TaskPatch{Status: ptr(docs.StatusDone)},
		}); err != nil {
			t.Fatalf("complete: %v", err)
		}
	}
	f.drain(t)

	saved, err := f.svc.Save(ctx, f.org, f.alice, rules.Rule{
		Name:    "Would comment on completion",
		Trigger: rules.Trigger{Event: jobs.EventStatus, Kind: docs.KindWorkOrder},
		Conditions: []rules.Condition{
			{Field: docs.Col("status"), Op: docs.OpIs, Values: []string{docs.StatusDone}},
		},
		Actions: []rules.Action{{Kind: rules.ActionComment, Text: "Would have said this."}},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	runs, err := f.engine.DryRun(ctx, f.org, saved.ID, 100)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}

	matched := 0
	for _, r := range runs {
		if !r.DryRun {
			t.Errorf("a dry run produced a real run row")
		}
		if r.Matched {
			matched++
			if len(r.ActionsTaken) == 0 || !strings.HasPrefix(r.ActionsTaken[0], "would ") {
				t.Errorf("a dry run did not say what it would do: %+v", r.ActionsTaken)
			}
		}
	}
	if matched != 3 {
		t.Errorf("the dry run matched %d of 3 completions", matched)
	}

	// And nothing happened.
	var comments int
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT count(*) FROM messages WHERE org_id=$1`, f.org).Scan(&comments); err != nil {
		t.Fatalf("count: %v", err)
	}
	if comments != 0 {
		t.Fatalf("a dry run left %d comments behind", comments)
	}
	_ = made
}

// A rule that does not match still records why, because "why did it not fire"
// is the question a rules engine gets asked.
func TestANonMatchIsRecordedWithItsReason(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	r := f.rule(t, rules.Rule{
		Name:    "Only urgent",
		Trigger: rules.Trigger{Event: jobs.EventStatus, Kind: docs.KindWorkOrder},
		Conditions: []rules.Condition{
			{Field: docs.Col("priority"), Op: docs.OpIs, Values: []string{"3"}},
		},
		Actions: []rules.Action{{Kind: rules.ActionComment, Text: "Escalating."}},
	})

	d, _ := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Not urgent",
		Task: &docs.TaskFields{Status: docs.StatusTodo, Priority: 0},
	})
	if _, err := f.docs.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Task: &docs.TaskPatch{Status: ptr(docs.StatusDone)},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	f.drain(t)

	runs, err := f.svc.Runs(ctx, f.org, r.ID, 10)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("a rule that considered an event and declined recorded nothing")
	}
	if runs[0].Matched {
		t.Fatal("the rule fired on a work order that does not meet its condition")
	}
	if !strings.Contains(runs[0].Reason, "priority") {
		t.Errorf("the reason does not name the condition that failed: %q", runs[0].Reason)
	}
}

// A rule with no actions, no trigger, or an action nobody can perform is
// refused at save time rather than at three in the morning.
func TestAnEmptyRuleIsRefused(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	cases := []rules.Rule{
		{Name: "", Trigger: rules.Trigger{Event: "created"},
			Actions: []rules.Action{{Kind: rules.ActionComment, Text: "x"}}},
		{Name: "No trigger", Actions: []rules.Action{{Kind: rules.ActionComment, Text: "x"}}},
		{Name: "No actions", Trigger: rules.Trigger{Event: "created"}},
		{Name: "Bad action", Trigger: rules.Trigger{Event: "created"},
			Actions: []rules.Action{{Kind: "launch_missiles"}}},
		{Name: "Webhook with no address", Trigger: rules.Trigger{Event: "created"},
			Actions: []rules.Action{{Kind: rules.ActionWebhook}}},
	}
	for _, c := range cases {
		if _, err := f.svc.Save(ctx, f.org, f.alice, c); !errors.Is(err, rules.ErrInvalid) {
			t.Errorf("%q was accepted: %v", c.Name, err)
		}
	}
}

// A rule arrives switched off, so there is somewhere to dry run it.
func TestARuleArrivesSwitchedOff(t *testing.T) {
	f := setup(t)
	saved, err := f.svc.Save(context.Background(), f.org, f.alice, rules.Rule{
		Name:    "Fresh",
		Trigger: rules.Trigger{Event: "created"},
		Actions: []rules.Action{{Kind: rules.ActionComment, Text: "hello"}},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Enabled {
		t.Error("a new rule started enabled, so nobody could dry run it first")
	}
}

func ptr[T any](v T) *T { return &v }

// A dry run over a long history has to show the recent end of it.
//
// Every rehearsal row is written within the same second, so ordering the run
// log by when the row was written means the tie-break decides the whole list.
// It sorted the oldest history to the top and pushed this morning off the
// bottom of a fifty row page: pressing dry run on a rule that would obviously
// have fired showed fifty rows of "did not match" and nothing else, which
// reads as a broken rule rather than a truncated page.
//
// Found by the fourth consecutive acceptance run against a database that kept
// what the previous three left, which is the only way this appears at all.
func TestADryRunShowsTheRecentEndOfALongHistory(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	// More completions than the page shows, so the answer cannot be "it all
	// fitted". The last one is the one somebody pressing dry run cares about.
	const history = 60
	var last string
	for i := 0; i < history; i++ {
		d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
			Kind: docs.KindWorkOrder, Title: fmt.Sprintf("Job %d", i),
			Task: &docs.TaskFields{Status: docs.StatusTodo},
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := f.docs.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
			Task: &docs.TaskPatch{Status: ptr(docs.StatusDone)},
		}); err != nil {
			t.Fatalf("complete: %v", err)
		}
		last = d.ID
		// In batches, because the drain gives up after a hundred rounds and
		// sixty jobs of history is more work than one pass of it.
		if i%10 == 9 {
			f.drain(t)
		}
	}
	f.drain(t)

	saved, err := f.svc.Save(ctx, f.org, f.alice, rules.Rule{
		Name:    "Would comment on completion",
		Trigger: rules.Trigger{Event: jobs.EventStatus, Kind: docs.KindWorkOrder},
		Conditions: []rules.Condition{
			{Field: docs.Col("status"), Op: docs.OpIs, Values: []string{docs.StatusDone}},
		},
		Actions: []rules.Action{{Kind: rules.ActionComment, Text: "Would have said this."}},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := f.engine.DryRun(ctx, f.org, saved.ID, 200); err != nil {
		t.Fatalf("dry run: %v", err)
	}

	// The page's own limit, which is the thing that was truncating.
	page, err := f.svc.Runs(ctx, f.org, saved.ID, 50)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(page) != 50 {
		t.Fatalf("%d rows, want the page's fifty", len(page))
	}
	found := false
	for _, r := range page {
		if r.EntityID == last {
			found = true
		}
	}
	if !found {
		t.Errorf("the most recent completion of %d is not on the first page of "+
			"the dry run.\nSomebody rehearsing a rule reads the top of this list, "+
			"and what is at the top has to be what just happened.", history)
	}
	// Newest event first, all the way down.
	for i := 1; i < len(page); i++ {
		if page[i-1].EventID < page[i].EventID {
			t.Fatalf("row %d rehearses event %d and row %d rehearses %d, so the "+
				"log is not in the order the events happened",
				i-1, page[i-1].EventID, i, page[i].EventID)
		}
	}
}
