package main

// Taking a company out of Hearth's CRM by id, with its contacts, through the same
// service functions the web page's delete buttons call, as the organisation's
// owner. It is how a proof company is removed when no one is signed in, and it
// names every id it deletes.
//
//	hearthctl crm-delete -company ID[,ID]
//	hearthctl crm-delete -contact ID[,ID]     contacts on their own, such as ones with no company
//	hearthctl crm-delete -deal ID[,ID]        deals
//	hearthctl crm-delete -activity ID[,ID]    notes and calls, from every timeline they are on
//
// Given together they go in the order that works: activities, then deals, then
// contacts, then companies, so nothing is removed from under something that still
// needs it to be seen.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

func runCRMDelete(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("crm-delete", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres connection string, or HEARTH_DATABASE_URL")
	ids := fs.String("company", "", "comma separated company ids to delete, with their contacts")
	contactIDs := fs.String("contact", "", "comma separated contact ids to delete on their own")
	dealIDs := fs.String("deal", "", "comma separated deal ids to delete")
	activityIDs := fs.String("activity", "", "comma separated note or call ids to delete")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*ids) == "" && strings.TrimSpace(*contactIDs) == "" &&
		strings.TrimSpace(*dealIDs) == "" && strings.TrimSpace(*activityIDs) == "" {
		return errors.New("crm-delete needs -company, -contact, -deal or -activity with the ids to delete")
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("a database is required: -dsn or HEARTH_DATABASE_URL")
	}
	pools, err := pg.Open(ctx, pg.DefaultOptions(*dsn))
	if err != nil {
		return err
	}
	defer pools.Close()
	az := authz.New(pools.Web)
	orgID, _, err := soleOwner(ctx, pools)
	if err != nil {
		return err
	}
	// A delete needs admin on the record. A super admin is deliberately only an
	// editor of what others made (authz.LevelFor), and the way to delete one is an
	// explicit admin grant, which leaves a row saying who decided it. The command
	// acts as the longest standing super admin and grants itself admin on each record
	// it is about to delete, then deletes through the same service the web page uses.
	var principalID string
	if err := pools.Web.QueryRow(ctx, `SELECT principal_id::text FROM capability_grants
		WHERE org_id=$1 AND capability='super_admin' ORDER BY granted_at LIMIT 1`, orgID).Scan(&principalID); err != nil {
		return fmt.Errorf("find a super admin to act as: %w", err)
	}
	actor, err := az.SubjectsFor(ctx, orgID, principalID)
	if err != nil {
		return fmt.Errorf("resolve the owner's subjects: %w", err)
	}
	svc := crm.New(pools.Web, az)
	for _, id := range strings.Split(*activityIDs, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if err := svc.DeleteActivity(ctx, orgID, actor, id); err != nil {
			return fmt.Errorf("delete activity %s: %w", id, err)
		}
		fmt.Printf("deleted activity %s\n", id)
	}
	for _, id := range strings.Split(*dealIDs, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if err := az.Grant(ctx, orgID, core.Ref{Type: core.EntityDeal, ID: id},
			core.Subject{Kind: core.SubjectPrincipal, ID: principalID}, core.AccessAdmin, principalID); err != nil {
			return fmt.Errorf("grant admin on deal %s: %w", id, err)
		}
		if err := svc.DeleteDeal(ctx, orgID, actor, id); err != nil {
			return fmt.Errorf("delete deal %s: %w", id, err)
		}
		fmt.Printf("deleted deal %s\n", id)
	}
	for _, id := range strings.Split(*contactIDs, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if err := az.Grant(ctx, orgID, core.Ref{Type: core.EntityContact, ID: id},
			core.Subject{Kind: core.SubjectPrincipal, ID: principalID}, core.AccessAdmin, principalID); err != nil {
			return fmt.Errorf("grant admin on contact %s: %w", id, err)
		}
		if err := svc.DeleteContact(ctx, orgID, actor, id); err != nil {
			return fmt.Errorf("delete contact %s: %w", id, err)
		}
		fmt.Printf("deleted contact %s\n", id)
	}
	for _, id := range strings.Split(*ids, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		rows, err := pools.Web.Query(ctx, `SELECT id::text, name FROM contacts WHERE org_id=$1 AND company_id=$2 AND deleted_at IS NULL`, orgID, id)
		if err != nil {
			return err
		}
		type ct struct{ id, name string }
		var cts []ct
		for rows.Next() {
			var c ct
			if err := rows.Scan(&c.id, &c.name); err != nil {
				rows.Close()
				return err
			}
			cts = append(cts, c)
		}
		rows.Close()
		for _, c := range cts {
			if err := az.Grant(ctx, orgID, core.Ref{Type: core.EntityContact, ID: c.id},
				core.Subject{Kind: core.SubjectPrincipal, ID: principalID}, core.AccessAdmin, principalID); err != nil {
				return fmt.Errorf("grant admin on contact %s: %w", c.id, err)
			}
			if err := svc.DeleteContact(ctx, orgID, actor, c.id); err != nil {
				return fmt.Errorf("delete contact %s: %w", c.id, err)
			}
			fmt.Printf("deleted contact %s %s\n", c.id, c.name)
		}
		var name string
		_ = pools.Web.QueryRow(ctx, `SELECT name FROM companies WHERE id=$1 AND org_id=$2`, id, orgID).Scan(&name)
		if err := az.Grant(ctx, orgID, core.Ref{Type: core.EntityCompany, ID: id},
			core.Subject{Kind: core.SubjectPrincipal, ID: principalID}, core.AccessAdmin, principalID); err != nil {
			return fmt.Errorf("grant admin on company %s: %w", id, err)
		}
		if err := svc.DeleteCompany(ctx, orgID, actor, id); err != nil {
			return fmt.Errorf("delete company %s: %w", id, err)
		}
		fmt.Printf("deleted company %s %s\n", id, name)
	}
	return nil
}
