// Package rules is "when this happens, do that".
//
// It is the feature people cite as the reason they stay on the tool they have,
// and it is the feature that goes wrong most memorably. Three things here exist
// because of how it goes wrong:
//
//   - Every run is recorded, including the ones that decided not to fire, with
//     the condition that failed. An automation nobody can debug gets switched
//     off, and the question is always "why did it not fire", which a table of
//     only the firings cannot answer.
//   - A rule fires once per event, enforced by a unique index rather than by
//     care. Delivery is at-least-once, so care is not enough.
//   - Nothing acts on a change another rule made. Rule A setting a field that
//     fires rule B setting it back is the failure mode of every rules engine
//     ever written, and a depth counter on the event stops it by construction.
package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

var (
	ErrNotFound = errors.New("rule not found")
	ErrInvalid  = errors.New("that rule will not do anything")
)

// Trigger says when a rule considers itself.
type Trigger struct {
	// Event is the outbox event name: created, status_changed, assigned,
	// updated, or message.
	Event string `json:"event"`
	// Kind narrows to one document kind, because "when a work order is
	// completed" and "when any document is" are rarely the same intention.
	Kind string `json:"kind,omitempty"`
}

// Condition is one test against the entity the event was about.
//
// The same Field and operator vocabulary as a view filter, deliberately.
// Somebody who has built a filtered list already knows how to write a rule, and
// there is one place where "status is done" is defined.
type Condition struct {
	Field  docs.Field `json:"field"`
	Op     string     `json:"op"`
	Values []string   `json:"values,omitempty"`
}

// Action is one thing to do.
type Action struct {
	// Kind is set_field, assign, comment, create_linked, notify, or webhook.
	Kind string `json:"kind"`
	// Field and Value drive set_field.
	Field docs.Field `json:"field,omitempty"`
	Value string     `json:"value,omitempty"`
	// Text drives comment and notify.
	Text string `json:"text,omitempty"`
	// PrincipalID drives assign and notify.
	PrincipalID string `json:"principal_id,omitempty"`
	// DocumentKind and Title drive create_linked.
	DocumentKind string `json:"document_kind,omitempty"`
	Title        string `json:"title,omitempty"`
	// URL and Secret drive webhook.
	URL    string `json:"url,omitempty"`
	Secret string `json:"secret,omitempty"`
}

// Action kinds.
const (
	ActionSetField     = "set_field"
	ActionAssign       = "assign"
	ActionComment      = "comment"
	ActionCreateLinked = "create_linked"
	ActionNotify       = "notify"
	ActionWebhook      = "webhook"
)

// Rule is one automation.
type Rule struct {
	ID         string
	OrgID      string
	SpaceID    *string
	Name       string
	Trigger    Trigger
	Conditions []Condition
	Actions    []Action
	Enabled    bool
	CreatedBy  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Run is one consideration of one event by one rule.
type Run struct {
	ID           int64
	RuleID       string
	OrgID        string
	RuleName     string
	EventID      int64
	EntityType   string
	EntityID     string
	Matched      bool
	Reason       string
	ActionsTaken []string
	Error        string
	DryRun       bool
	RanAt        time.Time
}

type Service struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

// Save creates or replaces a rule.
//
// Rules arrive disabled. A rule that starts running the moment it is saved
// gives nobody the chance to dry run it first, and the dry run is the entire
// reason anybody trusts one.
func (s *Service) Save(ctx context.Context, orgID, actorID string, r Rule) (*Rule, error) {
	r.Name = strings.TrimSpace(r.Name)
	if err := Validate(r); err != nil {
		return nil, err
	}

	trigger, _ := json.Marshal(r.Trigger)
	conditions, _ := json.Marshal(r.Conditions)
	actions, _ := json.Marshal(r.Actions)

	if r.ID == "" {
		r.ID = uuid.NewString()
		err := s.db.QueryRow(ctx, `
			INSERT INTO rules (id, org_id, space_id, name, trigger, conditions, actions,
			                   enabled, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			RETURNING created_at, updated_at`,
			r.ID, orgID, r.SpaceID, r.Name, trigger, conditions, actions, r.Enabled, actorID).
			Scan(&r.CreatedAt, &r.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("rules: create: %w", err)
		}
		r.OrgID, r.CreatedBy = orgID, actorID
		return &r, nil
	}

	ct, err := s.db.Exec(ctx, `
		UPDATE rules SET name=$3, trigger=$4, conditions=$5, actions=$6,
		                 space_id=$7, enabled=$8, updated_at=now()
		WHERE id=$1 AND org_id=$2`,
		r.ID, orgID, r.Name, trigger, conditions, actions, r.SpaceID, r.Enabled)
	if err != nil {
		return nil, fmt.Errorf("rules: update: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return &r, nil
}

// Validate is the rule's own sanity check, shared by every store that saves one.
// The name is trimmed by the caller first.
func Validate(r Rule) error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("%w: it needs a name", ErrInvalid)
	}
	if r.Trigger.Event == "" {
		return fmt.Errorf("%w: it needs something to happen first", ErrInvalid)
	}
	if len(r.Actions) == 0 {
		return fmt.Errorf("%w: it needs at least one action", ErrInvalid)
	}
	for _, a := range r.Actions {
		if err := validAction(a); err != nil {
			return err
		}
	}
	return nil
}

func validAction(a Action) error {
	switch a.Kind {
	case ActionSetField:
		if a.Field.Zero() {
			return fmt.Errorf("%w: set what?", ErrInvalid)
		}
	case ActionAssign:
		if a.PrincipalID == "" {
			return fmt.Errorf("%w: assign to whom?", ErrInvalid)
		}
	case ActionComment, ActionNotify:
		if strings.TrimSpace(a.Text) == "" {
			return fmt.Errorf("%w: say what?", ErrInvalid)
		}
	case ActionCreateLinked:
		if strings.TrimSpace(a.Title) == "" {
			return fmt.Errorf("%w: the new item needs a title", ErrInvalid)
		}
	case ActionWebhook:
		if !strings.HasPrefix(a.URL, "https://") && !strings.HasPrefix(a.URL, "http://") {
			return fmt.Errorf("%w: a webhook needs an http or https address", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: %q is not something a rule can do", ErrInvalid, a.Kind)
	}
	return nil
}

// SetEnabled turns a rule on or off.
func (s *Service) SetEnabled(ctx context.Context, orgID, id string, on bool) error {
	ct, err := s.db.Exec(ctx,
		`UPDATE rules SET enabled=$3, updated_at=now() WHERE id=$1 AND org_id=$2`, id, orgID, on)
	if err != nil {
		return fmt.Errorf("rules: set enabled: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, orgID, id string) error {
	ct, err := s.db.Exec(ctx, `DELETE FROM rules WHERE id=$1 AND org_id=$2`, id, orgID)
	if err != nil {
		return fmt.Errorf("rules: delete: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) Get(ctx context.Context, orgID, id string) (*Rule, error) {
	rows, err := s.db.Query(ctx, ruleSelect+` WHERE r.id=$1 AND r.org_id=$2`, id, orgID)
	if err != nil {
		return nil, fmt.Errorf("rules: get: %w", err)
	}
	defer rows.Close()
	list, err := scanRules(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return list[0], nil
}

// List returns every rule in an org, enabled first.
func (s *Service) List(ctx context.Context, orgID string) ([]*Rule, error) {
	rows, err := s.db.Query(ctx,
		ruleSelect+` WHERE r.org_id=$1 ORDER BY r.enabled DESC, r.name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("rules: list: %w", err)
	}
	defer rows.Close()
	return scanRules(rows)
}

// Matching returns the enabled rules that could fire on an event.
func (s *Service) Matching(ctx context.Context, orgID, event string) ([]*Rule, error) {
	rows, err := s.db.Query(ctx, ruleSelect+`
		WHERE r.org_id=$1 AND r.enabled AND r.trigger->>'event' = $2`, orgID, event)
	if err != nil {
		return nil, fmt.Errorf("rules: matching: %w", err)
	}
	defer rows.Close()
	return scanRules(rows)
}

const ruleSelect = `
	SELECT r.id::text, r.org_id::text, r.space_id::text, r.name,
	       r.trigger, r.conditions, r.actions, r.enabled,
	       r.created_by::text, r.created_at, r.updated_at
	FROM rules r`

func scanRules(rows pgx.Rows) ([]*Rule, error) {
	var out []*Rule
	for rows.Next() {
		r := &Rule{}
		var trigger, conditions, actions []byte
		if err := rows.Scan(&r.ID, &r.OrgID, &r.SpaceID, &r.Name,
			&trigger, &conditions, &actions, &r.Enabled,
			&r.CreatedBy, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("rules: scan: %w", err)
		}
		if err := json.Unmarshal(trigger, &r.Trigger); err != nil {
			return nil, fmt.Errorf("rules: decode trigger for %s: %w", r.ID, err)
		}
		if len(conditions) > 0 {
			_ = json.Unmarshal(conditions, &r.Conditions)
		}
		if len(actions) > 0 {
			_ = json.Unmarshal(actions, &r.Actions)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Runs returns a rule's history, newest first.
func (s *Service) Runs(ctx context.Context, orgID, ruleID string, limit int) ([]*Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args := []any{orgID, limit}
	where := `WHERE rr.org_id = $1`
	if ruleID != "" {
		args = append(args, ruleID)
		where += ` AND rr.rule_id = $3`
	}
	// Newest EVENT first, not newest row first.
	//
	// A real run happens moments after the event it is about, so the two orders
	// agree and this looks like a distinction without a difference. A dry run
	// is where it matters: a rehearsal writes one row per event in a fraction
	// of a second, so ran_at is effectively the same for all of them and the
	// tie-break decides the whole list. Ordering by the row put the oldest
	// history at the top and pushed this morning off the bottom of a fifty row
	// page, so pressing dry run on a rule that would obviously fire showed
	// fifty rows of "did not match" and nothing else.
	//
	// Outbox ids are monotonic, so ordering by the event is ordering by when it
	// happened, which is the question somebody reading this page is asking.
	rows, err := s.db.Query(ctx, `
		SELECT rr.id, rr.rule_id::text, r.name, rr.event_id, rr.entity_type,
		       rr.entity_id::text, rr.matched, COALESCE(rr.reason,''),
		       rr.actions_taken, COALESCE(rr.error,''), rr.dry_run, rr.ran_at
		FROM rule_runs rr JOIN rules r ON r.id = rr.rule_id
		`+where+` ORDER BY rr.event_id DESC NULLS LAST, rr.ran_at DESC, rr.id DESC
		LIMIT $2`, args...)
	if err != nil {
		return nil, fmt.Errorf("rules: runs: %w", err)
	}
	defer rows.Close()

	var out []*Run
	for rows.Next() {
		r := &Run{}
		var taken []byte
		if err := rows.Scan(&r.ID, &r.RuleID, &r.RuleName, &r.EventID, &r.EntityType,
			&r.EntityID, &r.Matched, &r.Reason, &taken, &r.Error, &r.DryRun, &r.RanAt); err != nil {
			return nil, fmt.Errorf("rules: scan run: %w", err)
		}
		if len(taken) > 0 {
			_ = json.Unmarshal(taken, &r.ActionsTaken)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordRun, FinishRun, ClearDryRuns and RecentEvents are the engine's view of
// where runs live.
func (s *Service) RecordRun(ctx context.Context, r *Run) (bool, error) { return s.record(ctx, r) }

func (s *Service) RecentEvents(ctx context.Context, orgID, event string, limit int) ([]jobs.Event, error) {
	return s.recentEvents(ctx, orgID, event, limit)
}

// FinishRun records what a claimed run did.
func (s *Service) FinishRun(ctx context.Context, ruleID string, eventID int64, taken []string, errText string) error {
	takenJSON, _ := json.Marshal(taken)
	var et any
	if errText != "" {
		et = errText
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE rule_runs SET actions_taken=$3, error=$4
		WHERE rule_id=$1 AND event_id=$2 AND NOT dry_run`,
		ruleID, eventID, takenJSON, et); err != nil {
		return fmt.Errorf("rules: record outcome: %w", err)
	}
	return nil
}

// ClearDryRuns removes a rule's previous rehearsal.
func (s *Service) ClearDryRuns(ctx context.Context, ruleID string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM rule_runs WHERE rule_id=$1 AND dry_run`, ruleID); err != nil {
		return fmt.Errorf("rules: clear dry runs: %w", err)
	}
	return nil
}

// record writes a run.
//
// Returns false when a real run for this rule and event already exists, which
// is the "fires once" guarantee. The unique index does the work; this reports
// what it decided so the caller knows whether to perform the actions.
func (s *Service) record(ctx context.Context, r *Run) (bool, error) {
	taken, _ := json.Marshal(r.ActionsTaken)
	var reason, errText any
	if r.Reason != "" {
		reason = r.Reason
	}
	if r.Error != "" {
		errText = r.Error
	}
	ct, err := s.db.Exec(ctx, `
		INSERT INTO rule_runs (rule_id, org_id, event_id, entity_type, entity_id,
		                       matched, reason, actions_taken, error, dry_run)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (rule_id, event_id) WHERE NOT dry_run DO NOTHING`,
		r.RuleID, r.OrgID, r.EventID, r.EntityType, r.EntityID,
		r.Matched, reason, taken, errText, r.DryRun)
	if err != nil {
		return false, fmt.Errorf("rules: record run: %w", err)
	}
	return ct.RowsAffected() > 0, nil
}

// EventsFor returns the last events an entity type saw, for a dry run.
func (s *Service) recentEvents(ctx context.Context, orgID, event string, limit int) ([]jobs.Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id::text, entity_type, entity_id::text, event, payload,
		       actor_id::text, created_at, published_at
		FROM outbox
		WHERE org_id = $1 AND event = $2 AND depth = 0
		ORDER BY id DESC LIMIT $3`, orgID, event, limit)
	if err != nil {
		return nil, fmt.Errorf("rules: recent events: %w", err)
	}
	defer rows.Close()

	var out []jobs.Event
	for rows.Next() {
		var e jobs.Event
		if err := rows.Scan(&e.ID, &e.OrgID, &e.EntityType, &e.EntityID, &e.Event,
			&e.Payload, &e.ActorID, &e.CreatedAt, &e.PublishedAt); err != nil {
			return nil, fmt.Errorf("rules: scan event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
