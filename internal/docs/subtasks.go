package docs

// Subtasks: a task inside another task.
//
// A parent is what a folder was in ClickUp -- "Ignition", with one subtask per
// person taking it -- and a subtask is an ordinary document in every other
// respect: its own grants, its own status, its own hours. Visibility never
// flows up. Seeing a subtask of your own says nothing about the parent, and a
// list shows such a subtask on its own rather than inside a parent you cannot
// open.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// ErrParentLoop is a move that would put a task inside itself, directly or
// through one of its own subtasks.
var ErrParentLoop = errors.New("docs: a task cannot sit inside itself or one of its subtasks")

// parentOf checks a would-be parent -- this org's, not deleted, visible to the
// actor -- and returns its space, which a new subtask lands in by default.
func (s *Service) parentOf(ctx context.Context, orgID string, actor core.Subjects, parentID string) (*string, error) {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: parentID}, core.AccessView); err != nil {
		return nil, fmt.Errorf("%w: parent", ErrNotFound)
	}
	var space *string
	err := s.db.QueryRow(ctx, `
		SELECT space_id::text FROM documents
		 WHERE id::text = $1 AND org_id = $2 AND deleted_at IS NULL`, parentID, orgID).Scan(&space)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: parent", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("docs: look up parent: %w", err)
	}
	return space, nil
}

// SetParent puts a task inside another, or takes it out with an empty parent.
//
// Needs edit on the task being moved and view on its new parent, the same pair
// Link asks for. Refuses a loop: walking up from the new parent must never
// reach the task itself, or the tree would have no top and every list built
// from it would recurse forever.
func (s *Service) SetParent(ctx context.Context, orgID string, actor core.Subjects, id, parentID string) error {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: id}, core.AccessEdit); err != nil {
		return err
	}
	var parent *string
	if parentID != "" {
		if parentID == id {
			return ErrParentLoop
		}
		if _, err := s.parentOf(ctx, orgID, actor, parentID); err != nil {
			return err
		}
		var loops bool
		if err := s.db.QueryRow(ctx, `
			WITH RECURSIVE up AS (
			    SELECT id, parent_id, 0 AS depth FROM documents WHERE id::text = $1
			  UNION ALL
			    SELECT d.id, d.parent_id, u.depth + 1 FROM documents d
			      JOIN up u ON d.id = u.parent_id
			     WHERE u.depth < 50
			)
			SELECT EXISTS (SELECT 1 FROM up WHERE id::text = $2)`, parentID, id).Scan(&loops); err != nil {
			return fmt.Errorf("docs: check for a loop: %w", err)
		}
		if loops {
			return ErrParentLoop
		}
		parent = &parentID
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE documents SET parent_id = $3, updated_at = now()
		 WHERE id::text = $1 AND org_id = $2 AND deleted_at IS NULL`, id, orgID, parent)
	if err != nil {
		return fmt.Errorf("docs: set parent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Children is a task's subtasks that the actor can see, in the order they were
// made -- the order a person filled a folder in.
func (s *Service) Children(ctx context.Context, orgID string, actor core.Subjects, parentID string) ([]*Document, error) {
	args := []any{orgID, parentID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	rows, err := s.db.Query(ctx, `
		SELECT `+selectList+`
		  FROM documents d
		  LEFT JOIN task_fields t ON t.document_id = d.id
		 WHERE d.org_id = $1 AND d.parent_id::text = $2 AND d.deleted_at IS NULL
		   AND `+actor.GrantsExists(core.EntityDocument, "d.id", arg)+`
		 ORDER BY d.created_at, d.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("docs: children: %w", err)
	}
	defer rows.Close()
	var out []*Document
	for rows.Next() {
		var sc docScan
		if err := rows.Scan(sc.dest()...); err != nil {
			return nil, fmt.Errorf("docs: scan child: %w", err)
		}
		out = append(out, sc.done())
	}
	return out, rows.Err()
}

type closedKey struct{}

// ClosedSink collects the ids of the subtasks a write closed along with its
// parent. A caller that wants to say so ("Closed with 3 subtasks") or to copy
// the change somewhere else (EmberOps) puts one on the context with
// WithClosedSink before the write and reads it after.
type ClosedSink struct{ IDs []string }

// WithClosedSink returns a context that records, in the returned sink, every
// subtask a status change on that context closes with its parent.
func WithClosedSink(ctx context.Context) (context.Context, *ClosedSink) {
	sink := &ClosedSink{}
	return context.WithValue(ctx, closedKey{}, sink), sink
}

// WithClosedSinkShared is WithClosedSink that reuses a sink already on the
// context, so a wrapper that needs the ids and the caller that asked for them
// both see the same list.
func WithClosedSinkShared(ctx context.Context) (context.Context, *ClosedSink) {
	if sink, ok := ctx.Value(closedKey{}).(*ClosedSink); ok && sink != nil {
		return ctx, sink
	}
	return WithClosedSink(ctx)
}

// NoteClosed records ids in the context's sink. closeSubtasks calls it after the
// commit; a store that closes subtasks some other way, or a test double for one,
// calls it too.
func NoteClosed(ctx context.Context, ids []string) {
	if sink, ok := ctx.Value(closedKey{}).(*ClosedSink); ok && sink != nil {
		sink.IDs = append(sink.IDs, ids...)
	}
}

// closeSubtasks moves every open task beneath parentID, at any depth, to done,
// inside the transaction that closed the parent.
//
// "Open" is anything that is not done and not cancelled (ClosedStatuses): a
// subtask somebody already finished or deliberately dropped keeps its status
// and its completed date. The walk goes through closed subtasks too, because
// an open task under a cancelled one is still an open task under the parent.
//
// Each one is closed the way a person closing it would: a new version with its
// history row, started and completed dates, a status event (so rules, search
// and notifications see it) and an audit entry naming who closed the parent.
// The events carry closed_with so notifications can fold them into the
// parent's. Permission was settled on the parent: whoever may close it closes
// what is inside it, the way a folder's contents went with the folder, and
// visibility never flows up so this grants nothing to anybody.
//
// Returns the ids closed, so the caller can report them.
func (s *Service) closeSubtasks(ctx context.Context, tx pgx.Tx, orgID, parentID, principal string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		WITH RECURSIVE below AS (
		    SELECT id, 1 AS depth FROM documents
		     WHERE parent_id::text = $1 AND org_id = $2 AND deleted_at IS NULL
		  UNION
		    SELECT d.id, b.depth + 1 FROM documents d
		      JOIN below b ON d.parent_id = b.id
		     WHERE d.org_id = $2 AND d.deleted_at IS NULL AND b.depth < 50
		)
		SELECT d.id::text, d.version, COALESCE(t.status, ''), d.title, d.body
		  FROM documents d
		  JOIN task_fields t ON t.document_id = d.id
		 WHERE d.id IN (SELECT id FROM below)
		   AND t.status NOT IN ('done', 'cancelled')
		 ORDER BY d.id
		   FOR UPDATE OF d`, parentID, orgID)
	if err != nil {
		return nil, fmt.Errorf("docs: find subtasks to close: %w", err)
	}
	type open struct {
		id, status, title, body string
		version                 int64
	}
	var todo []open
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.id, &o.version, &o.status, &o.title, &o.body); err != nil {
			rows.Close()
			return nil, fmt.Errorf("docs: scan subtask: %w", err)
		}
		todo = append(todo, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(todo))
	for _, o := range todo {
		next := o.version + 1
		if _, err := tx.Exec(ctx, `
			UPDATE documents SET version=$1, updated_by=$2, updated_at=now() WHERE id=$3`,
			next, principal, o.id); err != nil {
			return nil, fmt.Errorf("docs: close subtask: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO document_versions (document_id, version, title, body, author_id)
			VALUES ($1,$2,$3,$4,$5)`, o.id, next, o.title, o.body, principal); err != nil {
			return nil, fmt.Errorf("docs: record subtask version: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE task_fields SET status = $2,
			    started_at = COALESCE(started_at, now()),
			    completed_at = COALESCE(completed_at, now())
			WHERE document_id = $1`, o.id, StatusDone); err != nil {
			return nil, fmt.Errorf("docs: close subtask fields: %w", err)
		}
		if err := jobs.Publish(ctx, tx, jobs.EventSpec{
			OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: o.id,
			Event: jobs.EventUpdated, ActorID: principal,
			Payload: map[string]any{"version": next, "closed_with": parentID},
		}); err != nil {
			return nil, err
		}
		if err := jobs.Publish(ctx, tx, jobs.EventSpec{
			OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: o.id,
			Event: jobs.EventStatus, ActorID: principal,
			Payload: map[string]any{"from": o.status, "to": StatusDone, "closed_with": parentID},
		}); err != nil {
			return nil, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			OrgID: orgID, ActorID: principal, Action: audit.ActionTaskCloseWithParent,
			EntityType: string(core.EntityDocument), EntityID: o.id,
			Before: map[string]any{"status": o.status},
			After:  map[string]any{"status": StatusDone, "closed_with_parent": parentID},
		}); err != nil {
			return nil, err
		}
		ids = append(ids, o.id)
	}
	return ids, nil
}
