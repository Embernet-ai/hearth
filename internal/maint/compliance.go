package maint

// Compliance: what was due, what was done, and what was missed.
//
// This is the report an auditor asks for, and the reason it is a query over
// pm_occurrences rather than a reconstruction from work orders. A work order
// can be renamed, reassigned, or deleted; the occurrence records that the
// schedule came round on a particular date whatever happened to the job
// afterwards.

import (
	"context"
	"fmt"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Compliance is the summary for a window.
type Compliance struct {
	From time.Time
	To   time.Time

	// Due is every occurrence whose date falls in the window.
	Due int
	// OnTime was completed on or before its due date.
	OnTime int
	// Late was completed, but after.
	Late int
	// Missed is still not done and the date has passed.
	Missed int
	// Open is not done and not yet late.
	Open int

	Rows []Occurrence
}

// Percent is the number that goes at the top of the report: how much of what
// was due actually got done, on time or otherwise.
//
// Open work is excluded from the denominator rather than counted as a failure,
// because a job due on Friday is not a compliance breach on Wednesday.
func (c Compliance) Percent() int {
	settled := c.OnTime + c.Late + c.Missed
	if settled == 0 {
		return 100
	}
	return (c.OnTime + c.Late) * 100 / settled
}

// OnTimePercent is the stricter number, which is the one a regulator means.
func (c Compliance) OnTimePercent() int {
	settled := c.OnTime + c.Late + c.Missed
	if settled == 0 {
		return 100
	}
	return c.OnTime * 100 / settled
}

// Report builds the compliance view for a window.
//
// Restricted to assets the caller can see, like every other list. A compliance
// number computed over machines somebody is not allowed to look at is a leak
// wearing a percentage sign.
func (s *Service) Report(ctx context.Context, orgID string, actor core.Subjects,
	from, to time.Time) (*Compliance, error) {

	if to.Before(from) {
		from, to = to, from
	}
	c := &Compliance{From: from, To: to}

	rows, err := s.db.Query(ctx, `
		SELECT o.id::text, o.schedule_id::text, s.name, s.asset_id::text,
		       o.due_at, o.work_order_id::text,
		       COALESCE(NULLIF(d.title,''), ''),
		       o.generated_at, o.completed_at, o.reason
		FROM pm_occurrences o
		JOIN pm_schedules s ON s.id = o.schedule_id
		JOIN documents a ON a.id = s.asset_id AND a.deleted_at IS NULL
		LEFT JOIN documents d ON d.id = o.work_order_id
		WHERE o.org_id = $1 AND o.due_at >= $3 AND o.due_at < $4`+visibleAsset+`
		ORDER BY o.due_at DESC`, orgID, actor.GrantKeys(), from, to)
	if err != nil {
		return nil, fmt.Errorf("maint: compliance: %w", err)
	}
	defer rows.Close()

	now := time.Now()
	for rows.Next() {
		var o Occurrence
		if err := rows.Scan(&o.ID, &o.ScheduleID, &o.Name, &o.AssetID,
			&o.DueAt, &o.WorkOrderID, &o.WorkOrder,
			&o.GeneratedAt, &o.CompletedAt, &o.Reason); err != nil {
			return nil, fmt.Errorf("maint: scan occurrence: %w", err)
		}
		c.Due++
		switch {
		case o.CompletedAt != nil && !o.Late():
			c.OnTime++
		case o.CompletedAt != nil:
			c.Late++
		case o.Deadline().Before(now):
			c.Missed++
		default:
			c.Open++
		}
		c.Rows = append(c.Rows, o)
	}
	return c, rows.Err()
}

// HistoryForAsset is the maintenance history somebody standing at a machine
// wants: every occurrence, done or not, newest first.
func (s *Service) HistoryForAsset(ctx context.Context, orgID string, actor core.Subjects,
	assetID string, limit int) ([]Occurrence, error) {

	if err := s.canView(ctx, actor, assetID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
		SELECT o.id::text, o.schedule_id::text, s.name, s.asset_id::text,
		       o.due_at, o.work_order_id::text,
		       COALESCE(NULLIF(d.title,''), ''),
		       o.generated_at, o.completed_at, o.reason
		FROM pm_occurrences o
		JOIN pm_schedules s ON s.id = o.schedule_id
		LEFT JOIN documents d ON d.id = o.work_order_id AND d.deleted_at IS NULL
		WHERE o.org_id = $1 AND s.asset_id = $2
		ORDER BY o.due_at DESC LIMIT $3`, orgID, assetID, limit)
	if err != nil {
		return nil, fmt.Errorf("maint: asset history: %w", err)
	}
	defer rows.Close()

	var out []Occurrence
	for rows.Next() {
		var o Occurrence
		if err := rows.Scan(&o.ID, &o.ScheduleID, &o.Name, &o.AssetID,
			&o.DueAt, &o.WorkOrderID, &o.WorkOrder,
			&o.GeneratedAt, &o.CompletedAt, &o.Reason); err != nil {
			return nil, fmt.Errorf("maint: scan history: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
