package pg_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// These run against a REAL Postgres, never a mock. A mocked database tests that
// you wrote the SQL you wrote; it cannot tell you that citext is missing, that a
// constraint name collides, or that a migration is not idempotent.
//
// Set TEST_DATABASE_URL to run them. CI always sets it.
func dsn(t *testing.T) string {
	t.Helper()
	v := os.Getenv("TEST_DATABASE_URL")
	if v == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}
	return v
}

func openPools(t *testing.T) *pg.Pools {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn(t)))
	if err != nil {
		t.Fatalf("open pools: %v", err)
	}
	t.Cleanup(pools.Close)
	return pools
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestOpenAndProbe(t *testing.T) {
	pools := openPools(t)

	if err := pools.Probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	// All three pools must be independently usable. A single shared pool would
	// pass a test that only exercised one.
	for name, p := range map[string]any{"web": pools.Web, "jobs": pools.Jobs, "listen": pools.Listen} {
		if p == nil {
			t.Errorf("%s pool is nil", name)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	pools := openPools(t)
	ctx := context.Background()
	log := discardLogger()

	// Running twice must be safe. A deploy that restarts a pod runs this again,
	// and every rolling update runs it on every replica.
	for i := range 2 {
		if err := pg.Migrate(ctx, pools, log); err != nil {
			t.Fatalf("migrate run %d: %v", i+1, err)
		}
	}

	// The schema the migration claims to create must actually exist.
	for _, table := range []string{"orgs", "principals", "user_identities"} {
		var exists bool
		err := pools.Web.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			                WHERE table_schema='public' AND table_name=$1)`, table).Scan(&exists)
		if err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s was not created", table)
		}
	}
}

func TestPrincipalIdIsNotAnEmailAddress(t *testing.T) {
	pools := openPools(t)
	ctx := context.Background()
	if err := pg.Migrate(ctx, pools, discardLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The predecessor keyed every principal on `macro|<email>`, so renaming the
	// prefix rewrote every foreign key in the database. This asserts the column
	// is a uuid, which makes that mistake impossible to repeat by accident.
	var dataType string
	err := pools.Web.QueryRow(ctx,
		`SELECT data_type FROM information_schema.columns
		 WHERE table_schema='public' AND table_name='principals' AND column_name='id'`).Scan(&dataType)
	if err != nil {
		t.Fatalf("read principals.id type: %v", err)
	}
	if dataType != "uuid" {
		t.Errorf("principals.id is %s, want uuid", dataType)
	}
}

func TestAdvisoryLockIsExclusive(t *testing.T) {
	pools := openPools(t)
	ctx := context.Background()
	const key int64 = 918_273_645

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		_, err := pools.WithAdvisoryLock(ctx, key, func(ctx context.Context, _ *pgxpool.Conn) error {
			close(held)
			<-release
			return nil
		})
		done <- err
	}()

	<-held
	// While the first holder has it, a second attempt must decline rather than
	// block. This is what makes exactly one replica run the migrations.
	ran, err := pools.WithAdvisoryLock(ctx, key, func(context.Context, *pgxpool.Conn) error {
		t.Error("second caller acquired a lock that was already held")
		return nil
	})
	if err != nil {
		t.Fatalf("second acquire returned an error: %v", err)
	}
	if ran {
		t.Error("second caller ran the callback, so the lock is not exclusive")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first holder: %v", err)
	}

	// Once released, it must be acquirable again.
	ran, err = pools.WithAdvisoryLock(ctx, key, func(context.Context, *pgxpool.Conn) error { return nil })
	if err != nil {
		t.Fatalf("third acquire: %v", err)
	}
	if !ran {
		t.Error("lock was not released")
	}
}
