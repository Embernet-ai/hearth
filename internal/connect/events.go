package connect

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LocalEvent is a Hearth event as the calendar sync reads and writes it.
type LocalEvent = localEvent

// EventStore is where the calendar sync keeps a person's Hearth events. A standalone Hearth keeps them in its own
// calendar_events table (pgEvents). With the EmberOPS back end the events a person makes in Hearth live in EmberOPS, so
// that installation supplies a store that reads and writes both places (internal/emberops/stores.ConnectEvents).
type EventStore interface {
	// Load reads one event, or nil when there is none by that id.
	Load(ctx context.Context, orgID, id string) (*LocalEvent, error)
	// Candidates lists the ids of the events in a window that a sync may send to a provider: the ones the person made, or,
	// when space is set, everything in that department's calendar. The sync drops the ones already linked.
	Candidates(ctx context.Context, orgID, principalID, space string, from, to time.Time) ([]string, error)
	// Create makes an event that came from a provider and returns its id. It goes on the person's own calendar, or the
	// department's when space is set.
	Create(ctx context.Context, orgID, principalID, space string, e LocalEvent) (string, error)
	// Update changes an event to what the provider holds.
	Update(ctx context.Context, orgID, id string, e LocalEvent) error
	// Delete removes an event that is gone at the provider.
	Delete(ctx context.Context, orgID, id string) error
}

// pgEvents keeps events in Hearth's own calendar_events table.
type pgEvents struct{ db *pgxpool.Pool }

// NewPGEvents is the event store of a standalone Hearth.
func NewPGEvents(db *pgxpool.Pool) EventStore { return pgEvents{db: db} }

func (p pgEvents) Load(ctx context.Context, orgID, id string) (*LocalEvent, error) {
	e := &localEvent{ID: id}
	var space, person *string
	err := p.db.QueryRow(ctx, `
		SELECT title, notes, location, starts_at, ends_at, all_day, principal_id::text, space_id::text
		  FROM calendar_events WHERE org_id = $1 AND id = $2`, orgID, id).
		Scan(&e.Title, &e.Notes, &e.Place, &e.Start, &e.End, &e.AllDay, &person, &space)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("connect: read event: %w", err)
	}
	e.Start, e.End = e.Start.UTC(), e.End.UTC()
	if space != nil {
		e.Space = *space
	}
	if person != nil {
		e.Personal = *person
	}
	return e, nil
}

func (p pgEvents) Candidates(ctx context.Context, orgID, principalID, space string, from, to time.Time) ([]string, error) {
	var q string
	var args []any
	if space == "" {
		q = `SELECT e.id::text FROM calendar_events e
		      WHERE e.org_id=$1 AND e.principal_id=$2 AND e.created_by=$2
		        AND e.starts_at >= $3 AND e.starts_at < $4`
		args = []any{orgID, principalID, from, to}
	} else {
		q = `SELECT e.id::text FROM calendar_events e
		      WHERE e.org_id=$1 AND e.space_id=$2::uuid
		        AND e.starts_at >= $3 AND e.starts_at < $4`
		args = []any{orgID, space, from, to}
	}
	rows, err := p.db.Query(ctx, q, args...)
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
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (p pgEvents) Create(ctx context.Context, orgID, principalID, space string, e LocalEvent) (string, error) {
	id := uuid.NewString()
	var person, sp any
	if space == "" {
		person = principalID
	} else {
		sp = space
	}
	if _, err := p.db.Exec(ctx, `
		INSERT INTO calendar_events (id, org_id, principal_id, space_id, title, notes, location,
		                             starts_at, ends_at, all_day, created_by)
		VALUES ($1,$2,$3::uuid,$4::uuid,$5,$6,$7,$8,$9,$10,$11)`,
		id, orgID, person, sp, e.Title, e.Notes, e.Place, e.Start, e.End, e.AllDay, principalID); err != nil {
		return "", err
	}
	return id, nil
}

func (p pgEvents) Update(ctx context.Context, orgID, id string, e LocalEvent) error {
	_, err := p.db.Exec(ctx, `
		UPDATE calendar_events SET title=$3, notes=$4, location=$5, starts_at=$6, ends_at=$7,
		       all_day=$8, updated_at=now()
		 WHERE org_id=$1 AND id=$2`,
		orgID, id, e.Title, e.Notes, e.Place, e.Start, e.End, e.AllDay)
	return err
}

func (p pgEvents) Delete(ctx context.Context, orgID, id string) error {
	_, err := p.db.Exec(ctx, `DELETE FROM calendar_events WHERE org_id=$1 AND id=$2`, orgID, id)
	return err
}
