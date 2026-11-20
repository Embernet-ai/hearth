package docs

// Writes made by an automation rather than by a person.
//
// Separate entry points rather than a flag on the ordinary ones, because
// everything about them differs: there is no subject set to check, the event
// they publish carries a depth so nothing reacts to it, and the audit trail has
// to say which rule did it. Threading a "this is a rule" boolean through
// Update would put that decision at the bottom of a function that already has
// enough to think about.

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// RuleActor is an automation acting.
type RuleActor struct {
	RuleID string
	// PrincipalID is whoever wrote the rule. Every write still has a person
	// behind it, because "the system did it" is not an answer anybody accepts
	// when a work order was reassigned overnight.
	PrincipalID string
	// Depth is stamped on the event so nothing acts on it again.
	Depth int
}

// MatchesUnfiltered reports whether one document satisfies a query.
//
// Unfiltered means no permission check, and that is deliberate rather than an
// oversight: a rule is configured by somebody who can already see what it
// covers, and evaluating it against whichever subject set happened to trigger
// it would make the same rule fire or not depending on who pressed the button.
// It is unexported behaviour behind an obvious name, so nothing reaches for it
// by accident.
func (s *Service) MatchesUnfiltered(ctx context.Context, orgID, docID string, q Query) (bool, error) {
	b := &builder{}
	var sb string
	sb = "d.org_id = " + b.arg(orgID) + " AND d.id = " + b.arg(docID) + "::uuid" +
		" AND d.deleted_at IS NULL"
	if q.Kind != "" {
		sb += " AND d.kind = " + b.arg(q.Kind)
	}
	for _, f := range q.Filters {
		if frag := b.where(orgID, f); frag != "" {
			sb += " AND (" + frag + ")"
		}
	}

	var n int
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM documents d
		LEFT JOIN task_fields t ON t.document_id = d.id
		WHERE `+sb, b.args...).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("docs: match: %w", err)
	}
	return n > 0, nil
}

// SetFieldAsRule writes one field on behalf of an automation.
func (s *Service) SetFieldAsRule(ctx context.Context, orgID string, actor RuleActor,
	docID string, field Field, value string) error {

	if field.IsProperty() {
		def, err := s.PropertyDefByKey(ctx, orgID, field.Property)
		if err != nil {
			return err
		}
		return s.setPropertyAsRule(ctx, orgID, actor, docID, def.ID, value)
	}

	patch := &TaskPatch{}
	switch field.Column {
	case "status":
		if !validStatuses[value] {
			return fmt.Errorf("%w: %q", ErrField, value)
		}
		patch.Status = &value
	case "priority":
		n, err := strconv.ParseInt(value, 10, 16)
		if err != nil {
			return fmt.Errorf("%w: priority %q is not a number", ErrField, value)
		}
		p := int16(n)
		patch.Priority = &p
	case "assignee":
		if value == "" {
			patch.ClearAssignee = true
		} else {
			patch.AssigneeID = &value
		}
	default:
		return fmt.Errorf("%w: a rule cannot set %s", ErrField, field.Key())
	}
	return s.updateAsRule(ctx, orgID, actor, docID, patch)
}

// updateAsRule applies a task patch with no permission check and an event that
// carries the rule's depth.
func (s *Service) updateAsRule(ctx context.Context, orgID string, actor RuleActor,
	docID string, patch *TaskPatch) error {

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("docs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var curVersion int64
	var curStatus, curAssignee *string
	err = tx.QueryRow(ctx, `
		SELECT d.version, t.status, t.assignee_id::text
		FROM documents d LEFT JOIN task_fields t ON t.document_id = d.id
		WHERE d.id=$1 AND d.org_id=$2 AND d.deleted_at IS NULL
		FOR UPDATE OF d`, docID, orgID).Scan(&curVersion, &curStatus, &curAssignee)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("docs: lock for rule update: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE documents SET version=$1, updated_by=$2, updated_at=now() WHERE id=$3`,
		curVersion+1, actor.PrincipalID, docID); err != nil {
		return fmt.Errorf("docs: rule update: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE task_fields SET
		    status   = COALESCE($1, status),
		    priority = COALESCE($2, priority),
		    assignee_id = CASE WHEN $3 THEN NULL ELSE COALESCE($4, assignee_id) END,
		    started_at = CASE
		        WHEN COALESCE($1, status) IN ('in_progress','done')
		            THEN COALESCE(started_at, now()) ELSE started_at END,
		    completed_at = CASE
		        WHEN COALESCE($1, status) = 'done' THEN COALESCE(completed_at, now())
		        ELSE NULL END
		WHERE document_id=$5`,
		patch.Status, patch.Priority, patch.ClearAssignee, patch.AssigneeID, docID); err != nil {
		return fmt.Errorf("docs: rule task update: %w", err)
	}

	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: docID,
		Event: jobs.EventUpdated, ActorID: actor.PrincipalID,
		CauseRuleID: actor.RuleID, Depth: actor.Depth,
		Payload: map[string]any{"version": curVersion + 1, "by_rule": actor.RuleID},
	}); err != nil {
		return err
	}
	if patch.Status != nil && (curStatus == nil || *patch.Status != *curStatus) {
		from := ""
		if curStatus != nil {
			from = *curStatus
		}
		if err := jobs.Publish(ctx, tx, jobs.EventSpec{
			OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: docID,
			Event: jobs.EventStatus, ActorID: actor.PrincipalID,
			CauseRuleID: actor.RuleID, Depth: actor.Depth,
			Payload: map[string]any{"from": from, "to": *patch.Status},
		}); err != nil {
			return err
		}
	}
	if assignee, changed := assigneeChange(patch, curAssignee); changed {
		if err := jobs.Publish(ctx, tx, jobs.EventSpec{
			OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: docID,
			Event: jobs.EventAssigned, ActorID: actor.PrincipalID,
			CauseRuleID: actor.RuleID, Depth: actor.Depth,
			Payload: map[string]any{"assignee_id": assignee},
		}); err != nil {
			return err
		}
	}
	// A rule that marks a task done closes what is inside it, the same as a
	// person doing it; the subtasks are closed in the rule's name.
	var closed []string
	if patch.Status != nil && *patch.Status == StatusDone && (curStatus == nil || *curStatus != StatusDone) {
		if closed, err = s.closeSubtasks(ctx, tx, orgID, docID, actor.PrincipalID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	NoteClosed(ctx, closed)
	return nil
}

func (s *Service) setPropertyAsRule(ctx context.Context, orgID string, actor RuleActor,
	docID, defID, value string) error {

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("docs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var dataType string
	if err := tx.QueryRow(ctx,
		`SELECT data_type FROM property_defs WHERE id=$1 AND org_id=$2`,
		defID, orgID).Scan(&dataType); err != nil {
		return fmt.Errorf("docs: read property type: %w", err)
	}
	encoded, err := encodeValue(dataType, value)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO property_values (entity_type, entity_id, def_id, value, updated_by)
		VALUES ('document', $1, $2, $3, $4)
		ON CONFLICT (entity_type, entity_id, def_id)
		DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		docID, defID, encoded, actor.PrincipalID); err != nil {
		return fmt.Errorf("docs: rule set property: %w", err)
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: docID,
		Event: jobs.EventUpdated, ActorID: actor.PrincipalID,
		CauseRuleID: actor.RuleID, Depth: actor.Depth,
		Payload: map[string]any{"property": defID, "by_rule": actor.RuleID},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CreateAsRule makes a document on behalf of an automation and links it to
// whatever triggered the rule.
//
// The link is the point. "When a work order is completed, raise the follow-up"
// produces something anybody can trace back to the job that caused it, which is
// the difference between an automation and a mystery.
// The grants are copied from the thing that triggered it, so the new work order
// is visible to exactly the people who could see the one it came from. Granting
// only the rule's author would produce a job nobody on the shift can see. That
// is [Service.CreateAsSystem]'s job, and this is a rule-shaped door onto it: a
// rule has no use for the document it made, so it does not get handed one.
func (s *Service) CreateAsRule(ctx context.Context, orgID string, actor RuleActor,
	p CreateParams, linkTo core.Ref) error {

	_, err := s.CreateAsSystem(ctx, orgID, actor, p, linkTo)
	return err
}
