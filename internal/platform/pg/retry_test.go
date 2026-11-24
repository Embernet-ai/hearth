package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A scheduled job that starts while the database name does not resolve waits for it and goes on, instead of dying at
// the first try and leaving a Failed Job behind (hearth-mautic-sync-29924730: "lookup postgres: no such host").
func TestOpenKeepsTryingWhileTheDatabaseIsUnreachable(t *testing.T) {
	old, oldEvery := openPool, retryEvery
	t.Cleanup(func() { openPool, retryEvery = old, oldEvery })
	retryEvery = time.Millisecond
	calls := 0
	openPool = func(ctx context.Context, dsn string, n int32, d time.Duration) (*pgxpool.Pool, error) {
		calls++
		if calls < 4 {
			return nil, errors.New("ping: hostname resolving error: lookup postgres: no such host")
		}
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := openRetry(ctx, "postgres://x", 1, 0); err != nil || calls != 4 {
		t.Fatalf("calls = %d, err = %v, want it to succeed on the fourth try", calls, err)
	}
}

func TestOpenGivesUpWhenTheTimeIsUpAndNeverRetriesABadDSN(t *testing.T) {
	old, oldEvery := openPool, retryEvery
	t.Cleanup(func() { openPool, retryEvery = old, oldEvery })
	retryEvery = time.Millisecond
	openPool = func(context.Context, string, int32, time.Duration) (*pgxpool.Pool, error) {
		return nil, errors.New("ping: connection refused")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := openRetry(ctx, "postgres://x", 1, 0); err == nil {
		t.Fatal("an unreachable database did not end in an error")
	}
	openPool = old
	if _, err := Open(context.Background(), Options{DSN: "not a dsn %%%", ConnectTimeout: time.Minute}); err == nil {
		t.Fatal("a bad DSN was accepted")
	}
	start := time.Now()
	if _, err := Open(context.Background(), Options{DSN: "postgres://u@h:notaport/db", ConnectTimeout: time.Minute}); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("a bad DSN was retried for %v (err %v)", time.Since(start), err)
	}
}
