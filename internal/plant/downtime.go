package plant

// Downtime, which is the number that turns a maintenance history into a
// replacement decision.
//
// Cost of repair alone never justifies replacing anything: a cheap machine
// repaired often looks better than an expensive one repaired twice. The hours
// the line was not running is the number the argument is actually about, and
// nobody records it unless it takes five seconds.

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

var (
	// ErrAlreadyDown means this asset already has an open outage. Two
	// overlapping records is the same hour counted twice.
	ErrAlreadyDown = errors.New("that machine is already recorded as down")
	ErrNotDown     = errors.New("that machine is not recorded as down")
)

// Downtime is one outage.
type Downtime struct {
	ID          string
	AssetID     string
	AssetTitle  string
	WorkOrderID *string
	WorkOrder   string
	StartedAt   time.Time
	EndedAt     *time.Time
	Minutes     *int
	Planned     bool
	Note        string
	RecordedBy  string
	ByName      string
}

// Open reports whether the machine is still down.
func (d Downtime) Open() bool { return d.EndedAt == nil }

// Elapsed counts an open outage up to now, because an outage rendered as zero
// minutes while the line is stopped is a number nobody believes.
func (d Downtime) Elapsed() int {
	if d.Minutes != nil {
		return *d.Minutes
	}
	return int(time.Since(d.StartedAt).Minutes())
}

// GoDown records that an asset has stopped.
//
// Comment access, like logging time and ticking a checklist: saying the line is
// stopped is reporting what happened, not deciding what happens. The operator
// who notices is rarely the person who may edit the asset record.
func (s *Service) GoDown(ctx context.Context, orgID string, actor core.Subjects,
	assetID string, at time.Time, planned bool, workOrderID *string, note string) (*Downtime, error) {

	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: assetID}, core.AccessComment); err != nil {
		return nil, err
	}
	if at.IsZero() {
		at = time.Now()
	}
	if at.After(time.Now().Add(time.Minute)) {
		return nil, fmt.Errorf("%w: a machine cannot have stopped in the future", ErrInvalid)
	}
	if workOrderID != nil && *workOrderID == "" {
		workOrderID = nil
	}

	d := &Downtime{
		ID: uuid.NewString(), AssetID: assetID, StartedAt: at, Planned: planned,
		WorkOrderID: workOrderID, Note: strings.TrimSpace(note),
		RecordedBy: actor.Principal(),
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO asset_downtime (id, org_id, asset_id, work_order_id, started_at,
		                            planned, note, recorded_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		d.ID, orgID, assetID, d.WorkOrderID, d.StartedAt, planned, d.Note, d.RecordedBy)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyDown
		}
		return nil, fmt.Errorf("plant: record downtime: %w", err)
	}
	return d, nil
}

// ComeUp closes the open outage on an asset.
//
// Minutes are stored rather than derived, for the same reason time entries
// store them: a row with an end and no duration is counted as zero by every
// rollup and nobody ever finds out why the numbers are low.
func (s *Service) ComeUp(ctx context.Context, orgID string, actor core.Subjects,
	assetID string, at time.Time) (*Downtime, error) {

	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: assetID}, core.AccessComment); err != nil {
		return nil, err
	}
	if at.IsZero() {
		at = time.Now()
	}

	d := &Downtime{AssetID: assetID}
	err := s.db.QueryRow(ctx, `
		UPDATE asset_downtime
		   SET ended_at = GREATEST($3, started_at),
		       minutes  = GREATEST(0, (EXTRACT(EPOCH FROM (GREATEST($3, started_at) - started_at)) / 60)::int)
		 WHERE asset_id = $1 AND org_id = $2 AND ended_at IS NULL
		RETURNING id::text, started_at, ended_at, minutes, planned, note, recorded_by::text`,
		assetID, orgID, at).
		Scan(&d.ID, &d.StartedAt, &d.EndedAt, &d.Minutes, &d.Planned, &d.Note, &d.RecordedBy)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotDown
	case err != nil:
		return nil, fmt.Errorf("plant: end downtime: %w", err)
	}
	return d, nil
}

// LogDowntime records an outage that is already over, which is how most of them
// arrive. The line came back an hour ago and somebody is writing it up now.
func (s *Service) LogDowntime(ctx context.Context, orgID string, actor core.Subjects,
	assetID string, from, to time.Time, planned bool, workOrderID *string, note string) (*Downtime, error) {

	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: assetID}, core.AccessComment); err != nil {
		return nil, err
	}
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return nil, fmt.Errorf("%w: an outage needs a start and a later end", ErrInvalid)
	}
	if to.After(time.Now().Add(time.Minute)) {
		return nil, fmt.Errorf("%w: it cannot have ended in the future", ErrInvalid)
	}
	minutes := int(to.Sub(from).Minutes())
	if workOrderID != nil && *workOrderID == "" {
		workOrderID = nil
	}

	d := &Downtime{
		ID: uuid.NewString(), AssetID: assetID, StartedAt: from, EndedAt: &to,
		Minutes: &minutes, Planned: planned, WorkOrderID: workOrderID,
		Note: strings.TrimSpace(note), RecordedBy: actor.Principal(),
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO asset_downtime (id, org_id, asset_id, work_order_id, started_at,
		                            ended_at, minutes, planned, note, recorded_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		d.ID, orgID, assetID, d.WorkOrderID, from, to, minutes, planned, d.Note, d.RecordedBy)
	if err != nil {
		return nil, fmt.Errorf("plant: log downtime: %w", err)
	}
	return d, nil
}

// RemoveDowntime deletes a record.
//
// Edit on the asset, not comment. Recording an outage is reporting; deleting
// one changes what the history says happened, and the availability number an
// argument is built on has to be harder to erase than it was to write.
func (s *Service) RemoveDowntime(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	var assetID string
	err := s.db.QueryRow(ctx,
		`SELECT asset_id::text FROM asset_downtime WHERE id=$1 AND org_id=$2`, id, orgID).
		Scan(&assetID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("plant: find downtime: %w", err)
	}
	if err := s.canEdit(ctx, actor, assetID); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM asset_downtime WHERE id=$1`, id); err != nil {
		return fmt.Errorf("plant: remove downtime: %w", err)
	}
	return nil
}

// DowntimeFor lists an asset's outages, newest first.
func (s *Service) DowntimeFor(ctx context.Context, orgID string, actor core.Subjects,
	assetID string, limit int) ([]Downtime, error) {

	if err := s.canView(ctx, actor, assetID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
		SELECT dn.id::text, dn.work_order_id::text, COALESCE(NULLIF(wo.title,''), ''),
		       dn.started_at, dn.ended_at, dn.minutes, dn.planned, dn.note,
		       dn.recorded_by::text, COALESCE(NULLIF(p.display_name,''), '')
		FROM asset_downtime dn
		JOIN principals p ON p.id = dn.recorded_by
		LEFT JOIN documents wo ON wo.id = dn.work_order_id AND wo.deleted_at IS NULL
		WHERE dn.asset_id = $1 AND dn.org_id = $2
		ORDER BY dn.started_at DESC LIMIT $3`, assetID, orgID, limit)
	if err != nil {
		return nil, fmt.Errorf("plant: downtime for asset: %w", err)
	}
	defer rows.Close()

	var out []Downtime
	for rows.Next() {
		var d Downtime
		d.AssetID = assetID
		if err := rows.Scan(&d.ID, &d.WorkOrderID, &d.WorkOrder, &d.StartedAt,
			&d.EndedAt, &d.Minutes, &d.Planned, &d.Note, &d.RecordedBy, &d.ByName); err != nil {
			return nil, fmt.Errorf("plant: scan downtime: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Availability is what the report is actually for.
type Availability struct {
	From time.Time
	To   time.Time
	// Window is the total minutes in the period.
	Window int
	// Unplanned is the minutes lost to breakdowns, rolled up the tree.
	Unplanned int
	// Planned is shutdowns and scheduled work, kept separate because adding
	// them together makes the best-maintained machine look like the worst.
	Planned int
	// Outages counts the unplanned stops, which is the other half of the
	// story: one long outage and twenty short ones need different answers.
	Outages int
}

// Percent is uptime against the window, as a whole number.
func (a Availability) Percent() int {
	if a.Window <= 0 {
		return 100
	}
	up := a.Window - a.Unplanned
	if up < 0 {
		up = 0
	}
	return up * 100 / a.Window
}

// MeanBetween is the average minutes of running between unplanned stops, which
// is the number a reliability argument is made of. Zero outages means there is
// nothing to average, and the page says so rather than showing infinity.
func (a Availability) MeanBetween() int {
	if a.Outages == 0 {
		return 0
	}
	return (a.Window - a.Unplanned) / a.Outages
}

// AvailabilityFor rolls downtime up the tree for one asset over a window.
//
// Rolled up, because a line is down when any machine on it is down, and asking
// about the line is the question somebody actually has.
func (s *Service) AvailabilityFor(ctx context.Context, orgID string, actor core.Subjects,
	assetID string, from, to time.Time) (*Availability, error) {

	if err := s.canView(ctx, actor, assetID); err != nil {
		return nil, err
	}
	if to.Before(from) {
		from, to = to, from
	}
	av := &Availability{From: from, To: to, Window: int(to.Sub(from).Minutes())}

	// Clamped to the window, so an outage that started last month contributes
	// only the part of it that falls inside the period being reported on.
	err := s.db.QueryRow(ctx, `
		WITH RECURSIVE down AS (
		    SELECT $1::uuid AS id, 0 AS depth
		  UNION ALL
		    SELECT a.document_id, d.depth + 1
		    FROM asset_fields a JOIN down d ON a.parent_asset_id = d.id
		    WHERE d.depth < $2
		) CYCLE id SET looped USING down_path,
		clipped AS (
		    SELECT dn.planned,
		           GREATEST(0, EXTRACT(EPOCH FROM (
		               LEAST(COALESCE(dn.ended_at, now()), $4) -
		               GREATEST(dn.started_at, $3))) / 60)::int AS minutes
		    FROM asset_downtime dn
		    JOIN down ON down.id = dn.asset_id AND NOT down.looped
		    WHERE dn.org_id = $5
		      AND dn.started_at < $4
		      AND COALESCE(dn.ended_at, now()) > $3
		)
		SELECT COALESCE(SUM(minutes) FILTER (WHERE NOT planned), 0)::int,
		       COALESCE(SUM(minutes) FILTER (WHERE planned), 0)::int,
		       count(*) FILTER (WHERE NOT planned)::int
		FROM clipped`, assetID, maxDepth, from, to, orgID).
		Scan(&av.Unplanned, &av.Planned, &av.Outages)
	if err != nil {
		return nil, fmt.Errorf("plant: availability: %w", err)
	}
	return av, nil
}
