package timeoff

// Changing and cancelling somebody else's request.
//
// An approver, or whoever may decide the person's requests (the same rule as
// Decide: their named approver, a manager of their department, a super admin,
// People), may change a request that is pending or already approved, or cancel
// it. The person keeps their own Cancel for pending ones. Every change writes an
// audit row, in the same transaction as the change in Postgres, and the person is
// told.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/audit"
)

// Length is the days actually taken: the working days the dates span, less half a
// day when the first day is a half.
func (r *Request) Length() float64 {
	if r.HalfDay {
		return float64(r.Days) - 0.5
	}
	return float64(r.Days)
}

// Check validates what a request asks for, apart from the acknowledgement, and
// returns the working days the dates span.
func (p Params) Check() (int, error) {
	if p.StartsOn.IsZero() || p.EndsOn.IsZero() {
		return 0, fmt.Errorf("%w: a start and an end date are needed", ErrInvalid)
	}
	if p.EndsOn.Before(p.StartsOn) {
		return 0, fmt.Errorf("%w: the end date is before the start date", ErrInvalid)
	}
	if p.EndsOn.Sub(p.StartsOn) > 366*24*time.Hour {
		return 0, fmt.Errorf("%w: that is more than a year", ErrInvalid)
	}
	if KindLabel(p.Kind) == p.Kind {
		return 0, fmt.Errorf("%w: choose a type of leave", ErrInvalid)
	}
	days := WorkingDays(p.StartsOn, p.EndsOn)
	if days == 0 {
		return 0, fmt.Errorf("%w: those dates are all weekend", ErrInvalid)
	}
	if wd := p.StartsOn.Weekday(); p.HalfDay && (wd == time.Saturday || wd == time.Sunday) {
		return 0, fmt.Errorf("%w: a half day needs a working day to start on", ErrInvalid)
	}
	return days, nil
}

// Change is what an approver or admin changes. A nil field stays as it is.
type Change struct {
	StartsOn, EndsOn *time.Time
	Kind             *string
	HalfDay          *bool
	Reason, Comments *string
	// Note is why, kept in the audit row and sent to the person.
	Note string
}

// Apply lays the change over a request and checks the result. It returns the
// new request values and the working days, or ErrInvalid when nothing changes or
// the result is not a request.
func (c Change) Apply(r *Request) (Params, int, error) {
	p := Params{StartsOn: r.StartsOn, EndsOn: r.EndsOn, Kind: r.Kind, Reason: r.Reason, Comments: r.Comments,
		HalfDay: r.HalfDay, Acknowledged: true}
	if c.StartsOn != nil {
		p.StartsOn = *c.StartsOn
	}
	if c.EndsOn != nil {
		p.EndsOn = *c.EndsOn
	}
	if c.Kind != nil {
		p.Kind = *c.Kind
	}
	if c.HalfDay != nil {
		p.HalfDay = *c.HalfDay
	}
	if c.Reason != nil {
		p.Reason = strings.TrimSpace(*c.Reason)
	}
	if c.Comments != nil {
		p.Comments = strings.TrimSpace(*c.Comments)
	}
	days, err := p.Check()
	if err != nil {
		return p, 0, err
	}
	same := p.StartsOn.Equal(r.StartsOn) && p.EndsOn.Equal(r.EndsOn) && p.Kind == r.Kind &&
		p.HalfDay == r.HalfDay && p.Reason == r.Reason && p.Comments == r.Comments
	if same {
		return p, days, fmt.Errorf("%w: nothing is different from what is already there", ErrInvalid)
	}
	return p, days, nil
}

// CancelledNote is what a request says when somebody else cancelled it: who, and the
// reason they gave.
func CancelledNote(who, note string) string {
	out := "Cancelled"
	if who != "" {
		out += " by " + who
	}
	if note = strings.TrimSpace(note); note != "" {
		out += ": " + note
	}
	return out
}

// ChangedNote is what a request that was cancelled to be filed again with new dates
// says: who changed it, and why.
func ChangedNote(who, note string) string {
	out := "Replaced by a changed request"
	if who != "" {
		out = "Changed by " + who + " and filed again"
	}
	if note = strings.TrimSpace(note); note != "" {
		out += ": " + note
	}
	return out
}

// Changeable is whether a request is in a state somebody may change or cancel:
// pending, or approved. A denied or cancelled one is finished.
func Changeable(status string) bool { return status == "pending" || status == "approved" }

// Snapshot is what an audit row keeps of a request. The reason and comments are
// private to the person, so the row says only whether they changed.
type Snapshot struct {
	Person   string  `json:"person"`
	Status   string  `json:"status"`
	StartsOn string  `json:"starts_on"`
	EndsOn   string  `json:"ends_on"`
	Days     float64 `json:"days"`
	HalfDay  bool    `json:"half_day"`
	Kind     string  `json:"kind"`
	Note     string  `json:"note,omitempty"`
	// FiledAs is the id of the new request when a change was made by cancelling the
	// old one and filing another (EmberOps).
	FiledAs         string `json:"filed_as,omitempty"`
	ReasonChanged   bool   `json:"reason_changed,omitempty"`
	CommentsChanged bool   `json:"comments_changed,omitempty"`
}

// SnapshotOf is a request as an audit row keeps it.
func SnapshotOf(r *Request) Snapshot {
	return Snapshot{Person: r.RequesterName, Status: r.Status, StartsOn: r.StartsOn.Format("2006-01-02"),
		EndsOn: r.EndsOn.Format("2006-01-02"), Days: r.Length(), HalfDay: r.HalfDay, Kind: r.Kind}
}

// ChangeAudit is the audit entry for a change or a cancellation by somebody else.
func ChangeAudit(orgID, actorID, action string, before, after *Request, note string, c Change) audit.Entry {
	b, a := SnapshotOf(before), SnapshotOf(after)
	a.Note = strings.TrimSpace(note)
	if after.ID != before.ID {
		a.FiledAs = after.ID
	}
	a.ReasonChanged = c.Reason != nil && strings.TrimSpace(*c.Reason) != before.Reason
	a.CommentsChanged = c.Comments != nil && strings.TrimSpace(*c.Comments) != before.Comments
	return audit.Entry{OrgID: orgID, ActorID: actorID, Action: action, EntityType: "time_off_request",
		EntityID: before.ID, Before: b, After: a}
}

// Record writes an audit row on its own, for a back end that keeps the requests
// elsewhere (the row is still Hearth's, in Hearth's database).
func (s *Service) Record(ctx context.Context, e audit.Entry) error {
	return audit.Record(ctx, s.db, e)
}

// lockRow reads a request the actor may change, locked for the transaction.
// Nothing comes back for the requester themselves (their own Cancel is its own
// path), for somebody who may not decide it, or for one that is finished.
func lockRow(ctx context.Context, tx pgx.Tx, orgID, actorID, id string) (*Request, string, error) {
	r := &Request{}
	var actor string
	err := tx.QueryRow(ctx, `
		SELECT r.id::text, r.requester_id::text, p.display_name, r.starts_on, r.ends_on, r.days, r.kind,
		       r.reason, r.comments, r.status, r.half_day, r.calendar_event_id::text,
		       COALESCE((SELECT display_name FROM principals WHERE id = $2), '')
		  FROM time_off_requests r
		  JOIN principals p ON p.id = r.requester_id
		 WHERE r.id = $3 AND r.org_id = $1 AND r.requester_id <> $2
		   AND r.status IN ('pending', 'approved') AND `+mayDecide+`
		 FOR UPDATE OF r`, orgID, actorID, id).
		Scan(&r.ID, &r.RequesterID, &r.RequesterName, &r.StartsOn, &r.EndsOn, &r.Days, &r.Kind,
			&r.Reason, &r.Comments, &r.Status, &r.HalfDay, &r.CalendarEventID, &actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrDenied
	}
	if err != nil {
		return nil, "", fmt.Errorf("timeoff: read: %w", err)
	}
	return r, actor, nil
}

// Change changes a pending or approved request of somebody else's. On an
// approved one it also moves the "Out" on the calendar, and the person hears
// who changed it. The authority check is in the SELECT that locks the row.
func (s *Service) Change(ctx context.Context, orgID, actorID, id string, c Change) (*Request, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("timeoff: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	before, _, err := lockRow(ctx, tx, orgID, actorID, id)
	if err != nil {
		return nil, err
	}
	p, days, err := c.Apply(before)
	if err != nil {
		return nil, err
	}
	after := *before
	after.StartsOn, after.EndsOn, after.Days, after.Kind = p.StartsOn, p.EndsOn, days, p.Kind
	after.Reason, after.Comments, after.HalfDay = p.Reason, p.Comments, p.HalfDay
	if before.Status == "approved" {
		// A request decided again by whoever changed it, as it is in EmberOps.
		after.DecisionNote = strings.TrimSpace(c.Note)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE time_off_requests
		   SET starts_on = $2, ends_on = $3, days = $4, kind = $5, reason = $6, comments = $7, half_day = $8,
		       updated_at = now()
		 WHERE id = $1`, id, after.StartsOn, after.EndsOn, after.Days, after.Kind, after.Reason, after.Comments, after.HalfDay); err != nil {
		return nil, fmt.Errorf("timeoff: change: %w", err)
	}
	if before.Status == "approved" {
		if _, err := tx.Exec(ctx, `
			UPDATE time_off_requests SET decided_by = $2, decided_at = now(), decision_note = $3 WHERE id = $1`,
			id, actorID, after.DecisionNote); err != nil {
			return nil, fmt.Errorf("timeoff: decision: %w", err)
		}
		if before.CalendarEventID != nil {
			if _, err := tx.Exec(ctx, `
				UPDATE calendar_events SET starts_at = $3::date::timestamptz, ends_at = ($4::date + 1)::timestamptz
				 WHERE id = $1 AND org_id = $2`, *before.CalendarEventID, orgID, after.StartsOn, after.EndsOn); err != nil {
				return nil, fmt.Errorf("timeoff: calendar: %w", err)
			}
		}
	}
	if err := audit.Record(ctx, tx, ChangeAudit(orgID, actorID, audit.ActionTimeOffChange, before, &after, c.Note, c)); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("timeoff: commit: %w", err)
	}
	if s.notify != nil {
		s.notify.TimeOffChanged(ctx, orgID, actorID, &after, strings.TrimSpace(c.Note))
	}
	return s.Get(ctx, orgID, actorID, id)
}

// CancelFor cancels a pending or approved request of somebody else's and takes its
// "Out" off the calendar. The cancelling person and their note are kept as the
// request's last decision, and in the audit row, and the person is told.
func (s *Service) CancelFor(ctx context.Context, orgID, actorID, id, note string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("timeoff: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	before, actor, err := lockRow(ctx, tx, orgID, actorID, id)
	if err != nil {
		return err
	}
	note = strings.TrimSpace(note)
	said := CancelledNote(actor, note)
	if _, err := tx.Exec(ctx, `
		UPDATE time_off_requests
		   SET status = 'cancelled', decided_by = $2, decided_at = now(), decision_note = $3, updated_at = now()
		 WHERE id = $1`, id, actorID, said); err != nil {
		return fmt.Errorf("timeoff: cancel: %w", err)
	}
	if before.CalendarEventID != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM calendar_events WHERE id = $1 AND org_id = $2`, *before.CalendarEventID, orgID); err != nil {
			return fmt.Errorf("timeoff: calendar: %w", err)
		}
	}
	after := *before
	after.Status, after.DecisionNote = "cancelled", said
	if err := audit.Record(ctx, tx, ChangeAudit(orgID, actorID, audit.ActionTimeOffCancel, before, &after, note, Change{})); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("timeoff: commit: %w", err)
	}
	if s.notify != nil {
		s.notify.TimeOffChanged(ctx, orgID, actorID, &after, note)
	}
	return nil
}

// Get reads one request, for its requester or anybody who may decide it.
func (s *Service) Get(ctx context.Context, orgID, actorID, id string) (*Request, error) {
	rows, err := s.db.Query(ctx, `SELECT `+columns+`
		WHERE r.id = $3 AND r.org_id = $1 AND (r.requester_id = $2 OR `+mayDecide+`)`, orgID, actorID, id)
	if err != nil {
		return nil, fmt.Errorf("timeoff: get: %w", err)
	}
	out, err := scan(rows)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out[0], nil
}
