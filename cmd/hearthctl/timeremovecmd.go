package main

// Taking time entries out, as a super admin, from a list.
//
//	hearthctl time-remove -as EMAIL -file IDS -reason WHY            what would go, nothing changed
//	hearthctl time-remove -as EMAIL -file IDS -reason WHY -apply     remove them
//
// The file holds one entry id per line, or a CSV whose first column is the id
// (a first line that says id is a header and is skipped). Every entry that goes
// gets its own audit row naming the admin and the reason, and the whole list is
// one transaction: either every one is removed or none is. Entries already
// removed are reported and left alone. The entries are soft-removed, not deleted,
// so Restore on the time page still brings one back.

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// readEntryIDs parses the id file. Any line that is not an id stops the run.
func readEntryIDs(r io.Reader) ([]string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	var ids []string
	for line := 1; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if len(rec) == 0 {
			continue
		}
		v := strings.TrimSpace(rec[0])
		if v == "" {
			continue
		}
		if line == 1 && strings.EqualFold(v, "id") {
			continue
		}
		u, err := uuid.Parse(v)
		if err != nil {
			return nil, fmt.Errorf("line %d: %q is not an entry id", line, v)
		}
		ids = append(ids, u.String())
	}
	if len(ids) == 0 {
		return nil, errors.New("the file names no entries")
	}
	return ids, nil
}

func runTimeRemove(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("time-remove", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres URL, or HEARTH_DATABASE_URL")
	as := fs.String("as", "", "email of the super admin removing the entries (required)")
	file := fs.String("file", "", "the entry ids, one per line or a CSV with the id first (required)")
	reason := fs.String("reason", "", "why: it goes on every audit row (required)")
	apply := fs.Bool("apply", false, "remove them. Without this nothing changes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *as == "" || *file == "" || strings.TrimSpace(*reason) == "" {
		return errors.New("-as, -file and -reason are required")
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("a database is required: -dsn or HEARTH_DATABASE_URL")
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	ids, err := readEntryIDs(f)
	_ = f.Close()
	if err != nil {
		return err
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
	actorID, err := personByEmail(ctx, pools.Web, orgID, *as)
	if err != nil {
		return err
	}
	az := authz.New(pools.Web)
	actor, err := az.SubjectsFor(ctx, orgID, actorID)
	if err != nil {
		return fmt.Errorf("resolve %s's access: %w", *as, err)
	}
	lines, err := timelog.New(pools.Web, az).RemoveMany(ctx, orgID, actor, ids, *reason, *apply)
	if err != nil {
		return err
	}
	removed, already, minutes := 0, 0, 0
	for _, l := range lines {
		verb := "would remove"
		switch {
		case l.AlreadyRemoved:
			verb, already = "already removed", already+1
		case l.Removed:
			verb, removed, minutes = "removed", removed+1, minutes+l.Minutes
		default:
			removed, minutes = removed+1, minutes+l.Minutes
		}
		fmt.Printf("%-16s %s  %s  %4d min  %s  %s\n", verb, l.ID, l.StartedAt.Format("2006-01-02"), l.Minutes, l.PersonName, l.Note)
	}
	if *apply {
		fmt.Printf("removed %d entries (%d minutes), %d already removed, one audit row each, reason: %s\n", removed, minutes, already, strings.TrimSpace(*reason))
	} else {
		fmt.Printf("would remove %d entries (%d minutes), %d already removed; nothing changed, add -apply\n", removed, minutes, already)
	}
	return nil
}
