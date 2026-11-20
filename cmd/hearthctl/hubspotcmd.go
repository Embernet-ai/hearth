package main

// Bringing what changed in HubSpot since the last import into Hearth's CRM.
//
//	hearthctl hubspot-import -since 2026-09-01T00:00:00Z               dry run: what would be created, linked, updated, left alone
//	hearthctl hubspot-import -apply                                    write it, and remember where each object was read to
//	hearthctl hubspot-import -only companies,contacts                  limit the objects (companies contacts deals notes calls)
//
// Reads HubSpot with a private app token (HUBSPOT_TOKEN) and writes Hearth's Postgres through the CRM service, so every
// record gets its grants, event and an audit row. A dry run is the default and writes nothing, including no high-water
// mark. -since is only needed the first time: after an applied run each object remembers its own mark. A record the first
// import carried across is found again from its HubSpot id; a field somebody edited in Hearth is never overwritten, and
// is listed as a conflict. See internal/importer/hubspot.

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/importer/hubspot"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

func runHubSpotImport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("hubspot-import", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres connection string, or HEARTH_DATABASE_URL")
	apply := fs.Bool("apply", false, "write the changes. Without it nothing is written and no high-water mark moves")
	sinceFlag := fs.String("since", "", "RFC 3339 time to read from for an object with no high-water mark yet (when the last import ran)")
	baselineFlag := fs.String("baseline", "", "RFC 3339 time the first import finished: a record it carried across, untouched in Hearth since, may be updated (default -since)")
	only := fs.String("only", "", "comma separated objects: companies, contacts, deals, notes, calls (default all)")
	ownerFile := fs.String("owner-file", "", "lines of HUBSPOT_OWNER_ID,email, for when the token cannot read owners")
	as := fs.String("as", "", "email of the account the import acts as (default: the owner)")
	migrate := fs.Bool("migrate", false, "apply pending migrations first (the import's own tables are migration 61)")
	maxConflicts := fs.Int("max-conflicts", 300, "how many conflicts the report lists (all are counted)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	token := strings.TrimSpace(os.Getenv("HUBSPOT_TOKEN"))
	if token == "" {
		return errors.New("HUBSPOT_TOKEN is required")
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("a database is required: -dsn or HEARTH_DATABASE_URL")
	}
	opt := hubspot.Options{Apply: *apply, MaxConflicts: *maxConflicts, Log: func(s string) { fmt.Fprintln(os.Stderr, s) }}
	var err error
	if *sinceFlag != "" {
		if opt.Since, err = time.Parse(time.RFC3339, *sinceFlag); err != nil {
			return fmt.Errorf("-since: %w", err)
		}
	}
	if *baselineFlag != "" {
		if opt.Baseline, err = time.Parse(time.RFC3339, *baselineFlag); err != nil {
			return fmt.Errorf("-baseline: %w", err)
		}
	}
	for _, o := range strings.Split(*only, ",") {
		if o = strings.TrimSpace(o); o != "" {
			known := false
			for _, k := range hubspot.Order {
				known = known || k == o
			}
			if !known {
				return fmt.Errorf("-only: %q is not one of %s", o, strings.Join(hubspot.Order, ", "))
			}
			opt.Only = append(opt.Only, o)
		}
	}
	if *ownerFile != "" {
		if opt.OwnerEmails, err = readOwnerFile(*ownerFile); err != nil {
			return err
		}
	}

	pools, err := pg.Open(ctx, pg.DefaultOptions(*dsn))
	if err != nil {
		return err
	}
	defer pools.Close()
	if *migrate {
		if err := pg.Migrate(ctx, pools, slog.New(slog.NewTextHandler(os.Stderr, nil))); err != nil {
			return err
		}
	}
	az := authz.New(pools.Web)
	orgID, ownerID, err := soleOwner(ctx, pools)
	if err != nil {
		return err
	}
	list, err := auth.New(pools.Web, az, false).People(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list Hearth people: %w", err)
	}
	var people []hubspot.Person
	actorID, matched := ownerID, *as == ""
	for _, p := range list {
		if p.Email == "" || (p.Status != "" && p.Status != "active") {
			continue
		}
		people = append(people, hubspot.Person{PrincipalID: p.PrincipalID, Email: p.Email})
		if !matched && strings.EqualFold(strings.TrimSpace(p.Email), strings.TrimSpace(*as)) {
			actorID, matched = p.PrincipalID, true
		}
	}
	if !matched {
		return fmt.Errorf("-as: no active account has the email %s", *as)
	}
	actor, err := az.SubjectsFor(ctx, orgID, actorID)
	if err != nil {
		return fmt.Errorf("resolve the acting account: %w", err)
	}

	client := hubspot.NewClient(token)
	if b := strings.TrimSpace(os.Getenv("HUBSPOT_API_BASE")); b != "" {
		client.Base = b
	}
	rep, err := hubspot.New(pools.Web, crm.New(pools.Web, az), client, orgID, actor, people, opt).Run(ctx)
	if rep != nil {
		rep.Print(os.Stdout)
	}
	if err != nil {
		return err
	}
	if rep.Failed() {
		return errors.New("some records failed; nothing about them was remembered, run it again, it is safe to repeat")
	}
	return nil
}

func readOwnerFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, email, ok := strings.Cut(strings.NewReplacer("=", ",", "\t", ",").Replace(line), ",")
		if !ok || strings.TrimSpace(id) == "" || !strings.Contains(email, "@") {
			return nil, fmt.Errorf("%s line %d: want HUBSPOT_OWNER_ID,email", path, n)
		}
		out[strings.TrimSpace(id)] = strings.TrimSpace(email)
	}
	return out, sc.Err()
}
