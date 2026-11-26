package main

// Moving a space under another, from the command line, as a super admin.
//
//	hearthctl space-move -as ADMIN -space ID -parent ID [-agent HANDLE] [-team NAME]
//
// The move goes through the same call the Spaces page uses, so it refuses a loop and a move the admin may not make.
// -agent gives a service edit on the moved space and -team gives a team edit, so whoever works in the new home can
// reach what moved with it, the documents in it included. It is safe to run again: a space already under that parent stays there and the grants
// are added once.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/teams"
)

func runSpaceMove(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("space-move", flag.ExitOnError)
	dsn := envFlag(fs, "dsn", "HEARTH_DATABASE_URL", "Postgres URL, or HEARTH_DATABASE_URL")
	as := fs.String("as", "", "email of the super admin moving the space (required)")
	space := fs.String("space", "", "id of the space to move (required)")
	parent := fs.String("parent", "", "id of the space it goes inside (required)")
	agent := fs.String("agent", "", "handle of a service that gets edit on the moved space")
	team := fs.String("team", "", "name of a team that gets edit on the moved space")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *as == "" || strings.TrimSpace(*space) == "" || strings.TrimSpace(*parent) == "" {
		return errors.New("-as, -space and -parent are required")
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
	if err := set.Plant.MoveSpace(ctx, orgID, subjects, *space, parent); err != nil {
		return err
	}
	ref := core.Ref{Type: "space", ID: *space}
	var subs []core.Subject
	if h := strings.ToLower(strings.TrimSpace(*agent)); h != "" {
		var id string
		if err := pools.Web.QueryRow(ctx, `SELECT id::text FROM principals WHERE org_id=$1 AND kind='agent' AND handle=$2 AND status='active'`, orgID, h).Scan(&id); err != nil {
			return fmt.Errorf("no active service called %q: %w", h, err)
		}
		subs = append(subs, core.Subject{Kind: core.SubjectPrincipal, ID: id})
	}
	if t := strings.TrimSpace(*team); t != "" {
		list, err := teams.New(pools.Web).List(ctx, orgID)
		if err != nil {
			return err
		}
		granted := false
		for _, tm := range list {
			if strings.EqualFold(tm.Name, t) {
				subs = append(subs, core.Subject{Kind: core.SubjectTeam, ID: tm.ID})
				granted = true
			}
		}
		if !granted {
			return fmt.Errorf("there is no team called %q", t)
		}
	}
	// A document carries its own copy of its space's grants from the day it was filed, so what already sat in the
	// moved space (and in any space inside it) is given the same grants, or the new home's people would see the
	// space and none of its work.
	var docs []string
	rows, err := pools.Web.Query(ctx, `
		WITH RECURSIVE sub AS (
		    SELECT id FROM spaces WHERE id = $2::uuid AND org_id = $1
		  UNION ALL
		    SELECT c.id FROM spaces c JOIN sub ON c.parent_id = sub.id
		)
		SELECT d.id::text FROM documents d JOIN sub ON d.space_id = sub.id
		WHERE d.org_id = $1 AND d.deleted_at IS NULL`, orgID, *space)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		docs = append(docs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// A grant already held is left alone, so running this never lowers somebody's access.
	has := func(r core.Ref, sub core.Subject) (bool, error) {
		var n int
		err := pools.Web.QueryRow(ctx, `SELECT count(*) FROM grants WHERE entity_type=$1 AND entity_id=$2::uuid AND subject_key=$3`,
			string(r.Type), r.ID, sub.Key()).Scan(&n)
		return n > 0, err
	}
	for _, sub := range subs {
		targets := []core.Ref{ref}
		for _, d := range docs {
			targets = append(targets, core.Ref{Type: core.EntityDocument, ID: d})
		}
		for _, t := range targets {
			if held, err := has(t, sub); err != nil {
				return err
			} else if held {
				continue
			}
			if err := az.Grant(ctx, orgID, t, sub, core.AccessEdit, adminID); err != nil {
				return err
			}
		}
	}
	fmt.Printf("space %s is now inside %s\n", *space, *parent)
	return nil
}
