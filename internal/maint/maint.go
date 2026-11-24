// Package maint is preventive maintenance, and it is the reason this product
// exists rather than being a nicer task list.
//
// # The rule everything else follows
//
// A schedule generates FORWARD from completion, and never from a sweep over
// everything overdue. Get that wrong and the system produces a hundred
// duplicate jobs after a shutdown week, the crew stops trusting it, and no
// amount of tidying afterwards brings them back. Two things enforce it:
//
//   - There is at most ONE open occurrence per schedule. The sweep that raises
//     work does not ask "how many are overdue", it asks "is there one open",
//     so a schedule left alone for a year produces one job and not 365.
//   - The next due date is computed from when the work was DONE, not from when
//     it was due. A job finished three weeks late moves the whole series three
//     weeks, which is what a person would do with a paper card.
//
// # Two clocks
//
// Calendar and meter, whichever arrives first. A machine that sat idle for a
// quarter does not need the same service as one that ran three shifts, and a
// system that only knows about dates cannot tell the difference.
package maint

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
)

var (
	ErrNotFound = errors.New("schedule not found")
	ErrInvalid  = errors.New("that schedule would never come round")
)

// Reasons an occurrence came round.
const (
	ReasonCalendar = "calendar"
	ReasonMeter    = "meter"
	ReasonManual   = "manual"
)

// Schedule is one recurring job on one asset.
type Schedule struct {
	ID         string
	OrgID      string
	AssetID    string
	AssetTitle string
	TemplateID *string
	Name       string

	// EveryDays drives the calendar clock, nil for meter-only.
	EveryDays *int
	// MeterKey and EveryUnits drive the meter clock, both nil for
	// calendar-only.
	MeterKey   *string
	EveryUnits *float64

	LeadDays           int
	NextDueAt          time.Time
	LastGeneratedAt    *time.Time
	LastGeneratedUnits *float64
	Enabled            bool

	// CreatedBy is whoever set the schedule up, and the work it generates is
	// written in their name. "The system did it" is not an answer anybody
	// accepts when a work order appeared overnight.
	CreatedBy string

	// Open is the occurrence currently outstanding, if any. At most one, ever,
	// and that is the whole defence against a pile.
	Open *Occurrence
	// ProjectedMeterDue is when the meter is expected to reach the next
	// threshold, worked out from the trend. Nil when there is no meter or not
	// enough readings to say.
	ProjectedMeterDue *time.Time
}

// DueAt is when this schedule next comes round: the earlier of the two clocks.
func (s Schedule) DueAt() time.Time {
	if s.ProjectedMeterDue != nil && s.ProjectedMeterDue.Before(s.NextDueAt) {
		return *s.ProjectedMeterDue
	}
	return s.NextDueAt
}

// Overdue reports whether it should already have been done.
func (s Schedule) Overdue() bool { return s.DueAt().Before(time.Now()) }

// Occurrence is one time a schedule came round.
type Occurrence struct {
	ID          string
	ScheduleID  string
	Name        string
	AssetID     string
	DueAt       time.Time
	WorkOrderID *string
	WorkOrder   string
	GeneratedAt time.Time
	CompletedAt *time.Time
	Reason      string
}

// Deadline is the end of the day the work was due.
//
// A due date is a date. Grading lateness to the microsecond would mark a job
// raised at nine and finished at ten past as late, which is not what anybody
// standing in front of the machine means, and it would make a compliance
// report that nobody believes. Due Tuesday and done Tuesday is on time.
// The day is the server's local day, which is the same choice the workload
// week makes. A per-org timezone would be better and does not exist yet;
// grading against UTC on a site running in New York would call a job finished
// at eight in the evening late.
func (o Occurrence) Deadline() time.Time {
	y, m, d := o.DueAt.Local().Date()
	return time.Date(y, m, d, 23, 59, 59, int(time.Second-1), time.Local)
}

// Missed reports whether the day passed with the work still open.
func (o Occurrence) Missed() bool {
	return o.CompletedAt == nil && o.Deadline().Before(time.Now())
}

// Late reports whether it was done, but after the day it was due.
func (o Occurrence) Late() bool {
	return o.CompletedAt != nil && o.CompletedAt.After(o.Deadline())
}

// Settled reports whether this occurrence has an answer yet. Work that is not
// yet late is neither a success nor a failure, and counting it as either is how
// a compliance number lies in both directions.
func (o Occurrence) Settled() bool { return o.CompletedAt != nil || o.Missed() }

// Authorizer is the slice of access control this package needs.
type Authorizer interface {
	Require(ctx context.Context, subjects core.Subjects, ref core.Ref, required core.AccessLevel) error
}

// Documents is the slice of the spine this package needs, declared here by the
// consumer so the dependency points one way.
type Documents interface {
	CreateAsSystem(ctx context.Context, orgID string, actor docs.RuleActor, p docs.CreateParams, linkTo core.Ref) (*docs.Document, error)
	InstantiateAsSystem(ctx context.Context, orgID string, actor docs.RuleActor, p docs.InstantiateParams, linkTo core.Ref) (*docs.Document, error)
}

// The compiler checks the real thing satisfies it, so the interface above stays
// a description of what this package uses rather than drifting into fiction.
var _ Documents = (*docs.Service)(nil)

type Service struct {
	db    *pgxpool.Pool
	authz Authorizer
	docs  Documents
}

func New(db *pgxpool.Pool, az Authorizer, d Documents) *Service {
	return &Service{db: db, authz: az, docs: d}
}

// canView and canComment name the two questions this package asks, so the
// access level for reading a gauge is decided once rather than at four call
// sites.
func (s *Service) canView(ctx context.Context, actor core.Subjects, docID string) error {
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessView); err != nil {
		return ErrNotFound
	}
	return nil
}

func (s *Service) canComment(ctx context.Context, actor core.Subjects, docID string) error {
	return s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessComment)
}

// -----------------------------------------------------------------------------
// Schedules
// -----------------------------------------------------------------------------

// Save creates or replaces a schedule.
//
// Edit on the asset, because a schedule is a standing instruction to raise work
// against it. Comment is the line for recording what happened; deciding what
// happens next is the other side of it.
func (s *Service) Save(ctx context.Context, orgID string, actor core.Subjects, sch Schedule) (*Schedule, error) {
	// On an update the asset comes from the stored row, not from the caller.
	// The UPDATE below cannot move a schedule to another machine, and reading
	// the asset out of the form would let somebody who can edit asset A ask
	// permission about A while changing a schedule on B.
	if sch.ID != "" {
		if err := s.db.QueryRow(ctx,
			`SELECT asset_id::text FROM pm_schedules WHERE id=$1 AND org_id=$2`,
			sch.ID, orgID).Scan(&sch.AssetID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("maint: read schedule asset: %w", err)
		}
	}
	if sch.AssetID == "" {
		return nil, fmt.Errorf("%w: it needs an asset", ErrInvalid)
	}
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: sch.AssetID}, core.AccessEdit); err != nil {
		return nil, err
	}
	actorID := actor.Principal()

	sch.Name = strings.TrimSpace(sch.Name)
	if sch.Name == "" {
		return nil, fmt.Errorf("%w: it needs a name", ErrInvalid)
	}
	hasCalendar := sch.EveryDays != nil && *sch.EveryDays > 0
	hasMeter := sch.MeterKey != nil && *sch.MeterKey != "" && sch.EveryUnits != nil && *sch.EveryUnits > 0
	if !hasCalendar && !hasMeter {
		// A schedule with neither clock never comes round, which is worse than
		// no schedule: it looks like cover and is not.
		return nil, fmt.Errorf("%w: give it an interval in days, in meter units, or both", ErrInvalid)
	}
	if !hasCalendar {
		sch.EveryDays = nil
	}
	if !hasMeter {
		sch.MeterKey, sch.EveryUnits = nil, nil
	}
	if sch.LeadDays < 0 || sch.LeadDays > 365 {
		sch.LeadDays = 7
	}
	if sch.NextDueAt.IsZero() {
		// First one is due after a full interval from now, so creating a
		// schedule does not immediately raise a job for work that was done
		// last week.
		sch.NextDueAt = time.Now()
		if hasCalendar {
			sch.NextDueAt = sch.NextDueAt.AddDate(0, 0, *sch.EveryDays)
		}
	}

	if sch.ID == "" {
		sch.ID = uuid.NewString()
		_, err := s.db.Exec(ctx, `
			INSERT INTO pm_schedules (id, org_id, asset_id, template_id, name,
			                          every_days, meter_key, every_units, lead_days,
			                          next_due_at, enabled, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			sch.ID, orgID, sch.AssetID, sch.TemplateID, sch.Name,
			sch.EveryDays, sch.MeterKey, sch.EveryUnits, sch.LeadDays,
			sch.NextDueAt, true, actorID)
		if err != nil {
			return nil, fmt.Errorf("maint: create schedule: %w", err)
		}
		sch.OrgID, sch.Enabled = orgID, true
		return &sch, nil
	}

	ct, err := s.db.Exec(ctx, `
		UPDATE pm_schedules
		   SET template_id=$3, name=$4, every_days=$5, meter_key=$6, every_units=$7,
		       lead_days=$8, next_due_at=$9, enabled=$10, updated_at=now()
		 WHERE id=$1 AND org_id=$2`,
		sch.ID, orgID, sch.TemplateID, sch.Name, sch.EveryDays, sch.MeterKey,
		sch.EveryUnits, sch.LeadDays, sch.NextDueAt, sch.Enabled)
	if err != nil {
		return nil, fmt.Errorf("maint: update schedule: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return &sch, nil
}

const scheduleSelect = `
	SELECT s.id::text, s.org_id::text, s.asset_id::text,
	       COALESCE(NULLIF(a.title,''), 'Untitled'),
	       s.template_id::text, s.name, s.every_days, s.meter_key, s.every_units,
	       s.lead_days, s.next_due_at, s.last_generated_at, s.last_generated_units,
	       s.enabled, s.created_by::text
	FROM pm_schedules s
	JOIN documents a ON a.id = s.asset_id AND a.deleted_at IS NULL`

func scanSchedules(rows pgx.Rows) ([]*Schedule, error) {
	var out []*Schedule
	for rows.Next() {
		sch := &Schedule{}
		if err := rows.Scan(&sch.ID, &sch.OrgID, &sch.AssetID, &sch.AssetTitle,
			&sch.TemplateID, &sch.Name, &sch.EveryDays, &sch.MeterKey, &sch.EveryUnits,
			&sch.LeadDays, &sch.NextDueAt, &sch.LastGeneratedAt, &sch.LastGeneratedUnits,
			&sch.Enabled, &sch.CreatedBy); err != nil {
			return nil, fmt.Errorf("maint: scan schedule: %w", err)
		}
		out = append(out, sch)
	}
	return out, rows.Err()
}

// visibleAsset restricts a schedule query to assets the caller can see.
//
// A schedule inherits its asset's visibility rather than carrying grants of its
// own. Giving it a second, independent answer to "who can see this" is how a
// machine somebody was deliberately walled off from turns up on a list because
// of the service interval attached to it.
const visibleAsset = ` AND ($2::text[] @> ARRAY['*super'] OR EXISTS (
	SELECT 1 FROM grants g
	 WHERE g.entity_type = 'document'
	   AND g.entity_id = s.asset_id
	   AND g.subject_key = ANY($2)
	   AND (g.expires_at IS NULL OR g.expires_at > now())))`

// load reads one schedule with no permission check, for the generator, which
// acts on behalf of the schedule rather than on behalf of anybody.
func (s *Service) load(ctx context.Context, orgID, id string) (*Schedule, error) {
	rows, err := s.db.Query(ctx, scheduleSelect+` WHERE s.id=$1 AND s.org_id=$2`, id, orgID)
	if err != nil {
		return nil, fmt.Errorf("maint: get schedule: %w", err)
	}
	list, err := scanSchedules(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	if err := s.enrich(ctx, list); err != nil {
		return nil, err
	}
	return list[0], nil
}

// Get returns one schedule with its open occurrence and meter projection.
func (s *Service) Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*Schedule, error) {
	sch, err := s.load(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if err := s.canView(ctx, actor, sch.AssetID); err != nil {
		return nil, err
	}
	return sch, nil
}

// ForAsset lists an asset's schedules.
func (s *Service) ForAsset(ctx context.Context, orgID string, actor core.Subjects, assetID string) ([]*Schedule, error) {
	if err := s.canView(ctx, actor, assetID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx,
		scheduleSelect+` WHERE s.asset_id=$1 AND s.org_id=$2 ORDER BY s.next_due_at`,
		assetID, orgID)
	if err != nil {
		return nil, fmt.Errorf("maint: schedules for asset: %w", err)
	}
	list, err := scanSchedules(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	return list, s.enrich(ctx, list)
}

// List returns the schedules in an org the caller can see, soonest first.
func (s *Service) List(ctx context.Context, orgID string, actor core.Subjects) ([]*Schedule, error) {
	rows, err := s.db.Query(ctx,
		scheduleSelect+` WHERE s.org_id=$1`+visibleAsset+
			` ORDER BY s.enabled DESC, s.next_due_at`, orgID, actor.GrantKeys())
	if err != nil {
		return nil, fmt.Errorf("maint: list schedules: %w", err)
	}
	list, err := scanSchedules(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	return list, s.enrich(ctx, list)
}

// enrich attaches the open occurrence and the meter projection to each
// schedule, in two queries rather than two per row.
func (s *Service) enrich(ctx context.Context, list []*Schedule) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	byID := make(map[string]*Schedule, len(list))
	for i, sch := range list {
		ids[i] = sch.ID
		byID[sch.ID] = sch
	}

	rows, err := s.db.Query(ctx, `
		SELECT o.id::text, o.schedule_id::text, o.due_at, o.work_order_id::text,
		       COALESCE(NULLIF(d.title,''), ''), o.generated_at, o.completed_at, o.reason
		FROM pm_occurrences o
		LEFT JOIN documents d ON d.id = o.work_order_id
		WHERE o.schedule_id = ANY($1::uuid[]) AND o.completed_at IS NULL`, ids)
	if err != nil {
		return fmt.Errorf("maint: open occurrences: %w", err)
	}
	for rows.Next() {
		o := &Occurrence{}
		if err := rows.Scan(&o.ID, &o.ScheduleID, &o.DueAt, &o.WorkOrderID,
			&o.WorkOrder, &o.GeneratedAt, &o.CompletedAt, &o.Reason); err != nil {
			rows.Close()
			return fmt.Errorf("maint: scan occurrence: %w", err)
		}
		if sch, ok := byID[o.ScheduleID]; ok {
			sch.Open = o
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, sch := range list {
		if sch.MeterKey == nil || sch.EveryUnits == nil {
			continue
		}
		due, err := s.projectMeterDue(ctx, sch)
		if err != nil {
			return err
		}
		sch.ProjectedMeterDue = due
	}
	return nil
}

// mayChange checks the caller can edit the asset a schedule hangs off.
func (s *Service) mayChange(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	var assetID string
	err := s.db.QueryRow(ctx,
		`SELECT asset_id::text FROM pm_schedules WHERE id=$1 AND org_id=$2`, id, orgID).
		Scan(&assetID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("maint: read schedule asset: %w", err)
	}
	if err := s.canView(ctx, actor, assetID); err != nil {
		return err
	}
	return s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: assetID}, core.AccessEdit)
}

// SetEnabled turns a schedule on or off.
func (s *Service) SetEnabled(ctx context.Context, orgID string, actor core.Subjects, id string, on bool) error {
	if err := s.mayChange(ctx, orgID, actor, id); err != nil {
		return err
	}
	ct, err := s.db.Exec(ctx,
		`UPDATE pm_schedules SET enabled=$3, updated_at=now() WHERE id=$1 AND org_id=$2`,
		id, orgID, on)
	if err != nil {
		return fmt.Errorf("maint: set enabled: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a schedule. Its occurrences go with it; the work orders they
// produced stay, because they are a record of work that happened.
func (s *Service) Delete(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	if err := s.mayChange(ctx, orgID, actor, id); err != nil {
		return err
	}
	ct, err := s.db.Exec(ctx, `DELETE FROM pm_schedules WHERE id=$1 AND org_id=$2`, id, orgID)
	if err != nil {
		return fmt.Errorf("maint: delete schedule: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
