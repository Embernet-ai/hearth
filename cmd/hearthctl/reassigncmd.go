package main

// Moving work onto the people it belongs to, from a plan.
//
//	hearthctl reassign -csv plan.csv                      read the plan, report, write nothing
//	hearthctl reassign -csv plan.csv -apply -reverse-out undo.csv
//
// The plan is a CSV with `id` and `proposed_ids` (principal ids joined by ";",
// the first is the primary) and optionally `current_assignees` (names, to stop
// a stale plan) and `primary_id`. The reverse file it writes has the same
// shape, so `-restore -csv undo.csv -apply` puts everything back.
//
// A run is one transaction that proves itself before it commits: see
// docs.Reassign. Without -apply it does the whole job and rolls back, so the
// counts it prints are exactly what -apply would produce.

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

func runReassign(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("reassign", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres URL, or HEARTH_DATABASE_URL")
	planPath := fs.String("csv", "", "the plan: a CSV with id and proposed_ids")
	apply := fs.Bool("apply", false, "write it. Without this the run is rolled back")
	reverseOut := fs.String("reverse-out", "", "where to write the plan that undoes this one (required with -apply)")
	restore := fs.Bool("restore", false, "this plan is a reverse file: allow people who are no longer active")
	as := fs.String("as", "", "email of the account that makes the change (default: the owner)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *planPath == "" {
		return errors.New("-csv is required")
	}
	if *apply && *reverseOut == "" {
		return errors.New("-apply needs -reverse-out: the file that undoes it is written before anything is")
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("a database is required: -dsn or HEARTH_DATABASE_URL")
	}
	plan, err := readPlan(*planPath)
	if err != nil {
		return err
	}

	pools, err := pg.Open(ctx, pg.DefaultOptions(*dsn))
	if err != nil {
		return err
	}
	defer pools.Close()
	orgID, owner, err := soleOwner(ctx, pools)
	if err != nil {
		return err
	}
	actor := owner
	if *as != "" {
		if err := pools.Web.QueryRow(ctx, `
			SELECT p.id::text FROM principals p
			  LEFT JOIN user_identities u ON u.principal_id = p.id
			  LEFT JOIN local_credentials l ON l.principal_id = p.id
			 WHERE p.org_id = $1 AND p.status = 'active'
			   AND lower(COALESCE(u.email::text, l.email::text)) = lower($2) LIMIT 1`, orgID, *as).Scan(&actor); err != nil {
			return fmt.Errorf("find %s: %w", *as, err)
		}
	}
	svc := docs.New(pools.Web, authz.New(pools.Web))

	// Always a dry pass first: it is the source of the reverse file, and a
	// plan that fails fails here before anything is written.
	rep, err := svc.Reassign(ctx, orgID, actor, plan, false, *restore)
	if err != nil {
		return err
	}
	printReassign(rep, false)
	if !*apply {
		fmt.Println("\ndry run: nothing was written")
		return nil
	}
	if err := writeReverse(*reverseOut, rep); err != nil {
		return err
	}
	fmt.Printf("\nreverse plan written to %s (%d rows)\n", *reverseOut, len(rep.Reverse))
	done, err := svc.Reassign(ctx, orgID, actor, plan, true, *restore)
	if err != nil {
		return err
	}
	printReassign(done, true)
	return nil
}

func printReassign(r *docs.ReassignReport, committed bool) {
	state := "would change"
	if committed {
		state = "changed"
	}
	fmt.Printf("%d rows in the plan, %s %d\n", r.Rows, state, r.Changed)
	fmt.Printf("owner_id, created_by, updated_at checksum before and after: %s / %s  (%s)\n",
		r.ChecksumBefore, r.ChecksumAfter, map[bool]string{true: "identical", false: "DIFFERENT"}[r.ChecksumBefore == r.ChecksumAfter])
	seen := map[string]bool{}
	var ids []string
	for id := range r.Before {
		seen[id] = true
		ids = append(ids, id)
	}
	for id := range r.After {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return r.After[ids[i]] > r.After[ids[j]] })
	fmt.Printf("\n%-32s %8s %8s\n", "person", "before", "after")
	for _, id := range ids {
		fmt.Printf("%-32s %8d %8d\n", r.Names[id], r.Before[id], r.After[id])
	}
}

func readPlan(path string) ([]docs.Reassignment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cr := csv.NewReader(f)
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	for _, need := range []string{"id", "proposed_ids"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("the plan has no %q column", need)
		}
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	var plan []docs.Reassignment
	seen := map[string]bool{}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		id := get(rec, "id")
		if id == "" {
			continue
		}
		if seen[id] {
			return nil, fmt.Errorf("document %s appears twice in the plan", id)
		}
		seen[id] = true
		var ids []string
		for _, a := range strings.Split(get(rec, "proposed_ids"), ";") {
			if a = strings.TrimSpace(a); a != "" {
				ids = append(ids, a)
			}
		}
		row := docs.Reassignment{DocID: id, Assignees: ids}
		if len(ids) > 0 {
			row.Primary = ids[0]
			if p := get(rec, "primary_id"); p != "" {
				row.Primary = p
			}
		}
		if _, ok := col["current_assignees"]; ok {
			row.ExpectCurrent = []string{}
			for _, n := range strings.Split(get(rec, "current_assignees"), ";") {
				if n = strings.TrimSpace(n); n != "" {
					row.ExpectCurrent = append(row.ExpectCurrent, n)
				}
			}
		}
		plan = append(plan, row)
	}
	if len(plan) == 0 {
		return nil, errors.New("the plan has no rows")
	}
	return plan, nil
}

func writeReverse(path string, r *docs.ReassignReport) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("reverse file: %w", err)
	}
	cw := csv.NewWriter(f)
	_ = cw.Write([]string{"id", "proposed_ids", "primary_id", "was"})
	for _, rv := range r.Reverse {
		var was []string
		for _, a := range rv.Assignees {
			was = append(was, r.Names[a])
		}
		_ = cw.Write([]string{rv.DocID, strings.Join(rv.Assignees, ";"), rv.Primary, strings.Join(was, ";")})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
