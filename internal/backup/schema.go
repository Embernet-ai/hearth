package backup

// Working out what to back up, by asking the database rather than by keeping a
// list.
//
// A hard-coded table list is wrong the first time somebody adds a migration and
// forgets to update it, and the way that failure presents is a restore that
// succeeds and is missing a feature's worth of data. Twenty-two migrations in,
// nobody would notice for months.
//
// So the schema is discovered: every base table in public, ordered so that a
// row is always inserted after the rows it points at. That order comes out of
// the foreign keys, which are the same thing the database would enforce during
// the restore, so it cannot disagree with reality either.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// skipped is the one table that is not data.
//
// goose_db_version records which migrations have run, and a restore lands in a
// database that has already been migrated. Restoring it would either duplicate
// every row or fight the schema it is being restored into. The manifest records
// the version instead, and the restore refuses if it does not match, which is
// the check that actually matters: a backup taken at migration 22 restored into
// a database at 19 is a silent, partial, unrecoverable mess.
var skipped = map[string]bool{"goose_db_version": true}

// Table is one table and how to put it back.
type Table struct {
	Name string
	// Columns in a fixed order, so the CSV header and the COPY column list can
	// never drift apart. Ordinal order, which is stable for a given schema.
	Columns []string
	// Sequences owned by this table's columns, so they can be moved past the
	// restored rows. Without this the first insert after a restore collides
	// with a row that came out of the backup, and the error looks like a
	// corrupt database rather than an unset counter.
	Sequences []Sequence
}

// Sequence is a counter that has to be moved forward after a restore.
type Sequence struct {
	Column string
	Name   string
}

// Discover reads the schema and returns the tables in dependency order:
// parents before children, so a restore can insert straight down the list.
func Discover(ctx context.Context, db *pgxpool.Pool) ([]Table, error) {
	// attgenerated is empty for an ordinary column and 's' for a stored
	// generated one, which is what every search_tsv column in this schema is.
	// Those are excluded: Postgres computes them from the other columns and
	// refuses a COPY that tries to supply one, so putting them in the archive
	// would make it unrestorable. Found by running a restore, which failed on
	// the companies table with exactly that message.
	//
	// Identity columns are NOT excluded. COPY writes them, unlike INSERT, and
	// their values are foreign keys everywhere else in this database: an outbox
	// id restored as a different number is a rule run pointing at the wrong
	// event.
	rows, err := db.Query(ctx, `
		SELECT c.relname, a.attname
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  JOIN pg_attribute a ON a.attrelid = c.oid
		 WHERE n.nspname = 'public'
		   AND c.relkind = 'r'
		   AND a.attnum > 0
		   AND NOT a.attisdropped
		   AND a.attgenerated = ''
		 ORDER BY c.relname, a.attnum`)
	if err != nil {
		return nil, fmt.Errorf("backup: read columns: %w", err)
	}
	defer rows.Close()

	columns := map[string][]string{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return nil, fmt.Errorf("backup: scan column: %w", err)
		}
		if skipped[table] {
			continue
		}
		columns[table] = append(columns[table], column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("backup: this database has no tables in it")
	}

	deps, err := dependencies(ctx, db, columns)
	if err != nil {
		return nil, err
	}
	order, err := topological(columns, deps)
	if err != nil {
		return nil, err
	}
	seqs, err := sequences(ctx, db, columns)
	if err != nil {
		return nil, err
	}

	out := make([]Table, 0, len(order))
	for _, name := range order {
		out = append(out, Table{
			Name: name, Columns: columns[name], Sequences: seqs[name],
		})
	}
	return out, nil
}

// dependencies maps each table to the tables it references.
func dependencies(ctx context.Context, db *pgxpool.Pool, known map[string][]string) (map[string][]string, error) {
	// Deferrable keys are excluded, and that exclusion is what makes a cycle
	// survivable rather than fatal.
	//
	// Restore opens one transaction and issues SET CONSTRAINTS ALL DEFERRED
	// before copying anything, so a deferrable key is not checked until commit
	// and genuinely does not constrain the order rows go in. Treating it as an
	// ordering edge would keep a loop in the graph that has no effect on
	// whether the restore works.
	//
	// principals.department_id and spaces.created_by are the pair that made
	// this necessary: between them they closed a loop that stopped the whole
	// database being archivable. See 00030.
	rows, err := db.Query(ctx, `
		SELECT child.relname, parent.relname
		  FROM pg_constraint con
		  JOIN pg_class child  ON child.oid  = con.conrelid
		  JOIN pg_class parent ON parent.oid = con.confrelid
		  JOIN pg_namespace n  ON n.oid = child.relnamespace
		 WHERE con.contype = 'f' AND n.nspname = 'public'
		   AND NOT con.condeferrable`)
	if err != nil {
		return nil, fmt.Errorf("backup: read foreign keys: %w", err)
	}
	defer rows.Close()

	deps := map[string][]string{}
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			return nil, fmt.Errorf("backup: scan foreign key: %w", err)
		}
		// A table that points at itself, such as a space with a parent space,
		// is not a dependency between tables. Its own rows are ordered by the
		// insert, and a self reference in the graph would look like a cycle.
		_, childKnown := known[child]
		_, parentKnown := known[parent]
		if child == parent || !childKnown || !parentKnown {
			continue
		}
		deps[child] = append(deps[child], parent)
	}
	return deps, rows.Err()
}

// topological orders parents before children.
//
// Kahn's algorithm with the ready set sorted by name, so the order is the same
// on every run. A backup whose table order shuffles between runs is one where
// two archives of identical data do not compare equal, and comparing them is
// how a restore is checked.
func topological(tables map[string][]string, deps map[string][]string) ([]string, error) {
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)

	remaining := map[string]map[string]bool{}
	for _, name := range names {
		remaining[name] = map[string]bool{}
		for _, parent := range deps[name] {
			if _, ok := tables[parent]; ok {
				remaining[name][parent] = true
			}
		}
	}

	var out []string
	placed := map[string]bool{}
	for len(out) < len(names) {
		progress := false
		for _, name := range names {
			if placed[name] {
				continue
			}
			ready := true
			for parent := range remaining[name] {
				if !placed[parent] {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			out = append(out, name)
			placed[name] = true
			progress = true
		}
		if !progress {
			// A cycle between two tables. Not possible in this schema today,
			// and if somebody adds one the honest thing is to say so rather
			// than to emit an order that will not restore.
			var stuck []string
			for _, name := range names {
				if !placed[name] {
					stuck = append(stuck, name)
				}
			}
			return nil, fmt.Errorf(
				"backup: these tables reference each other in a loop and cannot be "+
					"ordered for a restore: %s", strings.Join(stuck, ", "))
		}
	}
	return out, nil
}

// sequences finds the counters behind serial columns.
func sequences(ctx context.Context, db *pgxpool.Pool, known map[string][]string) (map[string][]Sequence, error) {
	out := map[string][]Sequence{}
	for table, columns := range known {
		for _, column := range columns {
			var name *string
			if err := db.QueryRow(ctx,
				`SELECT pg_get_serial_sequence($1, $2)`, "public."+quote(table), column).Scan(&name); err != nil {
				return nil, fmt.Errorf("backup: read sequence for %s.%s: %w", table, column, err)
			}
			if name != nil && *name != "" {
				out[table] = append(out[table], Sequence{Column: column, Name: *name})
			}
		}
	}
	return out, nil
}

// quote makes an identifier safe to paste into SQL.
//
// Every table name in this schema is a plain lowercase word, so this never
// actually changes anything. It is here because the names come out of a query
// rather than out of a literal, and "the data happens to be safe today" is the
// shape of every injection there has ever been.
func quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
