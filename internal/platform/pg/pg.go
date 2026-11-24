// Package pg owns the Postgres connection pools.
//
// There are three pools, not one, and that is the single most important thing
// in this file. They have different timeouts and different sizes because they
// have different jobs:
//
//   - web serves HTTP. Small, with a hard statement timeout, so one pathological
//     query cannot occupy a connection forever.
//   - jobs runs background work. Separate, so a job storm cannot starve HTTP,
//     and with no statement timeout, because a reindex legitimately takes minutes.
//   - listen holds LISTEN/NOTIFY. It must be dedicated: a listening connection
//     cannot be shared, and if the pool hands it to someone else the
//     notifications stop arriving with no error anywhere.
//
// Sharing one pool across all three is the mistake that turns a slow query into
// an outage.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pools is the set of pools the application uses.
type Pools struct {
	Web    *pgxpool.Pool
	Jobs   *pgxpool.Pool
	Listen *pgxpool.Pool
}

// Options configures pool construction.
type Options struct {
	// DSN is the Postgres connection string.
	DSN string
	// WebMaxConns and JobsMaxConns bound each pool independently.
	WebMaxConns  int32
	JobsMaxConns int32
	// WebStatementTimeout bounds any single query on the web pool.
	WebStatementTimeout time.Duration
	// ConnectTimeout bounds the initial connection at startup. Open keeps trying within it, because a name that does not
	// resolve for a few seconds, or a database that is restarting, is not a reason to give up a scheduled job's whole run.
	ConnectTimeout time.Duration
}

// DefaultOptions returns sensible pool sizing for a single node.
func DefaultOptions(dsn string) Options {
	return Options{
		DSN:                 dsn,
		WebMaxConns:         20,
		JobsMaxConns:        8,
		WebStatementTimeout: 15 * time.Second,
		ConnectTimeout:      30 * time.Second,
	}
}

// Open creates and verifies all three pools.
//
// It pings each one before returning. A pool that has never connected is not
// evidence of anything, and the whole point of failing at boot is that the
// process does not come up reporting healthy while unable to reach its database.
func Open(ctx context.Context, opts Options) (*Pools, error) {
	if opts.DSN == "" {
		return nil, errors.New("pg: DSN is empty")
	}

	ctx, cancel := context.WithTimeout(ctx, opts.ConnectTimeout)
	defer cancel()

	web, err := openRetry(ctx, opts.DSN, opts.WebMaxConns, opts.WebStatementTimeout)
	if err != nil {
		return nil, fmt.Errorf("pg: web pool: %w", err)
	}

	jobs, err := openRetry(ctx, opts.DSN, opts.JobsMaxConns, 0)
	if err != nil {
		web.Close()
		return nil, fmt.Errorf("pg: jobs pool: %w", err)
	}

	// Two connections, never more. This pool exists to hold LISTEN, and a
	// listener that gets recycled stops delivering silently.
	listen, err := openRetry(ctx, opts.DSN, 2, 0)
	if err != nil {
		web.Close()
		jobs.Close()
		return nil, fmt.Errorf("pg: listen pool: %w", err)
	}

	return &Pools{Web: web, Jobs: jobs, Listen: listen}, nil
}

// retryEvery is how long Open waits between tries while the database is unreachable.
var retryEvery = 2 * time.Second

// openPool is open, replaceable by a test.
var openPool = open

// openRetry opens one pool, trying again every few seconds until ctx ends. A DSN that does not parse is wrong
// whatever the network does, so it is never retried; anything else (a name that does not resolve, a refused
// connection, a database still starting) is. The error returned is the last one seen.
func openRetry(ctx context.Context, dsn string, maxConns int32, stmtTimeout time.Duration) (*pgxpool.Pool, error) {
	for {
		pool, err := openPool(ctx, dsn, maxConns, stmtTimeout)
		if err == nil {
			return pool, nil
		}
		if errors.Is(err, errBadDSN) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(retryEvery):
		}
	}
}

var errBadDSN = errors.New("bad DSN")

func open(ctx context.Context, dsn string, maxConns int32, stmtTimeout time.Duration) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DSN: %w: %w", errBadDSN, err)
	}
	cfg.MaxConns = maxConns
	cfg.MinConns = 0
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute
	cfg.HealthCheckPeriod = time.Minute

	if stmtTimeout > 0 {
		if cfg.ConnConfig.RuntimeParams == nil {
			cfg.ConnConfig.RuntimeParams = map[string]string{}
		}
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = fmt.Sprintf("%d", stmtTimeout.Milliseconds())
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// Close releases every pool.
func (p *Pools) Close() {
	if p == nil {
		return
	}
	// Listen first: it is the one holding a long-lived connection.
	for _, pool := range []*pgxpool.Pool{p.Listen, p.Jobs, p.Web} {
		if pool != nil {
			pool.Close()
		}
	}
}

// Probe reports whether the database is usable, for readiness.
//
// It takes its own short timeout rather than inheriting the request's, because a
// readiness probe that hangs is worse than one that fails: Kubernetes keeps
// sending traffic while it waits.
func (p *Pools) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	var one int
	if err := p.Web.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("postgres unreachable: %w", err)
	}
	if one != 1 {
		return fmt.Errorf("postgres returned %d for SELECT 1", one)
	}
	return nil
}

// WithAdvisoryLock runs fn while holding a session-level advisory lock, and
// returns false without running fn if another process already holds it.
//
// This is how exactly one replica runs migrations, and later how exactly one
// runs the outbox dispatcher. It replaces the distributed lock Redis was doing.
func (p *Pools) WithAdvisoryLock(ctx context.Context, key int64, fn func(context.Context, *pgxpool.Conn) error) (bool, error) {
	conn, err := p.Web.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&got); err != nil {
		return false, fmt.Errorf("acquire advisory lock %d: %w", key, err)
	}
	if !got {
		return false, nil
	}
	defer func() {
		// Best effort. The lock is session scoped, so it also dies with the
		// connection, which is the backstop if this ever fails.
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", key)
	}()

	if err := fn(ctx, conn); err != nil {
		return true, err
	}
	return true, nil
}

// WithAdvisoryLockWait runs fn while holding a session-level advisory lock,
// BLOCKING until the lock is available rather than declining.
//
// The difference from WithAdvisoryLock matters and is not stylistic. Use the
// try-once version when only one participant should ever do the work and the
// rest should carry on (an outbox dispatcher). Use this one when every
// participant needs the work to be FINISHED before it proceeds, and only one
// should perform it.
//
// Migrations are the second case. A replica that declines and immediately
// checks the schema can observe it mid-migration and fail, which during a
// rolling deploy is a crash loop that resolves only by luck of timing.
func (p *Pools) WithAdvisoryLockWait(ctx context.Context, key int64, wait time.Duration, fn func(context.Context, *pgxpool.Conn) error) error {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	conn, err := p.Web.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	// pg_advisory_lock blocks server-side. The context deadline is what bounds
	// it, so a stuck holder surfaces as a timeout rather than a hung pod.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		return fmt.Errorf("wait for advisory lock %d: %w", key, err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", key)
	}()

	return fn(ctx, conn)
}

// ErrNoRows is re-exported so callers do not import pgx directly just to check
// for a missing row.
var ErrNoRows = pgx.ErrNoRows
