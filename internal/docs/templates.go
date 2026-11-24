package docs

// Templates.
//
// A template is a document with kind='template'. Not a separate table, not a
// JSON blob in a settings page: the same row, so it gets versioning,
// permissions, search, and attachments for free, and so writing one is exactly
// the same act as writing the thing it produces.
//
// Instantiating copies the body, the properties, and the checklist. Properties
// matter most: a preventive maintenance template whose criticality and asset
// tag have to be retyped every time is a template nobody uses twice.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// Template is one, for a picker.
type Template struct {
	ID    string
	Title string
	// Produces is the kind the template creates, taken from a property so a
	// site can have a work order template and an asset template without two
	// mechanisms.
	Produces string
	Items    int
}

// TemplateKindKey is the property that says what a template produces.
//
// A property rather than a column, because that is the promise this system
// makes about custom fields: if it is worth storing it is worth storing the
// same way as everything else, and adding a column here would be the first
// exception.
const TemplateKindKey = "template_kind"

// Templates lists what a person can instantiate.
func (s *Service) Templates(ctx context.Context, orgID string, actor core.Subjects) ([]Template, error) {
	rows, err := s.db.Query(ctx, `
		SELECT d.id::text, COALESCE(NULLIF(d.title,''), 'Untitled'),
		       COALESCE((SELECT v.value #>> '{}' FROM property_values v
		                 JOIN property_defs pd ON pd.id = v.def_id
		                 WHERE v.entity_type='document' AND v.entity_id = d.id
		                   AND pd.org_id = $1 AND pd.key = $3), ''),
		       (SELECT count(*) FROM checklist_items c WHERE c.document_id = d.id)
		FROM documents d
		WHERE d.org_id = $1 AND d.kind = 'template' AND d.deleted_at IS NULL
		  AND ($2::text[] @> ARRAY['*super'] OR EXISTS (
		      SELECT 1 FROM grants g
		       WHERE g.entity_type = 'document' AND g.entity_id = d.id
		         AND g.subject_key = ANY($2)
		         AND (g.expires_at IS NULL OR g.expires_at > now())))
		ORDER BY d.title`, orgID, actor.GrantKeys(), TemplateKindKey)
	if err != nil {
		return nil, fmt.Errorf("docs: templates: %w", err)
	}
	defer rows.Close()

	var out []Template
	for rows.Next() {
		var t Template
		if err := rows.Scan(&t.ID, &t.Title, &t.Produces, &t.Items); err != nil {
			return nil, fmt.Errorf("docs: scan template: %w", err)
		}
		if t.Produces == "" {
			t.Produces = KindWorkOrder
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// InstantiateParams describes what to make from a template.
type InstantiateParams struct {
	TemplateID string
	// Title overrides the template's, which is usually what somebody wants:
	// "Quarterly service" becomes "Quarterly service: Conveyor 3".
	Title string
	// Kind overrides what the template says it produces.
	Kind string
	Task *TaskFields
}

// Instantiate creates a document from a template.
//
// One transaction covering the document, its properties, and its checklist. A
// half-instantiated template is worse than none: somebody starts the job, does
// the four steps that copied, and never learns there were seven.
func (s *Service) Instantiate(ctx context.Context, orgID string, actor core.Subjects, p InstantiateParams) (*Document, error) {
	tmpl, err := s.Get(ctx, orgID, actor, p.TemplateID)
	if err != nil {
		return nil, err
	}
	principal := actor.Principal()
	if principal == "" {
		return nil, errors.New("docs: no acting principal")
	}
	return s.instantiate(ctx, orgID, tmpl, p,
		RuleActor{PrincipalID: principal}, core.Ref{})
}

// instantiate is the body both entry points share.
//
// actor carries the principal and, when an automation is behind it, the rule id
// and depth that go on the event. linkTo is the thing that caused it, empty for
// a person pressing the button: when it is set, the new document inherits that
// thing's grants and is linked to it.
func (s *Service) instantiate(ctx context.Context, orgID string, tmpl *Document,
	p InstantiateParams, actor RuleActor, linkTo core.Ref) (*Document, error) {

	if tmpl.Kind != KindTemplate {
		return nil, fmt.Errorf("%w: %s is not a template", ErrKind, p.TemplateID)
	}
	principal := actor.PrincipalID

	kind := p.Kind
	if kind == "" {
		kind = s.templateProduces(ctx, orgID, p.TemplateID)
	}
	if !validKinds[kind] || kind == KindTemplate {
		// A template that makes templates is a loop nobody asked for.
		kind = KindWorkOrder
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = tmpl.Title
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("docs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	d := &Document{
		ID: uuid.NewString(), OrgID: orgID, Kind: kind, Title: title,
		Body: tmpl.Body, SpaceID: tmpl.SpaceID, OwnerID: principal, Version: 1,
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO documents (id, org_id, space_id, kind, title, body, owner_id,
		                       version, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,1,$7,$7)
		RETURNING created_at, updated_at`,
		d.ID, orgID, d.SpaceID, kind, title, tmpl.Body, principal).
		Scan(&d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("docs: instantiate: %w", err)
	}

	if HasTaskFields(kind) {
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
			return nil, fmt.Errorf("docs: instantiate task fields: %w", err)
		}
		d.Task = t
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO document_versions (document_id, version, title, body, author_id)
		VALUES ($1, 1, $2, $3, $4)`, d.ID, title, tmpl.Body, principal); err != nil {
		return nil, fmt.Errorf("docs: instantiate version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		VALUES (gen_random_uuid(), $1, 'document', $2, $3, 'admin', $4)
		ON CONFLICT (entity_type, entity_id, subject_key) DO UPDATE
		   SET level = 'admin', expires_at = NULL, via_space_id = NULL`,
		orgID, d.ID, core.Subject{Kind: core.SubjectPrincipal, ID: principal}.Key(), principal,
	); err != nil {
		return nil, fmt.Errorf("docs: grant instantiated: %w", err)
	}

	// When something else caused this, the new document inherits that thing's
	// grants. A work order raised off an asset is visible to the people who can
	// see the asset; granting only the person who wrote the schedule would
	// produce a job nobody on the shift can find.
	if linkTo.ID != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
			SELECT gen_random_uuid(), $1, 'document', $2, g.subject_key, g.level, $4
			FROM grants g
			WHERE g.entity_type = $5 AND g.entity_id = $3
			ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
			orgID, d.ID, linkTo.ID, principal, string(linkTo.Type)); err != nil {
			return nil, fmt.Errorf("docs: copy grants: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO entity_links (id, org_id, from_type, from_id, to_type, to_id, kind, created_by)
			VALUES (gen_random_uuid(), $1, 'document', $2, $3, $4, 'concerns', $5)
			ON CONFLICT (from_type, from_id, to_type, to_id, kind) DO NOTHING`,
			orgID, d.ID, string(linkTo.Type), linkTo.ID, principal); err != nil {
			return nil, fmt.Errorf("docs: link instantiated: %w", err)
		}
	}

	// The properties, which is the half people notice is missing. The template
	// kind itself is not copied: it describes the template, not the thing.
	if _, err := tx.Exec(ctx, `
		INSERT INTO property_values (entity_type, entity_id, def_id, value, updated_by)
		SELECT 'document', $1, v.def_id, v.value, $3
		FROM property_values v
		JOIN property_defs pd ON pd.id = v.def_id
		WHERE v.entity_type = 'document' AND v.entity_id = $2 AND pd.key <> $4
		ON CONFLICT (entity_type, entity_id, def_id) DO NOTHING`,
		d.ID, p.TemplateID, principal, TemplateKindKey); err != nil {
		return nil, fmt.Errorf("docs: copy template properties: %w", err)
	}

	// And the checklist, unticked. Copying done_at would say the work was
	// already finished before anybody started it.
	if _, err := tx.Exec(ctx, `
		INSERT INTO checklist_items (id, document_id, position, text)
		SELECT gen_random_uuid(), $1, c.position, c.text
		FROM checklist_items c WHERE c.document_id = $2`,
		d.ID, p.TemplateID); err != nil {
		return nil, fmt.Errorf("docs: copy template checklist: %w", err)
	}

	// Where it came from, so somebody looking at a work order six months later
	// can find the procedure it was made from and fix it there.
	if _, err := tx.Exec(ctx, `
		INSERT INTO entity_links (id, org_id, from_type, from_id, to_type, to_id, kind, created_by)
		VALUES (gen_random_uuid(), $1, 'document', $2, 'document', $3, 'from_template', $4)
		ON CONFLICT (from_type, from_id, to_type, to_id, kind) DO NOTHING`,
		orgID, d.ID, p.TemplateID, principal); err != nil {
		return nil, fmt.Errorf("docs: link to template: %w", err)
	}

	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: d.ID,
		Event: jobs.EventCreated, ActorID: principal,
		CauseRuleID: actor.RuleID, Depth: actor.Depth,
		Payload: map[string]any{"kind": kind, "title": title, "from_template": p.TemplateID},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("docs: commit: %w", err)
	}
	return d, nil
}

func (s *Service) templateProduces(ctx context.Context, orgID, templateID string) string {
	var kind string
	err := s.db.QueryRow(ctx, `
		SELECT v.value #>> '{}' FROM property_values v
		JOIN property_defs pd ON pd.id = v.def_id
		WHERE v.entity_type='document' AND v.entity_id=$1
		  AND pd.org_id=$2 AND pd.key=$3`, templateID, orgID, TemplateKindKey).Scan(&kind)
	if err != nil {
		return KindWorkOrder
	}
	return kind
}

// EnsureTemplateProperties creates the field that says what a template makes.
//
// Seeded lazily and idempotently, like the asset fields, so an installation
// that predates templates gets one without a migration touching data.
func (s *Service) EnsureTemplateProperties(ctx context.Context, orgID string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO property_defs (id, org_id, key, display_name, data_type, applies_to, options)
		VALUES (gen_random_uuid(), $1, $2, 'Creates', 'select', ARRAY['template'],
		        '["work_order","task","doc","asset","wiki","note"]'::jsonb)
		ON CONFLICT (org_id, key) DO NOTHING`, orgID, TemplateKindKey)
	if err != nil {
		return fmt.Errorf("docs: seed template properties: %w", err)
	}
	return nil
}
