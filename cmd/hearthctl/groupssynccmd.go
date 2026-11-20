package main

// Google Workspace groups follow Hearth.
//
//	hearthctl groups-sync -config groups.json            what would change, writing nothing (the default)
//	hearthctl groups-sync -config groups.json -apply     make it so
//
// The config maps each group to Hearth teams and departments (see
// internal/groupsync). How it reaches Google is in groupsDirectory. Only plain
// members are added or removed.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Embernet-ai/hearth/internal/groupsync"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

func runGroupsSync(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("groups-sync", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres URL, or HEARTH_DATABASE_URL")
	cfgPath := fs.String("config", "", "the mapping file (JSON)")
	apply := fs.Bool("apply", false, "change the groups; without it nothing is written")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*dsn) == "" || *cfgPath == "" {
		return errors.New("a database (-dsn or HEARTH_DATABASE_URL) and -config are required")
	}
	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		return err
	}
	var cfg groupsync.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	dir, err := groupsDirectory()
	if err != nil {
		return err
	}
	pools, err := pg.Open(ctx, pg.DefaultOptions(*dsn))
	if err != nil {
		return err
	}
	defer pools.Close()

	changes, err := groupsync.Run(ctx, pools.Web, dir, cfg, *apply)
	for _, c := range changes {
		verb := "add   "
		if c.Remove {
			verb = "remove"
			if c.Skipped {
				verb = "keep  " // would remove; removals are off
			}
		}
		fmt.Printf("%s %-40s %-40s %s\n", verb, c.Group, c.Email, c.Why)
	}
	mode := "dry run, nothing written"
	if *apply {
		mode = "applied"
	}
	fmt.Printf("%d change(s), %s\n", len(changes), mode)
	return err
}

// groupsDirectory picks how to reach Google, in this order: a ready access
// token (an operator's one hour run), the keyless Vord federation the cluster
// uses, or a service account key file where keys are allowed.
func groupsDirectory() (*groupsync.Google, error) {
	subject := os.Getenv("HEARTH_GOOGLE_GROUPS_SUBJECT")
	if tok := os.Getenv("HEARTH_GOOGLE_GROUPS_ACCESS_TOKEN"); tok != "" {
		return &groupsync.Google{Static: tok}, nil
	}
	if os.Getenv("HEARTH_GOOGLE_GROUPS_VORD_TOKEN_URL") != "" {
		return groupsync.NewFederatedGoogle(&groupsync.Federated{
			VordTokenURL:   os.Getenv("HEARTH_GOOGLE_GROUPS_VORD_TOKEN_URL"),
			ClientID:       os.Getenv("HEARTH_GOOGLE_GROUPS_CLIENT_ID"),
			ClientSecret:   os.Getenv("HEARTH_GOOGLE_GROUPS_CLIENT_SECRET"),
			Audience:       os.Getenv("HEARTH_GOOGLE_GROUPS_AUDIENCE"),
			ServiceAccount: os.Getenv("HEARTH_GOOGLE_GROUPS_SERVICE_ACCOUNT"),
		}, subject)
	}
	keyFile := os.Getenv("HEARTH_GOOGLE_GROUPS_KEY_FILE")
	if keyFile == "" || subject == "" {
		return nil, errors.New("set HEARTH_GOOGLE_GROUPS_VORD_TOKEN_URL (keyless), or HEARTH_GOOGLE_GROUPS_KEY_FILE and HEARTH_GOOGLE_GROUPS_SUBJECT")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	return groupsync.NewGoogle(key, subject)
}
