package jobs_test

// The queue and the outbox, against a real Postgres.
//
// Every one of these is about concurrency or about transactions, and neither
// can be tested against a mock: a mock would confirm that FOR UPDATE SKIP
// LOCKED was written, which is not the question. The question is what two
// connections do to each other, and only a database can answer it.
//
// Every claim here names its kinds. The queue is global on purpose, so a claim
// with no filter takes whatever is there, and `go test ./...` runs packages
// concurrently against one database: an unfiltered claim in this file runs
// another package's search job, completes it, and makes a green suite lie in
// two places at once.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/jobs"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
)

type fx struct {
	q     *jobs.Queue
	relay *jobs.Relay
	pools *pg.Pools
	org   string
	actor string
	log   *slog.Logger
}

func setup(t *testing.T) *fx {
	t.Helper()
	// A database of this package's own. Sharing one with every other
	// integration package meant this fixture's drain claimed jobs belonging
	// to another one, and the failure surfaced over there as work that
	// never happened. See internal/platform/pg/pgtest.
	dsn := pgtest.Database(t, "hearth_test_jobs")
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

	f := &fx{
		// A short lease, so the lapse path can be exercised without the test
		// taking five minutes.
		q:     jobs.NewQueue(pools.Jobs, time.Second),
		relay: jobs.NewRelay(pools.Jobs, log),
		pools: pools, log: log,
		org: uuid.NewString(), actor: uuid.NewString(),
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Jobs Test')`, f.org, "jobs-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Alice')`,
		f.actor, f.org)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM jobs WHERE org_id=$1`,
			`DELETE FROM jobs_dead WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

// A job that panics must be retried like any other failure, and must reach the
// dead letter table rather than being lost or repeating forever.
//
// The panic path is the one that matters: a handler that returns an error is
// obviously handled, and a handler that panics is the one that takes the whole
// pool down in a queue written the easy way.
func TestAPanickingJobIsRetriedAndThenDies(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var attempts atomic.Int32
	worker := jobs.NewWorker(f.q, f.relay, f.log, jobs.WorkerOptions{
		Concurrency: 1, Poll: 50 * time.Millisecond, ID: "panic-test",
	})
	worker.Handle("test.panic", func(context.Context, *jobs.Job) error {
		attempts.Add(1)
		panic("the handler exploded")
	})

	if _, err := f.q.Enqueue(ctx, jobs.Spec{
		OrgID: f.org, Kind: "test.panic", MaxAttempts: 2,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = worker.Run(runCtx, nil) }()

	// Two attempts, then the dead letter table.
	dead := waitFor(t, ctx, "the job to reach the dead letter table", func() bool {
		list, err := f.q.Dead(ctx, f.org, 10)
		return err == nil && len(list) == 1
	})
	stop()
	<-done
	_ = dead

	if got := attempts.Load(); got != 2 {
		t.Errorf("the handler ran %d times, want 2 (one attempt then one retry)", got)
	}

	list, err := f.q.Dead(ctx, f.org, 10)
	if err != nil {
		t.Fatalf("dead: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("%d dead letters, want 1", len(list))
	}
	// The panic message and its stack have to survive, or the table records
	// only that something failed and never what.
	if !contains(list[0].Error, "panic: the handler exploded") {
		t.Errorf("the dead letter does not carry the panic: %q", list[0].Error)
	}
	if !contains(list[0].Error, "jobs.(*Worker).run") {
		t.Errorf("the dead letter does not carry a stack: %q", list[0].Error)
	}
	if list[0].Attempts != 2 {
		t.Errorf("dead letter records %d attempts, want 2", list[0].Attempts)
	}

	// And the pool survived it. A panic in one handler must not stop the queue.
	var remaining int
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT count(*) FROM jobs WHERE org_id=$1`, f.org).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d jobs left behind after the dead letter move", remaining)
	}
}

// A rolled back write must publish nothing. This is the entire reason the
// outbox is a table rather than a call to a message broker.
func TestARolledBackWritePublishesNoEvent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	entity := uuid.NewString()

	tx, err := f.pools.Web.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: f.org, EntityType: "document", EntityID: entity,
		Event: jobs.EventCreated, ActorID: f.actor,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// The change did not happen. Neither did the event.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	events, err := jobs.EventsFor(ctx, f.pools.Web, "document", entity, 10)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("a rolled back write left %d events behind", len(events))
	}

	// The other direction, so this is not passing because Publish does nothing.
	tx, err = f.pools.Web.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: f.org, EntityType: "document", EntityID: entity,
		Event: jobs.EventCreated, ActorID: f.actor,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	events, err = jobs.EventsFor(ctx, f.pools.Web, "document", entity, 10)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("a committed write produced %d events, want 1", len(events))
	}
}

// Two workers must never run the same job. This is what FOR UPDATE SKIP LOCKED
// buys, and the naive SELECT-then-UPDATE fails it rarely enough to look like a
// mystery rather than a race.
func TestTwoWorkersNeverClaimTheSameJob(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const count = 60
	for i := 0; i < count; i++ {
		if _, err := f.q.Enqueue(ctx, jobs.Spec{
			OrgID: f.org, Kind: "test.count", Payload: map[string]any{"n": i},
		}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	var mu sync.Mutex
	seen := map[int64]int{}
	var total atomic.Int32

	// A long lease, so nothing here is claimed twice because of a lapse. The
	// only way a job can be seen twice is if two claims took the same row.
	queue := jobs.NewQueue(f.pools.Jobs, time.Minute)
	handler := func(_ context.Context, j *jobs.Job) error {
		mu.Lock()
		seen[j.ID]++
		mu.Unlock()
		total.Add(1)
		return nil
	}

	// Four independent workers, which on a single node is the shape that
	// actually happens: one process, several goroutines, one database.
	runCtx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		w := jobs.NewWorker(queue, f.relay, f.log, jobs.WorkerOptions{
			Concurrency: 3, Poll: 20 * time.Millisecond, ID: fmt.Sprintf("w%d", i),
		})
		w.Handle("test.count", handler)
		wg.Add(1)
		go func() { defer wg.Done(); _ = w.Run(runCtx, nil) }()
	}

	waitFor(t, ctx, "every job to run", func() bool { return total.Load() >= count })
	// A moment longer, so a duplicate that was in flight has time to land and
	// be counted. Passing because the test stopped early would be worthless.
	time.Sleep(500 * time.Millisecond)
	stop()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != count {
		t.Errorf("%d distinct jobs ran, want %d", len(seen), count)
	}
	for id, times := range seen {
		if times != 1 {
			t.Errorf("job %d ran %d times", id, times)
		}
	}
	if int(total.Load()) != count {
		t.Errorf("%d runs in total for %d jobs", total.Load(), count)
	}
}

// A lapsed lease is the recovery path for a worker that died holding a job.
// Without it, one killed pod means that job never runs again.
func TestALapsedLeaseIsClaimableAgain(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	if _, err := f.q.Enqueue(ctx, jobs.Spec{OrgID: f.org, Kind: "test.lease"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	first, err := f.q.Claim(ctx, "worker-that-dies", []string{"test.lease"})
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}

	// Still held, so nobody else may take it.
	if _, err := f.q.Claim(ctx, "worker-two", []string{"test.lease"}); !errors.Is(err, jobs.ErrNoJob) {
		t.Fatalf("a held job was claimable: %v", err)
	}

	// The lease in this fixture is one second. The worker "dies" by simply not
	// heartbeating.
	time.Sleep(1200 * time.Millisecond)

	second, err := f.q.Claim(ctx, "worker-two", []string{"test.lease"})
	if err != nil {
		t.Fatalf("claim after the lease lapsed: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("claimed job %d, want the lapsed %d", second.ID, first.ID)
	}
}

// A heartbeat keeps a long job's lease alive, which is what stops a slow
// reindex becoming two slow reindexes and then four.
func TestAHeartbeatHoldsTheLease(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	if _, err := f.q.Enqueue(ctx, jobs.Spec{OrgID: f.org, Kind: "test.slow"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	j, err := f.q.Claim(ctx, "slow-worker", []string{"test.slow"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := f.q.Heartbeat(ctx, j.ID, "slow-worker"); err != nil {
			t.Fatalf("heartbeat: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// One more, immediately before the claim.
	//
	// Not padding. Without it the assertion races the machine: the last
	// heartbeat in the loop can be two hundred milliseconds old, and a stall
	// longer than the remaining lease makes the job genuinely stealable and
	// the test genuinely right to fail, while saying nothing about whether
	// heartbeating works. It flaked exactly once, at the end of a full suite
	// run with three containers busy.
	if err := f.q.Heartbeat(ctx, j.ID, "slow-worker"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	// The lease is one second, and more than that has passed since the claim,
	// but it has been held the whole time.
	if _, err := f.q.Claim(ctx, "other-worker", []string{"test.slow"}); !errors.Is(err, jobs.ErrNoJob) {
		t.Fatalf("a heartbeaten job was stolen: %v", err)
	}

	// And it IS stealable once nobody is holding it, or the assertion above
	// would pass on a queue that never lets anybody claim anything.
	time.Sleep(1200 * time.Millisecond)
	if _, err := f.q.Claim(ctx, "other-worker", []string{"test.slow"}); err != nil {
		t.Fatalf("a job whose lease lapsed was not reclaimable: %v", err)
	}
}

// The unique key collapses duplicates while one is pending. Four edits to one
// document in a minute is one reindex, not four, which is the difference
// between a queue that drains and one that grows all afternoon.
func TestAUniqueKeyCollapsesPendingDuplicates(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	first, err := f.q.Enqueue(ctx, jobs.Spec{
		OrgID: f.org, Kind: "test.index", UniqueKey: "doc:abc",
	})
	if err != nil || first == 0 {
		t.Fatalf("first enqueue: id=%d err=%v", first, err)
	}
	for i := 0; i < 3; i++ {
		id, err := f.q.Enqueue(ctx, jobs.Spec{
			OrgID: f.org, Kind: "test.index", UniqueKey: "doc:abc",
		})
		if err != nil {
			t.Fatalf("duplicate enqueue: %v", err)
		}
		if id != 0 {
			t.Errorf("a duplicate created job %d rather than collapsing", id)
		}
	}

	var pending int
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT count(*) FROM jobs WHERE org_id=$1 AND kind='test.index'`, f.org).Scan(&pending); err != nil {
		t.Fatalf("count: %v", err)
	}
	if pending != 1 {
		t.Fatalf("%d jobs queued for the same key, want 1", pending)
	}

	// Once it has run, the same key may be enqueued again. Otherwise a document
	// could only ever be indexed once.
	if err := f.q.Complete(ctx, first); err != nil {
		t.Fatalf("complete: %v", err)
	}
	again, err := f.q.Enqueue(ctx, jobs.Spec{
		OrgID: f.org, Kind: "test.index", UniqueKey: "doc:abc",
	})
	if err != nil || again == 0 {
		t.Fatalf("the key was still blocked after the job ran: id=%d err=%v", again, err)
	}
}

// The relay turns a committed event into work exactly once, even though the
// work itself runs at least once.
func TestTheRelayTurnsAnEventIntoWorkExactlyOnce(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	f.relay.Route(
		jobs.Route{Event: jobs.EventCreated, JobKind: "test.index", UniqueKey: "index"},
		jobs.Route{JobKind: "test.audit"},
	)

	entity := uuid.NewString()
	tx, err := f.pools.Web.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: f.org, EntityType: "document", EntityID: entity,
		Event: jobs.EventCreated, ActorID: f.actor,
		Payload: map[string]any{"kind": "work_order"},
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// The relay is deliberately global rather than per org: one process drains
	// the whole outbox. So the assertion is on the jobs THIS org's event
	// produced, not on how many rows the drain happened to see, which another
	// test package running against the same database can change underneath it.
	drainAll(t, ctx, f)

	var count int
	countJobs := func() int {
		t.Helper()
		if err := f.pools.Web.QueryRow(ctx,
			`SELECT count(*) FROM jobs WHERE org_id=$1`, f.org).Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		return count
	}
	// One event, two routes, two jobs.
	if got := countJobs(); got != 2 {
		t.Fatalf("%d jobs from one event with two routes, want 2", got)
	}

	// Draining again must produce nothing more. If published_at were set in a
	// separate transaction from the inserts, a crash between them would double
	// every job in the system, and this is the check for it.
	drainAll(t, ctx, f)
	if got := countJobs(); got != 2 {
		t.Fatalf("%d jobs after a second drain, want the same 2", got)
	}

	// The payload has to carry enough to find the entity again. Copying the row
	// into it would be stale by the time the job runs.
	var raw []byte
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT payload FROM jobs WHERE org_id=$1 AND kind='test.index'`, f.org).Scan(&raw); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	job := &jobs.Job{Payload: raw}
	var p jobs.EventPayload
	if err := job.Decode(&p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if p.EntityID != entity || p.Event != jobs.EventCreated {
		t.Errorf("payload lost the event: %+v", p)
	}
}

// A dead letter can be put back. A table you can only read is a list of things
// somebody has to redo by hand.
func TestADeadLetterCanBeRevivedOrDiscarded(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	id, err := f.q.Enqueue(ctx, jobs.Spec{
		OrgID: f.org, Kind: "test.dies", MaxAttempts: 1,
		Payload: map[string]any{"why": "the dependency was down"},
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	j, err := f.q.Claim(ctx, "w", []string{"test.dies"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.q.Fail(ctx, j, errors.New("connection refused")); err != nil {
		t.Fatalf("fail: %v", err)
	}

	list, err := f.q.Dead(ctx, f.org, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("dead letters: %d err=%v", len(list), err)
	}
	if list[0].JobID != id {
		t.Errorf("dead letter records job %d, want %d", list[0].JobID, id)
	}

	revived, err := f.q.Revive(ctx, f.org, list[0].ID)
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if revived == 0 {
		t.Fatal("revive returned no job id")
	}
	// It came back with its payload, or reviving it would run a different job.
	again, err := f.q.Claim(ctx, "w", []string{"test.dies"})
	if err != nil {
		t.Fatalf("claim revived: %v", err)
	}
	if !contains(string(again.Payload), "the dependency was down") {
		t.Errorf("the revived job lost its payload: %s", again.Payload)
	}
	if again.Attempts != 0 {
		t.Errorf("the revived job kept %d attempts, so it would die immediately", again.Attempts)
	}

	// And it is out of the dead letter table, not sitting in both.
	if list, err := f.q.Dead(ctx, f.org, 10); err != nil || len(list) != 0 {
		t.Fatalf("%d dead letters after reviving, want 0 (err=%v)", len(list), err)
	}

	// A revived job gets a fresh budget rather than inheriting the one it had
	// used up, so failing it once more reschedules instead of killing it
	// immediately. Reviving something into a state where it dies on the next
	// attempt would make the button pointless.
	if again.MaxAttempts <= 1 {
		t.Errorf("the revived job has %d attempts left, so retrying it is theatre", again.MaxAttempts)
	}
	if err := f.q.Fail(ctx, again, errors.New("still refused")); err != nil {
		t.Fatalf("fail again: %v", err)
	}
	if list, _ := f.q.Dead(ctx, f.org, 10); len(list) != 0 {
		t.Fatalf("the revived job died on its first retry, %d dead letters", len(list))
	}

	// Discard removes one for good.
	if _, err := f.q.Enqueue(ctx, jobs.Spec{
		OrgID: f.org, Kind: "test.dies.again", MaxAttempts: 1,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	doomed, err := f.q.Claim(ctx, "w", []string{"test.dies.again"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.q.Fail(ctx, doomed, errors.New("gone for good")); err != nil {
		t.Fatalf("fail: %v", err)
	}
	list, _ = f.q.Dead(ctx, f.org, 10)
	if len(list) != 1 {
		t.Fatalf("%d dead letters, want 1", len(list))
	}
	if err := f.q.Discard(ctx, f.org, list[0].ID); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if list, _ := f.q.Dead(ctx, f.org, 10); len(list) != 0 {
		t.Fatalf("%d dead letters after discarding", len(list))
	}
}

// The operator page's numbers have to be true, especially the one it leads
// with. Depth alone cannot tell a busy minute from a stopped worker.
func TestStatsReportWhatAnOperatorNeeds(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	// One ready, one held back, one in flight.
	if _, err := f.q.Enqueue(ctx, jobs.Spec{OrgID: f.org, Kind: "test.a"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := f.q.Enqueue(ctx, jobs.Spec{
		OrgID: f.org, Kind: "test.b", RunAfter: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("enqueue delayed: %v", err)
	}
	if _, err := f.q.Enqueue(ctx, jobs.Spec{OrgID: f.org, Kind: "test.c"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := f.q.Claim(ctx, "w", []string{"test.c"}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	s, err := f.q.Stats(ctx, f.org)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if s.Pending != 3 {
		t.Errorf("pending = %d, want 3", s.Pending)
	}
	// The delayed one is pending but not ready, and that distinction is the
	// whole reason both numbers are on the page.
	if s.Ready != 1 {
		t.Errorf("ready = %d, want 1", s.Ready)
	}
	if s.Claimed != 1 {
		t.Errorf("claimed = %d, want 1", s.Claimed)
	}
	if s.OldestUnclaimed <= 0 {
		t.Error("oldest unclaimed is zero while a job is waiting")
	}
	if len(s.ByKind) != 3 {
		t.Errorf("%d kinds in the breakdown, want 3", len(s.ByKind))
	}
}

// Backoff has to grow and has to be jittered. Without jitter, a hundred jobs
// that failed together because one dependency was down all retry at the same
// instant and take it down again the moment it recovers.
func TestBackoffGrowsAndIsJittered(t *testing.T) {
	if a, b := jobs.Backoff(1), jobs.Backoff(5); a >= b {
		t.Errorf("backoff did not grow: attempt 1 = %s, attempt 5 = %s", a, b)
	}
	if d := jobs.Backoff(40); d > time.Hour+time.Hour/4 {
		t.Errorf("backoff is uncapped: %s", d)
	}

	seen := map[time.Duration]bool{}
	for i := 0; i < 40; i++ {
		seen[jobs.Backoff(6)] = true
	}
	if len(seen) < 5 {
		t.Errorf("backoff produced %d distinct values in 40 tries, so it is not jittered", len(seen))
	}
}

// drainAll empties the outbox, however many passes that takes.
func drainAll(t *testing.T, ctx context.Context, f *fx) {
	t.Helper()
	for i := 0; i < 50; i++ {
		n, err := f.relay.Drain(ctx)
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
		if n == 0 {
			return
		}
	}
	t.Fatal("the outbox never emptied")
}

// waitFor polls until a condition holds, and fails the test rather than hanging
// forever if it never does.
func waitFor(t *testing.T, ctx context.Context, what string, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			t.Fatalf("context ended while waiting for %s", what)
		}
		if cond() {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return false
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
