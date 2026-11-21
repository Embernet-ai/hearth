package main

// Taking accounts and channels out as a super admin, the way the People and channel pages would, from a shell.
//
//	hearthctl person-suspend -as ADMIN -email A,B -reason WHY            who would be suspended, nothing changed
//	hearthctl person-suspend -as ADMIN -email A,B -reason WHY -apply     suspend them
//	hearthctl person-suspend -as ADMIN -id UUID,UUID -reason WHY -apply  the same, by principal id (an account whose sign-in identity is gone)
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
	"io"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

func runPersonSuspend(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("person-suspend", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres URL, or HEARTH_DATABASE_URL")
	as := fs.String("as", "", "email of the super admin suspending them (required)")
	emails := fs.String("email", "", "the accounts to suspend, emails separated by commas")
	ids := fs.String("id", "", "the accounts to suspend by principal id, separated by commas: for an account whose sign-in identity is gone")
	reason := fs.String("reason", "", "why, printed with the result (required)")
	apply := fs.Bool("apply", false, "suspend them. Without this nothing changes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *as == "" || (*emails == "" && *ids == "") || strings.TrimSpace(*reason) == "" {
		return errors.New("-as, -reason and at least one of -email or -id are required")
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
	n, err := suspendPeople(ctx, pools, orgID, actor, splitList(*emails), splitList(*ids), *apply, os.Stdout)
	if err != nil {
		return err
	}
	if *apply {
		fmt.Printf("suspended %d accounts, one audit row each, reason: %s\n", n, strings.TrimSpace(*reason))
	} else {
		fmt.Printf("would suspend %d accounts; nothing changed, add -apply\n", n)
	}
	return nil
}

func splitList(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// personByID is an account of this organisation by principal id, active or not
// yet suspended, for an account whose sign-in identity has been deleted and which
// personByEmail can no longer find. It must be a person: a bot or an agent is
// never suspended from here.
func personByID(ctx context.Context, db *pgxpool.Pool, orgID, id string) (string, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("%q is not a principal id", id)
	}
	var found string
	err := db.QueryRow(ctx, `
		SELECT id::text FROM principals
		 WHERE id = $1::uuid AND org_id = $2 AND kind = 'user' AND status = 'active'`, id, orgID).Scan(&found)
	if err != nil {
		return "", fmt.Errorf("find principal %s: %w", id, err)
	}
	return found, nil
}

// suspendPeople finds every named account first (by email or by principal id) and
// stops before any change if one is not an active person, then suspends each
// through the call the People page makes. It returns how many.
func suspendPeople(ctx context.Context, pools *pg.Pools, orgID, actor string, emails, ids []string, apply bool, w io.Writer) (int, error) {
	type target struct{ label, id, name string }
	var targets []target
	seen := map[string]bool{}
	add := func(label, id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		var name string
		_ = pools.Web.QueryRow(ctx, `SELECT COALESCE(NULLIF(display_name,''), id::text) FROM principals WHERE id = $1`, id).Scan(&name)
		targets = append(targets, target{label, id, name})
	}
	for _, e := range emails {
		id, err := personByEmail(ctx, pools.Web, orgID, e)
		if err != nil {
			return 0, fmt.Errorf("%w (nothing was changed)", err)
		}
		add(e, id)
	}
	for _, raw := range ids {
		id, err := personByID(ctx, pools.Web, orgID, raw)
		if err != nil {
			return 0, fmt.Errorf("%w (nothing was changed)", err)
		}
		add("id "+raw, id)
	}
	if len(targets) == 0 {
		return 0, errors.New("-email and -id name nobody")
	}
	svc := auth.New(pools.Web, authz.New(pools.Web), false)
	for _, t := range targets {
		var teams []string
		rows, err := pools.Web.Query(ctx, `SELECT t.name FROM team_members m JOIN teams t ON t.id = m.team_id WHERE m.principal_id = $1 ORDER BY 1`, t.id)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			teams = append(teams, n)
		}
		rows.Close()
		verb := "would suspend"
		if apply {
			if err := svc.SetPersonStatus(ctx, orgID, actor, t.id, "suspended"); err != nil {
				return 0, fmt.Errorf("%s: %w", t.label, err)
			}
			verb = "suspended"
		}
		fmt.Fprintf(w, "%-14s %s  %s  %s  teams: %s\n", verb, t.id, t.label, t.name, strings.Join(teams, ", "))
	}
	return len(targets), nil
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
