package main

// Making a space, and sharing it with a service, from the command line, as a super admin.
//
//	hearthctl space-create -as ADMIN -name "Codex beads" [-kind project] [-parent ID] [-agent HANDLE] [-team NAME]
//
// The space is made through the same call the Spaces page uses, so a space that already has that name is returned,
// not doubled. -agent gives a service (made by service-token) edit on the space, so it can file work in it, and -team
// gives a team edit, so the people on it see and work in it. It is safe to run again.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/teams"
)

func runSpaceCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("space-create", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres URL, or HEARTH_DATABASE_URL")
	as := fs.String("as", "", "email of the super admin making the space (required)")
	name := fs.String("name", "", "the space's name (required)")
	kind := fs.String("kind", "project", "folder, project or area")
	parent := fs.String("parent", "", "id of the space it goes inside")
	agent := fs.String("agent", "", "handle of a service that gets edit on it")
	team := fs.String("team", "", "name of a team that gets edit on it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *as == "" || strings.TrimSpace(*name) == "" {
		return errors.New("-as and -name are required")
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
	az := authz.New(pools.Web)
	adminID, err := personByEmail(ctx, pools.Web, orgID, *as)
	if err != nil {
		return err
	}
	subjects, err := az.SubjectsFor(ctx, orgID, adminID)
	if err != nil {
		return err
	}
	set, err := backendSet(ctx, pools.Web, az)
	if err != nil {
		return err
	}
	var par *string
	if strings.TrimSpace(*parent) != "" {
		par = parent
	}
	sp, err := set.Plant.CreateSpace(ctx, orgID, subjects, *name, *kind, par)
	if err != nil {
		return err
	}
	ref := core.Ref{Type: "space", ID: sp.ID}
	if h := strings.ToLower(strings.TrimSpace(*agent)); h != "" {
		var id string
		if err := pools.Web.QueryRow(ctx, `SELECT id::text FROM principals WHERE org_id=$1 AND kind='agent' AND handle=$2 AND status='active'`, orgID, h).Scan(&id); err != nil {
			return fmt.Errorf("no active service called %q: %w", h, err)
		}
		if err := az.Grant(ctx, orgID, ref, core.Subject{Kind: core.SubjectPrincipal, ID: id}, core.AccessEdit, adminID); err != nil {
			return err
		}
	}
	if t := strings.TrimSpace(*team); t != "" {
		list, err := teams.New(pools.Web).List(ctx, orgID)
		if err != nil {
			return err
		}
		granted := false
		for _, tm := range list {
			if strings.EqualFold(tm.Name, t) {
				if err := az.Grant(ctx, orgID, ref, core.Subject{Kind: core.SubjectTeam, ID: tm.ID}, core.AccessEdit, adminID); err != nil {
					return err
				}
				granted = true
			}
		}
		if !granted {
			return fmt.Errorf("there is no team called %q", t)
		}
	}
	fmt.Printf("space %q (%s) is %s\n", sp.Name, sp.Kind, sp.ID)
	return nil
}
