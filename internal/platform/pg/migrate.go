package pg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/Embernet-ai/hearth/migrations"
)

// migrationLockKey is the advisory lock every replica contends for before
// migrating. The value is arbitrary but must never change: two versions of the
// binary taking different locks would migrate concurrently, which is the exact
// thing this prevents.
const migrationLockKey int64 = 8_022_026_001

// Migrate applies all pending migrations while holding an advisory lock, so
// exactly one replica does it during a rolling deploy.
//
// A replica that loses the race does not fail. It waits, then VERIFIES the
// schema is current, because "another instance is handling it" is not evidence
// that it succeeded, and a binary running against a schema older than it expects
// fails later, further away, and much more confusingly.
func Migrate(ctx context.Context, pools *Pools, log *slog.Logger) error {
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(gooseLogger{log: log})
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}

	// goose wants a database/sql handle. Deriving it from the pgx pool means
	// there is exactly one connection configuration and no second one to drift.
	db := stdlib.OpenDBFromPool(pools.Web)
	defer db.Close()

	// BLOCKING lock, deliberately. Every replica waits here; exactly one does
	// the work and the rest find the schema already current when their turn
	// comes. Trying once and giving up lets a replica observe a half-migrated
	// schema and fail, which during a rolling deploy is a crash loop that
	// resolves only by luck of timing. Found by running two migrations
	// concurrently in the test suite.
	err := pools.WithAdvisoryLockWait(ctx, migrationLockKey, migrationLockWait,
		func(ctx context.Context, _ *pgxpool.Conn) error {
			before, err := goose.GetDBVersionContext(ctx, db)
			if err != nil {
				return fmt.Errorf("read schema version: %w", err)
			}
			if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
				return fmt.Errorf("apply migrations: %w", err)
			}
			after, err := goose.GetDBVersionContext(ctx, db)
			if err != nil {
				return fmt.Errorf("read schema version after migrating: %w", err)
			}
			switch {
			case after == before:
				log.Info("schema already current", "version", after)
			default:
				log.Info("schema migrated", "from", before, "to", after)
			}
			return nil
		})
	if err != nil {
		return err
	}

	// Verify regardless of who did the work. Applying migrations is not the
	// same claim as the schema being what this binary needs.
	return VerifyCurrent(ctx, db)
}

// migrationLockWait bounds how long a replica waits for whoever is migrating.
// Generous, because a large migration on a real database is slow, but finite,
// because a pod that hangs forever is harder to diagnose than one that exits.
const migrationLockWait = 5 * time.Minute

// VerifyCurrent returns an error if the database schema is older than the
// migrations compiled into this binary.
func VerifyCurrent(ctx context.Context, db *sql.DB) error {
	collected, err := goose.CollectMigrations(migrations.Dir, 0, goose.MaxVersion)
	if err != nil {
		return fmt.Errorf("collect migrations: %w", err)
	}
	last, err := collected.Last()
	if err != nil {
		if errors.Is(err, goose.ErrNoNextVersion) {
			return nil // no migrations exist yet
		}
		return fmt.Errorf("read latest migration: %w", err)
	}
	current, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current < last.Version {
		return fmt.Errorf("schema is at version %d but this binary requires %d", current, last.Version)
	}
	return nil
}

type gooseLogger struct{ log *slog.Logger }

func (g gooseLogger) Printf(format string, v ...any) {
	g.log.Info(fmt.Sprintf(format, v...))
}

func (g gooseLogger) Fatalf(format string, v ...any) {
	g.log.Error(fmt.Sprintf(format, v...))
}
