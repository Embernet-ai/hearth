package playground

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Purge deletes a playground organisation and everything in it.
//
// Nothing else in Hearth deletes an organisation, and the schema is built to
// make that hard: most keys to orgs and principals are RESTRICT, and the audit
// log refuses deletes outright. So this works from the catalogue rather than a
// hand-kept list -- a list would be out of date the day a migration adds a
// table, and the playground that then fails to reset is the bug report.
//
// Every table holding an org_id loses that organisation's rows; a table
// without one loses the rows that point at rows being deleted (a document's
// versions, a channel's members). Deletes run under savepoints and are simply
// retried in a later pass when a key still holds them back, until nothing is
// left or a pass makes no progress. One transaction: it all goes, or none.
//
// Uploaded files' bytes stay in the object store under org/<id>/ (only their
// rows go); nothing can reach them once the organisation is gone.
func (s *Service) Purge(ctx context.Context, orgID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("playground: begin purge: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The one check that matters most in this file.
	var is bool
	err = tx.QueryRow(ctx, `SELECT playground_of IS NOT NULL FROM orgs WHERE id = $1 FOR UPDATE`, orgID).Scan(&is)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("playground: check organisation: %w", err)
	}
	if !is {
		return ErrNotPlayground
	}

	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL DEFERRED`); err != nil {
		return fmt.Errorf("playground: defer constraints: %w", err)
	}
	// The audit log's own way out, for exactly this: an organisation going.
	if _, err := tx.Exec(ctx, `SELECT audit_purge_org($1)`, orgID); err != nil {
		return fmt.Errorf("playground: clear audit log: %w", err)
	}

	sch, err := readSchema(ctx, tx)
	if err != nil {
		return err
	}

	// Rows that point at rows of the same table (a sub-space's parent) would
	// hold each other back inside one DELETE; let go of those links first.
	for _, fk := range sch.fks {
		if fk.child == fk.parent && fk.nullable && sch.hasOrg[fk.child] {
			q := fmt.Sprintf(`UPDATE %s SET %s = NULL WHERE org_id = $1 AND %s IS NOT NULL`,
				ident(fk.child), ident(fk.childCol), ident(fk.childCol))
			if _, err := tx.Exec(ctx, q, orgID); err != nil {
				return fmt.Errorf("playground: unlink %s.%s: %w", fk.child, fk.childCol, err)
			}
		}
	}

	pending := map[string]string{}
	for _, t := range sch.tables {
		if t == "orgs" {
			continue
		}
		if cond := sch.cond(t, 0); cond != "" {
			pending[t] = cond
		}
	}
	pending["orgs"] = "id = $1"

	for pass := 0; len(pending) > 0; pass++ {
		progressed := false
		for _, t := range sch.tables {
			cond, ok := pending[t]
			if !ok {
				continue
			}
			if _, err := tx.Exec(ctx, `SAVEPOINT purge_step`); err != nil {
				return fmt.Errorf("playground: savepoint: %w", err)
			}
			_, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s`, ident(t), cond), orgID)
			if err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation: try again later
					if _, rerr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT purge_step`); rerr != nil {
						return fmt.Errorf("playground: rollback step: %w", rerr)
					}
					continue
				}
				return fmt.Errorf("playground: delete from %s: %w", t, err)
			}
			if _, err := tx.Exec(ctx, `RELEASE SAVEPOINT purge_step`); err != nil {
				return fmt.Errorf("playground: release savepoint: %w", err)
			}
			delete(pending, t)
			progressed = true
		}
		if !progressed {
			names := make([]string, 0, len(pending))
			for t := range pending {
				names = append(names, t)
			}
			return fmt.Errorf("playground: could not remove rows from %s", strings.Join(names, ", "))
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("playground: commit purge: %w", err)
	}
	s.is.Delete(orgID)
	return nil
}

type fkey struct {
	child, childCol, parent, parentCol string
	nullable                           bool
}

type schema struct {
	tables []string // children before parents, as far as the keys allow
	hasOrg map[string]bool
	fks    []fkey
}

// cond is the WHERE clause selecting a table's rows that belong to the
// organisation ($1): its own org_id, or else a key into a table whose rows
// do. Bounded depth, so a cycle of keys cannot recurse forever.
func (sc *schema) cond(table string, depth int) string {
	if table == "orgs" {
		return "id = $1"
	}
	if sc.hasOrg[table] {
		return "org_id = $1"
	}
	if depth > 3 {
		return ""
	}
	var ors []string
	for _, fk := range sc.fks {
		if fk.child != table || fk.parent == table {
			continue
		}
		if pc := sc.cond(fk.parent, depth+1); pc != "" {
			ors = append(ors, fmt.Sprintf("%s IN (SELECT %s FROM %s WHERE %s)",
				ident(fk.childCol), ident(fk.parentCol), ident(fk.parent), pc))
		}
	}
	return strings.Join(ors, " OR ")
}

func readSchema(ctx context.Context, tx pgx.Tx) (*schema, error) {
	sc := &schema{hasOrg: map[string]bool{}}

	rows, err := tx.Query(ctx, `
		SELECT c.relname,
		       EXISTS (SELECT 1 FROM pg_attribute a
		               WHERE a.attrelid = c.oid AND a.attname = 'org_id' AND NOT a.attisdropped)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname <> 'goose_db_version'
		ORDER BY c.relname`)
	if err != nil {
		return nil, fmt.Errorf("playground: read tables: %w", err)
	}
	var names []string
	for rows.Next() {
		var name string
		var has bool
		if err := rows.Scan(&name, &has); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, name)
		sc.hasOrg[name] = has
	}
	rows.Close()

	// Single-column keys only; this schema has no composite foreign keys into
	// org-owned rows, and a composite one would simply be retried by pass.
	rows, err = tx.Query(ctx, `
		SELECT ch.relname, ca.attname, pa.relname, pk.attname, NOT ca.attnotnull
		FROM pg_constraint con
		JOIN pg_class ch ON ch.oid = con.conrelid
		JOIN pg_class pa ON pa.oid = con.confrelid
		JOIN pg_namespace n ON n.oid = ch.relnamespace
		JOIN pg_attribute ca ON ca.attrelid = con.conrelid AND ca.attnum = con.conkey[1]
		JOIN pg_attribute pk ON pk.attrelid = con.confrelid AND pk.attnum = con.confkey[1]
		WHERE con.contype = 'f' AND n.nspname = 'public' AND array_length(con.conkey, 1) = 1`)
	if err != nil {
		return nil, fmt.Errorf("playground: read keys: %w", err)
	}
	for rows.Next() {
		var fk fkey
		if err := rows.Scan(&fk.child, &fk.childCol, &fk.parent, &fk.parentCol, &fk.nullable); err != nil {
			rows.Close()
			return nil, err
		}
		sc.fks = append(sc.fks, fk)
	}
	rows.Close()

	// Children first: a table is placed once every table referencing it is.
	// Whatever a cycle leaves over goes at the end, for the passes to sort out.
	refs := map[string]map[string]bool{} // parent -> children
	for _, fk := range sc.fks {
		if fk.child == fk.parent {
			continue
		}
		if refs[fk.parent] == nil {
			refs[fk.parent] = map[string]bool{}
		}
		refs[fk.parent][fk.child] = true
	}
	placed := map[string]bool{}
	for len(sc.tables) < len(names) {
		added := false
		for _, t := range names {
			if placed[t] {
				continue
			}
			ready := true
			for c := range refs[t] {
				if !placed[c] {
					ready = false
					break
				}
			}
			if ready {
				sc.tables = append(sc.tables, t)
				placed[t] = true
				added = true
			}
		}
		if !added {
			for _, t := range names {
				if !placed[t] {
					sc.tables = append(sc.tables, t)
					placed[t] = true
				}
			}
		}
	}
	return sc, nil
}

func ident(name string) string { return pgx.Identifier{name}.Sanitize() }
