// Package docs owns the entity spine: documents, and the task fields that hang
// off the ones with kind='task'.
//
// "Documents" is a misleading name for what this is. It is not prose. It is the
// row that a work order, an asset, a task, and a runbook are all made of, which
// is why they share one table. A work order gets versioning, linking,
// permissions, properties, and search because it IS a document, rather than
// being its own table that reimplements all five badly.
package docs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

var (
	ErrNotFound = errors.New("document not found")
	// ErrConflict means someone else saved while this edit was in flight.
	// It is a real answer, not a failure: last-write-wins without a version
	// check silently discards the other person's work.
	ErrConflict = errors.New("document was modified by someone else")
	ErrKind     = errors.New("unknown document kind")
	// ErrField means a view tried to write a field that is not writable, or a
	// value the column will not take.
	ErrField = errors.New("field cannot take that value")
	// ErrNotAllowed means the caller may see the task but not do this to it --
	// setting its assignees, which only its creator, a manager or an admin may.
	ErrNotAllowed = errors.New("not allowed")
)

// Kind values. These match the CHECK constraint in the migration; the database
// is the enforcement and this is the vocabulary.
const (
	KindDoc       = "doc"
	KindTask      = "task"
	KindWiki      = "wiki"
	KindWorkOrder = "work_order"
	KindAsset     = "asset"
	KindTemplate  = "template"
	KindNote      = "note"
	// KindPurchaseOrder is the product's thesis applied to itself: a purchase
	// order is the same row with a different kind, so it gets versioning,
	// permissions, links, attachments, comments, and search on the day it
	// lands rather than in the release after somebody asks.
	KindPurchaseOrder = "purchase_order"
	// KindDailyReport is a status update against a space, optionally linked to
	// a task via entity_links, same as any other document. Its own kind
	// rather than a Comments feature made more prominent: it needed a place
	// with its own audit trail, and every document already gets one for free
	// through document_versions -- who edited, a real diff, and Restore.
	KindDailyReport = "daily_report"
)

var validKinds = map[string]bool{
	KindDoc: true, KindTask: true, KindWiki: true, KindWorkOrder: true,
	KindAsset: true, KindTemplate: true, KindNote: true, KindPurchaseOrder: true,
	KindDailyReport: true,
}

// Body formats, matching the CHECK constraint in the substrate.
const (
	FormatMarkdown = "markdown"
	FormatPlain    = "plain"
)

// Task statuses.
const (
	StatusTodo       = "todo"
	StatusInProgress = "in_progress"
	StatusBlocked    = "blocked"
	StatusDone       = "done"
	StatusCancelled  = "cancelled"
)

// Statuses in lifecycle order. This is the order a board reads left to right,
// which is why it is a slice rather than a set: sorting the five alphabetically
// puts blocked first and todo last, and a board that reads backwards is worse
// than no board.
var Statuses = []string{StatusTodo, StatusInProgress, StatusBlocked, StatusDone, StatusCancelled}

var statusLabels = map[string]string{
	StatusTodo: "To do", StatusInProgress: "In progress", StatusBlocked: "Blocked",
	StatusDone: "Done", StatusCancelled: "Cancelled",
}

// StatusLabel is how a status reads to a person: "In progress", not the stored
// "in_progress". Anything that is not one of the five comes back unchanged, so
// it is safe to apply to a value that might not be a status at all.
func StatusLabel(s string) string {
	if l, ok := statusLabels[s]; ok {
		return l
	}
	return s
}

// IsValidStatus reports whether s is one of the five real statuses -- for a
// caller outside this package deciding whether to trust a status it did not
// itself generate (a form field, a group a new row was added inline from).
func IsValidStatus(s string) bool { return validStatuses[s] }

var validStatuses = map[string]bool{
	StatusTodo: true, StatusInProgress: true, StatusBlocked: true,
	StatusDone: true, StatusCancelled: true,
}

// ClosedStatuses are the statuses a list hides by default until asked for.
// Shared by the query layer's default exclusion and anything in the web
// layer that needs to know what "closed" means, so the two cannot drift.
var ClosedStatuses = []string{StatusDone, StatusCancelled}

// Document is one row of the spine.
type Document struct {
	ID    string
	OrgID string
	Kind  string
	Title string
	Body  string
	// BodyFormat has said markdown since the substrate landed while the page
	// rendered a <pre>. It is read now.
	BodyFormat string
	SpaceID    *string
	ChannelID  *string
	// ParentID is the task this one sits inside, when it is a subtask.
	ParentID  *string
	OwnerID   string
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time

	// Task is populated only for kind='task'. Work orders can carry task
	// fields too, because a work order is a task with a different name on the
	// screen and the same lifecycle underneath.
	Task *TaskFields
}

// TaskFields is the side table. It is separate so that document list queries
// get tight covering indexes without dragging bodies through the buffer pool.
type TaskFields struct {
	Status      string
	Priority    int16
	AssigneeID  *string
	DueAt       *time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
}

func (d *Document) Ref() core.Ref { return core.Ref{Type: core.EntityDocument, ID: d.ID} }

// HasTaskFields reports whether this kind carries a task lifecycle.
func HasTaskFields(kind string) bool {
	return kind == KindTask || kind == KindWorkOrder
}

// Writable reports whether this kind is edited directly on the page rather
// than through the Edit popover everything else uses. True for kinds whose
// whole reason to exist is being written and read (doc, daily_report), false
// for kinds where the body is secondary to something else the page is
// mostly about (a task's status/checklist/time, a work order's parts/calls).
func Writable(kind string) bool {
	return kind == KindDoc || kind == KindDailyReport
}

// Authorizer is the slice of access control this package needs.
//
// Declared HERE, by the consumer, rather than imported from authz. That is what
// keeps the dependency pointing one way and lets this package be tested without
// standing up the real thing.
type Authorizer interface {
	Require(ctx context.Context, subjects core.Subjects, ref core.Ref, required core.AccessLevel) error
	Grant(ctx context.Context, orgID string, ref core.Ref, subject core.Subject, level core.AccessLevel, grantedBy string) error
	Revoke(ctx context.Context, orgID string, ref core.Ref, subject core.Subject, actorID string) error
}

// Service reads and writes documents.
type Service struct {
	db    *pgxpool.Pool
	authz Authorizer
}

func New(db *pgxpool.Pool, az Authorizer) *Service {
	return &Service{db: db, authz: az}
}

// CreateParams describes a new document.
type CreateParams struct {
	Kind  string
	Title string
	Body  string
	// BodyFormat is markdown unless something says otherwise. An importer
	// bringing in text from a system with no formatting sets plain, so that
	// asterisks in a machine-generated body do not become emphasis.
	BodyFormat string
	SpaceID    *string
	ChannelID  *string
	Task       *TaskFields

	// History, for an import bringing work over from somewhere it already
	// lived. Nil means now, which is right for anything created here.
	//
	// A migration that cannot set these makes every document claim to have
	// been created on migration day: every list sorts wrong, "recently
	// updated" means nothing, and a decade of work appears to have happened in
	// one afternoon.
	CreatedAt *time.Time
	UpdatedAt *time.Time
	// CreatedBy attributes the document to whoever raised it in the source
	// rather than to whoever ran the import. Empty means the acting principal.
	CreatedBy string
	// ParentID makes this a subtask of that task. The caller has to be able to
	// see the parent; the subtask lands in the parent's space unless SpaceID
	// says otherwise, and whoever can reach the parent can reach it.
	ParentID *string

	// ReportDate is an instant inside the day a daily report is for; the day
	// is that instant's date in the office's time zone (ReportDay), never the
	// caller's or the server's. Nil means today in the office. Only read for daily_report, where
	// the database allows one per person per day.
	ReportDate *time.Time
}

// Create writes a document and grants its creator admin, in ONE transaction.
//
// The grant is not a separate step, and that matters: a document that exists
// with no grant is invisible to everyone including the person who just made it,
// and it can only be found by an admin going into the database. Creating the
// thing and being able to see it are the same operation.
func (s *Service) Create(ctx context.Context, orgID string, actor core.Subjects, p CreateParams) (*Document, error) {
	if p.Kind == "" {
		p.Kind = KindDoc
	}
	if !validKinds[p.Kind] {
		return nil, fmt.Errorf("%w: %q", ErrKind, p.Kind)
	}
	principal := actor.Principal()
	if principal == "" {
		return nil, errors.New("docs: no acting principal")
	}

	// A subtask needs a parent the caller can see, in this organisation. Not
	// found whether it is missing or hidden, the same answer Get gives.
	if p.ParentID != nil {
		if *p.ParentID == "" {
			p.ParentID = nil
		} else {
			parentSpace, err := s.parentOf(ctx, orgID, actor, *p.ParentID)
			if err != nil {
				return nil, err
			}
			if p.SpaceID == nil {
				p.SpaceID = parentSpace
			}
		}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("docs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The space and channel must be this organisation's. A foreign key only
	// says the row exists somewhere; filing into another organisation's space
	// would also copy that space's grants onto the new document below. With
	// one organisation this never came up; personal playgrounds are more.
	if p.SpaceID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM spaces WHERE id = $1 AND org_id = $2)`,
			*p.SpaceID, orgID).Scan(&ok); err != nil {
			return nil, fmt.Errorf("docs: check space: %w", err)
		}
		if !ok {
			return nil, fmt.Errorf("%w: space", ErrNotFound)
		}
	}
	if p.ChannelID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM channels WHERE id = $1 AND org_id = $2)`,
			*p.ChannelID, orgID).Scan(&ok); err != nil {
			return nil, fmt.Errorf("docs: check channel: %w", err)
		}
		if !ok {
			return nil, fmt.Errorf("%w: channel", ErrNotFound)
		}
	}

	if p.BodyFormat != FormatMarkdown && p.BodyFormat != FormatPlain {
		p.BodyFormat = FormatMarkdown
	}
	d := &Document{
		ID: uuid.NewString(), OrgID: orgID, Kind: p.Kind,
		Title: p.Title, Body: p.Body, BodyFormat: p.BodyFormat,
		SpaceID: p.SpaceID, ChannelID: p.ChannelID, ParentID: p.ParentID,
		OwnerID: principal, Version: 1,
	}

	// COALESCE rather than two statements: a NULL parameter takes the column
	// default, so the ordinary path is unchanged and an import can carry its
	// own history without a second code path to keep in step.
	author := principal
	if p.CreatedBy != "" {
		author = p.CreatedBy
	}
	var reportDate *string
	if d.Kind == KindDailyReport {
		day := ReportDay(time.Now())
		if p.ReportDate != nil {
			day = ReportDay(*p.ReportDate)
		}
		reportDate = &day
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO documents (id, org_id, space_id, channel_id, kind, title, body,
		                       body_format, owner_id, version, created_by, updated_by,
		                       created_at, updated_at, parent_id, report_date)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1,$10,$10,
		        COALESCE($11, now()), COALESCE($12, $11, now()), $13, $14::date)
		RETURNING created_at, updated_at`,
		d.ID, d.OrgID, d.SpaceID, d.ChannelID, d.Kind, d.Title, d.Body,
		d.BodyFormat, principal, author, p.CreatedAt, p.UpdatedAt, d.ParentID, reportDate,
	).Scan(&d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		if isReportDayTaken(err) {
			return nil, ErrReportExists
		}
		return nil, fmt.Errorf("docs: insert: %w", err)
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
			return nil, fmt.Errorf("docs: insert task fields: %w", err)
		}
		d.Task = t
	}

	// The first version, so history starts at creation rather than at the
	// first edit. Otherwise "what did this look like originally" has no answer.
	if _, err := tx.Exec(ctx, `
		INSERT INTO document_versions (document_id, version, title, body, author_id)
		VALUES ($1, 1, $2, $3, $4)`, d.ID, d.Title, d.Body, principal); err != nil {
		return nil, fmt.Errorf("docs: insert initial version: %w", err)
	}

	// Same transaction. See the comment above.
	if _, err := tx.Exec(ctx, `
		INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		VALUES (gen_random_uuid(), $1, 'document', $2, $3, 'admin', $4)
		ON CONFLICT (entity_type, entity_id, subject_key) DO UPDATE
		   SET level = 'admin', expires_at = NULL, via_space_id = NULL`,
		orgID, d.ID, core.Subject{Kind: core.SubjectPrincipal, ID: principal}.Key(), principal,
	); err != nil {
		// A person delegated the folder this is filed in already holds a row
		// from it; what they made themselves is theirs outright, so it
		// becomes theirs as owner.
		return nil, fmt.Errorf("docs: grant to creator: %w", err)
	}

	// The event, in the same transaction as the row. If this commit fails,
	// nothing downstream ever hears about a document that does not exist.
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: d.ID,
		Event: jobs.EventCreated, ActorID: principal,
		Payload: map[string]any{"kind": d.Kind, "title": d.Title},
	}); err != nil {
		return nil, err
	}

	// A document attached to a channel is visible to that channel. This is
	// "channel membership IS the permission model" actually doing something.
	if d.ChannelID != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
			VALUES (gen_random_uuid(), $1, 'document', $2, $3, 'edit', $4)
			ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
			orgID, d.ID, core.Subject{Kind: core.SubjectChannel, ID: *d.ChannelID}.Key(), principal,
		); err != nil {
			return nil, fmt.Errorf("docs: grant to channel: %w", err)
		}
	}

	// Whoever can already reach the space this is filed in can reach this too,
	// same "a tree where each node has to be shared separately is a tree
	// nobody uses twice" reasoning plant.go already applies to a new
	// sub-space's grants. Without this, being granted a department shows its
	// space in the tree and nothing that is actually filed in it: the grant
	// exists on the folder, never on what the folder holds.
	if d.SpaceID != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, expires_at, via_space_id)
			SELECT gen_random_uuid(), $1, 'document', $2, g.subject_key, g.level, $4, g.expires_at, g.via_space_id
			FROM grants g
			WHERE g.entity_type = 'space' AND g.entity_id = $3
			  AND (g.expires_at IS NULL OR g.expires_at > now())
			ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
			orgID, d.ID, *d.SpaceID, principal); err != nil {
			return nil, fmt.Errorf("docs: inherit space grants: %w", err)
		}
	}

	// And whoever can reach the parent reaches its subtasks: a parent is the
	// folder they were filed in, and a folder whose contents each had to be
	// shared again is one nobody uses. It flows down only. Somebody who holds
	// a subtask of their own -- a member, an assignee -- gains nothing above it.
	if d.ParentID != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by, expires_at, via_space_id)
			SELECT gen_random_uuid(), $1, 'document', $2, g.subject_key, g.level, $4, g.expires_at, g.via_space_id
			FROM grants g
			WHERE g.entity_type = 'document' AND g.entity_id = $3
			  AND (g.expires_at IS NULL OR g.expires_at > now())
			ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
			orgID, d.ID, *d.ParentID, principal); err != nil {
			return nil, fmt.Errorf("docs: inherit parent grants: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("docs: commit: %w", err)
	}
	return d, nil
}

// Get returns a document if the caller may view it.
//
// A caller without access gets ErrNotFound, never a permission error. Telling
// somebody that an id is real is itself a disclosure.
func (s *Service) Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*Document, error) {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: id}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	return s.load(ctx, orgID, id)
}

// GetAsSystem reads a document with no permission check, for work done on
// somebody's behalf that has already been authorised elsewhere, such as copying
// the subtasks a close took with it into EmberOPS.
func (s *Service) GetAsSystem(ctx context.Context, orgID, id string) (*Document, error) {
	return s.load(ctx, orgID, id)
}

func (s *Service) load(ctx context.Context, orgID, id string) (*Document, error) {
	var sc docScan
	err := s.db.QueryRow(ctx, `
		SELECT `+selectList+`
		FROM documents d
		LEFT JOIN task_fields t ON t.document_id = d.id
		WHERE d.id = $1 AND d.org_id = $2 AND d.deleted_at IS NULL`,
		id, orgID).Scan(sc.dest()...)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("docs: load %s: %w", id, err)
	}
	return sc.done(), nil
}

// TaskPatch is a partial edit of the task fields. A nil field is left alone.
//
// Pointers rather than values, because the zero value of every one of these is
// meaningful: priority 0 is a real priority, and an empty status is not a
// request to blank the status. Sending a whole TaskFields here is what made
// changing a status silently erase the assignee and the due date, since the
// update wrote all four columns from a struct the caller had only filled one
// field of.
//
// Clearing is therefore explicit. "Leave it alone" and "set it to nothing" are
// different intentions and cannot both be nil.
type TaskPatch struct {
	Status   *string
	Priority *int16

	AssigneeID    *string
	ClearAssignee bool

	DueAt    *time.Time
	ClearDue bool

	// StartedAt and CompletedAt exist for an import correcting work that was
	// done elsewhere. Ordinary use sets them from the lifecycle -- a task
	// becomes in-progress and picks up a start -- but a migration knows the
	// real ones and they are the difference between a history and a snapshot.
	StartedAt   *time.Time
	CompletedAt *time.Time

	// KeepSubtasks stops a status change from moving the task's open subtasks. Only an
	// import sets it: a history brought over from elsewhere says what was
	// finished, and moving the rest on the way in would invent work done.
	KeepSubtasks bool
}

// UpdateParams is a partial edit. Nil fields are left alone.
type UpdateParams struct {
	// CreatedAt and UpdatedAt let an import correct a document it already
	// brought over. Without them a re-run fixes the fields and leaves every
	// document still claiming it was created on the day of the first run.
	CreatedAt *time.Time
	UpdatedAt *time.Time

	Title *string
	Body  *string
	Task  *TaskPatch

	// ExpectedVersion is the version the editor was looking at. The update
	// fails with ErrConflict if it has moved.
	//
	// This is the entire concurrency story now that there is no CRDT. It is
	// not sophisticated, but it is honest: without it, two people editing at
	// once means one of them silently loses their work and never finds out.
	ExpectedVersion int64
}

// Update applies an edit, bumping the version and recording history.
func (s *Service) Update(ctx context.Context, orgID string, actor core.Subjects, id string, p UpdateParams) (*Document, error) {
	ref := core.Ref{Type: core.EntityDocument, ID: id}
	if err := s.authz.Require(ctx, actor, ref, core.AccessEdit); err != nil {
		// Distinguish "cannot see it" from "can see it but may not edit it".
		// Leaking existence to someone with view is not a disclosure, since
		// they can already read it.
		if viewErr := s.authz.Require(ctx, actor, ref, core.AccessView); viewErr != nil {
			return nil, ErrNotFound
		}
		return nil, err
	}
	principal := actor.Principal()

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("docs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The old task fields come back with the row, so the events below can say
	// what actually changed. Reading them afterwards would race with the very
	// update this is about to make.
	var curTitle, curBody string
	var curVersion int64
	var curStatus, curAssignee *string
	err = tx.QueryRow(ctx, `
		SELECT d.title, d.body, d.version, t.status, t.assignee_id::text
		FROM documents d
		LEFT JOIN task_fields t ON t.document_id = d.id
		WHERE d.id=$1 AND d.org_id=$2 AND d.deleted_at IS NULL
		FOR UPDATE OF d`, id, orgID).
		Scan(&curTitle, &curBody, &curVersion, &curStatus, &curAssignee)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("docs: lock for update: %w", err)
	}

	if p.ExpectedVersion != 0 && p.ExpectedVersion != curVersion {
		return nil, fmt.Errorf("%w: you edited version %d, current is %d",
			ErrConflict, p.ExpectedVersion, curVersion)
	}

	title, body := curTitle, curBody
	if p.Title != nil {
		title = *p.Title
	}
	if p.Body != nil {
		body = *p.Body
	}
	next := curVersion + 1

	if _, err := tx.Exec(ctx, `
		UPDATE documents SET title=$1, body=$2, version=$3, updated_by=$4,
		    -- now() unless the caller knows better. An import correcting a
		    -- document it brought over already knows when it really changed,
		    -- and stamping today over that loses the thing being corrected.
		    updated_at = COALESCE($6, now()),
		    created_at = COALESCE($7, created_at)
		WHERE id=$5`, title, body, next, principal, id, p.UpdatedAt, p.CreatedAt); err != nil {
		return nil, fmt.Errorf("docs: update: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO document_versions (document_id, version, title, body, author_id)
		VALUES ($1,$2,$3,$4,$5)`, id, next, title, body, principal); err != nil {
		return nil, fmt.Errorf("docs: record version: %w", err)
	}

	if p.Task != nil {
		t := p.Task
		// COALESCE so an unset field keeps what is already there. completed_at
		// and started_at are derived from the status rather than accepted from
		// the caller, so "done" and "when it was done" cannot disagree.
		if _, err := tx.Exec(ctx, `
			UPDATE task_fields SET
			    status   = COALESCE($1, status),
			    priority = COALESCE($2, priority),
			    assignee_id = CASE WHEN $3 THEN NULL ELSE COALESCE($4, assignee_id) END,
			    due_at      = CASE WHEN $5 THEN NULL ELSE COALESCE($6, due_at) END,
			    -- Derived from the status as before, EXCEPT when the caller
			    -- supplies a real one. An import knows when the work actually
			    -- started and finished; deriving now() over the top of that
			    -- turns a history into a snapshot taken on migration day.
			    started_at = CASE
			        WHEN $8::timestamptz IS NOT NULL THEN $8
			        WHEN COALESCE($1, status) IN ('in_progress','done')
			            THEN COALESCE(started_at, now())
			        ELSE started_at END,
			    completed_at = CASE
			        WHEN $9::timestamptz IS NOT NULL THEN $9
			        WHEN COALESCE($1, status) = 'done' THEN COALESCE(completed_at, now())
			        ELSE NULL END
			WHERE document_id=$7`,
			t.Status, t.Priority, t.ClearAssignee, t.AssigneeID,
			t.ClearDue, t.DueAt, id, t.StartedAt, t.CompletedAt); err != nil {
			return nil, fmt.Errorf("docs: update task fields: %w", err)
		}
	}

	// Events, in the same transaction. Separate ones for the changes anybody
	// downstream reacts differently to: a rule that fires on "assigned" should
	// not have to inspect an "updated" payload to work out whether it applies.
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: id,
		Event: jobs.EventUpdated, ActorID: principal,
		Payload: map[string]any{"version": next},
	}); err != nil {
		return nil, err
	}
	if p.Task != nil {
		if t := p.Task; t.Status != nil && (curStatus == nil || *t.Status != *curStatus) {
			from := ""
			if curStatus != nil {
				from = *curStatus
			}
			if err := jobs.Publish(ctx, tx, jobs.EventSpec{
				OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: id,
				Event: jobs.EventStatus, ActorID: principal,
				Payload: map[string]any{"from": from, "to": *t.Status},
			}); err != nil {
				return nil, err
			}
		}
		if assignee, changed := assigneeChange(p.Task, curAssignee); changed {
			// Being given a job grants you the job. Assigning work to somebody
			// who cannot open it is not assigning it, and every alternative is
			// worse: asking them to request access, or expecting whoever
			// assigned it to remember a second step. Edit rather than view,
			// because the person doing the work has to be able to record what
			// they did.
			if assignee != nil {
				if _, err := tx.Exec(ctx, `
					INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
					VALUES (gen_random_uuid(), $1, 'document', $2, $3, 'edit', $4)
					ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
					orgID, id, core.Subject{Kind: core.SubjectPrincipal, ID: *assignee}.Key(),
					principal); err != nil {
					return nil, fmt.Errorf("docs: grant to assignee: %w", err)
				}
			}
			if err := jobs.Publish(ctx, tx, jobs.EventSpec{
				OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: id,
				Event: jobs.EventAssigned, ActorID: principal,
				Payload: map[string]any{"assignee_id": assignee},
			}); err != nil {
				return nil, err
			}
		}
	}

	// Changing a task's status moves what is inside it, in this same
	// transaction so a parent and its open subtasks never disagree and a
	// failure part way leaves both as they were. Only when the status really
	// changes, not on a save of a task already in it, and only downward: a
	// subtask's change never reaches its parent or its siblings. Finished and
	// cancelled subtasks keep their status, so reopening a parent reopens
	// nothing.
	var closed []string
	var cascadedTo string
	if t := p.Task; t != nil && t.Status != nil && !t.KeepSubtasks {
		cascadedTo = *t.Status
		if closed, err = s.cascadeFrom(ctx, tx, orgID, id, principal, curStatus, t.Status); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("docs: commit: %w", err)
	}
	NoteCascaded(ctx, cascadedTo, closed)
	return s.load(ctx, orgID, id)
}

// assigneeChange reports the new assignee and whether it actually moved.
//
// Reassigning somebody to the person already holding it must not produce an
// event, or every save of an unrelated field notifies them again. That is the
// specific reason people turn notifications off.
func assigneeChange(t *TaskPatch, current *string) (*string, bool) {
	switch {
	case t.ClearAssignee:
		return nil, current != nil
	case t.AssigneeID != nil:
		return t.AssigneeID, current == nil || *current != *t.AssigneeID
	default:
		return nil, false
	}
}

// ListParams filters a listing.
type ListParams struct {
	Kind      string
	ChannelID *string
	Status    string
	Limit     int
}

// List returns documents the caller can see, and nothing else.
//
// The permission filter is part of the query rather than applied afterwards.
// Filtering in Go after the fact is both slower and wrong: it breaks LIMIT,
// because the database does not know which rows will survive.
func (s *Service) List(ctx context.Context, orgID string, actor core.Subjects, p ListParams) ([]*Document, error) {
	if p.Limit <= 0 || p.Limit > 200 {
		p.Limit = 50
	}

	var sb strings.Builder
	// Same filter as the list, through core, so a super admin cannot see a
	// document in one place and get a 404 for it in another.
	args := []any{orgID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	sb.WriteString(`
		SELECT ` + selectList + `
		FROM documents d
		LEFT JOIN task_fields t ON t.document_id = d.id
		WHERE d.org_id = $1
		  AND d.deleted_at IS NULL
		  AND ` + actor.GrantsExists(core.EntityDocument, "d.id", arg))

	if p.Kind != "" {
		args = append(args, p.Kind)
		fmt.Fprintf(&sb, " AND d.kind = $%d", len(args))
	}
	if p.ChannelID != nil {
		args = append(args, *p.ChannelID)
		fmt.Fprintf(&sb, " AND d.channel_id = $%d", len(args))
	}
	if p.Status != "" {
		args = append(args, p.Status)
		fmt.Fprintf(&sb, " AND t.status = $%d", len(args))
	}
	args = append(args, p.Limit)
	fmt.Fprintf(&sb, " ORDER BY d.updated_at DESC LIMIT $%d", len(args))

	rows, err := s.db.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("docs: list: %w", err)
	}
	defer rows.Close()

	var out []*Document
	for rows.Next() {
		var sc docScan
		if err := rows.Scan(sc.dest()...); err != nil {
			return nil, fmt.Errorf("docs: scan: %w", err)
		}
		out = append(out, sc.done())
	}
	return out, rows.Err()
}

// Link records a relationship between two entities, in both directions.
//
// This is what makes a work order point at the asset it concerns, and the asset
// show every work order raised against it, without either one knowing about the
// other's table.
func (s *Service) Link(ctx context.Context, orgID string, actor core.Subjects, from, to core.Ref, kind string) error {
	if err := s.authz.Require(ctx, actor, from, core.AccessEdit); err != nil {
		return err
	}
	if err := s.authz.Require(ctx, actor, to, core.AccessView); err != nil {
		return err
	}
	if kind == "" {
		kind = "mention"
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO entity_links (id, org_id, from_type, from_id, to_type, to_id, kind, created_by)
		VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (from_type, from_id, to_type, to_id, kind) DO NOTHING`,
		orgID, string(from.Type), from.ID, string(to.Type), to.ID, kind, actor.Principal())
	if err != nil {
		return fmt.Errorf("docs: link: %w", err)
	}
	return nil
}

// Delete soft-deletes a document. Requires admin, not edit: removing something
// is not the same authority as changing it.
func (s *Service) Delete(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	ref := core.Ref{Type: core.EntityDocument, ID: id}
	if err := s.authz.Require(ctx, actor, ref, core.AccessAdmin); err != nil {
		return err
	}
	// A transaction, so the event and the deletion cannot disagree. Without it
	// a failure between the two leaves either a deleted document nothing was
	// told about, or an event describing a deletion that did not happen.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("docs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var kind, title string
	err = tx.QueryRow(ctx, `
		UPDATE documents SET deleted_at = now()
		WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL
		RETURNING kind, title`, id, orgID).Scan(&kind, &title)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("docs: delete: %w", err)
	}

	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: id,
		Event: jobs.EventDeleted, ActorID: actor.Principal(),
		Payload: map[string]any{"kind": kind},
	}); err != nil {
		return err
	}
	// In the same transaction as the deletion, so the record of who did it
	// cannot fail on its own and leave something gone with nobody attached.
	// The title is kept in before, because "which work order was that" is the
	// first question asked about a deletion and the row it named is soft
	// deleted rather than readable.
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actor.Principal(),
		Action:     audit.ActionDocumentDelete,
		EntityType: string(core.EntityDocument), EntityID: id,
		Before: map[string]any{"kind": kind, "title": title},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetAssignees records the full set of people a document is assigned to.
//
// task_fields.assignee_id stays the primary and stays what every query, board
// and rollup reads. This is the set beside it, for the sources that allow more
// than one -- without it, everybody after the first survives only as prose.
//
// Replaces rather than merges: the source is authoritative about who is on a
// task, and merging would make an import unable to remove somebody.
func (s *Service) SetAssignees(ctx context.Context, docID string, principalIDs []string, primary string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("docs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM document_assignees WHERE document_id = $1`, docID); err != nil {
		return fmt.Errorf("docs: clear assignees: %w", err)
	}
	for _, id := range principalIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO document_assignees (document_id, principal_id, is_primary)
			VALUES ($1,$2,$3)
			ON CONFLICT (document_id, principal_id) DO UPDATE SET is_primary = EXCLUDED.is_primary`,
			docID, id, id == primary); err != nil {
			return fmt.Errorf("docs: add assignee: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("docs: commit: %w", err)
	}
	return nil
}

// Assignees returns everybody a document is assigned to, primary first.
func (s *Service) Assignees(ctx context.Context, docID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT principal_id::text FROM document_assignees
		WHERE document_id = $1 ORDER BY is_primary DESC, created_at`, docID)
	if err != nil {
		return nil, fmt.Errorf("docs: list assignees: %w", err)
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
