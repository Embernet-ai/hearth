package main

// Approving one person's waiting time by their own auto-approve setting.
//
//	hearthctl time-approve -person EMAIL -reason WHY                 how many would be approved, nothing changed
//	hearthctl time-approve -person EMAIL -reason WHY -apply          approve them
//	hearthctl time-approve -person EMAIL -reason WHY -apply -limit N approve at most N this run
//
// For a salaried person whose time is approved by standing order, and whose time
// from before the setting began is still waiting. The decider is the setting, as
// it is for time filed after it: the person's own auto-approve, with the reason as
// the decision note. It is never a human's name. It refuses while the person's
// setting is off (turn it on first with person-time), and it touches only that
// person's finished, waiting, not removed entries. Every batch leaves one
// time.approve.bulk audit row that names no actor, the person, and the reason.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// approveBatch is how many entries one AutoApproveMany call decides, so a refusal
// names a small set and an interrupted run has already recorded its batches.
const approveBatch = 50

// waitingOf keeps a person's finished entries that nobody has decided and that
// have not been removed, oldest first.
func waitingOf(es []timelog.Entry) []timelog.Entry {
	var out []timelog.Entry
	for _, e := range es {
		if e.EndedAt != nil && e.DecidedAt == nil && e.DeletedAt == nil {
			out = append(out, e)
		}
	}
	return out
}

func runTimeApprove(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("time-approve", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres URL, or HEARTH_DATABASE_URL")
	person := fs.String("person", "", "email of the person whose waiting time is approved; their auto-approve must be on (required)")
	reason := fs.String("reason", "", "why: it is the decision note on every entry and goes on the audit rows (required)")
	apply := fs.Bool("apply", false, "approve them. Without this nothing changes")
	limit := fs.Int("limit", 0, "approve at most this many in this run (0 is all), oldest first")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *person == "" || strings.TrimSpace(*reason) == "" {
		return errors.New("-person and -reason are required")
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
	personID, err := personByEmail(ctx, pools.Web, orgID, *person)
	if err != nil {
		return err
	}
	svc, _, err := backendStores(ctx, pools.Web, authz.New(pools.Web))
	if err != nil {
		return err
	}
	since, err := svc.AutoApproveSince(ctx, orgID, personID)
	if err != nil {
		return err
	}
	if since == nil {
		return timelog.ErrAutoApproveOff
	}
	all, err := svc.Mine(ctx, orgID, personID, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().AddDate(1, 0, 0), false)
	if err != nil {
		return err
	}
	waiting := waitingOf(all)
	minutes := 0
	for _, e := range waiting {
		minutes += e.Elapsed()
	}
	fmt.Printf("%s has %d entries waiting (%.1f hours); their auto-approve has been on since %s\n",
		*person, len(waiting), float64(minutes)/60, since.UTC().Format(time.RFC3339))
	if *limit > 0 && len(waiting) > *limit {
		waiting = waiting[:*limit]
	}
	if !*apply {
		fmt.Printf("would approve %d; nothing changed, add -apply\n", len(waiting))
		return nil
	}
	done := 0
	for len(waiting) > 0 {
		n := min(approveBatch, len(waiting))
		ids := make([]string, n)
		for i, e := range waiting[:n] {
			ids[i] = e.ID
		}
		got, err := svc.AutoApproveMany(ctx, orgID, personID, ids, strings.TrimSpace(*reason))
		if err != nil {
			return fmt.Errorf("approved %d, then: %w", done, err)
		}
		done += got
		waiting = waiting[n:]
		fmt.Printf("approved %d\n", done)
	}
	fmt.Printf("approved %d entries of %s by their auto-approve setting, one time.approve.bulk audit row per batch of %d\n", done, *person, approveBatch)
	return nil
}
