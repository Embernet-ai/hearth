package main

// A service's own token, minted from the command line by a super admin.
//
//	hearthctl service-token -as ADMIN -name phone-bridge [-team Sales] [-scope write|read] [-days N] -out FILE
//
// A service (the phone system's caller id bridge is the first) must not borrow a person's token: the day
// that person leaves, or changes team, the service stops or quietly sees something else. So this makes the
// service its own principal, an agent with its own handle, puts it on one team so the CRM's grants show it
// that team's records and nothing else, and mints it a token with the write scope (which includes read), because
// a company service reads and writes: the phone system looks callers up, logs each call and keeps contacts. -scope
// read makes a read-only token for a service that only ever looks things up.
//
// It is idempotent: the agent is found by handle if it exists, the team add is a no-op the second time, and
// a new token is minted each run (the old ones are listed on the Integrations page and revoked there).
// The token goes to -out, mode 600, and is never printed. -as must be a super admin; anybody else is refused
// by the same check the People page uses.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/agents"
	"github.com/Embernet-ai/hearth/internal/api"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/teams"
)

func runServiceToken(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("service-token", flag.ExitOnError)
	dsn := envFlag(fs, "dsn", "HEARTH_DATABASE_URL", "Postgres URL, or HEARTH_DATABASE_URL")
	as := fs.String("as", "", "email of the super admin making the token (required)")
	name := fs.String("name", "", "the service's name, for example phone-bridge (required)")
	team := fs.String("team", "Sales", "the team the service joins, which decides what it can read")
	scope := fs.String("scope", api.ScopeWrite, "write (reads and writes) or read")
	days := fs.Int("days", 0, "days until the token expires, 0 for none")
	out := fs.String("out", "", "file the token is written to, mode 600 (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *as == "" || *name == "" || *out == "" {
		return errors.New("-as, -name and -out are required")
	}
	if *scope != api.ScopeWrite && *scope != api.ScopeRead {
		return errors.New("-scope is write or read")
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("a database is required: -dsn or HEARTH_DATABASE_URL")
	}
	pools, err := pg.Open(ctx, pg.DefaultOptions(*dsn))
	if err != nil {
		return err
	}
	defer pools.Close()
	orgID, _, err := soleOwner(ctx, pools)
	if err != nil {
		return err
	}
	actor, err := personByEmail(ctx, pools.Web, orgID, *as)
	if err != nil {
		return err
	}
	secret, svcID, err := makeServiceToken(ctx, pools, orgID, actor, *name, *team, *scope, *days)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, []byte(secret+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Printf("service %s (%s) is on team %s with a %s token, written to %s (not printed)\n", *name, svcID, *team, *scope, *out)
	return nil
}

// makeServiceToken is the work, apart from the flags and the file, so a test can run it against a database it
// built. It returns the token secret and the service principal's id.
func makeServiceToken(ctx context.Context, pools *pg.Pools, orgID, actor, name, team, scope string, days int) (string, string, error) {
	az := authz.New(pools.Web)
	held, err := az.Capabilities(ctx, orgID, actor)
	if err != nil {
		return "", "", err
	}
	if !held.IsSuperAdmin() {
		return "", "", errors.New("only a super admin can make a service token")
	}

	handle := strings.ToLower(strings.TrimSpace(name))
	var svcID string
	err = pools.Web.QueryRow(ctx, `SELECT id::text FROM principals
		WHERE org_id=$1 AND kind='agent' AND handle=$2 AND status='active'`, orgID, handle).Scan(&svcID)
	if err != nil {
		a, cerr := agents.New(pools.Web, az).Create(ctx, orgID, actor, name, handle)
		if cerr != nil {
			return "", "", fmt.Errorf("make the service principal: %w", cerr)
		}
		svcID = a.ID
	}

	ts := teams.New(pools.Web)
	list, err := ts.List(ctx, orgID)
	if err != nil {
		return "", "", err
	}
	teamID := ""
	for _, t := range list {
		if strings.EqualFold(t.Name, team) {
			teamID = t.ID
		}
	}
	if teamID == "" {
		return "", "", fmt.Errorf("there is no team called %q", team)
	}
	if err := ts.Add(ctx, orgID, actor, teamID, svcID); err != nil {
		return "", "", err
	}

	var ttl time.Duration
	if days > 0 {
		ttl = time.Duration(days) * 24 * time.Hour
	}
	tok, err := api.New(pools.Web, az, nil).Mint(ctx, orgID, svcID, name, []string{scope}, ttl)
	if err != nil {
		return "", "", err
	}
	return tok.Secret, svcID, nil
}
