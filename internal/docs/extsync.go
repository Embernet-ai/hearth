package docs

// Copying edits made in EmberOPS into the spine.
//
// Only the EmberOPS back end calls these. They exist so that a document edited
// inside EmberOPS reaches Hearth's version history and search index (the
// outbox event below feeds the search pipeline exactly as an ordinary edit
// does) instead of waiting for Hearth's next save of it.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// ExternalTask is the task side of an edit made elsewhere. It replaces the
// spine's values outright: EmberOPS is the source of record for them.
type ExternalTask struct {
	Status      string
	Priority    int16
	AssigneeID  *string
	DueAt       *time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
}

// ExternalChange is one document as EmberOPS now holds it.
type ExternalChange struct {
	Title, Body string
	Task        *ExternalTask
	// AssigneeIDs, when non-nil, replaces the set of assignees; the primary is
	// Task.AssigneeID.
	AssigneeIDs []string
	// By is who made the edit, when EmberOPS says so and Hearth knows them.
	// Empty means the document's owner, because a version needs an author.
	By string
	// Modified is EmberOPS's stamp for this state, recorded in the same
	// transaction so the change is not copied twice.
	Modified string
}

// ExternalMarks returns, for each document id given, the EmberOPS `modified`
// stamp last recorded. A document with no mark is absent from the map.
func (s *Service) ExternalMarks(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT document_id::text, modified FROM emberops_doc_sync WHERE document_id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("docs: read sync marks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, m string
		if err := rows.Scan(&id, &m); err != nil {
			return nil, err
		}
		out[id] = m
	}
	return out, rows.Err()
}

// SetExternalMarks records the EmberOPS stamp for documents without changing
// them: after Hearth writes a record, and as the baseline for documents that
// have never been compared. One statement however many documents.
func (s *Service) SetExternalMarks(ctx context.Context, marks map[string]string) error {
	if len(marks) == 0 {
		return nil
	}
	ids, mods := make([]string, 0, len(marks)), make([]string, 0, len(marks))
	for id, m := range marks {
		ids, mods = append(ids, id), append(mods, m)
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO emberops_doc_sync (document_id, modified)
		SELECT u.id, u.m FROM unnest($1::uuid[], $2::text[]) AS u(id, m)
		WHERE EXISTS (SELECT 1 FROM documents d WHERE d.id = u.id)
		ON CONFLICT (document_id) DO UPDATE SET modified = EXCLUDED.modified, synced_at = now()`,
		ids, mods)
	if err != nil {
		return fmt.Errorf("docs: record sync marks: %w", err)
	}
	return nil
}

// ChecklistItemDoc says which document a checklist item belongs to.
func (s *Service) ChecklistItemDoc(ctx context.Context, orgID, itemID string) (string, error) {
	return s.checklistParent(ctx, orgID, itemID)
}

// ApplyExternal copies an edit made in EmberOPS into the spine: a new version
// with its history row, the task fields, and an "updated" event (plus status and
// assigned events when those moved) so search and rules see it. It checks no
// permission; the edit was already allowed by EmberOPS.
func (s *Service) ApplyExternal(ctx context.Context, orgID, id string, c ExternalChange) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("docs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var curTitle, curBody, owner string
	var curVersion int64
	var curStatus, curAssignee *string
	var curPrio *int16
	var curDue, curStart, curDone *time.Time
	err = tx.QueryRow(ctx, `
		SELECT d.title, d.body, d.version, d.owner_id::text, t.status, t.assignee_id::text,
		       t.priority, t.due_at, t.started_at, t.completed_at
		FROM documents d LEFT JOIN task_fields t ON t.document_id = d.id
		WHERE d.id=$1 AND d.org_id=$2 AND d.deleted_at IS NULL
		FOR UPDATE OF d`, id, orgID).Scan(&curTitle, &curBody, &curVersion, &owner, &curStatus, &curAssignee,
		&curPrio, &curDue, &curStart, &curDone)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("docs: lock for external update: %w", err)
	}

	// Another reader may have copied this very state while we waited for the
	// lock, and a state that matches the spine is not an edit: a comment on
	// the EmberOPS record moves `modified` without changing anything Hearth
	// shows, and must not mint a version.
	var recorded string
	_ = tx.QueryRow(ctx, `SELECT modified FROM emberops_doc_sync WHERE document_id=$1 FOR UPDATE`, id).Scan(&recorded)
	same := c.Title == curTitle && c.Body == curBody
	if t := c.Task; same && t != nil {
		same = curStatus != nil && *curStatus == t.Status && curPrio != nil && *curPrio == t.Priority &&
			sameStr(curAssignee, t.AssigneeID) && sameTime(curDue, t.DueAt) &&
			sameTime(curStart, t.StartedAt) && sameTime(curDone, t.CompletedAt)
	}
	if (recorded != "" && recorded == c.Modified) || same {
		if c.Modified != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO emberops_doc_sync (document_id, modified) VALUES ($1,$2)
				ON CONFLICT (document_id) DO UPDATE SET modified = EXCLUDED.modified, synced_at = now()`,
				id, c.Modified); err != nil {
				return fmt.Errorf("docs: record sync mark: %w", err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("docs: commit: %w", err)
		}
		return s.syncAssignees(ctx, id, c)
	}
	by := c.By
	if by == "" {
		by = owner
	}
	next := curVersion + 1
	if _, err := tx.Exec(ctx, `
		UPDATE documents SET title=$1, body=$2, version=$3, updated_by=$4, updated_at=now() WHERE id=$5`,
		c.Title, c.Body, next, by, id); err != nil {
		return fmt.Errorf("docs: external update: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO document_versions (document_id, version, title, body, author_id)
		VALUES ($1,$2,$3,$4,$5)`, id, next, c.Title, c.Body, by); err != nil {
		return fmt.Errorf("docs: record version: %w", err)
	}
	var newAssignee *string
	if t := c.Task; t != nil {
		newAssignee = t.AssigneeID
		if _, err := tx.Exec(ctx, `
			UPDATE task_fields SET status=$1, priority=$2, assignee_id=$3, due_at=$4, started_at=$5, completed_at=$6
			WHERE document_id=$7`,
			t.Status, t.Priority, t.AssigneeID, t.DueAt, t.StartedAt, t.CompletedAt, id); err != nil {
			return fmt.Errorf("docs: external task fields: %w", err)
		}
	}
	if c.Modified != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO emberops_doc_sync (document_id, modified) VALUES ($1,$2)
			ON CONFLICT (document_id) DO UPDATE SET modified = EXCLUDED.modified, synced_at = now()`,
			id, c.Modified); err != nil {
			return fmt.Errorf("docs: record sync mark: %w", err)
		}
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: id,
		Event: jobs.EventUpdated, ActorID: by,
		Payload: map[string]any{"version": next, "external": true},
	}); err != nil {
		return err
	}
	if t := c.Task; t != nil {
		if curStatus == nil || *curStatus != t.Status {
			from := ""
			if curStatus != nil {
				from = *curStatus
			}
			if err := jobs.Publish(ctx, tx, jobs.EventSpec{
				OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: id,
				Event: jobs.EventStatus, ActorID: by,
				Payload: map[string]any{"from": from, "to": t.Status},
			}); err != nil {
				return err
			}
		}
		if (curAssignee == nil) != (newAssignee == nil) || (curAssignee != nil && *curAssignee != *newAssignee) {
			if err := jobs.Publish(ctx, tx, jobs.EventSpec{
				OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: id,
				Event: jobs.EventAssigned, ActorID: by,
				Payload: map[string]any{"assignee_id": newAssignee},
			}); err != nil {
				return err
			}
		}
	}
	// Changed in EmberOPS: the subtasks follow, as they do for an edit made here.
	// EmberOPS's own Task hook moved them there already; this copy makes the
	// spine agree and writes its audit rows, and it is idempotent.
	var closed []string
	var cascadedTo string
	if t := c.Task; t != nil {
		cascadedTo = t.Status
		if closed, err = s.cascadeFrom(ctx, tx, orgID, id, by, curStatus, &t.Status); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("docs: commit: %w", err)
	}
	NoteCascaded(ctx, cascadedTo, closed)
	return s.syncAssignees(ctx, id, c)
}

func (s *Service) syncAssignees(ctx context.Context, id string, c ExternalChange) error {
	if c.AssigneeIDs == nil {
		return nil
	}
	primary := ""
	if c.Task != nil && c.Task.AssigneeID != nil {
		primary = *c.Task.AssigneeID
	}
	return s.SetAssignees(ctx, id, c.AssigneeIDs, primary)
}

func sameStr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// sameTime compares to the second, which is what EmberOPS keeps.
func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Truncate(time.Second).Equal(b.Truncate(time.Second))
}
