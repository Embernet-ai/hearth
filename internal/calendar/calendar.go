// Package calendar answers "what is happening, and when", from the things
// Hearth already knows plus the events that are only events.
//
// The important decision here is what is NOT stored. Work has a due date,
// maintenance has a schedule, and hours were logged on a day. None of that is
// copied into calendar_events, because a calendar that keeps its own copy of a
// due date is a calendar that disagrees with the work order by Thursday. They
// are read from where they live, on the way past.
//
// So this package owns one table and three queries against other people's.
package calendar

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrNotFound covers both absent and not-yours, deliberately. Telling
	// somebody an event exists but is not theirs is telling them about a
	// meeting they are not in.
	ErrNotFound = errors.New("calendar: no such event")
	// ErrNoFeed is returned when a token matches nothing.
	ErrNoFeed = errors.New("calendar: that feed does not exist")
)

// Kind is what an entry on the calendar actually is, so the page can style it
// and the feed can say so.
type Kind string

const (
	// KindEvent is a real event: a meeting, a shift, somebody's leave.
	KindEvent Kind = "event"
	// KindDue is a piece of work due that day, read from task_fields.
	KindDue Kind = "due"
	// KindMaintenance is a scheduled job, read from the maintenance schedules.
	KindMaintenance Kind = "maintenance"
	// KindGoogle came from somebody's connected Google calendar. Read only:
	// nothing here is ever written back, and it is marked so the page can say
	// where it came from rather than implying Hearth owns it.
	KindGoogle Kind = "google"
)

// Entry is one thing on the calendar, whatever it came from.
type Entry struct {
	ID    string
	Kind  Kind
	Title string
	Notes string
	Place string

	StartsAt time.Time
	EndsAt   time.Time
	AllDay   bool

	// DocumentID is set when this came from a piece of work, so the page can
	// link to it and the feed can carry a URL.
	DocumentID string
	// OwnerName is whose or which department's, for a shared view.
	OwnerName string
	// Link is an address somewhere else, for an entry Hearth does not own.
	Link string
}

// Service reads and writes the calendar.
type Service struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

// Params is a new or edited event.
type Params struct {
	Title    string
	Notes    string
	Place    string
	StartsAt time.Time
	EndsAt   time.Time
	AllDay   bool
	// SpaceID makes this a department's event. Empty makes it the actor's own.
	SpaceID string
}

func (p Params) validate() error {
	if strings.TrimSpace(p.Title) == "" {
		return errors.New("calendar: an event needs a title")
	}
	if p.StartsAt.IsZero() {
		return errors.New("calendar: an event needs a start")
	}
	if p.EndsAt.Before(p.StartsAt) {
		return errors.New("calendar: it cannot finish before it starts")
	}
	return nil
}

// Create adds an event, owned by the actor or by a department.
func (s *Service) Create(ctx context.Context, orgID, actorID string, p Params) (*Entry, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	end := p.EndsAt
	if end.IsZero() {
		// An event with no end is an hour. Better than storing an instant and
		// leaving every reader to invent a duration.
		end = p.StartsAt.Add(time.Hour)
	}

	e := &Entry{
		ID: uuid.NewString(), Kind: KindEvent,
		Title: strings.TrimSpace(p.Title), Notes: strings.TrimSpace(p.Notes),
		Place:    strings.TrimSpace(p.Place),
		StartsAt: p.StartsAt, EndsAt: end, AllDay: p.AllDay,
	}

	// The space is looked up scoped to the org rather than trusted, so an event
	// cannot be filed onto another tenant's department.
	_, err := s.db.Exec(ctx, `
		INSERT INTO calendar_events
		    (id, org_id, principal_id, space_id, title, notes, location,
		     starts_at, ends_at, all_day, created_by)
		SELECT $1, $2,
		       CASE WHEN $3 = '' THEN $4::uuid ELSE NULL END,
		       (SELECT id FROM spaces WHERE org_id = $2 AND id = NULLIF($3,'')::uuid),
		       $5, $6, $7, $8, $9, $10, $4::uuid`,
		e.ID, orgID, p.SpaceID, actorID, e.Title, e.Notes, e.Place,
		e.StartsAt, e.EndsAt, e.AllDay)
	if err != nil {
		return nil, fmt.Errorf("calendar: create: %w", err)
	}
	return e, nil
}

// Delete removes an event the actor owns, or one on a department they manage.
func (s *Service) Delete(ctx context.Context, orgID, actorID, id string) error {
	tag, err := s.db.Exec(ctx, `
		DELETE FROM calendar_events e
		 WHERE e.id = $3 AND e.org_id = $1
		   AND (
		     e.principal_id = $2
		     OR EXISTS (SELECT 1 FROM space_managers m
		                 WHERE m.space_id = e.space_id AND m.principal_id = $2)
		     OR EXISTS (SELECT 1 FROM capability_grants g
		                 WHERE g.org_id = $1 AND g.principal_id = $2
		                   AND g.capability = 'super_admin')
		   )`, orgID, actorID, id)
	if err != nil {
		return fmt.Errorf("calendar: delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Between returns everything one person should see in a window: their own
// events, their departments' events, and the work assigned to them that falls
// due in it.
//
// One method rather than three, because a calendar assembled by the page from
// three calls is a page that renders two thirds of a calendar when one of them
// fails.
func (s *Service) Between(ctx context.Context, orgID, principalID string,
	from, to time.Time) ([]Entry, error) {

	if to.Before(from) {
		from, to = to, from
	}

	// Events: their own, plus any department they are in or manage.
	rows, err := s.db.Query(ctx, `
		SELECT e.id::text, e.title, e.notes, e.location,
		       e.starts_at, e.ends_at, e.all_day,
		       COALESCE(NULLIF(sp.name,''), '')
		  FROM calendar_events e
		  LEFT JOIN spaces sp ON sp.id = e.space_id
		 WHERE e.org_id = $1
		   AND e.starts_at < $3 AND e.ends_at >= $2
		   AND (
		     e.principal_id = $4
		     OR EXISTS (SELECT 1 FROM principal_departments pd
		                 WHERE pd.principal_id = $4 AND pd.space_id = e.space_id)
		     OR EXISTS (SELECT 1 FROM space_managers m
		                 WHERE m.space_id = e.space_id AND m.principal_id = $4)
		   )
		 ORDER BY e.starts_at`, orgID, from, to, principalID)
	if err != nil {
		return nil, fmt.Errorf("calendar: read events: %w", err)
	}
	var out []Entry
	for rows.Next() {
		var e Entry
		e.Kind = KindEvent
		if err := rows.Scan(&e.ID, &e.Title, &e.Notes, &e.Place,
			&e.StartsAt, &e.EndsAt, &e.AllDay, &e.OwnerName); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Work due, read from where it lives rather than copied here. Assigned to
	// them, not deleted, not finished: a calendar full of jobs somebody already
	// closed is a calendar people stop opening.
	due, err := s.db.Query(ctx, `
		SELECT d.id::text, COALESCE(NULLIF(d.title,''),'Untitled'), t.due_at
		  FROM task_fields t
		  JOIN documents d ON d.id = t.document_id
		 WHERE d.org_id = $1 AND d.deleted_at IS NULL
		   AND t.assignee_id = $4
		   AND t.status NOT IN ('done','cancelled')
		   AND t.due_at >= $2 AND t.due_at < $3
		 ORDER BY t.due_at`, orgID, from, to, principalID)
	if err != nil {
		return nil, fmt.Errorf("calendar: read due work: %w", err)
	}
	defer due.Close()
	for due.Next() {
		var e Entry
		e.Kind = KindDue
		e.AllDay = true
		if err := due.Scan(&e.DocumentID, &e.Title, &e.StartsAt); err != nil {
			return nil, err
		}
		e.ID = "due-" + e.DocumentID
		e.EndsAt = e.StartsAt
		out = append(out, e)
	}
	if err := due.Err(); err != nil {
		return nil, err
	}

	// Whatever was pulled from Google, if this person has connected.
	//
	// Read from the cache rather than called for on every render: a calendar
	// page that makes an API call per load is slow when Google is slow and
	// blank when Google is down. The table is a cache with an owner, and if it
	// does not exist yet this simply returns nothing.
	goog, err := s.db.Query(ctx, `
		SELECT google_id, title, location, starts_at, ends_at, all_day, html_link
		  FROM google_events
		 WHERE principal_id = $1 AND starts_at < $3 AND ends_at >= $2
		 ORDER BY starts_at`, principalID, from, to)
	if err != nil {
		// Not fatal. Somebody's own events and their due work are still worth
		// showing when the connected calendar cannot be read.
		return out, nil
	}
	defer goog.Close()
	for goog.Next() {
		var e Entry
		var link string
		e.Kind = KindGoogle
		if err := goog.Scan(&e.ID, &e.Title, &e.Place,
			&e.StartsAt, &e.EndsAt, &e.AllDay, &link); err != nil {
			return out, nil
		}
		e.ID = "google-" + e.ID
		e.Link = link
		out = append(out, e)
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// The subscribable feed
// -----------------------------------------------------------------------------

// hashToken is how a token is stored and looked up. SHA-256 and no salt on
// purpose: the token is 32 random bytes from crypto/rand, so there is no
// dictionary to attack and a per-row salt would only stop the single lookup
// this table exists to do.
func hashToken(tok string) []byte {
	sum := sha256.Sum256([]byte(tok))
	return sum[:]
}

// IssueFeed makes or replaces somebody's feed token and returns it ONCE.
//
// Only the hash is kept, so this is the only moment the token exists in a form
// anybody can copy. Calling it again rotates, which is what revoking a leaked
// URL looks like: the old one stops working immediately.
func (s *Service) IssueFeed(ctx context.Context, orgID, principalID string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("calendar: generate token: %w", err)
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)

	_, err := s.db.Exec(ctx, `
		INSERT INTO calendar_feeds (principal_id, org_id, token_hash)
		VALUES ($1,$2,$3)
		ON CONFLICT (principal_id) DO UPDATE
		   SET token_hash = EXCLUDED.token_hash, created_at = now(), last_read_at = NULL`,
		principalID, orgID, hashToken(tok))
	if err != nil {
		return "", fmt.Errorf("calendar: issue feed: %w", err)
	}
	return tok, nil
}

// HasFeed reports whether somebody has a feed, so the page can offer to rotate
// rather than to create. It cannot show the token, which is the point.
func (s *Service) HasFeed(ctx context.Context, principalID string) (bool, time.Time, error) {
	var created time.Time
	err := s.db.QueryRow(ctx,
		`SELECT created_at FROM calendar_feeds WHERE principal_id = $1`,
		principalID).Scan(&created)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, time.Time{}, nil
	case err != nil:
		return false, time.Time{}, fmt.Errorf("calendar: read feed: %w", err)
	}
	return true, created, nil
}

// RevokeFeed removes the feed entirely, so the URL stops resolving.
func (s *Service) RevokeFeed(ctx context.Context, principalID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM calendar_feeds WHERE principal_id = $1`, principalID)
	if err != nil {
		return fmt.Errorf("calendar: revoke feed: %w", err)
	}
	return nil
}

// Feed resolves a token to the person it belongs to and returns their entries.
//
// The window is fixed rather than taken from the request: a calendar
// application refetches this on its own timer and has no way to ask for a
// range, and an unbounded feed grows until it times out. A month back covers
// "what did I do", a year forward covers anything anybody schedules.
func (s *Service) Feed(ctx context.Context, token string, now time.Time) (string, []Entry, error) {
	orgID, principalID, name, err := s.FeedOwner(ctx, token)
	if err != nil {
		return "", nil, err
	}

	entries, err := s.Between(ctx, orgID, principalID,
		now.AddDate(0, -1, 0), now.AddDate(1, 0, 0))
	if err != nil {
		return "", nil, err
	}
	return name, entries, nil
}

// FeedOwner finds whose feed a token is and notes that it was read. It is the
// first half of Feed, for a back end that reads the events from elsewhere.
func (s *Service) FeedOwner(ctx context.Context, token string) (orgID, principalID, name string, err error) {
	err = s.db.QueryRow(ctx, `
		UPDATE calendar_feeds f
		   SET last_read_at = now()
		  FROM principals p
		 WHERE f.token_hash = $1 AND p.id = f.principal_id
		RETURNING f.org_id::text, f.principal_id::text,
		          COALESCE(NULLIF(p.display_name,''), 'Hearth')`,
		hashToken(token)).Scan(&orgID, &principalID, &name)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", "", "", ErrNoFeed
	case err != nil:
		return "", "", "", fmt.Errorf("calendar: feed: %w", err)
	}
	return orgID, principalID, name, nil
}

// SpacesOf is the departments a person is in or manages, whose events they see.
func (s *Service) SpacesOf(ctx context.Context, principalID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT space_id::text FROM principal_departments WHERE principal_id = $1
		UNION
		SELECT space_id::text FROM space_managers WHERE principal_id = $1`, principalID)
	if err != nil {
		return nil, fmt.Errorf("calendar: spaces of: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
