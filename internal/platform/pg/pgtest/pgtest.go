// Package pgtest gives a test binary a database of its own.
//
// It exists because of a real and confusing failure. `go test ./...` runs
// packages in parallel, every integration package pointed at the same
// TEST_DATABASE_URL, and five of them drive the job queue: jobs, maint, notify,
// rules and search. Neither jobs.Queue.Claim nor jobs.Relay.Drain is scoped to
// an organisation, and they are right not to be, because in production there is
// one queue and one worker pool and that is the point of them.
//
// In a test it means one package's drain claims another package's job and runs
// it through the wrong worker. The package that was waiting for that job then
// sees nothing happen and reports something like "the rule left 0 comments,
// want 1", which reads as a bug in rules and is nothing of the sort. It moves
// between runs depending on scheduling, so it looks like flake and gets
// re-run rather than fixed.
//
// CI already learned this exact lesson for the acceptance suite, and the
// comment there says so: a suite that needs a database to itself has to be
// given one rather than asked to share carefully.
package pgtest

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// safeName keeps an identifier to characters that cannot end a quoted
// identifier early. The name is a constant written by us rather than anything a
// caller supplies, and this is here so that stays true.
var safeName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,48}$`)

type made struct {
	dsn string
	err error
}

var (
	mu    sync.Mutex
	built = map[string]*made{}
)

// Database returns a DSN for a freshly migrated database called name.
//
// It is created once per test binary however many times it is called, dropped
// and recreated at the start rather than cleaned up at the end. A run that
// crashes half way leaves a database behind either way, and recreating on the
// way in means the next run starts empty whether or not the last one finished.
//
// Skips the test when TEST_DATABASE_URL is unset, exactly as the fixtures did
// before, so a machine with no Postgres still runs the unit tests.
func Database(t *testing.T, name string) string {
	t.Helper()

	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if !safeName.MatchString(name) {
		t.Fatalf("pgtest: %q is not a usable database name", name)
	}

	mu.Lock()
	defer mu.Unlock()
	if m, ok := built[name]; ok {
		if m.err != nil {
			t.Fatalf("pgtest: %v", m.err)
		}
		return m.dsn
	}

	dsn, err := create(base, name)
	built[name] = &made{dsn: dsn, err: err}
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	return dsn
}

func create(base, name string) (string, error) {
	ctx := context.Background()

	// Connect to the server the caller named, but to the maintenance database,
	// because a database cannot be dropped from a connection to itself.
	admin, err := swapDatabase(base, "postgres")
	if err != nil {
		return "", err
	}
	pool, err := pgxpool.New(ctx, admin)
	if err != nil {
		return "", fmt.Errorf("connect to postgres: %w", err)
	}
	defer pool.Close()

	// FORCE, because a pool left open by an earlier run would otherwise make
	// the drop fail and the whole package skip on a stale database.
	if _, err := pool.Exec(ctx, `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`); err != nil {
		return "", fmt.Errorf("drop %s: %w", name, err)
	}
	if _, err := pool.Exec(ctx, `CREATE DATABASE "`+name+`"`); err != nil {
		return "", fmt.Errorf("create %s: %w", name, err)
	}

	dsn, err := swapDatabase(base, name)
	if err != nil {
		return "", err
	}

	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		return "", fmt.Errorf("open %s: %w", name, err)
	}
	defer pools.Close()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := pg.Migrate(ctx, pools, log); err != nil {
		return "", fmt.Errorf("migrate %s: %w", name, err)
	}
	return dsn, nil
}

// swapDatabase rewrites the database name in a DSN, leaving the host,
// credentials and every query parameter alone. sslmode in particular has to
// survive, because losing it turns a working connection into a TLS error that
// looks nothing like a test harness problem.
func swapDatabase(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("TEST_DATABASE_URL is not a URL: %w", err)
	}
	u.Path = "/" + strings.TrimPrefix(name, "/")
	return u.String(), nil
}
