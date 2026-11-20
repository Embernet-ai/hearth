package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
	"github.com/Embernet-ai/hearth/internal/platform/config"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// subjectsFinder is how a command turns a person into what they may reach: *authz.Service.
type subjectsFinder interface {
	SubjectsFor(ctx context.Context, orgID, principalID string) (core.Subjects, error)
}

// backendStores are the stores a command that writes time or documents must go through. In the Postgres back end they are the
// Postgres services. With HEARTH_BACKEND=emberops they are the EmberOps stores, so a daily report or a person's imported time
// lands where the app reads it. Before this, daily-report and time-import built the Postgres services directly, and after
// the switch their rows would have existed only in Postgres, invisible to Hearth. The EmberOps stores act with the service
// client's token, because there is no signed-in person.
func backendStores(ctx context.Context, db *pgxpool.Pool, az *authz.Service) (timelog.Store, docs.Store, error) {
	pgDocs, pgTime := docs.New(db, az), timelog.New(db, az)
	if strings.ToLower(strings.TrimSpace(os.Getenv("HEARTH_BACKEND"))) != "emberops" {
		return pgTime, pgDocs, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	provider, err := oidc.NewProvider(ctx, strings.TrimSuffix(cfg.OIDCIssuer, "/"))
	if err != nil {
		return nil, nil, fmt.Errorf("discover the identity provider: %w", err)
	}
	client := emberops.New(emberops.Config{BaseURL: cfg.EmberOPSURL, CacheTTL: cfg.EmberOPSCacheTTL,
		Service: &emberops.ClientCredentials{TokenURL: provider.Endpoint().TokenURL, ClientID: cfg.EmberOPSClientID,
			ClientSecret: cfg.EmberOPSClientSecret, Scope: cfg.EmberOPSAudience}})
	set := stores.EmberOPS(client, stores.PG{Docs: pgDocs, Time: pgTime}, stores.Options{
		Loc: cfg.EmberOPSTimezone, Authz: az, CurrencyPerCent: cfg.EmberOPSCurrencyPerCent})
	return set.Time, set.Docs, nil
}
