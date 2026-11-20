package docs

// Writes made by the system on a schedule, rather than by a person or by a
// rule reacting to something.
//
// The difference from [Service.CreateAsRule] is small and worth having: a
// maintenance schedule needs the document back so it can record which work
// order an occurrence produced, and it needs to set a due date on it. A rule
// firing needs neither, so its entry point returns an error and nothing else.
// Both go through the same body, so there is one place where a system write
// decides who can see what.

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// CreateAsSystem makes a document with no subject set to check, links it to
// whatever caused it, and returns it.
//
// linkTo is required. A document created by machinery with nothing pointing at
// it is a job with no explanation, and the first question anybody asks about an
// automatically raised work order is what raised it.
func (s *Service) CreateAsSystem(ctx context.Context, orgID string, actor RuleActor,
	p CreateParams, linkTo core.Ref) (*Document, error) {

	if p.Kind == "" {
		p.Kind = KindWorkOrder
	}
	if !validKinds[p.Kind] {
		return nil, fmt.Errorf("%w: %q", ErrKind, p.Kind)
	}
	if actor.PrincipalID == "" {
		return nil, errors.New("docs: no acting principal")
	}
	if linkTo.ID == "" {
		return nil, errors.New("docs: a system write has to say what caused it")
	}
	if p.BodyFormat != FormatMarkdown && p.BodyFormat != FormatPlain {
		p.BodyFormat = FormatMarkdown
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("docs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	d := &Document{
		ID: uuid.NewString(), OrgID: orgID, Kind: p.Kind, Title: p.Title,
		Body: p.Body, BodyFormat: p.BodyFormat, SpaceID: p.SpaceID,
		OwnerID: actor.PrincipalID, Version: 1,
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO documents (id, org_id, space_id, kind, title, body, body_format,
		                       owner_id, version, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1,$8,$8)
		RETURNING created_at, updated_at`,
		d.ID, orgID, d.SpaceID, d.Kind, d.Title, d.Body, d.BodyFormat, actor.PrincipalID).
		Scan(&d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("docs: system create: %w", err)
	}

	if HasTaskFields(d.Kind) {
		t := p.Task
		if t == nil {
			t = &TaskFields{Status: StatusTodo}
		}
		if t.Status == "" {
			t.Status = StatusTodo
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO task_fields (document_id, status, priority, assignee_id, due_at)
			VALUES ($1,$2,$3,$4,$5)`,
			d.ID, t.Status, t.Priority, t.AssigneeID, t.DueAt); err != nil {
			return nil, fmt.Errorf("docs: system task fields: %w", err)
		}
		d.Task = t
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO document_versions (document_id, version, title, body, author_id)
		VALUES ($1,1,$2,$3,$4)`, d.ID, d.Title, d.Body, actor.PrincipalID); err != nil {
		return nil, fmt.Errorf("docs: system create version: %w", err)
	}

	if err := s.systemGrantsAndLink(ctx, tx, orgID, d.ID, actor, linkTo); err != nil {
		return nil, err
	}

	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: d.ID,
		Event: jobs.EventCreated, ActorID: actor.PrincipalID,
		CauseRuleID: actor.RuleID, Depth: actor.Depth,
		Payload: map[string]any{"kind": d.Kind, "title": d.Title, "caused_by": linkTo.ID},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("docs: commit: %w", err)
	}
	return d, nil
}

// InstantiateAsSystem builds a document from a template with no subject set to
// check, and links it to whatever caused it.
//
// This is the path a maintenance schedule takes when it has a procedure
// attached, and the reason it goes through the template machinery rather than
// copying a title is the checklist. A work order raised without its steps is a
// reminder, and the crew has plenty of those already.
func (s *Service) InstantiateAsSystem(ctx context.Context, orgID string, actor RuleActor,
	p InstantiateParams, linkTo core.Ref) (*Document, error) {

	if actor.PrincipalID == "" {
		return nil, errors.New("docs: no acting principal")
	}
	if linkTo.ID == "" {
		return nil, errors.New("docs: a system write has to say what caused it")
	}
	tmpl, err := s.load(ctx, orgID, p.TemplateID)
	if err != nil {
		return nil, err
	}
	return s.instantiate(ctx, orgID, tmpl, p, actor, linkTo)
}

// CreateAsAgent makes a document on behalf of an agent and shares it the way
// the agent itself is shared.
//
// The problem this solves is not obvious until it bites. An agent is a
// principal, so Create grants the new document to the agent and to nobody
// else, exactly as it would for a person. But a person can then share what
// they made, and an agent cannot: it has no hands. The result is a work order
// that exists, is correctly attributed, and is invisible to every human being
// on site, which is a feature that looks like it works and does nothing.
//
// So the agent's CHANNELS come with it. An agent in the maintenance channel
// raises work the maintenance channel can edit, which is the product's own
// thesis applied to a non-human: channel membership is the permission model,
// and an agent is a member like anybody else.
//
// Not the org subject, and that restraint took an acceptance run to learn.
// Copying every subject is one line shorter and grants the whole site edit on
// everything any agent ever makes, which is a permission path that exists
// nowhere else in this product. Introducing one quietly, through the agent
// door, is exactly the second permission system these packages keep warning
// about. The second run of the suite found it, because rows kept appearing in
// somebody else's saved view.
//
// Edit rather than view. A work order nobody can complete is not a work order.
//
// An agent in no channel produces something only it can see. That is the
// honest outcome rather than a guess at who should be told, and the agents
// page says so rather than leaving somebody to discover it.
func (s *Service) CreateAsAgent(ctx context.Context, orgID string, actor core.Subjects,
	p CreateParams) (*Document, error) {

	d, err := s.Create(ctx, orgID, actor, p)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(actor))
	for _, subject := range actor {
		if subject.Kind == core.SubjectChannel || subject.Kind == core.SubjectTeam {
			keys = append(keys, subject.Key())
		}
	}
	if len(keys) == 0 {
		return d, nil
	}
	if _, err := s.db.Exec(ctx, `
		INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		SELECT gen_random_uuid(), $1, 'document', $2, k, 'edit', $3
		FROM unnest($4::text[]) AS k
		ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
		orgID, d.ID, actor.Principal(), keys); err != nil {
		return nil, fmt.Errorf("docs: share what the agent made: %w", err)
	}
	return d, nil
}

// systemGrantsAndLink gives the new document the grants of the thing that
// caused it, plus one for the principal behind the machinery, and records the
// link between them.
func (s *Service) systemGrantsAndLink(ctx context.Context, tx pgx.Tx,
	orgID, docID string, actor RuleActor, linkTo core.Ref) error {

	if _, err := tx.Exec(ctx, `
		INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		SELECT gen_random_uuid(), $1, 'document', $2, g.subject_key, g.level, $4
		FROM grants g
		WHERE g.entity_type = $5 AND g.entity_id = $3
		ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
		orgID, docID, linkTo.ID, actor.PrincipalID, string(linkTo.Type)); err != nil {
		return fmt.Errorf("docs: copy grants: %w", err)
	}
	// And the principal behind it, so something caused by a thing with no
	// grants at all is still reachable by somebody.
	if _, err := tx.Exec(ctx, `
		INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		VALUES (gen_random_uuid(), $1, 'document', $2, $3, 'admin', $4)
		ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
		orgID, docID, core.Subject{Kind: core.SubjectPrincipal, ID: actor.PrincipalID}.Key(),
		actor.PrincipalID); err != nil {
		return fmt.Errorf("docs: grant to system principal: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO entity_links (id, org_id, from_type, from_id, to_type, to_id, kind, created_by)
		VALUES (gen_random_uuid(), $1, 'document', $2, $3, $4, 'concerns', $5)
		ON CONFLICT (from_type, from_id, to_type, to_id, kind) DO NOTHING`,
		orgID, docID, string(linkTo.Type), linkTo.ID, actor.PrincipalID); err != nil {
		return fmt.Errorf("docs: link system creation: %w", err)
	}
	return nil
}
