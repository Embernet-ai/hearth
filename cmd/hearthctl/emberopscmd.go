package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/migrate"
	"github.com/Embernet-ai/hearth/internal/platform/config"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// runEmberOPSMigrate copies Hearth's Postgres data into EmberOPS.
//
// It reads the same environment as the server will after the switch, so run it
// with HEARTH_BACKEND=emberops and the HEARTH_EMBEROPS_* and HEARTH_OIDC_*
// settings in place; the running server keeps its own environment until it is
// restarted on the new one. A dry run reads and maps everything and writes
// nothing, to either side.
func runEmberOPSMigrate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("emberops-migrate", flag.ExitOnError)
	dry := fs.Bool("dry-run", false, "read and map everything, write nothing")
	restart := fs.Bool("restart", false, "ignore saved progress and start every step again")
	only := fs.String("only", "", "comma separated step names to run, e.g. tasks,work orders")
	batch := fs.Int("batch", 200, "rows per request (at most 500)")
	activity := fs.String("activity-type", "", "Activity Type for time entries that carry no tag")
	warehouse := fs.String("warehouse", "", "warehouse for part movements (default: Hearth Settings on the EmberOps site)")
	unit := fs.Float64("currency-per-cent", 0.01, "value of one cent in the site's currency")
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.EmberOPSBackend() {
		return errors.New("set HEARTH_BACKEND=emberops and the HEARTH_EMBEROPS_* settings in this command's environment " +
			"(the running server can stay on postgres until you switch it)")
	}

	pools, err := pg.Open(ctx, pg.DefaultOptions(cfg.DatabaseURL))
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pools.Close()

	var client *emberops.Client
	if !*dry {
		provider, err := oidc.NewProvider(ctx, strings.TrimSuffix(cfg.OIDCIssuer, "/"))
		if err != nil {
			return fmt.Errorf("discover the identity provider: %w", err)
		}
		client = emberops.New(emberops.Config{
			BaseURL: cfg.EmberOPSURL,
			// A page of two hundred tasks takes a real Frappe site longer than the 30 seconds the live client gives a call
			// (each is saved, assigned and tagged), so the migration waits longer for each one.
			HTTP: &http.Client{Timeout: 15 * time.Minute},
			Service: &emberops.ClientCredentials{
				TokenURL: provider.Endpoint().TokenURL, ClientID: cfg.EmberOPSClientID,
				ClientSecret: cfg.EmberOPSClientSecret, Scope: cfg.EmberOPSAudience,
			},
		})
	}

	opts := migrate.Options{
		DryRun: *dry, Restart: *restart, BatchSize: *batch, Loc: cfg.EmberOPSTimezone,
		ActivityType: *activity, CRMParties: cfg.EmberOPSCRMParties == "emberops", Warehouse: *warehouse, CurrencyUnit: *unit,
		Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	if *only != "" {
		for _, s := range strings.Split(*only, ",") {
			opts.Only = append(opts.Only, strings.TrimSpace(s))
		}
	}
	rep, err := migrate.NewRunner(pools.Web, client).Run(ctx, opts)
	rep.Write(os.Stdout)
	if err != nil {
		return err
	}
	if !rep.Ok() {
		return errors.New("some records were not accepted by EmberOps")
	}
	return nil
}
