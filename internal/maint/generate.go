package maint

// Generation, and the two rules that keep it honest.
//
// The sweep asks "is there one open", never "how many are overdue". The next
// due date is computed from when the work was DONE, never from when it was
// due. Between them, a schedule ignored for a year produces one work order and
// a job finished three weeks late moves the whole series three weeks, which is
// what a person would do with a paper card.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// Job kinds.
const (
	KindSweep    = "maint.sweep"
	KindComplete = "maint.completed"
)

// sweepInterval is how often the raiser looks. Hourly is enough for a schedule
// measured in days, and the meter path is what needs the responsiveness: a
// machine can cross a threshold in an afternoon.
const sweepInterval = time.Hour

// Generator raises the work.
type Generator struct {
	svc   *Service
	queue *jobs.Queue
	log   *slog.Logger
}

func NewGenerator(svc *Service, q *jobs.Queue, log *slog.Logger) *Generator {
	return &Generator{svc: svc, queue: q, log: log}
}

// Register wires the handlers onto a worker.
func (g *Generator) Register(h interface {
	Handle(kind string, fn jobs.Handler)
}) {
	h.Handle(KindSweep, g.handleSweep)
	h.Handle(KindComplete, g.handleCompleted)
}

// Routes subscribe to completion.
//
// This is the important half. A preventive maintenance job completing is what
// schedules the next one, so the whole series is driven by work actually being
// done rather than by a clock nobody is watching.
func Routes() []jobs.Route {
	return []jobs.Route{
		{Event: jobs.EventStatus, EntityType: string(core.EntityDocument), JobKind: KindComplete},
	}
}

// Start queues the recurring sweep for one org. Keyed, so a restart does not
// multiply it.
func (g *Generator) Start(ctx context.Context, orgID string) error {
	_, err := g.queue.Enqueue(ctx, jobs.Spec{
		OrgID: orgID, Kind: KindSweep, UniqueKey: "pm_sweep",
	})
	return err
}

// StartAll queues the sweep for every org.
func (g *Generator) StartAll(ctx context.Context) error {
	rows, err := g.svc.db.Query(ctx, `SELECT id::text FROM orgs WHERE playground_of IS NULL`)
	if err != nil {
		return fmt.Errorf("maint: list orgs: %w", err)
	}
	defer rows.Close()
	var orgs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("maint: scan org: %w", err)
		}
		orgs = append(orgs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, org := range orgs {
		if err := g.Start(ctx, org); err != nil {
			return err
		}
	}
	return nil
}

// handleSweep raises work for schedules that have come round.
//
// This IS a sweep, and it is safe because of what it asks. It does not
// enumerate missed periods; it looks for schedules that are due, have no open
// occurrence, and raises exactly one each. A schedule untouched for a year
// produces one work order, which is the whole point.
func (g *Generator) handleSweep(ctx context.Context, j *jobs.Job) error {
	rows, err := g.svc.db.Query(ctx, `
		SELECT s.id::text
		FROM pm_schedules s
		-- The asset has to still be there. Deleting a machine is a soft delete,
		-- so without this a schedule outlives the thing it maintains and the
		-- sweep keeps raising jobs for a press that left the building.
		JOIN documents a ON a.id = s.asset_id AND a.deleted_at IS NULL
		WHERE s.org_id = $1 AND s.enabled
		  AND NOT EXISTS (
		      SELECT 1 FROM pm_occurrences o
		       WHERE o.schedule_id = s.id AND o.completed_at IS NULL)
		LIMIT 1000`, j.OrgID)
	if err != nil {
		return fmt.Errorf("maint: sweep: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("maint: scan sweep: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, id := range ids {
		sch, err := g.svc.load(ctx, j.OrgID, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return err
		}
		// Due, including the lead time, so somebody has notice rather than
		// finding out it was due this morning.
		if sch.DueAt().After(time.Now().AddDate(0, 0, sch.LeadDays)) {
			continue
		}
		reason := ReasonCalendar
		if sch.ProjectedMeterDue != nil && sch.ProjectedMeterDue.Before(sch.NextDueAt) {
			reason = ReasonMeter
		}
		if _, err := g.raise(ctx, sch, sch.DueAt(), reason); err != nil {
			return err
		}
	}

	// Chain the next sweep rather than relying on a scheduler. Same trick as
	// the digest and the due-soon check: the queue can already run a job on a
	// delay, and a second thing that can be down is a second thing that can be
	// down.
	_, err = g.queue.Enqueue(ctx, jobs.Spec{
		OrgID: j.OrgID, Kind: KindSweep, UniqueKey: "pm_sweep",
		RunAfter: time.Now().Add(sweepInterval),
	})
	return err
}

// raise creates one occurrence and the work order for it.
//
// The unique index on (schedule, due date) is what makes this idempotent: a
// redelivered job, a second sweep, and a completion arriving twice all try to
// write the same row and only one of them does.
func (g *Generator) raise(ctx context.Context, sch *Schedule, dueAt time.Time, reason string) (string, error) {
	// Claim the occurrence first. If somebody else already has it, there is
	// nothing to do and no work order to create.
	occID := uuid.NewString()
	ct, err := g.svc.db.Exec(ctx, `
		INSERT INTO pm_occurrences (id, schedule_id, org_id, due_at, reason)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (schedule_id, due_at) DO NOTHING`,
		occID, sch.ID, sch.OrgID, dueAt.UTC().Truncate(time.Second), reason)
	if err != nil {
		return "", fmt.Errorf("maint: claim occurrence: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return "", nil
	}

	// The work order. From the template when there is one, so the procedure
	// and its checklist come with it; a bare reminder with no steps is not a
	// work order, it is a note.
	title := fmt.Sprintf("%s: %s", sch.Name, sch.AssetTitle)

	// Depth zero, and no rule id. A schedule is not a rule reacting to a
	// change, it is the system doing an original piece of work, and rules
	// should apply to a generated work order exactly as they do to one somebody
	// typed. Stamping it at depth one would silently exempt every preventive
	// job from "assign new work orders on the pump line to the on-call tech",
	// which is the rule people write first.
	actor := docs.RuleActor{PrincipalID: sch.CreatedBy}

	var workOrderID string
	if sch.TemplateID != nil && *sch.TemplateID != "" {
		wo, err := g.svc.docs.InstantiateAsSystem(ctx, sch.OrgID, actor, docs.InstantiateParams{
			TemplateID: *sch.TemplateID, Title: title, Kind: docs.KindWorkOrder,
			Task: &docs.TaskFields{Status: docs.StatusTodo, DueAt: &dueAt},
		}, core.Ref{Type: core.EntityDocument, ID: sch.AssetID})
		if err != nil {
			return "", err
		}
		workOrderID = wo.ID
	} else {
		wo, err := g.svc.docs.CreateAsSystem(ctx, sch.OrgID, actor, docs.CreateParams{
			Kind: docs.KindWorkOrder, Title: title,
			Body: "Raised automatically by the maintenance schedule.",
			Task: &docs.TaskFields{Status: docs.StatusTodo, DueAt: &dueAt},
		}, core.Ref{Type: core.EntityDocument, ID: sch.AssetID})
		if err != nil {
			return "", err
		}
		workOrderID = wo.ID
	}

	if _, err := g.svc.db.Exec(ctx, `
		UPDATE pm_occurrences SET work_order_id=$2 WHERE id=$1`, occID, workOrderID); err != nil {
		return "", fmt.Errorf("maint: attach work order: %w", err)
	}
	if _, err := g.svc.db.Exec(ctx, `
		UPDATE pm_schedules SET last_generated_at = now() WHERE id = $1`, sch.ID); err != nil {
		return "", fmt.Errorf("maint: record generation: %w", err)
	}

	g.log.InfoContext(ctx, "raised a preventive maintenance job",
		"schedule", sch.Name, "asset", sch.AssetTitle, "due", dueAt, "reason", reason)
	return workOrderID, nil
}

// handleCompleted is the important half: a preventive maintenance job being
// finished is what schedules the next one.
func (g *Generator) handleCompleted(ctx context.Context, j *jobs.Job) error {
	var p jobs.EventPayload
	if err := j.Decode(&p); err != nil {
		return err
	}
	var body struct {
		To string `json:"to"`
	}
	if len(p.Payload) > 0 {
		if err := json.Unmarshal(p.Payload, &body); err != nil {
			return fmt.Errorf("maint: decode status change: %w", err)
		}
	}
	if body.To != docs.StatusDone && body.To != docs.StatusCancelled {
		return nil
	}

	// Is this work order one of ours?
	var occID, schedID string
	var completed *time.Time
	err := g.svc.db.QueryRow(ctx, `
		SELECT o.id::text, o.schedule_id::text, o.completed_at
		FROM pm_occurrences o
		WHERE o.work_order_id = $1 AND o.org_id = $2`, p.EntityID, j.OrgID).
		Scan(&occID, &schedID, &completed)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("maint: find occurrence: %w", err)
	}
	if completed != nil {
		// Already closed. At-least-once delivery makes this the normal case
		// for a redelivered job rather than an anomaly, and doing the work
		// again would move the series a second time.
		return nil
	}

	now := time.Now()
	if _, err := g.svc.db.Exec(ctx,
		`UPDATE pm_occurrences SET completed_at = $2 WHERE id = $1 AND completed_at IS NULL`,
		occID, now); err != nil {
		return fmt.Errorf("maint: close occurrence: %w", err)
	}

	sch, err := g.svc.load(ctx, j.OrgID, schedID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if !sch.Enabled {
		return nil
	}

	// FORWARD from the completion. Not from the due date, which is the trap:
	// a job finished three weeks late would otherwise be due again
	// immediately, and a schedule ignored over a shutdown would come back with
	// a stack of them.
	next := now
	if sch.EveryDays != nil {
		next = now.AddDate(0, 0, *sch.EveryDays)
	} else {
		// Meter-driven only. The date is a placeholder far enough out that the
		// meter decides, which it does through the projection.
		next = now.AddDate(1, 0, 0)
	}

	// And the meter threshold moves to where the machine actually is, rather
	// than to where the schedule hoped it would be.
	var units *float64
	if sch.MeterKey != nil {
		if latest, err := g.svc.LatestReading(ctx, sch.AssetID, *sch.MeterKey); err != nil {
			return err
		} else if latest != nil {
			units = &latest.Value
		}
	}

	if _, err := g.svc.db.Exec(ctx, `
		UPDATE pm_schedules
		   SET next_due_at = $2,
		       last_generated_units = COALESCE($3, last_generated_units),
		       updated_at = now()
		 WHERE id = $1`, schedID, next, units); err != nil {
		return fmt.Errorf("maint: move the schedule on: %w", err)
	}

	g.log.InfoContext(ctx, "preventive maintenance completed, next one scheduled",
		"schedule", sch.Name, "next", next)

	// If the new date is already inside the lead window, the sweep will pick
	// it up on its next pass. Nudging it here would raise the successor in the
	// same breath as closing the last one, which on a short interval means two
	// open jobs where the design promises one.
	return nil
}

// RaiseNow creates the next occurrence immediately, for somebody who wants the
// job now rather than when it comes round.
//
// It goes through the same path, so it is subject to the same one-open rule: a
// person pressing the button twice gets one work order.
func (g *Generator) RaiseNow(ctx context.Context, orgID string, actor core.Subjects, scheduleID string) (string, error) {
	if err := g.svc.mayChange(ctx, orgID, actor, scheduleID); err != nil {
		return "", err
	}
	sch, err := g.svc.load(ctx, orgID, scheduleID)
	if err != nil {
		return "", err
	}
	if sch.Open != nil {
		return "", nil
	}
	return g.raise(ctx, sch, time.Now(), ReasonManual)
}
