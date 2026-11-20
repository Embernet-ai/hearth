package main

// A person's time routing, set from the command line by a super admin, for the
// two settings the People page sets with a click.
//
//	hearthctl person-time -as ADMIN -person EMAIL -approver EMAIL      who signs off their time ("none" clears it)
//	hearthctl person-time -as ADMIN -person EMAIL -auto-approve on     time they log from now on approves itself
//	hearthctl person-time -as ADMIN -person EMAIL -auto-approve off
//
// It calls the same authz.SetApprover and authz.SetTimeAutoApprove the People
// page calls, as the named super admin, so the same checks and the same audit
// rows apply and anybody who is not a super admin is refused.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// personByEmail finds an active person in the org by sign-in email.
func personByEmail(ctx context.Context, db *pgxpool.Pool, orgID, email string) (string, error) {
	var id string
	err := db.QueryRow(ctx, `
		SELECT p.id::text FROM principals p
		  LEFT JOIN user_identities u ON u.principal_id = p.id
		  LEFT JOIN local_credentials l ON l.principal_id = p.id
		 WHERE p.org_id = $1 AND p.status = 'active'
		   AND lower(COALESCE(u.email::text, l.email::text)) = lower($2) LIMIT 1`, orgID, email).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("find %s: %w", email, err)
	}
	return id, nil
}

func runPersonTime(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("person-time", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres URL, or HEARTH_DATABASE_URL")
	as := fs.String("as", "", "email of the super admin making the change (required)")
	person := fs.String("person", "", "email of the person whose time routing changes (required)")
	approver := fs.String("approver", "", "email of who signs off their time, or none to clear")
	auto := fs.String("auto-approve", "", "on or off: their time is approved as they log it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *as == "" || *person == "" || (*approver == "" && *auto == "") {
		return errors.New("-as, -person, and at least one of -approver or -auto-approve are required")
	}
	if *auto != "" && *auto != "on" && *auto != "off" {
		return errors.New("-auto-approve is on or off")
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
	target, err := personByEmail(ctx, pools.Web, orgID, *person)
	if err != nil {
		return err
	}
	az := authz.New(pools.Web)
	if *approver != "" {
		who := ""
		if !strings.EqualFold(*approver, "none") {
			if who, err = personByEmail(ctx, pools.Web, orgID, *approver); err != nil {
				return err
			}
		}
		if err := az.SetApprover(ctx, orgID, actor, target, who); err != nil {
			return err
		}
		fmt.Printf("%s: time is signed off by %s\n", *person, map[bool]string{true: "their department", false: *approver}[who == ""])
	}
	if *auto != "" {
		if err := az.SetTimeAutoApprove(ctx, orgID, actor, target, *auto == "on"); err != nil {
			return err
		}
		fmt.Printf("%s: time is approved automatically: %s\n", *person, *auto)
	}
	return nil
}
