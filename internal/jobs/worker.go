package jobs

// The worker pool, and the relay loop that feeds it.
//
// Both run inside the web binary behind a flag rather than as a second image.
// Two images is two builds, two deploys, and two chances for the running
// versions to disagree about what a payload means. One binary that does both
// cannot be half-upgraded.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler does the work for one kind of job.
//
// Returning an error schedules a retry. Panicking does the same thing, because
// the worker recovers: a handler that panics on one malformed payload must not
// take the whole pool down with it, and it must not silently stop the queue
// either.
type Handler func(ctx context.Context, j *Job) error

// Worker runs jobs until its context is cancelled.
type Worker struct {
	queue    *Queue
	relay    *Relay
	log      *slog.Logger
	handlers map[string]Handler

	// concurrency is how many jobs run at once.
	concurrency int
	// poll is the fallback interval. A notification usually gets there first;
	// this is what makes the queue drain anyway when one is missed, and one
	// will be missed, because NOTIFY is not delivered to a connection that was
	// reconnecting at the time.
	poll time.Duration
	// heartbeat is how often a running job extends its lease.
	heartbeat time.Duration
	// id names this worker in claimed_by, so an operator can see which replica
	// is holding something.
	id string

	// wake is signalled by the listener. Buffered with one slot, because two
	// wake-ups arriving together mean the same thing as one.
	wake chan struct{}
}

// WorkerOptions configures the pool.
type WorkerOptions struct {
	Concurrency int
	Poll        time.Duration
	Lease       time.Duration
	Heartbeat   time.Duration
	ID          string
}

// NewWorker builds a pool. Handlers are registered with Handle before Run.
func NewWorker(q *Queue, relay *Relay, log *slog.Logger, opts WorkerOptions) *Worker {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	if opts.Poll <= 0 {
		opts.Poll = time.Second
	}
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = 30 * time.Second
	}
	if opts.ID == "" {
		opts.ID = "worker"
	}
	return &Worker{
		queue: q, relay: relay, log: log,
		handlers:    map[string]Handler{},
		concurrency: opts.Concurrency,
		poll:        opts.Poll,
		heartbeat:   opts.Heartbeat,
		id:          opts.ID,
		wake:        make(chan struct{}, 1),
	}
}

// Handle registers the handler for a kind of job.
func (w *Worker) Handle(kind string, h Handler) { w.handlers[kind] = h }

// Kinds lists what this worker knows how to run.
func (w *Worker) Kinds() []string {
	out := make([]string, 0, len(w.handlers))
	for k := range w.handlers {
		out = append(out, k)
	}
	return out
}

// Notify channel names. Postgres notification channels are identifiers, so they
// are lowercase and unquoted everywhere they appear.
const (
	channelJobs   = "hearth_jobs"
	channelOutbox = "hearth_outbox"
)

// Run starts the relay loop and the worker pool, and blocks until ctx ends.
func (w *Worker) Run(ctx context.Context, listen *pgxpool.Pool) error {
	var wg sync.WaitGroup

	// The listener is an optimisation and never a requirement. If it cannot
	// connect, the poll interval still drains the queue, so a failure here is
	// logged and the process carries on rather than refusing to work.
	if listen != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.listen(ctx, listen)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		w.relayLoop(ctx)
	}()

	for i := 0; i < w.concurrency; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			w.loop(ctx, fmt.Sprintf("%s-%d", w.id, n))
		}(i)
	}

	w.log.Info("worker started",
		"concurrency", w.concurrency, "kinds", w.Kinds(), "poll", w.poll)
	wg.Wait()
	w.log.Info("worker stopped")
	return nil
}

// listen holds LISTEN on a dedicated connection and wakes the pool when
// something arrives.
func (w *Worker) listen(ctx context.Context, pool *pgxpool.Pool) {
	for ctx.Err() == nil {
		if err := w.listenOnce(ctx, pool); err != nil && ctx.Err() == nil {
			// A dropped listener is normal on a database restart. Say it once
			// at warn and then retry quietly rather than filling the log.
			w.log.Warn("job listener dropped, falling back to polling until it returns",
				"error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func (w *Worker) listenOnce(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire listener: %w", err)
	}
	defer conn.Release()

	for _, channel := range []string{channelJobs, channelOutbox} {
		if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
			return fmt.Errorf("listen %s: %w", channel, err)
		}
	}
	for {
		if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
			return err
		}
		w.signal()
	}
}

func (w *Worker) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
		// Already pending. Two wake-ups mean the same as one.
	}
}

// relayLoop turns committed events into jobs.
//
// It drains in a tight loop while there is anything there, so a backlog does
// not take one poll interval per batch to clear.
func (w *Worker) relayLoop(ctx context.Context) {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()

	for {
		for {
			n, err := w.relay.Drain(ctx)
			if err != nil {
				if ctx.Err() == nil {
					w.log.Error("outbox relay failed", "error", err)
				}
				break
			}
			if n > 0 {
				// New jobs exist, so do not wait for a notification that this
				// process is the one that would have sent.
				w.signal()
			}
			if n < w.relay.batch {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.wake:
		}
	}
}

// loop is one worker goroutine.
func (w *Worker) loop(ctx context.Context, id string) {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()

	for {
		// Drain rather than take one job per tick. A queue that processes one
		// job a second regardless of depth is not a queue, it is a metronome.
		for {
			ran, err := w.once(ctx, id)
			if err != nil || !ran {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.wake:
		}
	}
}

// once claims and runs a single job. It reports whether it ran anything.
func (w *Worker) once(ctx context.Context, id string) (bool, error) {
	j, err := w.queue.Claim(ctx, id, w.Kinds())
	switch {
	case errors.Is(err, ErrNoJob):
		return false, nil
	case err != nil:
		if ctx.Err() == nil {
			w.log.Error("claim failed", "error", err)
		}
		return false, err
	}

	handler, ok := w.handlers[j.Kind]
	if !ok {
		// Claimed by kind, so this can only happen if the registry changed
		// under us. Fail it rather than dropping it, so the job is retried by
		// whichever replica does know the kind.
		w.fail(ctx, j, fmt.Errorf("no handler for kind %q", j.Kind))
		return true, nil
	}

	// The job gets its own context, not the request's and not one that is
	// cancelled by a heartbeat failure. Shutdown cancels it, which is what
	// makes a rolling deploy drain rather than abandon.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stop := w.beat(runCtx, j.ID, id)
	err = w.run(runCtx, handler, j)
	stop()

	if err != nil {
		w.fail(ctx, j, err)
		return true, nil
	}

	// Completion uses the parent context deliberately. A job that finished
	// during shutdown must be marked done, or it runs again on the next boot
	// having already had its effect.
	completeCtx, cancelComplete := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelComplete()
	if err := w.queue.Complete(completeCtx, j.ID); err != nil {
		w.log.Error("could not mark job complete, it will run again",
			"job", j.ID, "kind", j.Kind, "error", err)
	}
	return true, nil
}

// RunOne executes one already-claimed job with this worker's handlers.
//
// Exported so that a test can drive a pipeline deterministically rather than
// starting a pool and racing it. A test that waits for a background goroutine
// passes on an idle laptop and fails on a busy build machine, which teaches
// people to rerun the suite instead of reading it.
func (w *Worker) RunOne(ctx context.Context, j *Job) error {
	h, ok := w.handlers[j.Kind]
	if !ok {
		return fmt.Errorf("jobs: no handler for kind %q", j.Kind)
	}
	return w.run(ctx, h, j)
}

// run calls the handler and turns a panic into an error.
//
// A handler that panics on one bad payload must not take the pool down, and it
// must not quietly stop the queue either. The stack goes into last_error, so
// the dead letter table says where it happened rather than only that it did.
func (w *Worker) run(ctx context.Context, h Handler, j *Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return h(ctx, j)
}

// beat keeps the lease alive while a job runs, and returns a function to stop.
func (w *Worker) beat(ctx context.Context, jobID int64, workerID string) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(w.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := w.queue.Heartbeat(ctx, jobID, workerID); err != nil && ctx.Err() == nil {
					w.log.Warn("heartbeat failed, the lease may lapse",
						"job", jobID, "error", err)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (w *Worker) fail(ctx context.Context, j *Job, cause error) {
	// Recording the failure must survive shutdown for the same reason
	// completion must: a job whose failure was never written is a job that
	// retries with attempts still at the old number, forever.
	failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	attempts := j.Attempts + 1
	if attempts >= j.MaxAttempts {
		w.log.Error("job gave up and moved to the dead letter table",
			"job", j.ID, "kind", j.Kind, "attempts", attempts, "error", cause)
	} else {
		w.log.Warn("job failed, will retry",
			"job", j.ID, "kind", j.Kind, "attempt", attempts,
			"retry_in", Backoff(attempts).Round(time.Second), "error", cause)
	}
	if err := w.queue.Fail(failCtx, j, cause); err != nil {
		w.log.Error("could not record job failure", "job", j.ID, "error", err)
	}
}

// Kick tells any listening worker that there is something to do.
//
// Called after a write that enqueued work, outside the transaction, because a
// notification sent inside one is delivered on commit and a notification sent
// for a rollback is a wake-up for work that does not exist. It is best effort:
// the poll interval is what makes the queue correct, and this only makes it
// prompt.
func Kick(ctx context.Context, db *pgxpool.Pool) {
	_, _ = db.Exec(ctx, "SELECT pg_notify($1, '')", channelJobs)
}
