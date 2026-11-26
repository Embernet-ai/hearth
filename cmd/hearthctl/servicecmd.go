package main

// Service histories in bulk: one PDF per customer folder and one CSV of every
// event, made by the same code behind the Service history page, so a file
// generated here is the file somebody would have downloaded there.
//
//	hearthctl service-history -under "White Team Service" -out DIR [-as yara@example.com]
//
// Read only. It reads as a named account, so what is written is what that
// account could open in the browser, and it never writes to the database.

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/reportpdf"
	"github.com/Embernet-ai/hearth/internal/servicereport"
)

func runServiceHistory(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("service-history", flag.ExitOnError)
	dsn := envFlag(fs, "dsn", "HEARTH_DATABASE_URL", "Postgres URL, or HEARTH_DATABASE_URL")
	under := fs.String("under", "White Team Service", "the department whose child folders are the customers")
	out := fs.String("out", "", "directory to write the PDFs and the CSV into")
	as := fs.String("as", "", "email of the account to read as (default: the owner)")
	splitMap := fs.String("split-map", "", "CSV (folder,job,end_customer) that splits a reseller's jobs into end customers")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("-out is required")
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("a database is required: -dsn or HEARTH_DATABASE_URL")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
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
	who := owner
	if *as != "" {
		if err := pools.Web.QueryRow(ctx, `
			SELECT p.id::text FROM principals p
			  JOIN user_identities u ON u.principal_id = p.id
			 WHERE p.org_id = $1 AND p.status = 'active' AND lower(u.email::text) = lower($2)`,
			orgID, *as).Scan(&who); err != nil {
			return fmt.Errorf("find %s: %w", *as, err)
		}
	}
	az := authz.New(pools.Web)
	actor, err := az.SubjectsFor(ctx, orgID, who)
	if err != nil {
		return err
	}
	svc := docs.New(pools.Web, az)

	// The customers are the folders directly beneath the parent.
	rows, err := pools.Web.Query(ctx, `
		SELECT c.id::text, c.name FROM spaces c JOIN spaces p ON c.parent_id = p.id
		 WHERE p.org_id = $1 AND p.name = $2 ORDER BY c.name`, orgID, *under)
	if err != nil {
		return err
	}
	type cust struct{ id, name string }
	var customers []cust
	for rows.Next() {
		var c cust
		if err := rows.Scan(&c.id, &c.name); err != nil {
			rows.Close()
			return err
		}
		customers = append(customers, c)
	}
	rows.Close()
	if len(customers) == 0 {
		return fmt.Errorf("no folders under %q", *under)
	}

	split, err := readSplitMap(*splitMap)
	if err != nil {
		return err
	}

	var all []*docs.ServiceRecord
	fmt.Printf("%-44s %8s %8s  %s\n", "customer", "records", "events", "first to last day")
	for _, c := range customers {
		recs, err := svc.ServiceHistory(ctx, orgID, actor, docs.ServiceFilter{SpaceID: c.id})
		if err != nil {
			return fmt.Errorf("%s: %w", c.name, err)
		}
		if len(recs) == 0 {
			continue
		}
		for _, r := range recs {
			r.Customer = c.name
		}
		// A reseller's folder is split: each job that is for a named end
		// customer gets its own file, and the rest stay under the reseller.
		groups := []group{{name: c.name, recs: recs}}
		if m := split[c.name]; m != nil {
			groups = splitByEndCustomer(c.name, recs, m)
		}
		for _, g := range groups {
			if len(g.recs) == 0 {
				continue
			}
			if err := writeGroup(*out, *under, g); err != nil {
				return err
			}
			all = append(all, g.recs...)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].SpacePath < all[j].SpacePath })
	f, err := os.Create(filepath.Join(*out, "all-customers.csv"))
	if err != nil {
		return err
	}
	werr := servicereport.WriteCSV(f, all)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	fmt.Printf("\nwrote %d records to %s\n", len(all), *out)
	return nil
}

// group is one file: a customer, or an end customer served through a reseller.
type group struct {
	name string
	recs []*docs.ServiceRecord
}

// splitByEndCustomer separates a reseller's records by the end customer their
// job is for. A job not in the map, or mapped to nothing, stays under the
// reseller, so nothing is dropped and nothing is guessed.
func splitByEndCustomer(folder string, recs []*docs.ServiceRecord, jobs map[string]string) []group {
	own := group{name: folder}
	byEnd := map[string]*group{}
	var order []string
	for _, r := range recs {
		end := jobs[r.Job]
		if end == "" {
			own.recs = append(own.recs, r)
			continue
		}
		r.EndCustomer = end
		g := byEnd[end]
		if g == nil {
			g = &group{name: end + " (via " + folder + ")"}
			byEnd[end] = g
			order = append(order, end)
		}
		g.recs = append(g.recs, r)
	}
	out := []group{own}
	sort.Strings(order)
	for _, e := range order {
		out = append(out, *byEnd[e])
	}
	return out
}

// readSplitMap reads folder,job,end_customer rows. An empty path is no map.
func readSplitMap(path string) (map[string]map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("split map: %w", err)
	}
	out := map[string]map[string]string{}
	for i, r := range rows {
		if i == 0 || len(r) < 3 {
			continue
		}
		if out[r[0]] == nil {
			out[r[0]] = map[string]string{}
		}
		out[r[0]][strings.TrimSpace(r[1])] = strings.TrimSpace(r[2])
	}
	return out, nil
}

// writeGroup writes one PDF and prints its line in the summary.
func writeGroup(dir, under string, g group) error {
	events, first, last := 0, "", ""
	for _, r := range g.recs {
		for _, e := range r.Events {
			events++
			if first == "" || e.Day < first {
				first = e.Day
			}
			if e.Day > last {
				last = e.Day
			}
		}
	}
	fmt.Printf("%-44s %8d %8d  %s to %s\n", g.name, len(g.recs), events, first, last)
	doc := servicereport.Build(g.recs, g.name+": service history", under+" / "+g.name+", "+first+" to "+last)
	f, err := os.Create(filepath.Join(dir, safeName(g.name)+".pdf"))
	if err != nil {
		return err
	}
	werr := reportpdf.Write(f, doc)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("%s pdf: %w", g.name, werr)
	}
	return nil
}
func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '"' || r < 0x20:
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
