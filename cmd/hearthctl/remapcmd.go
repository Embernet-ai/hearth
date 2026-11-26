package main

// Putting the imported tree into the shape the company actually has.
//
// ClickUp's spaces are not an org chart. Red Team, White Team and White Team
// Service sit beside Sales as peers there, because ClickUp has one flat level
// and people used it for whatever they needed. The real structure is
// departments with sub-departments under them, and every "my department's
// work" view depends on that being true in the data rather than in somebody's
// head.
//
// Idempotent: a department that exists is reused, and a sub-department already
// in the right place is left alone. Safe to run twice, and safe to run again
// after a further import brings in a new space.
//
//	hearthctl remap-org [-dry-run]

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// orgChart is the structure to build, confirmed 2026-08-18.
//
// The key is the department. The value is the imported spaces that become
// sub-departments beneath it. A department with no imported spaces is still
// created -- Finance has no ClickUp space and still exists as a department,
// because the org chart is a statement about the company rather than a
// reflection of what one tool happened to hold.
var orgChart = []struct {
	Department string
	Children   []string
}{
	{"Engineering", []string{"Red Team", "White Team", "White Team Service", "Embernet"}},
	{"Sales", []string{"Sales"}},
	{"Marketing", []string{"Marketing"}},
	{"Operations", []string{"Ops"}},
	{"HR", []string{"HR"}},
	{"Finance", nil},
	{"Knowledge base", []string{"Knowledge base"}},
}

func runRemapOrg(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("remap-org", flag.ExitOnError)
	dsn := envFlag(fs, "dsn", "HEARTH_DATABASE_URL", "Postgres URL, or HEARTH_DATABASE_URL")
	dry := fs.Bool("dry-run", false, "say what would move, change nothing")
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
	az := authz.New(pools.Web)
	actor, err := az.SubjectsFor(ctx, orgID, principalID)
	if err != nil {
		return err
	}
	spaces := plant.New(pools.Web, az)

	// Everything currently at the root, which is what the importer produced.
	type node struct{ id, name, kind string }
	rows, err := pools.Web.Query(ctx, `
		SELECT id::text, name, kind FROM spaces
		WHERE org_id = $1 AND parent_id IS NULL ORDER BY name`, orgID)
	if err != nil {
		return fmt.Errorf("read the root: %w", err)
	}
	roots := map[string]node{}
	for rows.Next() {
		var n node
		if err := rows.Scan(&n.id, &n.name, &n.kind); err != nil {
			rows.Close()
			return err
		}
		roots[strings.ToLower(n.name)] = n
	}
	rows.Close()

	fmt.Printf("%d spaces at the root\n\n", len(roots))

	moved, created, already := 0, 0, 0
	for _, dept := range orgChart {
		// The department itself. CreateSpace returns an existing one, so this
		// is the same call whether or not it has been run before.
		// A department that is already a root space IS the department --
		// "Sales" is both the department and the imported ClickUp space, and
		// nesting it inside a second "Sales" would be an invented level.
		var deptID string
		if existing, ok := roots[strings.ToLower(dept.Department)]; ok {
			deptID = existing.id
			fmt.Printf("%-16s exists\n", dept.Department)
		} else if *dry {
			fmt.Printf("%-16s would be created\n", dept.Department)
			deptID = ""
		} else {
			sp, err := spaces.CreateSpace(ctx, orgID, actor, dept.Department, "area", nil)
			if err != nil {
				return fmt.Errorf("create department %q: %w", dept.Department, err)
			}
			deptID = sp.ID
			created++
			fmt.Printf("%-16s created\n", dept.Department)
		}

		for _, child := range dept.Children {
			c, ok := roots[strings.ToLower(child)]
			if !ok {
				fmt.Printf("    %-24s not at the root, skipped\n", child)
				continue
			}
			if c.id == deptID {
				// A department named the same as its only child -- Sales under
				// Sales. Nothing to move; it is already the department.
				already++
				fmt.Printf("    %-24s is the department itself\n", child)
				continue
			}
			if *dry || deptID == "" {
				fmt.Printf("    %-24s would move under %s\n", child, dept.Department)
				continue
			}
			// A sub-department is a folder, not an area, now that it has a
			// parent. The kind is what the navigation reads to decide depth.
			if _, err := pools.Web.Exec(ctx,
				`UPDATE spaces SET kind='folder', updated_at=now() WHERE id=$1`, c.id); err != nil {
				return fmt.Errorf("set kind on %q: %w", child, err)
			}
			if err := spaces.MoveSpace(ctx, orgID, actor, c.id, &deptID); err != nil {
				return fmt.Errorf("move %q under %q: %w", child, dept.Department, err)
			}
			moved++
			fmt.Printf("    %-24s moved under %s\n", child, dept.Department)
		}
	}

	// Anything left at the root that the chart does not mention. Reported
	// rather than moved: a space nobody planned for is a question, not
	// something to file away silently.
	var leftovers []string
	for _, n := range roots {
		if !inChart(n.name) {
			leftovers = append(leftovers, n.name)
		}
	}
	sort.Strings(leftovers)
	if len(leftovers) > 0 {
		fmt.Println("\nstill at the root, not in the org chart:")
		for _, n := range leftovers {
			fmt.Println("  " + n)
		}
	}

	fmt.Printf("\ndepartments created %d, sub-departments moved %d, already right %d\n",
		created, moved, already)
	if *dry {
		fmt.Println("dry run: nothing was written")
	}
	return nil
}

func inChart(name string) bool {
	for _, d := range orgChart {
		if strings.EqualFold(d.Department, name) {
			return true
		}
		for _, c := range d.Children {
			if strings.EqualFold(c, name) {
				return true
			}
		}
	}
	return false
}
