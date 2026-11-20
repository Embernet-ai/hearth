package main

// Taking accounts and channels out as a super admin, the way the People and channel pages would, from a shell.
//
//	hearthctl person-suspend -as ADMIN -email A,B -reason WHY            who would be suspended, nothing changed
//	hearthctl person-suspend -as ADMIN -email A,B -reason WHY -apply     suspend them
//	hearthctl channel-remove -as ADMIN -channel ID -reason WHY [-apply]  archive a channel
//
// person-suspend goes through auth.SetPersonStatus, the same call the People page makes: the admin check, the owner and
// self refusals, the teams they leave, their sessions ended and the audit row all come with it. The whole list is one
// check first and then one change each, and a name that is not an active account stops the run before anything changes.
// channel-remove archives rather than deletes (archived_at), so every message is still in the database, and writes an
// audit row naming the admin and the reason.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

func runPersonSuspend(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("person-suspend", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres URL, or HEARTH_DATABASE_URL")
	as := fs.String("as", "", "email of the super admin suspending them (required)")
	emails := fs.String("email", "", "the accounts to suspend, emails separated by commas (required)")
	reason := fs.String("reason", "", "why, printed with the result (required)")
	apply := fs.Bool("apply", false, "suspend them. Without this nothing changes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *as == "" || *emails == "" || strings.TrimSpace(*reason) == "" {
		return errors.New("-as, -email and -reason are required")
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
	type target struct{ email, id, name string }
	var targets []target
	seen := map[string]bool{}
	for _, e := range strings.Split(*emails, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		id, err := personByEmail(ctx, pools.Web, orgID, e)
		if err != nil {
			return fmt.Errorf("%w (nothing was changed)", err)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		var name string
		_ = pools.Web.QueryRow(ctx, `SELECT COALESCE(NULLIF(display_name,''), id::text) FROM principals WHERE id = $1`, id).Scan(&name)
		targets = append(targets, target{e, id, name})
	}
	if len(targets) == 0 {
		return errors.New("-email names nobody")
	}
	svc := auth.New(pools.Web, authz.New(pools.Web), false)
	for _, t := range targets {
		var teams []string
		rows, err := pools.Web.Query(ctx, `SELECT t.name FROM team_members m JOIN teams t ON t.id = m.team_id WHERE m.principal_id = $1 ORDER BY 1`, t.id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			teams = append(teams, n)
		}
		rows.Close()
		verb := "would suspend"
		if *apply {
			if err := svc.SetPersonStatus(ctx, orgID, actor, t.id, "suspended"); err != nil {
				return fmt.Errorf("%s: %w", t.email, err)
			}
			verb = "suspended"
		}
		fmt.Printf("%-14s %s  %s  %s  teams: %s\n", verb, t.id, t.email, t.name, strings.Join(teams, ", "))
	}
	if *apply {
		fmt.Printf("suspended %d accounts, one audit row each, reason: %s\n", len(targets), strings.TrimSpace(*reason))
	} else {
		fmt.Printf("would suspend %d accounts; nothing changed, add -apply\n", len(targets))
	}
	return nil
}

func runChannelRemove(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("channel-remove", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres URL, or HEARTH_DATABASE_URL")
	as := fs.String("as", "", "email of the super admin removing the channel (required)")
	channel := fs.String("channel", "", "the channel id, or its first eight characters (required)")
	reason := fs.String("reason", "", "why: it goes on the audit row (required)")
	apply := fs.Bool("apply", false, "archive it. Without this nothing changes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *as == "" || *channel == "" || strings.TrimSpace(*reason) == "" {
		return errors.New("-as, -channel and -reason are required")
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
	svc := auth.New(pools.Web, authz.New(pools.Web), false)
	admin, err := svc.IsAdmin(ctx, orgID, actor)
	if err != nil {
		return err
	}
	if !admin {
		return auth.ErrNotOwner
	}
	return removeChannel(ctx, pools, orgID, actor, strings.TrimSpace(*channel), strings.TrimSpace(*reason), *apply)
}

// removeChannel archives one channel in one transaction with its audit row. A short id must match exactly one
// channel, so a typo cannot archive a different one.
func removeChannel(ctx context.Context, pools *pg.Pools, orgID, actorID, ref, reason string, apply bool) error {
	tx, err := pools.Web.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id, name, visibility string
	var messages, members int
	q := `SELECT c.id::text, c.name, c.visibility,
	             (SELECT count(*) FROM messages m WHERE m.channel_id = c.id),
	             (SELECT count(*) FROM channel_members cm WHERE cm.channel_id = c.id)
	        FROM channels c WHERE c.org_id = $1 AND c.archived_at IS NULL AND `
	var row pgx.Row
	if u, perr := uuid.Parse(ref); perr == nil {
		row = tx.QueryRow(ctx, q+`c.id = $2 FOR UPDATE OF c`, orgID, u.String())
	} else {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM channels WHERE org_id = $1 AND archived_at IS NULL AND id::text LIKE $2 || '%'`, orgID, ref).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("%q matches %d active channels, it has to match exactly one", ref, n)
		}
		row = tx.QueryRow(ctx, q+`c.id::text LIKE $2 || '%' FOR UPDATE OF c`, orgID, ref)
	}
	if err := row.Scan(&id, &name, &visibility, &messages, &members); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("no active channel %q", ref)
		}
		return err
	}
	verb := "would archive"
	if apply {
		if _, err := tx.Exec(ctx, `UPDATE channels SET archived_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			OrgID: orgID, ActorID: actorID, Action: audit.ActionChannelArchive,
			EntityType: "channel", EntityID: id,
			Before: map[string]any{"name": name, "visibility": visibility, "messages": messages, "members": members},
			After:  map[string]any{"archived": true, "reason": reason},
		}); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		verb = "archived"
	}
	fmt.Printf("%-13s %s  %s  %s  %d messages, %d members\n", verb, id, name, visibility, messages, members)
	if apply {
		fmt.Printf("archived 1 channel, one audit row, reason: %s\n", reason)
	} else {
		fmt.Println("would archive 1 channel; nothing changed, add -apply")
	}
	return nil
}
