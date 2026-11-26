package main

// Keeping Hearth's CRM and Mautic, the email marketing tool, in step.
//
//	hearthctl mautic-sync                          what would be sent and pulled, writing nothing (the default)
//	hearthctl mautic-sync -write                   do it
//	hearthctl mautic-sync -write -only ID,ID       only these Hearth contacts (a proof)
//
// Reads and writes Hearth's Postgres and talks to Mautic's REST API with an OAuth2 client of its own (MAUTIC_URL, MAUTIC_CLIENT_ID,
// MAUTIC_CLIENT_SECRET). docs/mautic-sync.md says what it does and what it never does.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/Embernet-ai/hearth/internal/mautic"
	"github.com/Embernet-ai/hearth/internal/mauticsync"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

func runMauticSync(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mautic-sync", flag.ExitOnError)
	write := fs.Bool("write", false, "send and record; without it nothing is written to either side")
	only := fs.String("only", "", "comma separated Hearth contact ids; empty is every contact with an email address")
	workers := fs.Int("workers", 8, "contacts sent at once")
	dsn := envFlag(fs, "dsn", "HEARTH_DATABASE_URL", "Postgres connection string, or HEARTH_DATABASE_URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	site := strings.TrimSpace(os.Getenv("MAUTIC_URL"))
	id, secret := strings.TrimSpace(os.Getenv("MAUTIC_CLIENT_ID")), strings.TrimSpace(os.Getenv("MAUTIC_CLIENT_SECRET"))
	for name, v := range map[string]string{"MAUTIC_URL": site, "MAUTIC_CLIENT_ID": id, "MAUTIC_CLIENT_SECRET": secret, "HEARTH_DATABASE_URL": *dsn} {
		if v == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if u, err := url.Parse(site); err != nil || u.Host == "" {
		return errors.New("MAUTIC_URL is not a URL")
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
	opt := mauticsync.Options{Write: *write, Workers: *workers}
	for _, s := range strings.Split(*only, ",") {
		if s = strings.TrimSpace(s); s != "" {
			opt.OnlyContacts = append(opt.OnlyContacts, s)
		}
	}
	rep, err := mauticsync.New(pools.Web, mautic.New(site, id, secret), orgID).Run(ctx, opt)
	rep.Print(os.Stdout)
	if err != nil {
		return err
	}
	if rep.Failed() {
		return errors.New("some records could not be written; run it again, it is safe to repeat")
	}
	return nil
}
