package main

// Building teams from the org chart, and working out who is on them.
//
// The departments exist as spaces and the people exist as accounts, and until
// now nothing joined the two. "My department's work" and "my team's updates"
// both need that join, and neither can be answered by a hierarchy of folders
// with nobody in it.
//
// Membership is INFERRED, from where somebody's work actually is. ClickUp has
// no department field -- it has spaces, and people are in a space because they
// are assigned things in it. That is a real signal and it is also a guess, so
// it is reported per person and per team rather than applied silently, and
// anybody can be moved afterwards on the Teams page.
//
//	hearthctl sync-teams [-dry-run] [-min 1]

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/teams"
)

func runSyncTeams(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sync-teams", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres URL, or HEARTH_DATABASE_URL")
	dry := fs.Bool("dry-run", false, "say what would change, write nothing")
	min := fs.Int("min", 1,
		"how many assigned documents in a department before somebody counts as a member")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("a database is required: -dsn or HEARTH_DATABASE_URL")
	}

	pools, err := pg.Open(ctx, pg.DefaultOptions(*dsn))
	if err != nil {
		return err
	}
	defer pools.Close()

	orgID, principalID, err := soleOwner(ctx, pools)
	if err != nil {
		return err
	}
	svc := teams.New(pools.Web)

	// Departments are the top of the tree. Sub-departments get their own team
	// too: "White Team" is a real group of people and asking them something is
	// a different act from asking all of Engineering.
	type dept struct{ id, name, kind string }
	rows, err := pools.Web.Query(ctx, `
		SELECT s.id::text, s.name, s.kind
		FROM spaces s
		WHERE s.org_id = $1 AND s.kind IN ('area','folder')
		  AND (s.parent_id IS NULL OR EXISTS (
		        SELECT 1 FROM spaces p WHERE p.id = s.parent_id AND p.parent_id IS NULL))
		ORDER BY s.kind DESC, lower(s.name)`, orgID)
	if err != nil {
		return fmt.Errorf("read departments: %w", err)
	}
	var depts []dept
	for rows.Next() {
		var d dept
		if err := rows.Scan(&d.id, &d.name, &d.kind); err != nil {
			rows.Close()
			return err
		}
		depts = append(depts, d)
	}
	rows.Close()

	existing := map[string]string{} // lowercased name -> team id
	if list, err := svc.List(ctx, orgID); err == nil {
		for _, t := range list {
			existing[strings.ToLower(t.Name)] = t.ID
		}
	}

	fmt.Printf("%d departments and sub-departments\n\n", len(depts))
	createdTeams, assigned := 0, 0

	for _, d := range depts {
		// Who has work in this department's subtree. Bounded depth for the
		// same reason the list filter is: a cycle from a bad move must not
		// spin here.
		memberRows, err := pools.Web.Query(ctx, `
			WITH RECURSIVE subtree AS (
			    SELECT id, 0 AS depth FROM spaces WHERE id = $1::uuid
			  UNION ALL
			    SELECT c.id, p.depth+1 FROM spaces c JOIN subtree p ON c.parent_id = p.id
			    WHERE p.depth < 10
			)
			SELECT a.principal_id::text, p.display_name, count(*) AS n
			FROM document_assignees a
			JOIN documents d ON d.id = a.document_id AND d.deleted_at IS NULL
			JOIN principals p ON p.id = a.principal_id
			WHERE d.space_id IN (SELECT id FROM subtree)
			GROUP BY 1,2 HAVING count(*) >= $2
			ORDER BY n DESC`, d.id, *min)
		if err != nil {
			return fmt.Errorf("members of %q: %w", d.name, err)
		}
		type member struct {
			id, name string
			n        int
		}
		var members []member
		for memberRows.Next() {
			var m member
			if err := memberRows.Scan(&m.id, &m.name, &m.n); err != nil {
				memberRows.Close()
				return err
			}
			members = append(members, m)
		}
		memberRows.Close()

		label := d.name
		if d.kind == "folder" {
			label = "  " + d.name
		}
		if len(members) == 0 {
			fmt.Printf("%-26s nobody has work here yet\n", label)
			continue
		}

		names := make([]string, 0, len(members))
		ids := make([]string, 0, len(members))
		for _, m := range members {
			names = append(names, fmt.Sprintf("%s(%d)", firstWord(m.name), m.n))
			ids = append(ids, m.id)
		}
		sort.Strings(names)
		fmt.Printf("%-26s %d: %s\n", label, len(members), strings.Join(names, " "))

		if *dry {
			continue
		}
		teamID, ok := existing[strings.ToLower(d.name)]
		if !ok {
			t, err := svc.Create(ctx, orgID, principalID, d.name,
				"From the org chart. Membership inferred from where work is assigned.")
			if err != nil {
				fmt.Printf("    could not create: %v\n", err)
				continue
			}
			teamID = t.ID
			existing[strings.ToLower(d.name)] = teamID
			createdTeams++
		}
		// SetMembers rather than Add per person: the org chart is the
		// authority, so a re-run should also remove somebody who no longer has
		// work here rather than leaving them on a team forever.
		added, removed, err := svc.SetMembers(ctx, orgID, principalID, teamID, ids)
		if err != nil {
			fmt.Printf("    could not set members: %v\n", err)
			continue
		}
		assigned += added
		if added > 0 || removed > 0 {
			fmt.Printf("    +%d -%d\n", added, removed)
		}
	}

	fmt.Printf("\nteams created %d, memberships added %d\n", createdTeams, assigned)
	if *dry {
		fmt.Println("dry run: nothing was written")
	}
	return nil
}

// firstWord keeps the output readable when a directory hands back a full name
// and a department has fifteen people in it.
func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}
