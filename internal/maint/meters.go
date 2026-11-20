package maint

// Meters, and the projection that turns readings into a date.
//
// "Every 500 run hours" is not a date, and the useful question is when it will
// be one. A schedule that can only say "not yet" until the morning it says
// "overdue" gives nobody time to order a part.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Reading is one meter value at one moment.
type Reading struct {
	ID       string
	AssetID  string
	Key      string
	Value    float64
	ReadAt   time.Time
	ReadBy   string
	ReadName string
	Note     string
}

// RecordReading stores a meter value.
//
// Comment access, like ticking a checklist and logging time: reading a gauge is
// doing the work rather than changing what the work is, and the person with the
// clipboard is rarely the person who may edit the asset record.
func (s *Service) RecordReading(ctx context.Context, orgID string, actor core.Subjects,
	assetID, key string, value float64, note string) (*Reading, error) {

	if err := s.canComment(ctx, actor, assetID); err != nil {
		return nil, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%w: a reading needs to say which meter", ErrInvalid)
	}
	if value < 0 {
		return nil, fmt.Errorf("%w: a meter does not go backwards", ErrInvalid)
	}

	r := &Reading{
		ID: uuid.NewString(), AssetID: assetID, Key: key, Value: value,
		ReadBy: actor.Principal(), Note: strings.TrimSpace(note),
	}
	err := s.db.QueryRow(ctx, `
		INSERT INTO meter_readings (id, org_id, asset_id, key, value, read_by, note)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING read_at`,
		r.ID, orgID, assetID, key, value, r.ReadBy, r.Note).Scan(&r.ReadAt)
	if err != nil {
		return nil, fmt.Errorf("maint: record reading: %w", err)
	}
	return r, nil
}

// LatestReading is the most recent value for one meter on one asset.
func (s *Service) LatestReading(ctx context.Context, assetID, key string) (*Reading, error) {
	r := &Reading{AssetID: assetID, Key: key}
	err := s.db.QueryRow(ctx, `
		SELECT id::text, value, read_at, read_by::text
		FROM meter_readings
		WHERE asset_id = $1 AND key = $2
		ORDER BY read_at DESC LIMIT 1`, assetID, key).
		Scan(&r.ID, &r.Value, &r.ReadAt, &r.ReadBy)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("maint: latest reading: %w", err)
	}
	return r, nil
}

// Readings lists an asset's meter history, newest first.
func (s *Service) Readings(ctx context.Context, orgID string, actor core.Subjects,
	assetID string, limit int) ([]Reading, error) {

	if err := s.canView(ctx, actor, assetID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
		SELECT m.id::text, m.key, m.value, m.read_at, m.read_by::text,
		       COALESCE(NULLIF(p.display_name,''), ''), m.note
		FROM meter_readings m
		JOIN principals p ON p.id = m.read_by
		WHERE m.asset_id = $1 AND m.org_id = $2
		ORDER BY m.read_at DESC LIMIT $3`, assetID, orgID, limit)
	if err != nil {
		return nil, fmt.Errorf("maint: readings: %w", err)
	}
	defer rows.Close()

	var out []Reading
	for rows.Next() {
		var r Reading
		r.AssetID = assetID
		if err := rows.Scan(&r.ID, &r.Key, &r.Value, &r.ReadAt, &r.ReadBy,
			&r.ReadName, &r.Note); err != nil {
			return nil, fmt.Errorf("maint: scan reading: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// trendWindow is how far back the projection looks. Long enough to average out
// a quiet fortnight, short enough that a machine which changed shift pattern
// last month is not judged on how it ran before.
const trendWindow = 90 * 24 * time.Hour

// projectMeterDue works out when the meter will reach the next threshold.
//
// From the trend rather than from a guess. Two readings and a rate: units per
// day since the earliest reading in the window, applied to how far there is
// left to go. It returns nil rather than a date when there is nothing to go
// on, because a projection built from one reading is a number with the shape
// of an answer and none of the content.
func (s *Service) projectMeterDue(ctx context.Context, sch *Schedule) (*time.Time, error) {
	if sch.MeterKey == nil || sch.EveryUnits == nil || *sch.EveryUnits <= 0 {
		return nil, nil
	}

	var firstValue, lastValue float64
	var firstAt, lastAt time.Time
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT count(*),
		       COALESCE(MIN(value) FILTER (WHERE read_at = w.first_at), 0),
		       COALESCE(MAX(value) FILTER (WHERE read_at = w.last_at), 0),
		       w.first_at, w.last_at
		FROM meter_readings m
		CROSS JOIN LATERAL (
		    SELECT MIN(read_at) AS first_at, MAX(read_at) AS last_at
		    FROM meter_readings
		    WHERE asset_id = $1 AND key = $2 AND read_at >= now() - $3::interval
		) w
		WHERE m.asset_id = $1 AND m.key = $2 AND m.read_at >= now() - $3::interval
		GROUP BY w.first_at, w.last_at`,
		sch.AssetID, *sch.MeterKey, trendWindow.String()).
		Scan(&n, &firstValue, &lastValue, &firstAt, &lastAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("maint: meter trend: %w", err)
	}
	if n < 2 || !lastAt.After(firstAt) {
		return nil, nil
	}

	// Where the threshold sits: the reading the last service was done at, plus
	// the interval. With no recorded baseline, measure from the first reading
	// in the window, which is the best available answer rather than a made-up
	// zero.
	base := firstValue
	if sch.LastGeneratedUnits != nil {
		base = *sch.LastGeneratedUnits
	}
	threshold := base + *sch.EveryUnits

	if lastValue >= threshold {
		// Already past it. Due now, and the sweep will raise it on the next
		// pass rather than the projection pretending it is in the future.
		now := time.Now()
		return &now, nil
	}

	days := lastAt.Sub(firstAt).Hours() / 24
	perDay := (lastValue - firstValue) / days
	if perDay <= 0 {
		// Idle, or the meter was reset. Either way there is no rate to project
		// with, and inventing one would put a date on a machine that is not
		// running.
		return nil, nil
	}

	remaining := (threshold - lastValue) / perDay
	if remaining > 3650 {
		// A decade out is not a projection anybody should act on, and showing
		// one makes the calendar clock look wrong by comparison.
		return nil, nil
	}
	due := lastAt.Add(time.Duration(remaining * float64(24*time.Hour)))
	return &due, nil
}

// MeterKeys lists the meters an asset has readings for, so a form can offer
// them rather than asking somebody to remember the spelling.
func (s *Service) MeterKeys(ctx context.Context, assetID string) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT DISTINCT key FROM meter_readings WHERE asset_id=$1 ORDER BY key`, assetID)
	if err != nil {
		return nil, fmt.Errorf("maint: meter keys: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("maint: scan meter key: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
