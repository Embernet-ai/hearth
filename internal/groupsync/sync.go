package groupsync

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Wanted returns the addresses of the active people in any of the named teams
// or departments. A suspended person is never wanted. A person with no email on
// file is skipped: there is nothing to put in the group.
func Wanted(ctx context.Context, db *pgxpool.Pool, teams, departments []string) ([]string, error) {
	rows, err := db.Query(ctx, `
		SELECT DISTINCT lower(ui.email)
		  FROM principals p
		  JOIN (SELECT principal_id, max(email) AS email FROM user_identities GROUP BY 1) ui
		    ON ui.principal_id = p.id
		 WHERE p.status = 'active' AND p.kind = 'user' AND btrim(coalesce(ui.email,'')) <> ''
		   AND (
		     p.id IN (SELECT m.principal_id FROM team_members m JOIN teams t ON t.id = m.team_id
		               WHERE lower(t.name) = ANY($1))
		  OR p.id IN (SELECT d.principal_id FROM principal_departments d JOIN spaces s ON s.id = d.space_id
		               WHERE lower(s.name) = ANY($2)))
		 ORDER BY 1`, lower(teams), lower(departments))
	if err != nil {
		return nil, fmt.Errorf("read who belongs: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Missing returns the names that match no team (kind "team") or no top-level
// department. A mapping that names something that does not exist would read as
// "nobody belongs", so it is refused instead of emptying a group.
func Missing(ctx context.Context, db *pgxpool.Pool, teams, departments []string) ([]string, error) {
	var out []string
	check := func(kind, q string, names []string) error {
		for _, n := range names {
			var ok bool
			if err := db.QueryRow(ctx, q, strings.ToLower(strings.TrimSpace(n))).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				out = append(out, kind+" "+n)
			}
		}
		return nil
	}
	if err := check("team", `SELECT EXISTS (SELECT 1 FROM teams WHERE lower(name) = $1)`, teams); err != nil {
		return nil, err
	}
	if err := check("department", `SELECT EXISTS (SELECT 1 FROM spaces WHERE lower(name) = $1)`, departments); err != nil {
		return nil, err
	}
	return out, nil
}

func lower(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.ToLower(strings.TrimSpace(s)))
	}
	return out
}

// Run reconciles every mapping. With apply false it changes nothing and
// returns what it would do. A mapping naming no team and no department is an
// error, not an instruction to empty the group.
func Run(ctx context.Context, db *pgxpool.Pool, dir Directory, cfg Config, apply bool) ([]Change, error) {
	var all []Change
	for _, m := range cfg.Mappings {
		if strings.TrimSpace(m.Group) == "" || (len(m.Teams) == 0 && len(m.Departments) == 0) {
			return all, errors.New("every mapping needs a group and at least one team or department")
		}
		miss, err := Missing(ctx, db, m.Teams, m.Departments)
		if err != nil {
			return all, err
		}
		if len(miss) > 0 {
			return all, fmt.Errorf("%s: no such %s in Hearth, refusing to read it as an empty group", m.Group, strings.Join(miss, ", "))
		}
		want, err := Wanted(ctx, db, m.Teams, m.Departments)
		if err != nil {
			return all, err
		}
		have, err := dir.Members(ctx, m.Group)
		if err != nil {
			return all, err
		}
		for _, c := range Plan(m.Group, want, have, cfg.Protect) {
			if c.Remove && !cfg.Remove {
				c.Skipped = true
			}
			all = append(all, c)
			if !apply || c.Skipped {
				continue
			}
			if c.Remove {
				err = dir.Remove(ctx, c.Group, c.Email)
			} else {
				err = dir.Add(ctx, c.Group, c.Email)
			}
			if err != nil {
				return all, err
			}
		}
	}
	return all, nil
}
