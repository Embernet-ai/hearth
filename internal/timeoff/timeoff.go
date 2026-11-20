// Package timeoff is requesting days away and deciding on them.
//
// Who decides is the same rule that decides a person's hours (timelog's
// approverClause): their named approver, a manager of their department, a
// super admin -- plus anybody holding the People capability, which is how HR
// sees every request. Never the person themselves.
//
// A request is private to the requester, whoever may decide it, and People:
// leave reasons are often medical. Approval puts an all-day "Out" on the
// person's department calendar (their own if they have none) so the team knows
// they are away -- the dates and a name, never the reason.
package timeoff

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("timeoff: no such request")
	ErrDenied   = errors.New("timeoff: you cannot do that")
	ErrInvalid  = errors.New("timeoff: invalid request")
)

// Kinds, in the order the form offers them -- the same list as HR's paper form.
var Kinds = []Kind{
	{"vacation", "Vacation leave"}, {"sick", "Sick leave"}, {"personal", "Personal leave"},
	{"medical", "Medical leave"}, {"family", "Family leave"}, {"bereavement", "Bereavement leave"},
	{"jury_duty", "Jury duty leave"}, {"other", "Other"},
}

type Kind struct{ Key, Label string }

// KindLabel is what a kind key is called on a page.
func KindLabel(key string) string {
	for _, k := range Kinds {
		if k.Key == key {
			return k.Label
		}
	}
	return key
}

// Request is one ask for time off.
type Request struct {
	ID            string
	RequesterID   string
	RequesterName string
	StartsOn      time.Time
	EndsOn        time.Time
	Days          int
	Kind          string
	Reason        string
	Comments      string
	Status        string
	DeciderName   string
	DecidedAt     *time.Time
	DecisionNote  string
	CreatedAt     time.Time
}

// Params is what the form sends.
type Params struct {
	StartsOn, EndsOn time.Time
	Kind             string
	Reason, Comments string
	// Acknowledged is "I understand this request is subject to approval".
	Acknowledged bool
}

// WorkingDays counts Monday to Friday between two dates, both included.
func WorkingDays(from, to time.Time) int {
	n := 0
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			n++
		}
	}
	return n
}

// Notifier is how an approver hears about a request and a requester about the
// answer. Optional; nil sends nothing.
type Notifier interface {
	TimeOffRequested(ctx context.Context, orgID string, r *Request, approverIDs []string)
	TimeOffDecided(ctx context.Context, orgID, deciderID string, r *Request)
}

type Service struct {
	db     *pgxpool.Pool
	notify Notifier
}

func New(db *pgxpool.Pool, n Notifier) *Service { return &Service{db: db, notify: n} }

// mayDecide is timelog's approver rule, plus People. $1 org, $2 actor, and
// `p` the requester's principal row.
const mayDecide = `(
	EXISTS (SELECT 1 FROM capability_grants g
	         WHERE g.org_id = $1 AND g.principal_id = $2
	           AND g.capability IN ('super_admin', 'people'))
	OR COALESCE(p.approver_id = $2, false)
	OR EXISTS (SELECT 1
	             FROM principal_departments pd
	             JOIN space_managers m ON m.space_id = pd.space_id
	            WHERE pd.principal_id = p.id AND m.principal_id = $2)
)`

// Ask files a request from the actor.
func (s *Service) Ask(ctx context.Context, orgID, actorID string, p Params) (*Request, error) {
	if !p.Acknowledged {
		return nil, fmt.Errorf("%w: please confirm that the request is subject to approval", ErrInvalid)
	}
	if p.StartsOn.IsZero() || p.EndsOn.IsZero() {
		return nil, fmt.Errorf("%w: a start and an end date are needed", ErrInvalid)
	}
	if p.EndsOn.Before(p.StartsOn) {
		return nil, fmt.Errorf("%w: the end date is before the start date", ErrInvalid)
	}
	if p.EndsOn.Sub(p.StartsOn) > 366*24*time.Hour {
		return nil, fmt.Errorf("%w: that is more than a year", ErrInvalid)
	}
	if KindLabel(p.Kind) == p.Kind {
		return nil, fmt.Errorf("%w: choose a type of leave", ErrInvalid)
	}
	days := WorkingDays(p.StartsOn, p.EndsOn)
	if days == 0 {
		return nil, fmt.Errorf("%w: those dates are all weekend", ErrInvalid)
	}

	r := &Request{
		ID: uuid.NewString(), RequesterID: actorID, StartsOn: p.StartsOn, EndsOn: p.EndsOn,
		Days: days, Kind: p.Kind, Reason: strings.TrimSpace(p.Reason),
		Comments: strings.TrimSpace(p.Comments), Status: "pending",
	}
	err := s.db.QueryRow(ctx, `
		INSERT INTO time_off_requests (id, org_id, requester_id, starts_on, ends_on, days, kind, reason, comments)
		SELECT $1, $2, p.id, $4, $5, $6, $7, $8, $9 FROM principals p WHERE p.id = $3 AND p.org_id = $2
		RETURNING created_at, (SELECT display_name FROM principals WHERE id = $3)`,
		r.ID, orgID, actorID, r.StartsOn, r.EndsOn, r.Days, r.Kind, r.Reason, r.Comments).
		Scan(&r.CreatedAt, &r.RequesterName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, fmt.Errorf("timeoff: ask: %w", err)
	}
	if s.notify != nil {
		if ids, err := s.deciders(ctx, orgID, actorID); err == nil {
			s.notify.TimeOffRequested(ctx, orgID, r, ids)
		}
	}
	return r, nil
}

// deciders is who should hear about a new request: the named approver and
// department managers. Not everybody with People -- HR sees the queue on the
// page; an inbox entry for every request in the company would be noise.
func (s *Service) deciders(ctx context.Context, orgID, requesterID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT approver_id::text FROM principals WHERE id = $2 AND org_id = $1 AND approver_id IS NOT NULL
		UNION
		SELECT m.principal_id::text FROM principal_departments pd
		  JOIN space_managers m ON m.space_id = pd.space_id
		 WHERE pd.principal_id = $2 AND pd.org_id = $1`, orgID, requesterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != requesterID {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

const columns = `
	r.id::text, r.requester_id::text, p.display_name, r.starts_on, r.ends_on, r.days, r.kind,
	r.reason, r.comments, r.status, COALESCE(dp.display_name, ''), r.decided_at, r.decision_note, r.created_at
	FROM time_off_requests r
	JOIN principals p ON p.id = r.requester_id
	LEFT JOIN principals dp ON dp.id = r.decided_by`

func scan(rows pgx.Rows) ([]*Request, error) {
	defer rows.Close()
	var out []*Request
	for rows.Next() {
		r := &Request{}
		if err := rows.Scan(&r.ID, &r.RequesterID, &r.RequesterName, &r.StartsOn, &r.EndsOn, &r.Days,
			&r.Kind, &r.Reason, &r.Comments, &r.Status, &r.DeciderName, &r.DecidedAt,
			&r.DecisionNote, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Mine is the actor's own requests, newest first.
func (s *Service) Mine(ctx context.Context, orgID, actorID string) ([]*Request, error) {
	rows, err := s.db.Query(ctx, `SELECT `+columns+`
		WHERE r.org_id = $1 AND r.requester_id = $2
		ORDER BY r.starts_on DESC LIMIT 100`, orgID, actorID)
	if err != nil {
		return nil, fmt.Errorf("timeoff: mine: %w", err)
	}
	return scan(rows)
}

// ToDecide is everybody else's requests the actor may decide: pending first
// (oldest first, the one waiting longest), then the recent decisions.
func (s *Service) ToDecide(ctx context.Context, orgID, actorID string) ([]*Request, error) {
	rows, err := s.db.Query(ctx, `SELECT `+columns+`
		WHERE r.org_id = $1 AND r.requester_id <> $2 AND `+mayDecide+`
		  AND (r.status = 'pending' OR r.decided_at > now() - interval '30 days')
		ORDER BY (r.status = 'pending') DESC, r.starts_on
		LIMIT 200`, orgID, actorID)
	if err != nil {
		return nil, fmt.Errorf("timeoff: to decide: %w", err)
	}
	return scan(rows)
}

// PendingCount is how many requests are waiting on the actor, for the badge.
func (s *Service) PendingCount(ctx context.Context, orgID, actorID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM time_off_requests r JOIN principals p ON p.id = r.requester_id
		 WHERE r.org_id = $1 AND r.status = 'pending' AND r.requester_id <> $2 AND `+mayDecide,
		orgID, actorID).Scan(&n)
	return n, err
}

// Decide approves or denies a pending request. The authority check is inside
// the UPDATE, as in timelog.Decide: there is no window between deciding
// somebody may act and acting, and no way to reach the write without it.
// Approval also puts the "Out" on the calendar, in the same transaction.
func (s *Service) Decide(ctx context.Context, orgID, actorID, id string, approve bool, note string) error {
	status := "denied"
	if approve {
		status = "approved"
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("timeoff: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	r := &Request{}
	err = tx.QueryRow(ctx, `
		UPDATE time_off_requests r
		   SET status = $4, decided_by = $2, decided_at = now(), decision_note = $5, updated_at = now()
		  FROM principals p
		 WHERE r.id = $3 AND r.org_id = $1 AND r.status = 'pending'
		   AND p.id = r.requester_id AND r.requester_id <> $2 AND `+mayDecide+`
		RETURNING r.id::text, r.requester_id::text, p.display_name, r.starts_on, r.ends_on, r.days, r.kind, r.status`,
		orgID, actorID, id, status, strings.TrimSpace(note)).
		Scan(&r.ID, &r.RequesterID, &r.RequesterName, &r.StartsOn, &r.EndsOn, &r.Days, &r.Kind, &r.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return fmt.Errorf("timeoff: decide: %w", err)
	}

	if approve {
		// The team's view: a name and dates, no reason. On the requester's
		// department if they have one, so their colleagues see it.
		eventID := uuid.NewString()
		if _, err := tx.Exec(ctx, `
			INSERT INTO calendar_events (id, org_id, principal_id, space_id, title, starts_at, ends_at, all_day, created_by)
			SELECT $1, $2,
			       CASE WHEN d.space_id IS NULL THEN $3::uuid END,
			       d.space_id,
			       'Out: ' || p.display_name, $4::date::timestamptz, ($5::date + 1)::timestamptz, true, $6
			  FROM principals p
			  LEFT JOIN LATERAL (SELECT space_id FROM principal_departments
			                      WHERE principal_id = p.id AND org_id = $2 LIMIT 1) d ON true
			 WHERE p.id = $3`,
			eventID, orgID, r.RequesterID, r.StartsOn, r.EndsOn, actorID); err != nil {
			return fmt.Errorf("timeoff: calendar: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE time_off_requests SET calendar_event_id = $2 WHERE id = $1`, r.ID, eventID); err != nil {
			return fmt.Errorf("timeoff: link calendar: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("timeoff: commit: %w", err)
	}
	if s.notify != nil {
		s.notify.TimeOffDecided(ctx, orgID, actorID, r)
	}
	return nil
}

// Cancel withdraws the actor's own request -- pending, or approved and not yet
// started -- and takes its "Out" off the calendar.
func (s *Service) Cancel(ctx context.Context, orgID, actorID, id string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("timeoff: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var event *string
	err = tx.QueryRow(ctx, `
		UPDATE time_off_requests SET status = 'cancelled', updated_at = now()
		 WHERE id = $3 AND org_id = $1 AND requester_id = $2
		   AND (status = 'pending' OR (status = 'approved' AND starts_on > current_date))
		RETURNING calendar_event_id::text`, orgID, actorID, id).Scan(&event)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return fmt.Errorf("timeoff: cancel: %w", err)
	}
	if event != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM calendar_events WHERE id = $1 AND org_id = $2`, *event, orgID); err != nil {
			return fmt.Errorf("timeoff: calendar: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// Decidable reports which of these requesters' requests the actor may decide,
// by the same rule Decide applies in SQL. For a back end that keeps the
// requests elsewhere but still takes approvals from Hearth's own approver and
// department structure.
func (s *Service) Decidable(ctx context.Context, orgID, actorID string, requesterIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(requesterIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text FROM principals p
		 WHERE p.org_id = $1 AND p.id = ANY($3::uuid[]) AND p.id <> $2 AND `+mayDecide,
		orgID, actorID, requesterIDs)
	if err != nil {
		return nil, fmt.Errorf("timeoff: decidable: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// Deciders is who should hear about a new request from this person.
func (s *Service) Deciders(ctx context.Context, orgID, requesterID string) ([]string, error) {
	return s.deciders(ctx, orgID, requesterID)
}

// DepartmentSpace is the first department a person belongs to, or "".
func (s *Service) DepartmentSpace(ctx context.Context, orgID, principalID string) (string, error) {
	var id *string
	err := s.db.QueryRow(ctx, `SELECT space_id::text FROM principal_departments
		WHERE principal_id = $2 AND org_id = $1 LIMIT 1`, orgID, principalID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) || id == nil {
		return "", nil
	}
	return *id, err
}
