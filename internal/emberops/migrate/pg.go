package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
)

// PG reads Hearth's own database and keeps progress in emberops_migration.
type PG struct{ DB *pgxpool.Pool }

var _ Source = PG{}

// Org is the one real organisation. Playgrounds are organisations too and are
// not migrated.
func (p PG) Org(ctx context.Context) (string, error) {
	var id string
	err := p.DB.QueryRow(ctx, `SELECT id::text FROM orgs WHERE playground_of IS NULL ORDER BY created_at LIMIT 1`).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("migrate: find the organisation: %w", err)
	}
	return id, nil
}

// Count is how many rows the step reads.
func (p PG) Count(ctx context.Context, st Step, org string) (int, error) {
	var n int
	return n, p.DB.QueryRow(ctx, st.count, org).Scan(&n)
}

// Page reads the next rows of a step.
func (p PG) Page(ctx context.Context, st Step, cursor string, limit int, org string) ([]Row, error) {
	rows, err := p.DB.Query(ctx, st.query, cursor, limit, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var cur string
		var raw []byte
		if err := rows.Scan(&cur, &raw); err != nil {
			return nil, err
		}
		var rec mapping.Rec
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("decode row %s: %w", cur, err)
		}
		out = append(out, Row{Cur: cur, Rec: rec})
	}
	return out, rows.Err()
}

// Progress reads a step's checkpoint.
func (p PG) Progress(ctx context.Context, step string) (string, bool, error) {
	var cursor string
	var done bool
	err := p.DB.QueryRow(ctx, `SELECT cursor, done FROM emberops_migration WHERE step = $1`, step).Scan(&cursor, &done)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	return cursor, done, nil
}

// Save checkpoints a page.
func (p PG) Save(ctx context.Context, step, cursor string, rows int) error {
	_, err := p.DB.Exec(ctx, `INSERT INTO emberops_migration (step, cursor, rows_done) VALUES ($1,$2,$3)
		ON CONFLICT (step) DO UPDATE SET cursor = $2, rows_done = emberops_migration.rows_done + $3, updated_at = now()`,
		step, cursor, rows)
	return err
}

// Finish marks a step complete.
func (p PG) Finish(ctx context.Context, step, cursor string) error {
	_, err := p.DB.Exec(ctx, `INSERT INTO emberops_migration (step, cursor, done) VALUES ($1,$2,true)
		ON CONFLICT (step) DO UPDATE SET done = true, updated_at = now()`, step, cursor)
	return err
}

// Reset forgets a step's checkpoint.
func (p PG) Reset(ctx context.Context, step string) error {
	_, err := p.DB.Exec(ctx, `DELETE FROM emberops_migration WHERE step = $1`, step)
	return err
}
