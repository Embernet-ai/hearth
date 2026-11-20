package main

// Google Workspace groups follow Hearth.
//
//	hearthctl groups-sync -config groups.json            what would change, writing nothing (the default)
//	hearthctl groups-sync -config groups.json -apply     make it so
//
// The config maps each group to Hearth teams and departments (see
// internal/groupsync). The service account key comes from
// HEARTH_GOOGLE_GROUPS_KEY_FILE and the admin it acts as from
// HEARTH_GOOGLE_GROUPS_SUBJECT. Only plain members are added or removed.

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
	keyFile, subject := os.Getenv("HEARTH_GOOGLE_GROUPS_KEY_FILE"), os.Getenv("HEARTH_GOOGLE_GROUPS_SUBJECT")
	if keyFile == "" || subject == "" {
		return errors.New("HEARTH_GOOGLE_GROUPS_KEY_FILE and HEARTH_GOOGLE_GROUPS_SUBJECT are required")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return err
	}
	dir, err := groupsync.NewGoogle(key, subject)
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
