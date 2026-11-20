// Package jobs is the durable queue: work that must happen, and must not happen
// inside the request.
//
// # Why Postgres and not Redis
//
// The queue has to participate in the same transaction as the data it is about.
// A second datastore cannot, so a second datastore can always disagree with the
// first: the row commits and the enqueue fails, or the enqueue succeeds and the
// row does not. FOR UPDATE SKIP LOCKED gives a correct queue in a few hundred
// lines against a database that is already there, already backed up, and
// already the thing being restored in a disaster.
//
// # At-least-once, and what that costs
//
// A claim is a lease. The worker holding one heartbeats while it works, and a
// worker that dies stops heartbeating, so the lease lapses and the job is
// picked up again. That means a handler CAN run twice, and every handler has to
// be written for it. The alternative, at-most-once, loses work whenever a pod
// is killed mid-job, and losing a notification is worse than sending one twice.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoJob is returned by Claim when there is nothing ready to run.
var ErrNoJob = errors.New("no job ready")

// Job is one unit of work.
type Job struct {
	ID          int64
	OrgID       string
	Kind        string
	Payload     json.RawMessage
	UniqueKey   *string
	RunAfter    time.Time
	Attempts    int
	MaxAttempts int
	LastError   *string
	CreatedAt   time.Time
}

// Decode unmarshals the payload into v.
func (j *Job) Decode(v any) error {
	if len(j.Payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(j.Payload, v); err != nil {
		return fmt.Errorf("jobs: decode %s payload: %w", j.Kind, err)
	}
	return nil
}

// Spec describes a job to enqueue.
type Spec struct {
	OrgID string
	Kind  string
	// Payload is marshalled to jsonb. Keep it to identifiers: a payload that
	// carries a copy of the row is stale by the time it runs.
	Payload any
	// UniqueKey collapses duplicates while a matching job is still pending.
	// Empty means every enqueue is its own job.
	UniqueKey string
	// RunAfter delays the first attempt. Zero means as soon as possible.
	RunAfter time.Time
	// MaxAttempts overrides the default. Zero takes the default.
	MaxAttempts int
}

// Defaults for a job that does not say otherwise.
const (
	DefaultMaxAttempts = 8
	// maxBackoff caps the exponential. Past an hour the retry is no longer
	// recovering from a blip, it is waiting for a person, and the dead letter
	// table is where a person looks.
	maxBackoff = time.Hour
)

// Queue enqueues and claims work.
type Queue struct {
	db *pgxpool.Pool
	// lease is how long a claim is good for without a heartbeat.
	lease time.Duration
}

// NewQueue returns a queue over the given pool. Use the jobs pool: the web pool
// has a statement timeout and a claim under contention can legitimately wait.
func NewQueue(db *pgxpool.Pool, lease time.Duration) *Queue {
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	return &Queue{db: db, lease: lease}
}

// Enqueue adds a job.
//
// It returns the id, or zero when a pending job with the same unique key
// already exists. Collapsing rather than erroring is the point of the key:
// asking twice for the same work is not a mistake, it is the normal case when
// somebody edits a document four times in a minute.
func (q *Queue) Enqueue(ctx context.Context, s Spec) (int64, error) {
	return enqueue(ctx, q.db, s)
}

// EnqueueTx adds a job inside a caller's transaction.
//
// This is what makes "the work happens if and only if the change commits" true.
// Enqueueing outside the transaction gives a job that runs against a row that
// was rolled back, which is the failure the outbox exists to prevent and this
// is the same guarantee for work that has no event.
func EnqueueTx(ctx context.Context, tx pgx.Tx, s Spec) (int64, error) {
	return enqueue(ctx, tx, s)
}

// querier is the part of a pool and a transaction that enqueueing needs. One
// function serves both, so there is one piece of insert SQL rather than two
// that drift apart the first time the table gains a column.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func enqueue(ctx context.Context, db querier, s Spec) (int64, error) {
	if s.Kind == "" {
		return 0, errors.New("jobs: a job needs a kind")
	}
	payload := []byte("{}")
	if s.Payload != nil {
		var err error
		if payload, err = json.Marshal(s.Payload); err != nil {
			return 0, fmt.Errorf("jobs: encode %s payload: %w", s.Kind, err)
		}
	}
	runAfter := s.RunAfter
	if runAfter.IsZero() {
		runAfter = time.Now()
	}
	maxAttempts := s.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	var key *string
	if s.UniqueKey != "" {
		key = &s.UniqueKey
	}

	var id int64
	err := db.QueryRow(ctx, `
		INSERT INTO jobs (org_id, kind, payload, unique_key, run_after, max_attempts)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (org_id, kind, unique_key) WHERE unique_key IS NOT NULL
		DO NOTHING
		RETURNING id`,
		s.OrgID, s.Kind, payload, key, runAfter, maxAttempts).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Collapsed into a job that is already waiting. Not an error.
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("jobs: enqueue %s: %w", s.Kind, err)
	}
	return id, nil
}

// Claim takes the next ready job for this worker, or returns ErrNoJob.
//
// FOR UPDATE SKIP LOCKED is what makes this safe with any number of workers:
// each one takes a row lock on a different row and nobody blocks. The
// alternative, SELECT then UPDATE, hands the same job to two workers whenever
// they read between each other's writes, and it does it rarely enough that it
// looks like a mystery rather than a race.
func (q *Queue) Claim(ctx context.Context, workerID string, kinds []string) (*Job, error) {
	// A lapsed lease is claimable. That is the recovery path for a worker that
	// was killed holding a job, and it is why handlers must tolerate a second
	// run rather than assuming exclusivity.
	sql := `
		WITH next AS (
		    SELECT id FROM jobs
		     WHERE run_after <= now()
		       AND (claimed_at IS NULL OR claimed_at < now() - $2::interval)
		       AND ($3::text[] IS NULL OR kind = ANY($3))
		     ORDER BY run_after, id
		     FOR UPDATE SKIP LOCKED
		     LIMIT 1
		)
		UPDATE jobs j
		   SET claimed_at = now(), claimed_by = $1
		  FROM next
		 WHERE j.id = next.id
		RETURNING j.id, j.org_id::text, j.kind, j.payload, j.unique_key,
		          j.run_after, j.attempts, j.max_attempts, j.last_error, j.created_at`

	var kindFilter any
	if len(kinds) > 0 {
		kindFilter = kinds
	}

	j := &Job{}
	err := q.db.QueryRow(ctx, sql, workerID, q.lease.String(), kindFilter).
		Scan(&j.ID, &j.OrgID, &j.Kind, &j.Payload, &j.UniqueKey,
			&j.RunAfter, &j.Attempts, &j.MaxAttempts, &j.LastError, &j.CreatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNoJob
	case err != nil:
		return nil, fmt.Errorf("jobs: claim: %w", err)
	}
	return j, nil
}

// Heartbeat extends a claim while the job is still running.
//
// Without it a job that legitimately takes longer than the lease gets picked up
// by a second worker while the first is still going, which is how a slow
// reindex turns into two slow reindexes and then four.
func (q *Queue) Heartbeat(ctx context.Context, id int64, workerID string) error {
	_, err := q.db.Exec(ctx,
		`UPDATE jobs SET claimed_at = now() WHERE id = $1 AND claimed_by = $2`, id, workerID)
	if err != nil {
		return fmt.Errorf("jobs: heartbeat %d: %w", id, err)
	}
	return nil
}

// Complete removes a finished job.
func (q *Queue) Complete(ctx context.Context, id int64) error {
	if _, err := q.db.Exec(ctx, `DELETE FROM jobs WHERE id = $1`, id); err != nil {
		return fmt.Errorf("jobs: complete %d: %w", id, err)
	}
	return nil
}

// Fail records an attempt that did not work, and either schedules a retry or
// moves the job to the dead letter table.
//
// The dead letter move and the delete are one transaction. Two statements would
// leave a window where a job is in neither table, and the job that vanishes is
// the one nobody can explain a week later.
func (q *Queue) Fail(ctx context.Context, j *Job, cause error) error {
	attempts := j.Attempts + 1
	message := cause.Error()

	if attempts >= j.MaxAttempts {
		tx, err := q.db.Begin(ctx)
		if err != nil {
			return fmt.Errorf("jobs: begin dead letter: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		if _, err := tx.Exec(ctx, `
			INSERT INTO jobs_dead (job_id, org_id, kind, payload, attempts, error)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			j.ID, j.OrgID, j.Kind, j.Payload, attempts, message); err != nil {
			return fmt.Errorf("jobs: record dead letter for %d: %w", j.ID, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM jobs WHERE id = $1`, j.ID); err != nil {
			return fmt.Errorf("jobs: remove dead job %d: %w", j.ID, err)
		}
		return tx.Commit(ctx)
	}

	_, err := q.db.Exec(ctx, `
		UPDATE jobs
		   SET attempts = $1, last_error = $2, run_after = $3,
		       claimed_at = NULL, claimed_by = NULL
		 WHERE id = $4`,
		attempts, message, time.Now().Add(Backoff(attempts)), j.ID)
	if err != nil {
		return fmt.Errorf("jobs: reschedule %d: %w", j.ID, err)
	}
	return nil
}

// Backoff is exponential with jitter, capped.
//
// The jitter is not decoration. Without it, a hundred jobs that failed together
// because one dependency was down all retry at the same instant, which takes
// the dependency down again the moment it recovers.
func Backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	seconds := math.Pow(2, float64(attempts))
	d := time.Duration(seconds) * time.Second
	if d > maxBackoff || d <= 0 {
		d = maxBackoff
	}
	// Plus or minus twenty percent.
	spread := int64(d) / 5
	if spread <= 0 {
		return d
	}
	n, err := rand.Int(rand.Reader, big.NewInt(2*spread))
	if err != nil {
		return d
	}
	return d + time.Duration(n.Int64()-spread)
}

// -----------------------------------------------------------------------------
// Operations
// -----------------------------------------------------------------------------

// Stats is what an operator needs to know at a glance.
type Stats struct {
	// Pending is work waiting or in flight.
	Pending int
	// Ready is the subset that could run right now, which is the number that
	// matters: a large Pending made entirely of backed-off retries is a
	// different problem from a large Ready.
	Ready int
	// Claimed is in flight.
	Claimed int
	// Dead is how many gave up.
	Dead int
	// OldestUnclaimed is how long the longest-waiting ready job has waited. It
	// is the single best signal that the workers have stopped, because depth
	// alone looks the same during a busy minute.
	OldestUnclaimed time.Duration
	// Unpublished is outbox rows the relay has not turned into jobs yet.
	Unpublished int
	// ByKind breaks the pending work down.
	ByKind []KindCount
}

// KindCount is one row of the breakdown.
type KindCount struct {
	Kind    string
	Pending int
	Ready   int
}

// Stats reads the queue's state.
func (q *Queue) Stats(ctx context.Context, orgID string) (*Stats, error) {
	s := &Stats{}
	var oldest *time.Time
	err := q.db.QueryRow(ctx, `
		SELECT
		  count(*)                                                        AS pending,
		  count(*) FILTER (WHERE claimed_at IS NULL AND run_after <= now()) AS ready,
		  count(*) FILTER (WHERE claimed_at IS NOT NULL)                  AS claimed,
		  min(run_after) FILTER (WHERE claimed_at IS NULL AND run_after <= now())
		FROM jobs WHERE org_id = $1`, orgID).
		Scan(&s.Pending, &s.Ready, &s.Claimed, &oldest)
	if err != nil {
		return nil, fmt.Errorf("jobs: stats: %w", err)
	}
	if oldest != nil {
		s.OldestUnclaimed = time.Since(*oldest)
	}

	if err := q.db.QueryRow(ctx,
		`SELECT count(*) FROM jobs_dead WHERE org_id = $1`, orgID).Scan(&s.Dead); err != nil {
		return nil, fmt.Errorf("jobs: dead count: %w", err)
	}
	if err := q.db.QueryRow(ctx,
		`SELECT count(*) FROM outbox WHERE org_id = $1 AND published_at IS NULL`,
		orgID).Scan(&s.Unpublished); err != nil {
		return nil, fmt.Errorf("jobs: unpublished count: %w", err)
	}

	rows, err := q.db.Query(ctx, `
		SELECT kind, count(*),
		       count(*) FILTER (WHERE claimed_at IS NULL AND run_after <= now())
		FROM jobs WHERE org_id = $1 GROUP BY kind ORDER BY count(*) DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("jobs: breakdown: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k KindCount
		if err := rows.Scan(&k.Kind, &k.Pending, &k.Ready); err != nil {
			return nil, fmt.Errorf("jobs: scan breakdown: %w", err)
		}
		s.ByKind = append(s.ByKind, k)
	}
	return s, rows.Err()
}

// DeadLetter is one job that gave up.
type DeadLetter struct {
	ID       int64
	JobID    int64
	Kind     string
	Payload  json.RawMessage
	Attempts int
	Error    string
	FailedAt time.Time
}

// Dead lists recent dead letters, newest first.
func (q *Queue) Dead(ctx context.Context, orgID string, limit int) ([]DeadLetter, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := q.db.Query(ctx, `
		SELECT id, job_id, kind, payload, attempts, error, failed_at
		FROM jobs_dead WHERE org_id = $1
		ORDER BY failed_at DESC, id DESC LIMIT $2`, orgID, limit)
	if err != nil {
		return nil, fmt.Errorf("jobs: list dead: %w", err)
	}
	defer rows.Close()

	var out []DeadLetter
	for rows.Next() {
		var d DeadLetter
		if err := rows.Scan(&d.ID, &d.JobID, &d.Kind, &d.Payload,
			&d.Attempts, &d.Error, &d.FailedAt); err != nil {
			return nil, fmt.Errorf("jobs: scan dead: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Revive puts a dead letter back on the queue with its attempt count reset.
//
// A dead letter table you can only read is a list of things somebody has to
// redo by hand. The usual reason a job dies is a dependency that was down, and
// the usual fix is to run it again once it is back.
func (q *Queue) Revive(ctx context.Context, orgID string, deadID int64) (int64, error) {
	tx, err := q.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("jobs: begin revive: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var kind string
	var payload json.RawMessage
	err = tx.QueryRow(ctx,
		`DELETE FROM jobs_dead WHERE id = $1 AND org_id = $2 RETURNING kind, payload`,
		deadID, orgID).Scan(&kind, &payload)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, errors.New("jobs: no such dead letter")
	case err != nil:
		return 0, fmt.Errorf("jobs: take dead letter: %w", err)
	}

	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO jobs (org_id, kind, payload) VALUES ($1,$2,$3) RETURNING id`,
		orgID, kind, payload).Scan(&id); err != nil {
		return 0, fmt.Errorf("jobs: requeue dead letter: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("jobs: commit revive: %w", err)
	}
	return id, nil
}

// Discard removes a dead letter for good.
func (q *Queue) Discard(ctx context.Context, orgID string, deadID int64) error {
	ct, err := q.db.Exec(ctx,
		`DELETE FROM jobs_dead WHERE id = $1 AND org_id = $2`, deadID, orgID)
	if err != nil {
		return fmt.Errorf("jobs: discard dead letter: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return errors.New("jobs: no such dead letter")
	}
	return nil
}
