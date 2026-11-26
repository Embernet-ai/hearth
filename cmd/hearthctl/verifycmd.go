package main

// Reconciling the import against the source.
//
// "It said created 1,887" is not evidence. The count a migration prints is what
// it believes it did, computed from the same code that did it, so a bug that
// skipped a list is a bug that also does not mention the list. The only honest
// check reads both sides independently and compares.
//
// Three questions, and the second and third are the ones nobody asks until it
// is too late:
//
//	Is everything there?      every source task resolves to a document here
//	Is anything extra?        nothing here claims a source id the source
//	                          does not have -- a duplicate, or something a
//	                          half-run left behind
//	Did the fields arrive?    counted, not assumed: assignees, real dates,
//	                          statuses, custom values
//
//	hearthctl verify-clickup -token pk_...

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/Embernet-ai/hearth/internal/importer/clickup"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

func runVerifyClickUp(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("verify-clickup", flag.ExitOnError)
	token := envFlag(fs, "token", "CLICKUP_TOKEN", "ClickUp API token, or CLICKUP_TOKEN")
	dsn := envFlag(fs, "dsn", "HEARTH_DATABASE_URL", "Postgres URL, or HEARTH_DATABASE_URL")
	archived := fs.Bool("archived", true, "count archived lists too")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*token) == "" {
		return errors.New("a ClickUp token is required")
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("a database is required")
	}

	client := clickup.NewClient(*token)
	client.Archived = *archived

	pools, err := pg.Open(ctx, pg.DefaultOptions(*dsn))
	if err != nil {
		return err
	}
	defer pools.Close()

	// ---- the source ------------------------------------------------------
	lists, err := client.Lists(ctx)
	if err != nil {
		return err
	}
	source := map[string]string{} // task id -> space
	// What the status SHOULD be here, computed from the source's own words
	// through the same mapping the importer uses. Comparing against this is
	// the only way to know a status is right rather than merely plausible: a
	// count of 204 in progress looks reasonable whether or not it is the same
	// 204 ClickUp has.
	wantStatus := map[string]string{}
	rawStatus := map[string]string{}
	perSpace := map[string]int{}
	fmt.Println("reading the source...")
	for i, l := range lists {
		if l.Tasks == 0 {
			continue
		}
		tasks, err := client.TasksIn(ctx, l.ID)
		if err != nil {
			fmt.Printf("  could not read %q in %q: %v\n", l.Name, l.Space, err)
			continue
		}
		for _, t := range tasks {
			if t.ExternalID != "" {
				source[t.ExternalID] = l.Space
				wantStatus[t.ExternalID] = clickup.MapStatus(t.Status)
				rawStatus[t.ExternalID] = t.Status
				perSpace[l.Space]++
			}
		}
		if (i+1)%40 == 0 {
			fmt.Printf("  %d/%d lists\n", i+1, len(lists))
		}
	}

	// ---- this side -------------------------------------------------------
	// Tasks only, on both sides.
	//
	// external_refs also holds the knowledge base -- ClickUp Docs, which are a
	// different API and never appear in a task list -- and every imported
	// comment. Comparing all of them against the task walk reported the 130
	// wiki pages as "extra", which reads as 130 things that should not be here
	// when they are the whole reason the docs pass exists.
	here := map[string]bool{}
	rows, err := pools.Web.Query(ctx, `
		SELECT e.external_id
		FROM external_refs e
		JOIN documents d ON d.id = e.entity_id
		WHERE e.source = 'clickup' AND e.entity_type = 'document'
		  AND e.external_id NOT LIKE 'comment:%'
		  AND d.kind <> 'wiki'
		  AND d.deleted_at IS NULL`)
	if err != nil {
		return fmt.Errorf("read imported refs: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		here[id] = true
	}
	rows.Close()

	// ---- compare ---------------------------------------------------------
	missingBySpace := map[string]int{}
	missing := 0
	for id, space := range source {
		if !here[id] {
			missing++
			missingBySpace[space]++
		}
	}
	extra := 0
	for id := range here {
		if _, ok := source[id]; !ok {
			extra++
		}
	}

	fmt.Println()
	fmt.Println("reconciliation")
	fmt.Printf("  in ClickUp                 %5d tasks\n", len(source))
	fmt.Printf("  imported here              %5d documents\n", len(here))
	fmt.Printf("  MISSING (in source, not here) %5d\n", missing)
	fmt.Printf("  EXTRA   (here, not in source) %5d\n", extra)
	if missing > 0 {
		fmt.Println("  missing by space:")
		names := make([]string, 0, len(missingBySpace))
		for s := range missingBySpace {
			names = append(names, s)
		}
		sort.Strings(names)
		for _, s := range names {
			fmt.Printf("    %-24s %d of %d\n", s, missingBySpace[s], perSpace[s])
		}
	}

	// ---- did the fields arrive? -----------------------------------------
	// Counted from the database rather than trusted from the run, because a
	// field that silently failed to write looks identical to one that was
	// never there.
	fmt.Println()
	fmt.Println("fields, counted here")
	for _, q := range []struct {
		label string
		sql   string
	}{
		{"task documents from ClickUp", `SELECT count(*) FROM external_refs e JOIN documents d ON d.id=e.entity_id WHERE e.source='clickup' AND e.entity_type='document' AND e.external_id NOT LIKE 'comment:%' AND d.kind<>'wiki' AND d.deleted_at IS NULL`},
		{"knowledge base pages from ClickUp", `SELECT count(*) FROM external_refs e JOIN documents d ON d.id=e.entity_id WHERE e.source='clickup' AND d.kind='wiki' AND d.deleted_at IS NULL`},
		{"with a real assignee", `SELECT count(*) FROM task_fields t JOIN external_refs e ON e.entity_id=t.document_id WHERE e.source='clickup' AND t.assignee_id IS NOT NULL`},
		{"still marked in progress", `SELECT count(*) FROM task_fields t JOIN external_refs e ON e.entity_id=t.document_id WHERE e.source='clickup' AND t.status='in_progress'`},
		{"created before today (real dates)", `SELECT count(*) FROM documents d JOIN external_refs e ON e.entity_id=d.id WHERE e.source='clickup' AND d.created_at::date < current_date`},
		{"with a completion date", `SELECT count(*) FROM task_fields t JOIN external_refs e ON e.entity_id=t.document_id WHERE e.source='clickup' AND t.completed_at IS NOT NULL`},
		{"custom field values", `SELECT count(*) FROM property_values`},
		{"comments", `SELECT count(*) FROM messages WHERE entity_type='document'`},
		{"time entries", `SELECT count(*) FROM time_entries`},
		{"checklist items", `SELECT count(*) FROM checklist_items`},
		{"links between tasks", `SELECT count(*) FROM entity_links`},
		{"watches", `SELECT count(*) FROM watches`},
		{"spaces", `SELECT count(*) FROM spaces`},
		{"knowledge base pages", `SELECT count(*) FROM documents WHERE kind='wiki' AND deleted_at IS NULL`},
	} {
		var n int
		if err := pools.Web.QueryRow(ctx, q.sql).Scan(&n); err != nil {
			fmt.Printf("  %-34s (could not count: %v)\n", q.label, err)
			continue
		}
		fmt.Printf("  %-34s %6d\n", q.label, n)
	}

	if extra > 0 {
		fmt.Println()
		fmt.Println("here but not in the source:")
		shown := 0
		for id := range here {
			if _, ok := source[id]; ok {
				continue
			}
			var title, kind string
			if err := pools.Web.QueryRow(ctx, `
				SELECT d.title, d.kind FROM external_refs e
				JOIN documents d ON d.id = e.entity_id
				WHERE e.source='clickup' AND e.external_id = $1`, id).Scan(&title, &kind); err != nil {
				continue
			}
			fmt.Println(fmt.Sprintf("  %-10s %-12s %s", id, kind, title))
			shown++
			if shown >= 25 {
				fmt.Println(fmt.Sprintf("  ... and %d more", extra-shown))
				break
			}
		}
	}

	// ---- is every status the one the source implies? --------------------
	//
	// The count alone proves nothing. "204 in progress" is a reasonable-looking
	// number whether or not those are the same 204 ClickUp has, and the bug
	// that made 700 tasks claim to be in progress produced an equally
	// reasonable-looking 863.
	fmt.Println()
	fmt.Println("status, task by task")
	statusRows, err := pools.Web.Query(ctx, `
		SELECT e.external_id, t.status
		FROM external_refs e
		JOIN documents d ON d.id = e.entity_id
		JOIN task_fields t ON t.document_id = d.id
		WHERE e.source='clickup' AND e.entity_type='document'
		  AND e.external_id NOT LIKE 'comment:%' AND d.deleted_at IS NULL`)
	if err != nil {
		fmt.Println("  could not read statuses:", err)
	} else {
		checked, wrong := 0, 0
		byPair := map[string]int{}
		for statusRows.Next() {
			var extID, got string
			if err := statusRows.Scan(&extID, &got); err != nil {
				break
			}
			want, ok := wantStatus[extID]
			if !ok {
				continue // not a task in this walk
			}
			checked++
			if got != want {
				wrong++
				byPair[fmt.Sprintf("%q -> %s, but here it is %s", rawStatus[extID], want, got)]++
			}
		}
		statusRows.Close()
		fmt.Println(fmt.Sprintf("  checked %d, wrong %d", checked, wrong))
		if wrong > 0 {
			keys := make([]string, 0, len(byPair))
			for k := range byPair {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool { return byPair[keys[i]] > byPair[keys[j]] })
			for i, k := range keys {
				if i >= 10 {
					break
				}
				fmt.Println(fmt.Sprintf("    %5d  %s", byPair[k], k))
			}
		}
	}

	fmt.Println()
	switch {
	case missing == 0 && extra == 0:
		fmt.Println("every source task resolves here, and nothing here is unaccounted for.")
	case missing > 0:
		fmt.Printf("%d tasks have not arrived. Re-run the import; it updates in place.\n", missing)
	default:
		fmt.Printf("%d documents here carry a ClickUp id the source no longer has. "+
			"That is expected if somebody deleted tasks there after the import.\n", extra)
	}
	return nil
}
