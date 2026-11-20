package jobs

// The transactional outbox.
//
// An event is written in the SAME transaction as the change it describes. That
// is the whole design, and it closes both directions of a failure that has no
// other fix:
//
//   - publish inside the request, then the commit fails: an event exists for a
//     change that never happened, and a notification describes a rollback.
//   - commit, then publish, and the publish fails: the change happened and
//     nothing downstream ever hears about it.
//
// Nothing outside a transaction can fix that, because a message broker cannot
// join a Postgres commit. So the event goes in the database, and a relay turns
// committed events into jobs afterwards.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event names. Kept as constants because a typo in a subscriber's filter is
// silence rather than an error, which is the hardest kind of bug to notice.
const (
	EventCreated   = "created"
	EventUpdated   = "updated"
	EventDeleted   = "deleted"
	EventStatus    = "status_changed"
	EventAssigned  = "assigned"
	EventCommented = "commented"
	EventLinked    = "linked"
)

// Event is one thing that happened.
type Event struct {
	ID          int64
	OrgID       string
	EntityType  string
	EntityID    string
	Event       string
	Payload     json.RawMessage
	ActorID     *string
	CreatedAt   time.Time
	PublishedAt *time.Time
}

// Decode unmarshals the payload into v.
func (e *Event) Decode(v any) error {
	if len(e.Payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("jobs: decode %s payload: %w", e.Event, err)
	}
	return nil
}

// EventSpec describes an event to record.
type EventSpec struct {
	OrgID      string
	EntityType string
	EntityID   string
	Event      string
	Payload    any
	ActorID    string
	// CauseRuleID and Depth record that an automation did this. Depth is what
	// stops two rules triggering each other forever: nothing acts on an event
	// above zero. On the row rather than in the payload, so "show me
	// everything a rule caused this morning" is one query.
	CauseRuleID string
	Depth       int
}

// Publish records an event inside the caller's transaction.
//
// It takes a pgx.Tx and not a pool, deliberately and with no convenience
// overload that takes a pool. Making the transaction impossible to omit is the
// only way this guarantee holds: a helper that quietly opened its own would
// reintroduce exactly the bug the table exists to prevent, and it would do it
// the first afternoon somebody was in a hurry.
func Publish(ctx context.Context, tx pgx.Tx, s EventSpec) error {
	payload := []byte("{}")
	if s.Payload != nil {
		var err error
		if payload, err = json.Marshal(s.Payload); err != nil {
			return fmt.Errorf("jobs: encode %s payload: %w", s.Event, err)
		}
	}
	var actor *string
	if s.ActorID != "" {
		actor = &s.ActorID
	}
	var cause *string
	if s.CauseRuleID != "" {
		cause = &s.CauseRuleID
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO outbox (org_id, entity_type, entity_id, event, payload, actor_id,
		                    cause_rule_id, depth)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		s.OrgID, s.EntityType, s.EntityID, s.Event, payload, actor, cause, s.Depth)
	if err != nil {
		return fmt.Errorf("jobs: publish %s on %s: %w", s.Event, s.EntityType, err)
	}
	return nil
}

// Route decides which job kinds an event turns into.
type Route struct {
	// Event is the event name to match, or "" for every event.
	Event string
	// EntityType narrows further, or "" for every kind of thing.
	EntityType string
	// JobKind is the work to enqueue.
	JobKind string
	// UniqueKey collapses duplicates. When set, it is combined with the
	// entity id, so four edits to one document while the indexer is behind
	// become one reindex rather than four.
	UniqueKey string
}

// Relay turns committed events into jobs.
//
// Reading the events and marking them published happen in ONE transaction with
// the inserts they produce, so an event becomes work exactly once even though
// the work itself runs at least once. Marking published first and enqueueing
// after would lose the work whenever the process died in between, and doing it
// the other way round would duplicate it.
type Relay struct {
	db     *pgxpool.Pool
	routes []Route
	log    *slog.Logger
	// batch bounds one pass. Large enough that a backlog drains quickly, small
	// enough that the transaction is short and does not hold rows against the
	// writers producing more of them.
	batch int
}

func NewRelay(db *pgxpool.Pool, log *slog.Logger) *Relay {
	return &Relay{db: db, log: log, batch: 200}
}

// Route registers a subscription. Registering the same job kind twice for one
// event means it is enqueued twice, so this is additive and callers should say
// what they want exactly once.
func (r *Relay) Route(routes ...Route) { r.routes = append(r.routes, routes...) }

// Routes reports what is subscribed, for the operator page.
func (r *Relay) Routes() []Route { return r.routes }

// Drain publishes one batch and returns how many events it handled.
//
// Returning the count is what lets the caller loop until empty rather than
// sleeping between batches while a backlog sits there.
func (r *Relay) Drain(ctx context.Context) (int, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("jobs: begin relay: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// SKIP LOCKED so two relays cannot both take the same event, even though
	// only one should be running. Correctness should not depend on an advisory
	// lock being held, because the moment it does, a lock that quietly fails to
	// be taken duplicates everything.
	rows, err := tx.Query(ctx, `
		SELECT id, org_id::text, entity_type, entity_id::text, event, payload,
		       actor_id::text, created_at
		FROM outbox
		WHERE published_at IS NULL
		ORDER BY id
		FOR UPDATE SKIP LOCKED
		LIMIT $1`, r.batch)
	if err != nil {
		return 0, fmt.Errorf("jobs: read outbox: %w", err)
	}

	var events []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.OrgID, &e.EntityType, &e.EntityID,
			&e.Event, &e.Payload, &e.ActorID, &e.CreatedAt); err != nil {
			rows.Close()
			return 0, fmt.Errorf("jobs: scan outbox: %w", err)
		}
		events = append(events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("jobs: iterate outbox: %w", err)
	}
	if len(events) == 0 {
		return 0, nil
	}

	ids := make([]int64, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
		for _, route := range r.routes {
			if route.Event != "" && route.Event != e.Event {
				continue
			}
			if route.EntityType != "" && route.EntityType != e.EntityType {
				continue
			}
			key := ""
			if route.UniqueKey != "" {
				key = route.UniqueKey + ":" + e.EntityID
			}
			if _, err := EnqueueTx(ctx, tx, Spec{
				OrgID:     e.OrgID,
				Kind:      route.JobKind,
				UniqueKey: key,
				Payload: map[string]any{
					"event_id":    e.ID,
					"event":       e.Event,
					"entity_type": e.EntityType,
					"entity_id":   e.EntityID,
					"actor_id":    e.ActorID,
					"payload":     e.Payload,
				},
			}); err != nil {
				return 0, fmt.Errorf("jobs: enqueue %s for event %d: %w", route.JobKind, e.ID, err)
			}
		}
	}

	if _, err := tx.Exec(ctx,
		`UPDATE outbox SET published_at = now() WHERE id = ANY($1)`, ids); err != nil {
		return 0, fmt.Errorf("jobs: mark published: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("jobs: commit relay: %w", err)
	}
	return len(events), nil
}

// EventPayload is what a routed job carries. Identifiers, not a copy of the
// row: a payload holding the document as it was is already stale by the time
// the job runs, and a handler that trusts it indexes yesterday's title.
type EventPayload struct {
	EventID    int64           `json:"event_id"`
	Event      string          `json:"event"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	ActorID    *string         `json:"actor_id"`
	Payload    json.RawMessage `json:"payload"`
}

// EventsFor returns the recorded history of one entity, newest first. This is
// the raw material for an activity feed and for the audit log later.
func EventsFor(ctx context.Context, db *pgxpool.Pool, entityType, entityID string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(ctx, `
		SELECT id, org_id::text, entity_type, entity_id::text, event, payload,
		       actor_id::text, created_at, published_at
		FROM outbox
		WHERE entity_type = $1 AND entity_id = $2
		ORDER BY id DESC LIMIT $3`, entityType, entityID, limit)
	if err != nil {
		return nil, fmt.Errorf("jobs: events for %s %s: %w", entityType, entityID, err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.OrgID, &e.EntityType, &e.EntityID, &e.Event,
			&e.Payload, &e.ActorID, &e.CreatedAt, &e.PublishedAt); err != nil {
			return nil, fmt.Errorf("jobs: scan event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
