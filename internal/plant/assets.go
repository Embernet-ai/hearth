package plant

// The asset tree, and the rollups that are the reason it exists.
//
// Site, building, line, machine, component. A failure at the bottom has to be
// visible at the top, because the person deciding whether to run the line
// tonight is asking about the line and the answer is made of things four levels
// below it.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Asset is one node of the plant tree with everything rolled up onto it.
type Asset struct {
	ID          string
	Title       string
	ParentID    *string
	ParentTitle string
	Code        string
	InstalledAt *time.Time
	RetiredAt   *time.Time

	// Depth and Path place it in the tree for rendering.
	Depth int
	Path  string

	// Criticality comes from the seeded select property rather than a column.
	// The property system exists so a field like this needs no migration, and
	// a column beside it would be a second answer to one question.
	Criticality string

	// Own counts only this asset. Rolled includes every descendant, which is
	// the number somebody standing in front of a line actually wants.
	OwnOpen        int
	RolledOpen     int
	OwnMinutes     int
	RolledMinutes  int
	OwnCostCents   int
	RolledCost     int
	OwnDowntime    int
	RolledDowntime int
	// Children is how many assets hang directly off this one.
	Children int
	// Down reports whether this asset has an open downtime record right now.
	Down bool
}

// Retired reports whether the machine has left the estate. Retired assets stay
// in the tree because their history is the reason the next one was bought.
func (a Asset) Retired() bool { return a.RetiredAt != nil && a.RetiredAt.Before(time.Now()) }

// HasRollup reports whether anything below this asset contributed, so a page
// can show one number instead of two identical ones.
func (a Asset) HasRollup() bool {
	return a.RolledOpen != a.OwnOpen || a.RolledMinutes != a.OwnMinutes ||
		a.RolledDowntime != a.OwnDowntime
}

// Fields is the editable half of an asset's place in the tree.
type Fields struct {
	ParentAssetID *string
	Code          string
	InstalledAt   *time.Time
	RetiredAt     *time.Time
}

// SetFields writes an asset's tree position and identity.
//
// Edit on the asset, and edit on the new parent as well: hanging a machine off
// a line is a change to that line's rollup, and somebody who can only read the
// line should not be able to change what it reports.
func (s *Service) SetFields(ctx context.Context, orgID string, actor core.Subjects,
	assetID string, f Fields) error {

	if err := s.canEdit(ctx, actor, assetID); err != nil {
		return err
	}
	if f.ParentAssetID != nil && *f.ParentAssetID != "" {
		if err := s.canEdit(ctx, actor, *f.ParentAssetID); err != nil {
			return err
		}
	} else {
		f.ParentAssetID = nil
	}
	f.Code = strings.ToUpper(strings.TrimSpace(f.Code))
	if strings.ContainsAny(f.Code, " /?#&") {
		// The code goes in a URL that a scanner types for somebody. Anything
		// that needs escaping there is a code that will be entered wrong by
		// hand at least once.
		return fmt.Errorf("%w: a code cannot contain spaces or URL punctuation", ErrInvalid)
	}
	if len(f.Code) > 32 {
		return fmt.Errorf("%w: that code is too long to put on a label", ErrInvalid)
	}
	if f.RetiredAt != nil && f.InstalledAt != nil && f.RetiredAt.Before(*f.InstalledAt) {
		return fmt.Errorf("%w: it cannot be retired before it was installed", ErrInvalid)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("plant: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockTree(ctx, tx, orgID); err != nil {
		return err
	}
	if f.ParentAssetID != nil {
		if err := checkNoCycle(ctx, tx, "asset_fields", "parent_asset_id", assetID, *f.ParentAssetID); err != nil {
			return err
		}
	}

	// Release the code from a scrapped machine that is still holding it.
	//
	// Deleting a document is a soft delete, so nothing cascades and the old
	// asset's row keeps its claim on the tag. Putting the same tag on the
	// replacement is not an edge case, it is what happens every time a machine
	// is swapped: the label on the wall says CV-003 and the new conveyor is
	// CV-003. Without this the second machine can never have the code and the
	// error blames the person for a row they cannot see.
	if f.Code != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE asset_fields a
			   SET code = '', updated_at = now()
			  FROM documents d
			 WHERE a.document_id = d.id
			   AND a.org_id = $1 AND upper(a.code) = upper($2)
			   AND a.document_id <> $3
			   AND d.deleted_at IS NOT NULL`, orgID, f.Code, assetID); err != nil {
			return fmt.Errorf("plant: release code from a deleted asset: %w", err)
		}
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO asset_fields (document_id, org_id, parent_asset_id, code, installed_at, retired_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (document_id) DO UPDATE SET
		    parent_asset_id = EXCLUDED.parent_asset_id,
		    code            = EXCLUDED.code,
		    installed_at    = EXCLUDED.installed_at,
		    retired_at      = EXCLUDED.retired_at,
		    updated_at      = now()`,
		assetID, orgID, f.ParentAssetID, f.Code, f.InstalledAt, f.RetiredAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: another asset already carries the code %s", ErrInvalid, f.Code)
		}
		return fmt.Errorf("plant: set asset fields: %w", err)
	}
	return tx.Commit(ctx)
}

// FieldsFor reads one asset's tree position, with zero values when it has never
// been placed. An asset with no row here is at the top of the tree, which is
// where every asset starts.
func (s *Service) FieldsFor(ctx context.Context, orgID, assetID string) (Fields, error) {
	var f Fields
	err := s.db.QueryRow(ctx, `
		SELECT parent_asset_id::text, code, installed_at, retired_at
		FROM asset_fields WHERE document_id=$1 AND org_id=$2`, assetID, orgID).
		Scan(&f.ParentAssetID, &f.Code, &f.InstalledAt, &f.RetiredAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Fields{}, nil
	case err != nil:
		return Fields{}, fmt.Errorf("plant: asset fields: %w", err)
	}
	return f, nil
}

// ByCode finds an asset from the label on the machine.
//
// This is the whole scanning feature on the read side: a code goes in, a
// document id comes out, and the caller redirects. Case-insensitive, because
// the code is read by a person as often as by a scanner.
func (s *Service) ByCode(ctx context.Context, orgID string, actor core.Subjects, code string) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", ErrNotFound
	}
	var id string
	err := s.db.QueryRow(ctx, `
		SELECT a.document_id::text
		FROM asset_fields a
		JOIN documents d ON d.id = a.document_id AND d.deleted_at IS NULL
		WHERE a.org_id = $1 AND upper(a.code) = upper($2)`, orgID, code).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", ErrNotFound
	case err != nil:
		return "", fmt.Errorf("plant: asset by code: %w", err)
	}
	// The code is not a capability. Somebody who scans a label for a machine
	// they have no grant for gets the same answer as somebody who scans a
	// label that does not exist.
	if err := s.canView(ctx, actor, id); err != nil {
		return "", ErrNotFound
	}
	return id, nil
}

// -----------------------------------------------------------------------------
// The tree, and the rollup
// -----------------------------------------------------------------------------

// treeQuery is the whole feature in one statement.
//
// Three recursive steps, and the shape matters:
//
//  1. up walks every asset to its ancestors, so each (asset, ancestor) pair
//     exists exactly once. That is what makes a rollup a GROUP BY rather than
//     a query per node.
//  2. own aggregates the work on each asset alone.
//  3. rolled joins the two, so every ancestor picks up everything beneath it.
//
// # Surviving a cycle in the data
//
// The write path refuses to make one, but a migration or somebody with psql
// can, and a recursive walk over a loop never returns. Three things, and they
// do different jobs:
//
//   - CYCLE stops the recursion exactly, at the first node that repeats on its
//     own path. Not a bound and not an approximation: the rollup is still
//     correct with a loop in the table, rather than merely finite.
//   - The depth bound is a product decision rather than a safety one. Site,
//     building, line, cell, machine, assembly, component is seven, and
//     rendering deeper than sixteen is not useful to anybody.
//   - Anything the walk could not place is added as a root. Without that, a
//     cycle has no root, so the loop and everything under it vanishes from the
//     page entirely. A machine that quietly disappears is far worse than one
//     shown in the wrong place.
const treeQuery = `
WITH RECURSIVE
-- Every asset the caller can see, plus its parent when that is visible too.
visible AS (
    SELECT d.id, d.title,
           CASE WHEN EXISTS (
               SELECT 1 FROM grants pg
                WHERE pg.entity_type = 'document' AND pg.entity_id = a.parent_asset_id
                  AND pg.subject_key = ANY($2)
                  AND (pg.expires_at IS NULL OR pg.expires_at > now()))
           THEN a.parent_asset_id END AS parent_id,
           COALESCE(a.code, '') AS code, a.installed_at, a.retired_at
    FROM documents d
    LEFT JOIN asset_fields a ON a.document_id = d.id
    WHERE d.org_id = $1 AND d.kind = 'asset' AND d.deleted_at IS NULL
      AND EXISTS (
          SELECT 1 FROM grants g
           WHERE g.entity_type = 'document' AND g.entity_id = d.id
             AND g.subject_key = ANY($2)
             AND (g.expires_at IS NULL OR g.expires_at > now()))
),
-- Each asset paired with itself and every ancestor above it.
up AS (
    SELECT v.id AS asset_id, v.id AS ancestor_id, 0 AS depth FROM visible v
  UNION ALL
    SELECT u.asset_id, p.parent_id, u.depth + 1
    FROM up u
    JOIN visible p ON p.id = u.ancestor_id
    WHERE p.parent_id IS NOT NULL AND u.depth < $3
) CYCLE ancestor_id SET looped USING up_path,
-- What sits on each asset on its own: open work, logged time, cost, downtime.
own AS (
    SELECT v.id,
           COALESCE(w.open_jobs, 0)   AS open_jobs,
           COALESCE(w.minutes, 0)     AS minutes,
           COALESCE(w.cost_cents, 0) + COALESCE(pc.cents, 0) AS cost_cents,
           COALESCE(dt.minutes, 0)    AS downtime,
           COALESCE(dt.open_now, 0)   AS down_now
    FROM visible v
    LEFT JOIN LATERAL (
        SELECT count(*) FILTER (
                   WHERE t.status IS NOT NULL AND t.status NOT IN ('done','cancelled'))::int AS open_jobs,
               COALESCE(SUM(te.minutes), 0)::int AS minutes,
               COALESCE(SUM(te.minutes * COALESCE(cap.rate_cents, 0)) / 60, 0)::int AS cost_cents
        FROM entity_links l
        JOIN documents job ON job.id = l.from_id AND job.deleted_at IS NULL
        LEFT JOIN task_fields t ON t.document_id = job.id
        LEFT JOIN time_entries te ON te.document_id = job.id AND te.minutes IS NOT NULL
        LEFT JOIN capacity cap ON cap.principal_id = te.principal_id
        WHERE l.to_type = 'document' AND l.to_id = v.id
          AND l.from_type = 'document' AND l.kind = 'concerns'
    ) w ON true
    -- Parts, through the same links as the labour. An asset costed on hours
    -- alone is wrong by the price of every bearing that went into it, and the
    -- replacement argument is made from this number.
    LEFT JOIN LATERAL (
        SELECT COALESCE(SUM(-m.quantity * m.unit_cost_cents), 0)::int AS cents
        FROM part_movements m
        JOIN entity_links pl
          ON pl.from_type = 'document' AND pl.from_id = m.document_id
         AND pl.to_type = 'document' AND pl.to_id = v.id AND pl.kind = 'concerns'
        JOIN documents pd ON pd.id = m.document_id AND pd.deleted_at IS NULL
        WHERE m.reason IN ('consumed','returned')
    ) pc ON true
    LEFT JOIN LATERAL (
        SELECT COALESCE(SUM(dn.minutes) FILTER (WHERE NOT dn.planned), 0)::int AS minutes,
               count(*) FILTER (WHERE dn.ended_at IS NULL)::int AS open_now
        FROM asset_downtime dn WHERE dn.asset_id = v.id
    ) dt ON true
),
-- And the same numbers gathered up the tree.
rolled AS (
    SELECT u.ancestor_id AS id,
           SUM(o.open_jobs)::int  AS open_jobs,
           SUM(o.minutes)::int    AS minutes,
           SUM(o.cost_cents)::int AS cost_cents,
           SUM(o.downtime)::int   AS downtime
    FROM up u JOIN own o ON o.id = u.asset_id
    WHERE NOT u.looped
    GROUP BY u.ancestor_id
),
-- Depth and path for rendering, from the roots down. A node whose parent is
-- invisible is a root here, so work somebody was granted is never hidden
-- behind a machine they were not.
placed AS (
    SELECT v.id, v.parent_id, v.title, 0 AS depth,
           v.title AS path, ARRAY[lower(v.title), v.id::text] AS sort
    FROM visible v WHERE v.parent_id IS NULL
  UNION ALL
    SELECT c.id, c.parent_id, c.title, p.depth + 1,
           p.path || ' / ' || c.title, p.sort || lower(c.title) || c.id::text
    FROM visible c JOIN placed p ON c.parent_id = p.id
    WHERE p.depth < $3
) CYCLE id SET looped USING placed_path,
-- Everything the walk reached, plus everything it could not. A cycle has no
-- root, so without the second half a loop takes its machines off the page.
all_placed AS (
    SELECT id, parent_id, title, depth, path, sort FROM placed WHERE NOT looped
  UNION ALL
    SELECT v.id, NULL::uuid, v.title, 0, v.title,
           ARRAY[lower(v.title), v.id::text]
    FROM visible v
    WHERE NOT EXISTS (SELECT 1 FROM placed p WHERE p.id = v.id AND NOT p.looped)
)
SELECT pl.id::text, pl.title, pl.parent_id::text,
       COALESCE(par.title, ''), v.code, v.installed_at, v.retired_at,
       pl.depth, pl.path,
       COALESCE(crit.value #>> '{}', '') AS criticality,
       o.open_jobs, r.open_jobs, o.minutes, r.minutes,
       o.cost_cents, r.cost_cents, o.downtime, r.downtime,
       (SELECT count(*) FROM visible ch WHERE ch.parent_id = pl.id)::int,
       o.down_now > 0
FROM all_placed pl
JOIN visible v ON v.id = pl.id
JOIN own o    ON o.id = pl.id
JOIN rolled r ON r.id = pl.id
LEFT JOIN documents par ON par.id = pl.parent_id
LEFT JOIN property_values crit
       ON crit.entity_type = 'document' AND crit.entity_id = pl.id
      AND crit.def_id = (SELECT pd.id FROM property_defs pd
                          WHERE pd.org_id = $1 AND pd.key = 'criticality')
`

// Tree returns every asset the caller can see, in tree order, with the rollups
// attached.
func (s *Service) Tree(ctx context.Context, orgID string, actor core.Subjects) ([]*Asset, error) {
	rows, err := s.db.Query(ctx, treeQuery+` ORDER BY pl.sort`, orgID, actor.Keys(), maxDepth)
	if err != nil {
		return nil, fmt.Errorf("plant: asset tree: %w", err)
	}
	defer rows.Close()
	return scanAssets(rows)
}

// Get returns one asset with its rollup, which is what the asset page shows.
func (s *Service) Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*Asset, error) {
	if err := s.canView(ctx, actor, id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, treeQuery+` WHERE pl.id = $4`, orgID, actor.Keys(), maxDepth, id)
	if err != nil {
		return nil, fmt.Errorf("plant: asset: %w", err)
	}
	list, err := scanAssets(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		// Visible but not in the tree means the recursive walk did not reach
		// it, which happens when it sits below its depth bound. Reporting
		// not-found would be a lie; reporting the asset with no rollup is the
		// honest answer, and the tree page shows the truncation.
		return nil, ErrNotFound
	}
	return list[0], nil
}

// Children lists what hangs directly off one asset.
func (s *Service) Children(ctx context.Context, orgID string, actor core.Subjects, id string) ([]*Asset, error) {
	if err := s.canView(ctx, actor, id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, treeQuery+` WHERE pl.parent_id = $4 ORDER BY pl.sort`,
		orgID, actor.Keys(), maxDepth, id)
	if err != nil {
		return nil, fmt.Errorf("plant: children: %w", err)
	}
	defer rows.Close()
	return scanAssets(rows)
}

func scanAssets(rows pgx.Rows) ([]*Asset, error) {
	var out []*Asset
	for rows.Next() {
		a := &Asset{}
		if err := rows.Scan(&a.ID, &a.Title, &a.ParentID, &a.ParentTitle, &a.Code,
			&a.InstalledAt, &a.RetiredAt, &a.Depth, &a.Path, &a.Criticality,
			&a.OwnOpen, &a.RolledOpen, &a.OwnMinutes, &a.RolledMinutes,
			&a.OwnCostCents, &a.RolledCost, &a.OwnDowntime, &a.RolledDowntime,
			&a.Children, &a.Down); err != nil {
			return nil, fmt.Errorf("plant: scan asset: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Ancestors returns the path from the root down to one asset, for a breadcrumb
// that a person can click back up.
func (s *Service) Ancestors(ctx context.Context, orgID string, actor core.Subjects, id string) ([]*Asset, error) {
	if err := s.canView(ctx, actor, id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		WITH RECURSIVE up AS (
		    SELECT a.document_id, a.parent_asset_id, 0 AS depth
		    FROM asset_fields a WHERE a.document_id = $1 AND a.org_id = $2
		  UNION ALL
		    SELECT p.document_id, p.parent_asset_id, u.depth + 1
		    FROM asset_fields p JOIN up u ON p.document_id = u.parent_asset_id
		    WHERE u.depth < $3
		) CYCLE document_id SET looped USING up_path
		SELECT d.id::text, d.title, up.depth
		FROM up JOIN documents d ON d.id = up.document_id
		WHERE up.depth > 0 AND NOT up.looped AND d.deleted_at IS NULL
		  AND EXISTS (
		      SELECT 1 FROM grants g
		       WHERE g.entity_type = 'document' AND g.entity_id = d.id
		         AND g.subject_key = ANY($4)
		         AND (g.expires_at IS NULL OR g.expires_at > now()))
		ORDER BY up.depth DESC`, id, orgID, maxDepth, actor.Keys())
	if err != nil {
		return nil, fmt.Errorf("plant: ancestors: %w", err)
	}
	defer rows.Close()

	var out []*Asset
	for rows.Next() {
		a := &Asset{}
		if err := rows.Scan(&a.ID, &a.Title, &a.Depth); err != nil {
			return nil, fmt.Errorf("plant: scan ancestor: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}
